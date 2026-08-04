package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/shreyam1008/dbterm/config"
)

func TestBuildRestorePlanDefaultsAndCopiesInputs(t *testing.T) {
	t.Parallel()

	inspection := restoreTestInspection(FormatPostgresCustom, config.PostgreSQL)
	target := restorePostgresTarget()
	plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{})
	if err != nil {
		t.Fatalf("BuildRestorePlan() error = %v", err)
	}
	if plan.Options.Mode != RestoreModeMerge || !plan.Options.StopOnError || !plan.Options.SingleTransaction {
		t.Fatalf("zero-value options normalized to %#v, want safe merge/stop/single defaults", plan.Options)
	}
	if !containsString(plan.Warnings, "Merge mode") {
		t.Errorf("Warnings = %v, want merge warning", plan.Warnings)
	}

	inspection.Wrappers = append(inspection.Wrappers, WrapperGzip)
	inspection.Evidence[0] = "mutated"
	target.Database = "mutated"
	if len(plan.Inspection.Wrappers) != 0 || plan.Inspection.Evidence[0] == "mutated" || plan.Target.Database == "mutated" {
		t.Fatal("restore plan retained mutable caller-owned data")
	}
}

func TestBuildRestorePlanRejectsUnsafeCombinations(t *testing.T) {
	t.Parallel()

	postgres := restorePostgresTarget()
	mysql := restoreMySQLTarget()
	sqlite := config.ConnectionConfig{Type: config.SQLite, FilePath: filepath.Join(t.TempDir(), "target.sqlite3")}

	tests := []struct {
		name       string
		inspection *Inspection
		target     *config.ConnectionConfig
		options    RestoreOptions
		wantError  string
	}{
		{name: "locked", inspection: withRestoreInspection(func(value *Inspection) { value.Locked = true }), target: &postgres, wantError: "locked"},
		{name: "unknown", inspection: restoreTestInspection(FormatUnknown, ""), target: &postgres, wantError: "unknown"},
		{name: "generic SQL", inspection: restoreTestInspection(FormatGenericSQL, ""), target: &postgres, wantError: "ambiguous"},
		{name: "ambiguous confidence", inspection: withRestoreInspection(func(value *Inspection) { value.Confidence = ConfidenceAmbiguous }), target: &postgres, wantError: "ambiguous"},
		{name: "weak confidence", inspection: withRestoreInspection(func(value *Inspection) { value.Confidence = ConfidenceUnknown }), target: &postgres, wantError: "not sufficient"},
		{name: "inconsistent inspection", inspection: restoreTestInspection(FormatPostgresCustom, config.MySQL), target: &postgres, wantError: "inconsistent"},
		{name: "engine mismatch", inspection: restoreTestInspection(FormatPostgresCustom, config.PostgreSQL), target: &mysql, wantError: "mismatch"},
		{name: "invalid mode", inspection: restoreTestInspection(FormatPostgresCustom, config.PostgreSQL), target: &postgres, options: RestoreOptions{Mode: "replace"}, wantError: "invalid restore mode"},
		{name: "negative decoded limit", inspection: restoreTestInspection(FormatPostgresCustom, config.PostgreSQL), target: &postgres, options: RestoreOptions{MaxDecodedBytes: -1}, wantError: "maximum decoded size"},
		{name: "age identity absent", inspection: withRestoreInspection(func(value *Inspection) { value.Wrappers = []Wrapper{WrapperAge} }), target: &postgres, options: RestoreOptions{Mode: RestoreModeMerge}, wantError: "identity"},
		{name: "too many wrappers", inspection: withRestoreInspection(func(value *Inspection) {
			value.Wrappers = []Wrapper{WrapperGzip, WrapperGzip, WrapperGzip, WrapperGzip}
		}), target: &postgres, wantError: "at most 3"},
		{name: "invalid digest", inspection: withRestoreInspection(func(value *Inspection) { value.SHA256 = "nope" }), target: &postgres, wantError: "SHA-256"},
		{name: "empty artifact", inspection: withRestoreInspection(func(value *Inspection) { value.Size = 0 }), target: &postgres, wantError: "positive"},
		{name: "SQLite file merge", inspection: restoreTestInspection(FormatSQLiteDatabase, config.SQLite), target: &sqlite, options: RestoreOptions{Mode: RestoreModeMerge}, wantError: "explicit clean"},
		{name: "unsupported target", inspection: restoreTestInspection(FormatSQLiteDatabase, config.SQLite), target: &config.ConnectionConfig{Type: config.Turso, Host: "example"}, options: RestoreOptions{Mode: RestoreModeClean}, wantError: "mismatch"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := BuildRestorePlan(test.inspection, test.target, test.options)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("BuildRestorePlan() error = %v, want error containing %q", err, test.wantError)
			}
		})
	}
}

func TestBuildRestorePlanNilInputs(t *testing.T) {
	t.Parallel()

	target := restorePostgresTarget()
	if _, err := BuildRestorePlan(nil, &target, RestoreOptions{}); err == nil || !strings.Contains(err.Error(), "inspection") {
		t.Fatalf("nil inspection error = %v", err)
	}
	if _, err := BuildRestorePlan(restoreTestInspection(FormatPostgresCustom, config.PostgreSQL), nil, RestoreOptions{}); err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("nil target error = %v", err)
	}
	if err := ExecuteRestore(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "plan") {
		t.Fatalf("nil plan error = %v", err)
	}
}

func TestBuildRestorePlanValidatesTargetDetails(t *testing.T) {
	t.Parallel()

	inspection := restoreTestInspection(FormatPostgresCustom, config.PostgreSQL)
	tests := []struct {
		name      string
		mutate    func(*config.ConnectionConfig)
		wantError string
	}{
		{name: "user", mutate: func(value *config.ConnectionConfig) { value.User = "" }, wantError: "user name"},
		{name: "database", mutate: func(value *config.ConnectionConfig) { value.Database = "" }, wantError: "database name"},
		{name: "port", mutate: func(value *config.ConnectionConfig) { value.Port = "99999" }, wantError: "port"},
		{name: "control character", mutate: func(value *config.ConnectionConfig) { value.Database = "safe\nother" }, wantError: "control character"},
		{name: "pgpass newline", mutate: func(value *config.ConnectionConfig) { value.Password = "one\ntwo" }, wantError: "line break"},
		{name: "SSL mode", mutate: func(value *config.ConnectionConfig) { value.SSLMode = "sometimes" }, wantError: "SSL mode"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			target := restorePostgresTarget()
			test.mutate(&target)
			_, err := BuildRestorePlan(inspection, &target, RestoreOptions{})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("BuildRestorePlan() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestRestoreCommandArgumentsUseSafeDefaults(t *testing.T) {
	t.Parallel()

	postgresPlan := &RestorePlan{
		Target:  restorePostgresTarget(),
		Options: RestoreOptions{Mode: RestoreModeMerge, StopOnError: true, SingleTransaction: true},
	}
	archiveArgs := postgresArchiveRestoreArgs(postgresPlan)
	for _, required := range []string{"--no-password", "--no-owner", "--no-privileges", "--exit-on-error", "--single-transaction"} {
		if !slices.Contains(archiveArgs, required) {
			t.Errorf("pg_restore args %v missing %s", archiveArgs, required)
		}
	}
	if slices.Contains(archiveArgs, "--clean") || slices.Contains(archiveArgs, "--if-exists") {
		t.Errorf("merge pg_restore args unexpectedly clean: %v", archiveArgs)
	}
	postgresPlan.Options.Mode = RestoreModeClean
	cleanArgs := postgresArchiveRestoreArgs(postgresPlan)
	if !slices.Contains(cleanArgs, "--clean") || !slices.Contains(cleanArgs, "--if-exists") {
		t.Errorf("clean pg_restore args = %v", cleanArgs)
	}

	psqlArgs := postgresSQLRestoreArgs(postgresPlan, "/private/staged.sql")
	for _, required := range []string{"--no-psqlrc", "ON_ERROR_STOP=on", "--single-transaction", "--file"} {
		if !slices.Contains(psqlArgs, required) {
			t.Errorf("psql args %v missing %s", psqlArgs, required)
		}
	}
	if strings.Contains(strings.Join(psqlArgs, " "), postgresPlan.Target.Password) {
		t.Fatal("PostgreSQL password appeared in command arguments")
	}

	mysqlPlan := &RestorePlan{
		Target:  restoreMySQLTarget(),
		Options: RestoreOptions{Mode: RestoreModeMerge, StopOnError: true},
	}
	mysqlArgs := mysqlRestoreArgs(mysqlPlan, "/private/client.cnf")
	if !strings.HasPrefix(mysqlArgs[0], "--defaults-extra-file=") {
		t.Fatalf("mysql first arg = %q, want defaults file first", mysqlArgs[0])
	}
	for _, required := range []string{"--local-infile=0", "--skip-auto-rehash", "--batch", "--binary-mode", "--disable-pager"} {
		if !slices.Contains(mysqlArgs, required) {
			t.Errorf("mysql args %v missing %s", mysqlArgs, required)
		}
	}
	if strings.Contains(strings.Join(mysqlArgs, " "), mysqlPlan.Target.Password) {
		t.Fatal("MySQL password appeared in command arguments")
	}
	mysqlPlan.Options.StopOnError = false
	if args := mysqlRestoreArgs(mysqlPlan, ""); !slices.Contains(args, "--force") {
		t.Errorf("mysql continue-on-error args = %v, want --force", args)
	}
}

func TestRestoreEnvironmentRemovesAmbientPasswordVariables(t *testing.T) {
	t.Parallel()

	environment := environmentWithout([]string{"PATH=/bin", "PGPASSWORD=secret", "pgpassfile=old", "KEEP=yes"}, "PGPASSWORD", "PGPASSFILE")
	if !reflect.DeepEqual(environment, []string{"PATH=/bin", "KEEP=yes"}) {
		t.Fatalf("environmentWithout() = %v", environment)
	}
	target := restorePostgresTarget()
	for _, entry := range postgresRestoreEnvironment(&target, "/private/pgpass") {
		if strings.HasPrefix(strings.ToUpper(entry), "PGPASSWORD=") {
			t.Fatalf("postgres environment contains PGPASSWORD: %q", entry)
		}
	}
}

func TestRestoreSQLGuards(t *testing.T) {
	t.Parallel()

	t.Run("MySQL allows selected target and ignores strings and comments", func(t *testing.T) {
		data := []byte("-- USE other\nCREATE TABLE notes(v text);\nINSERT INTO notes VALUES ('USE other; CREATE DATABASE wrong;');\nUSE `target_db`;\nCREATE DATABASE IF NOT EXISTS target_db;\n")
		path := writeInspectionFixture(t, "safe.sql", data)
		if err := validateMySQLSQLForRestore(path, "target_db"); err != nil {
			t.Fatalf("validateMySQLSQLForRestore() error = %v", err)
		}
	})

	mysqlBlocked := []struct {
		name string
		sql  string
		want string
	}{
		{name: "USE elsewhere", sql: "USE `other`;", want: "not selected target"},
		{name: "CREATE elsewhere", sql: "CREATE DATABASE IF NOT EXISTS other;", want: "not selected target"},
		{name: "ALTER elsewhere", sql: "ALTER DATABASE other CHARACTER SET utf8mb4;", want: "not selected target"},
		{name: "DROP even target", sql: "DROP DATABASE target_db;", want: "never drops"},
		{name: "executable comment", sql: "/*!40101 USE other */;", want: "executable"},
		{name: "MariaDB executable comment", sql: "/*M!999999 DROP DATABASE other */;", want: "executable"},
		{name: "SOURCE local file", sql: "SOURCE /private/secret.sql;", want: "SOURCE"},
		{name: "system shell", sql: "system touch /tmp/unsafe\n", want: "SYSTEM"},
		{name: "short system shell", sql: "\\! touch /tmp/unsafe\n", want: "client command"},
	}

	t.Run("MySQL allows MariaDB sandbox header", func(t *testing.T) {
		path := writeInspectionFixture(t, "mariadb.sql", []byte("/*M!999999\\- enable the sandbox mode */\nCREATE TABLE t(id int);\n"))
		if err := validateMySQLSQLForRestore(path, "target_db"); err != nil {
			t.Fatalf("validateMySQLSQLForRestore() error = %v", err)
		}
	})
	for _, test := range mysqlBlocked {
		test := test
		t.Run("MySQL blocks "+test.name, func(t *testing.T) {
			t.Parallel()
			path := writeInspectionFixture(t, "blocked.sql", []byte(test.sql))
			err := validateMySQLSQLForRestore(path, "target_db")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}

	t.Run("PostgreSQL allows pg_dump guards and COPY data", func(t *testing.T) {
		data := []byte("--\n-- PostgreSQL database dump\n--\n\\restrict key\nCOPY public.t (v) FROM stdin;\n\\connect is data here\n\\.\n\\unrestrict key\n")
		path := writeInspectionFixture(t, "safe.sql", data)
		if err := validatePostgresSQLForRestore(path, "target"); err != nil {
			t.Fatalf("validatePostgresSQLForRestore() error = %v", err)
		}
	})

	for _, command := range []string{
		`\! id`,
		`\include /tmp/file.sql`,
		`\connect elsewhere`,
		`\copy t from '/tmp/data'`,
		`\g /tmp/query-output.txt`,
		`\lo_import /tmp/local-file`,
		`\edit /tmp/local-file`,
		`\setenv PSQL_EDITOR /tmp/program`,
	} {
		command := command
		t.Run("PostgreSQL blocks "+command, func(t *testing.T) {
			t.Parallel()
			path := writeInspectionFixture(t, "blocked.sql", []byte(command+"\n"))
			if err := validatePostgresSQLForRestore(path, "target"); err == nil || !strings.Contains(err.Error(), "blocked") {
				t.Fatalf("validation error = %v", err)
			}
		})
	}

	for _, test := range []struct {
		name string
		sql  string
	}{
		{name: "inline g pipe", sql: "SELECT 1 \\g |touch /tmp/unsafe\n"},
		{name: "inline shell", sql: "SELECT 1; \\! touch /tmp/unsafe\n"},
		{name: "command after long prefix", sql: strings.Repeat(" ", 40<<10) + "\\g |touch /tmp/unsafe\n"},
		{name: "inline guard", sql: "SELECT 1; \\restrict key\n"},
	} {
		test := test
		t.Run("PostgreSQL blocks "+test.name, func(t *testing.T) {
			path := writeInspectionFixture(t, "blocked-inline.sql", []byte(test.sql))
			if err := validatePostgresSQLForRestore(path, "target"); err == nil || !strings.Contains(err.Error(), "blocked") {
				t.Fatalf("validation error = %v", err)
			}
		})
	}

	t.Run("PostgreSQL ignores command text inside SQL lexical regions", func(t *testing.T) {
		data := "-- \\! comment\n" +
			"SELECT '\\g |not-a-command', \"\\!identifier\", $$\\lo_export 1 /tmp/x$$;\n" +
			"/* \\connect elsewhere */\n"
		path := writeInspectionFixture(t, "safe-lexical.sql", []byte(data))
		if err := validatePostgresSQLForRestore(path, "target"); err != nil {
			t.Fatalf("validatePostgresSQLForRestore() error = %v", err)
		}
	})
}

func TestExecuteSQLiteDatabaseRestoreStagesSnapshotsAndReplaces(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source.sqlite3")
	createRestoreSQLiteDatabase(t, sourcePath, `CREATE TABLE restored(id INTEGER PRIMARY KEY, value TEXT); INSERT INTO restored(value) VALUES ('new');`)
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(directory, "source.sqlite3.zst")
	if err := os.WriteFile(artifactPath, zstdFixture(t, sourceBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(context.Background(), artifactPath, InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}

	targetPath := filepath.Join(directory, "target.sqlite3")
	createRestoreSQLiteDatabase(t, targetPath, `CREATE TABLE original(id INTEGER PRIMARY KEY, value TEXT); INSERT INTO original(value) VALUES ('old');`)
	target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
	plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{Mode: RestoreModeClean, StopOnError: true, SingleTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	var progress []string
	if err := ExecuteRestore(context.Background(), plan, func(message string) { progress = append(progress, message) }); err != nil {
		t.Fatalf("ExecuteRestore() error = %v; progress = %v", err, progress)
	}
	if got := queryRestoreSQLiteString(t, targetPath, `SELECT value FROM restored LIMIT 1`); got != "new" {
		t.Fatalf("restored value = %q", got)
	}
	assertRestoreSQLiteQueryFails(t, targetPath, `SELECT value FROM original`)

	snapshots, err := filepath.Glob(targetPath + ".pre-restore-*.sqlite3")
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("pre-restore snapshots = %v, error = %v", snapshots, err)
	}
	if got := queryRestoreSQLiteString(t, snapshots[0], `SELECT value FROM original LIMIT 1`); got != "old" {
		t.Fatalf("snapshot value = %q", got)
	}
	stages, err := filepath.Glob(filepath.Join(directory, ".target.sqlite3.restore-*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("restore staging files left behind: %v (error %v)", stages, err)
	}
	if !containsString(progress, "pre-restore SQLite snapshot") || !containsString(progress, "completed") {
		t.Errorf("progress = %v", progress)
	}
}

func TestExecuteSQLiteDatabaseRestoreSupportsAgeAndGzip(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source.sqlite3")
	createRestoreSQLiteDatabase(t, sourcePath, `CREATE TABLE encrypted(v TEXT); INSERT INTO encrypted VALUES ('works');`)
	payload, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, identity := ageFixture(t, gzipFixture(t, payload), false)
	artifactPath := filepath.Join(directory, "source.sqlite3.gz.age")
	if err := os.WriteFile(artifactPath, ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(directory, "identity.txt")
	if err := os.WriteFile(identityPath, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(context.Background(), artifactPath, InspectOptions{AgeIdentityPath: identityPath})
	if err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(directory, "new-target.sqlite3")
	target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
	plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{Mode: RestoreModeClean, AgeIdentityPath: identityPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := ExecuteRestore(context.Background(), plan, nil); err != nil {
		t.Fatalf("ExecuteRestore() error = %v", err)
	}
	if got := queryRestoreSQLiteString(t, targetPath, `SELECT v FROM encrypted`); got != "works" {
		t.Fatalf("restored value = %q", got)
	}
	if snapshots, _ := filepath.Glob(targetPath + ".pre-restore-*.sqlite3"); len(snapshots) != 0 {
		t.Fatalf("new target unexpectedly has snapshot: %v", snapshots)
	}
}

func TestMaterializeRestorePayloadHonorsConfiguredDecodedLimit(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source.sqlite3")
	createRestoreSQLiteDatabase(t, sourcePath, `CREATE TABLE restored(v TEXT); INSERT INTO restored VALUES ('new');`)
	payload, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(directory, "source.sqlite3.gz")
	if err := os.WriteFile(artifactPath, gzipFixture(t, payload), 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(context.Background(), artifactPath, InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := materializeRestorePayload(context.Background(), inspection, RestoreOptions{MaxDecodedBytes: 64})
	if materialized != nil {
		materialized.cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "configured limit of 64 bytes") {
		t.Fatalf("materializeRestorePayload() error = %v, want configured decoded-size limit", err)
	}
}

func TestExecuteSQLiteRestoreRefusesChangedArtifactAndSidecars(t *testing.T) {
	t.Parallel()

	t.Run("artifact changed", func(t *testing.T) {
		directory := t.TempDir()
		artifactPath := filepath.Join(directory, "backup.sqlite3")
		createRestoreSQLiteDatabase(t, artifactPath, `CREATE TABLE restored(v TEXT);`)
		inspection, err := Inspect(context.Background(), artifactPath, InspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(artifactPath, append(mustReadRestoreFile(t, artifactPath), 0), 0o600); err != nil {
			t.Fatal(err)
		}
		targetPath := filepath.Join(directory, "target.sqlite3")
		createRestoreSQLiteDatabase(t, targetPath, `CREATE TABLE original(v TEXT); INSERT INTO original VALUES ('safe');`)
		target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
		plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{Mode: RestoreModeClean})
		if err != nil {
			t.Fatal(err)
		}
		err = ExecuteRestore(context.Background(), plan, nil)
		if err == nil || !strings.Contains(err.Error(), "size changed") {
			t.Fatalf("ExecuteRestore() error = %v", err)
		}
		if got := queryRestoreSQLiteString(t, targetPath, `SELECT v FROM original`); got != "safe" {
			t.Fatalf("target changed after rejected artifact: %q", got)
		}
	})

	t.Run("active sidecar", func(t *testing.T) {
		directory := t.TempDir()
		artifactPath := filepath.Join(directory, "backup.sqlite3")
		createRestoreSQLiteDatabase(t, artifactPath, `CREATE TABLE restored(v TEXT);`)
		inspection, err := Inspect(context.Background(), artifactPath, InspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		targetPath := filepath.Join(directory, "target.sqlite3")
		createRestoreSQLiteDatabase(t, targetPath, `CREATE TABLE original(v TEXT); INSERT INTO original VALUES ('safe');`)
		if err := os.WriteFile(targetPath+"-wal", []byte("active"), 0o600); err != nil {
			t.Fatal(err)
		}
		target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
		plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{Mode: RestoreModeClean})
		if err != nil {
			t.Fatal(err)
		}
		err = ExecuteRestore(context.Background(), plan, nil)
		if err == nil || !strings.Contains(err.Error(), "close every connection") {
			t.Fatalf("ExecuteRestore() error = %v", err)
		}
		if snapshots, _ := filepath.Glob(targetPath + ".pre-restore-*.sqlite3"); len(snapshots) != 0 {
			t.Fatalf("snapshot created despite active sidecar: %v", snapshots)
		}
	})
}

func TestExecuteSQLiteSQLRestoreClean(t *testing.T) {
	t.Parallel()
	requireSQLite3ForRestoreTest(t)

	path := writeInspectionFixture(t, "backup.sql", []byte("PRAGMA foreign_keys=OFF;\nBEGIN TRANSACTION;\nCREATE TABLE restored(v TEXT);\nINSERT INTO restored VALUES ('new');\nCOMMIT;\n"))
	inspection, err := Inspect(context.Background(), path, InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(t.TempDir(), "target.sqlite3")
	createRestoreSQLiteDatabase(t, targetPath, `CREATE TABLE original(v TEXT); INSERT INTO original VALUES ('old');`)
	target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
	plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{Mode: RestoreModeClean, StopOnError: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := ExecuteRestore(context.Background(), plan, nil); err != nil {
		t.Fatalf("ExecuteRestore() error = %v", err)
	}
	if got := queryRestoreSQLiteString(t, targetPath, `SELECT v FROM restored`); got != "new" {
		t.Fatalf("restored value = %q", got)
	}
	assertRestoreSQLiteQueryFails(t, targetPath, `SELECT v FROM original`)
	snapshots, _ := filepath.Glob(targetPath + ".pre-restore-*.sqlite3")
	if len(snapshots) != 1 || queryRestoreSQLiteString(t, snapshots[0], `SELECT v FROM original`) != "old" {
		t.Fatalf("verified pre-restore snapshot = %v", snapshots)
	}
}

func TestExecuteSQLiteSQLRestoreMerge(t *testing.T) {
	t.Parallel()
	requireSQLite3ForRestoreTest(t)

	path := writeInspectionFixture(t, "merge.sql", []byte("PRAGMA foreign_keys=OFF;\nBEGIN TRANSACTION;\nCREATE TABLE added(v TEXT);\nINSERT INTO added VALUES ('merged');\nCOMMIT;\n"))
	inspection, err := Inspect(context.Background(), path, InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(t.TempDir(), "target.sqlite3")
	createRestoreSQLiteDatabase(t, targetPath, `CREATE TABLE original(v TEXT); INSERT INTO original VALUES ('preserved');`)
	target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
	plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{Mode: RestoreModeMerge, StopOnError: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := ExecuteRestore(context.Background(), plan, nil); err != nil {
		t.Fatalf("ExecuteRestore() error = %v", err)
	}
	if got := queryRestoreSQLiteString(t, targetPath, `SELECT v FROM original`); got != "preserved" {
		t.Fatalf("original merge value = %q", got)
	}
	if got := queryRestoreSQLiteString(t, targetPath, `SELECT v FROM added`); got != "merged" {
		t.Fatalf("added merge value = %q", got)
	}
	snapshots, _ := filepath.Glob(targetPath + ".pre-restore-*.sqlite3")
	if len(snapshots) != 1 {
		t.Fatalf("merge snapshots = %v", snapshots)
	}
	assertRestoreSQLiteQueryFails(t, snapshots[0], `SELECT v FROM added`)
}

func TestExecuteSQLiteSQLFailureAndCancellationLeaveTargetUnchanged(t *testing.T) {
	t.Parallel()
	requireSQLite3ForRestoreTest(t)

	t.Run("client failure discards partial stage", func(t *testing.T) {
		directory := t.TempDir()
		path := writeInspectionFixture(t, "broken.sql", []byte("PRAGMA foreign_keys=OFF;\nBEGIN TRANSACTION;\nCREATE TABLE partial(v TEXT);\nTHIS IS NOT SQL;\nCOMMIT;\n"))
		inspection, err := Inspect(context.Background(), path, InspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		targetPath := filepath.Join(directory, "target.sqlite3")
		createRestoreSQLiteDatabase(t, targetPath, `CREATE TABLE original(v TEXT); INSERT INTO original VALUES ('safe');`)
		target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
		plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{Mode: RestoreModeClean})
		if err != nil {
			t.Fatal(err)
		}
		err = ExecuteRestore(context.Background(), plan, nil)
		if err == nil || !strings.Contains(err.Error(), "sqlite3 restore failed") {
			t.Fatalf("ExecuteRestore() error = %v", err)
		}
		if got := queryRestoreSQLiteString(t, targetPath, `SELECT v FROM original`); got != "safe" {
			t.Fatalf("target changed after SQL failure: %q", got)
		}
		stages, _ := filepath.Glob(filepath.Join(directory, ".target.sqlite3.sql-restore-*"))
		if len(stages) != 0 {
			t.Fatalf("partial SQL stages retained: %v", stages)
		}
		snapshots, _ := filepath.Glob(targetPath + ".pre-restore-*.sqlite3")
		if len(snapshots) != 1 || queryRestoreSQLiteString(t, snapshots[0], `SELECT v FROM original`) != "safe" {
			t.Fatalf("verified failure snapshot = %v", snapshots)
		}
	})

	t.Run("canceled context does not snapshot or replace", func(t *testing.T) {
		directory := t.TempDir()
		path := writeInspectionFixture(t, "backup.sql", []byte("PRAGMA foreign_keys=OFF;\nBEGIN TRANSACTION;\nCREATE TABLE replaced(v);\nCOMMIT;\n"))
		inspection, err := Inspect(context.Background(), path, InspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		targetPath := filepath.Join(directory, "target.sqlite3")
		createRestoreSQLiteDatabase(t, targetPath, `CREATE TABLE original(v TEXT); INSERT INTO original VALUES ('safe');`)
		target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
		plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{Mode: RestoreModeClean})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err = ExecuteRestore(ctx, plan, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ExecuteRestore() error = %v, want context.Canceled", err)
		}
		if got := queryRestoreSQLiteString(t, targetPath, `SELECT v FROM original`); got != "safe" {
			t.Fatalf("target changed after cancellation: %q", got)
		}
		if snapshots, _ := filepath.Glob(targetPath + ".pre-restore-*.sqlite3"); len(snapshots) != 0 {
			t.Fatalf("pre-canceled restore created snapshot: %v", snapshots)
		}
	})
}

func TestSQLiteRestoreCancellationAtPublishBoundaryLeavesTargetUnchanged(t *testing.T) {
	t.Parallel()

	t.Run("database file", func(t *testing.T) {
		directory := t.TempDir()
		artifact := filepath.Join(directory, "backup.sqlite3")
		createRestoreSQLiteDatabase(t, artifact, `CREATE TABLE restored(v TEXT); INSERT INTO restored VALUES ('new');`)
		inspection, err := Inspect(context.Background(), artifact, InspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		targetPath := filepath.Join(directory, "target.sqlite3")
		createRestoreSQLiteDatabase(t, targetPath, `CREATE TABLE original(v TEXT); INSERT INTO original VALUES ('safe');`)
		target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
		plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{Mode: RestoreModeClean})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		err = ExecuteRestore(ctx, plan, func(message string) {
			if strings.Contains(message, "Replacing the SQLite target") {
				cancel()
			}
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ExecuteRestore() error = %v, want context.Canceled", err)
		}
		if got := queryRestoreSQLiteString(t, targetPath, `SELECT v FROM original`); got != "safe" {
			t.Fatalf("target changed at cancellation boundary: %q", got)
		}
	})

	t.Run("SQL", func(t *testing.T) {
		requireSQLite3ForRestoreTest(t)
		directory := t.TempDir()
		artifact := writeInspectionFixture(t, "backup.sql", []byte("PRAGMA foreign_keys=OFF;\nBEGIN TRANSACTION;\nCREATE TABLE restored(v TEXT);\nCOMMIT;\n"))
		inspection, err := Inspect(context.Background(), artifact, InspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		targetPath := filepath.Join(directory, "target.sqlite3")
		createRestoreSQLiteDatabase(t, targetPath, `CREATE TABLE original(v TEXT); INSERT INTO original VALUES ('safe');`)
		target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
		plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{Mode: RestoreModeClean})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		err = ExecuteRestore(ctx, plan, func(message string) {
			if strings.Contains(message, "Replacing the SQLite target") {
				cancel()
			}
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ExecuteRestore() error = %v, want context.Canceled", err)
		}
		if got := queryRestoreSQLiteString(t, targetPath, `SELECT v FROM original`); got != "safe" {
			t.Fatalf("target changed at cancellation boundary: %q", got)
		}
	})
}

func TestExecuteSQLiteSQLRejectsUnsafeInputBeforeSnapshot(t *testing.T) {
	t.Parallel()

	path := writeInspectionFixture(t, "unsafe.sql", []byte("PRAGMA foreign_keys=OFF;\nBEGIN TRANSACTION;\n.shell echo unsafe\nCREATE TABLE replaced(v);\nCOMMIT;\n"))
	inspection, err := Inspect(context.Background(), path, InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(t.TempDir(), "target.sqlite3")
	createRestoreSQLiteDatabase(t, targetPath, `CREATE TABLE original(v TEXT); INSERT INTO original VALUES ('safe');`)
	target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
	plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{Mode: RestoreModeClean})
	if err != nil {
		t.Fatal(err)
	}
	err = ExecuteRestore(context.Background(), plan, nil)
	if err == nil || !strings.Contains(err.Error(), "dot command") {
		t.Fatalf("ExecuteRestore() error = %v", err)
	}
	if got := queryRestoreSQLiteString(t, targetPath, `SELECT v FROM original`); got != "safe" {
		t.Fatalf("unsafe SQL changed target: %q", got)
	}
	if snapshots, _ := filepath.Glob(targetPath + ".pre-restore-*.sqlite3"); len(snapshots) != 0 {
		t.Fatalf("unsafe SQL created a snapshot before rejection: %v", snapshots)
	}
}

// This test owns the package-level command hooks and therefore must remain
// non-parallel. It verifies real ExecuteRestore wiring without launching a
// database client or placing a password in argv/environment/progress output.
func TestExecuteExternalRestoreUsesPrivateCredentialsAndRedactsSecrets(t *testing.T) {
	originalFind := findRestoreTool
	originalRun := runRestoreInvocation
	defer func() {
		findRestoreTool = originalFind
		runRestoreInvocation = originalRun
	}()
	findRestoreTool = func(name string) (string, error) { return "/fake/" + name, nil }

	t.Run("PostgreSQL archive", func(t *testing.T) {
		secret := "postgres-secret-value"
		artifact := writeInspectionFixture(t, "backup.dump", append([]byte("PGDMP"), 1, 15, 0, 4))
		inspection, err := Inspect(context.Background(), artifact, InspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		target := restorePostgresTarget()
		target.Password = secret
		plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var credentialPath, payloadPath string
		runRestoreInvocation = func(_ context.Context, invocation restoreInvocation) error {
			if invocation.label != "pg_restore" {
				t.Fatalf("invocation label = %q", invocation.label)
			}
			joined := strings.Join(invocation.args, " ")
			if strings.Contains(joined, secret) || slices.Contains(invocation.args, "--clean") {
				t.Fatalf("unsafe pg_restore args = %v", invocation.args)
			}
			payloadPath = invocation.args[len(invocation.args)-1]
			if _, err := os.Stat(payloadPath); err != nil {
				t.Fatalf("staged payload unavailable during invocation: %v", err)
			}
			credentialPath = environmentValue(invocation.env, "PGPASSFILE")
			if credentialPath == "" || environmentValue(invocation.env, "PGPASSWORD") != "" {
				t.Fatalf("PostgreSQL credential environment = %v", invocation.env)
			}
			credential := string(mustReadRestoreFile(t, credentialPath))
			if !strings.Contains(credential, secret) {
				t.Fatal("private pgpass file did not contain expected credential")
			}
			return fmt.Errorf("client repeated %s", secret)
		}
		var progress []string
		err = ExecuteRestore(context.Background(), plan, func(message string) { progress = append(progress, message) })
		if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[redacted]") {
			t.Fatalf("redacted restore error = %v", err)
		}
		if strings.Contains(strings.Join(progress, " "), secret) {
			t.Fatalf("progress leaked password: %v", progress)
		}
		for _, path := range []string{credentialPath, payloadPath} {
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Errorf("private restore file was not removed: %s (%v)", path, statErr)
			}
		}
	})

	t.Run("MySQL SQL", func(t *testing.T) {
		secret := "mysql-secret-value"
		artifact := writeInspectionFixture(t, "backup.sql", []byte("-- MySQL dump 10.13\nUSE `target_db`;\nCREATE TABLE t(id int);\n"))
		inspection, err := Inspect(context.Background(), artifact, InspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		target := restoreMySQLTarget()
		target.Password = secret
		plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var credentialPath, payloadPath string
		runRestoreInvocation = func(_ context.Context, invocation restoreInvocation) error {
			if invocation.label != "mysql" || invocation.inputPath == "" {
				t.Fatalf("mysql invocation = %#v", invocation)
			}
			payloadPath = invocation.inputPath
			if strings.Contains(strings.Join(invocation.args, " "), secret) || environmentValue(invocation.env, "MYSQL_PWD") != "" {
				t.Fatalf("MySQL secret reached argv/environment: args=%v env=%v", invocation.args, invocation.env)
			}
			prefix := "--defaults-extra-file="
			if len(invocation.args) == 0 || !strings.HasPrefix(invocation.args[0], prefix) {
				t.Fatalf("mysql args = %v", invocation.args)
			}
			credentialPath = strings.TrimPrefix(invocation.args[0], prefix)
			if !strings.Contains(string(mustReadRestoreFile(t, credentialPath)), secret) {
				t.Fatal("private MySQL defaults file did not contain expected credential")
			}
			return nil
		}
		if err := ExecuteRestore(context.Background(), plan, nil); err != nil {
			t.Fatalf("ExecuteRestore() error = %v", err)
		}
		for _, path := range []string{credentialPath, payloadPath} {
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Errorf("private restore file was not removed: %s (%v)", path, statErr)
			}
		}
	})

	t.Run("SQLite client cancellation discards stage", func(t *testing.T) {
		directory := t.TempDir()
		artifact := writeInspectionFixture(t, "backup.sql", []byte("PRAGMA foreign_keys=OFF;\nBEGIN TRANSACTION;\nCREATE TABLE restored(v);\nCOMMIT;\n"))
		inspection, err := Inspect(context.Background(), artifact, InspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		targetPath := filepath.Join(directory, "target.sqlite3")
		createRestoreSQLiteDatabase(t, targetPath, `CREATE TABLE original(v TEXT); INSERT INTO original VALUES ('safe');`)
		target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
		plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{Mode: RestoreModeClean})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		var stagePath string
		runRestoreInvocation = func(commandContext context.Context, invocation restoreInvocation) error {
			if invocation.label != "sqlite3" || len(invocation.args) == 0 {
				t.Fatalf("SQLite invocation = %#v", invocation)
			}
			stagePath = invocation.args[len(invocation.args)-1]
			database, err := sql.Open("sqlite", stagePath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TABLE partial(v TEXT);`); err != nil {
				_ = database.Close()
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			cancel()
			<-commandContext.Done()
			return commandContext.Err()
		}
		err = ExecuteRestore(ctx, plan, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ExecuteRestore() error = %v, want context.Canceled", err)
		}
		if got := queryRestoreSQLiteString(t, targetPath, `SELECT v FROM original`); got != "safe" {
			t.Fatalf("target changed after client cancellation: %q", got)
		}
		if _, statErr := os.Stat(stagePath); !os.IsNotExist(statErr) {
			t.Fatalf("canceled SQLite stage retained: %s (%v)", stagePath, statErr)
		}
		snapshots, _ := filepath.Glob(targetPath + ".pre-restore-*.sqlite3")
		if len(snapshots) != 1 || queryRestoreSQLiteString(t, snapshots[0], `SELECT v FROM original`) != "safe" {
			t.Fatalf("canceled restore snapshot = %v", snapshots)
		}
	})
}

func TestRestoreTailBufferKeepsBoundedSuffix(t *testing.T) {
	t.Parallel()

	buffer := &restoreTailBuffer{limit: 5}
	_, _ = buffer.Write([]byte("abc"))
	_, _ = buffer.Write([]byte("defg"))
	if got := buffer.String(); got != "cdefg" {
		t.Fatalf("tail = %q, want cdefg", got)
	}
	_, _ = buffer.Write([]byte("123456"))
	if got := buffer.String(); got != "23456" {
		t.Fatalf("tail = %q, want 23456", got)
	}
}

func TestSQLiteSQLSafetyScanBlocksFilesystemEscapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sql  string
		want string
	}{
		{name: "dot command", sql: "  .shell echo unsafe\n", want: "dot command"},
		{name: "dot command after BOM", sql: "\ufeff.load /tmp/plugin\n", want: "dot command"},
		{name: "attach", sql: "ATTACH DATABASE '/tmp/other.sqlite' AS other;", want: "ATTACH"},
		{name: "detach", sql: "DETACH DATABASE main;", want: "DETACH"},
		{name: "vacuum into", sql: "VACUUM main INTO '/tmp/copy.sqlite';", want: "VACUUM INTO"},
		{name: "extension load", sql: "SELECT load_extension('/tmp/plugin');", want: "LOAD_EXTENSION"},
		{name: "quoted extension load", sql: "SELECT \"load_extension\"('/tmp/plugin');", want: "LOAD_EXTENSION"},
		{name: "read file", sql: "SELECT readfile('/etc/passwd');", want: "READFILE"},
		{name: "write file", sql: "SELECT writefile('/tmp/output', 'x');", want: "WRITEFILE"},
		{name: "file virtual table", sql: "CREATE VIRTUAL TABLE files USING csv(filename='/etc/passwd');", want: "CSV"},
		{name: "filesystem pragma", sql: "PRAGMA temp_store_directory='/tmp';", want: "TEMP_STORE_DIRECTORY"},
		{name: "open transaction", sql: "BEGIN TRANSACTION; CREATE TABLE partial(v);", want: "open transaction"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := writeInspectionFixture(t, "unsafe.sql", []byte(test.sql))
			err := validateSQLiteSQLForRestore(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateSQLiteSQLForRestore() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSQLiteSQLSafetyScanAllowsDataAndBuiltInVirtualTables(t *testing.T) {
	t.Parallel()

	data := "-- ATTACH DATABASE '/tmp/comment' AS x;\n" +
		"BEGIN TRANSACTION;\n" +
		"CREATE TABLE notes(v TEXT);\n" +
		"INSERT INTO notes VALUES ('readfile(''/etc/passwd''); .shell ignored');\n" +
		"CREATE TRIGGER notes_ai AFTER INSERT ON notes BEGIN UPDATE notes SET v=v WHERE rowid=new.rowid; END;\n" +
		"CREATE VIRTUAL TABLE docs USING fts5(body);\n" +
		"COMMIT;\n" +
		"VACUUM;\n" +
		"SELECT .5;\n"
	path := writeInspectionFixture(t, "safe.sql", []byte(data))
	if err := validateSQLiteSQLForRestore(path); err != nil {
		t.Fatalf("validateSQLiteSQLForRestore() error = %v", err)
	}
}

func TestClientToolCandidatesCoverServicePaths(t *testing.T) {
	t.Parallel()

	postgres := clientToolCandidates("darwin", "pg_restore")
	for _, expected := range []string{"/opt/homebrew/bin/pg_restore", "/opt/homebrew/opt/libpq/bin/pg_restore", "/usr/local/opt/libpq/bin/pg_restore", "/opt/local/bin/pg_restore"} {
		if !slices.Contains(postgres, expected) {
			t.Errorf("PostgreSQL candidates %v missing %s", postgres, expected)
		}
	}
	mysql := clientToolCandidates("darwin", "mysql")
	if !slices.Contains(mysql, "/opt/homebrew/opt/mysql-client/bin/mysql") {
		t.Errorf("MySQL candidates = %v", mysql)
	}
	sqlite := clientToolCandidates("darwin", "sqlite3")
	if !slices.Contains(sqlite, "/opt/homebrew/opt/sqlite/bin/sqlite3") {
		t.Errorf("SQLite candidates = %v", sqlite)
	}
	if candidates := clientToolCandidates("windows", "sqlite3"); len(candidates) != 0 {
		t.Errorf("Windows fixed candidates = %v, want PATH-only", candidates)
	}
	windowsRoot := filepath.Join(string(filepath.Separator), "Program Files")
	if patterns := windowsClientToolPatterns(windowsRoot, "pg_dump", "pg_dump.exe"); !slices.Contains(patterns, filepath.Join(windowsRoot, "PostgreSQL", "*", "bin", "pg_dump.exe")) {
		t.Errorf("Windows PostgreSQL patterns = %v", patterns)
	}
	if patterns := windowsClientToolPatterns(windowsRoot, "mysql", "mysql.exe"); !slices.Contains(patterns, filepath.Join(windowsRoot, "MySQL", "MySQL Server *", "bin", "mysql.exe")) {
		t.Errorf("Windows MySQL patterns = %v", patterns)
	}
}

func TestUsableClientToolModeDoesNotRequireUnixExecuteBitsOnWindows(t *testing.T) {
	mode := os.FileMode(0o600)
	if !usableClientToolMode("windows", mode) {
		t.Fatal("regular Windows executable mode was rejected without Unix execute bits")
	}
	if usableClientToolMode("darwin", mode) {
		t.Fatal("non-Windows executable mode was accepted without Unix execute bits")
	}
	if usableClientToolMode("windows", os.ModeDir|0o700) {
		t.Fatal("Windows directory was accepted as a client executable")
	}
}

func TestNaturalPathCompareOrdersVersionDirectoriesNumerically(t *testing.T) {
	if naturalPathCompare(`c:\program files\postgresql\17\bin\pg_dump.exe`, `c:\program files\postgresql\9.6\bin\pg_dump.exe`) <= 0 {
		t.Fatal("PostgreSQL 17 did not sort newer than 9.6")
	}
	if naturalPathCompare(`c:\program files\mysql\mysql server 8.4\bin\mysql.exe`, `c:\program files\mysql\mysql server 8.0\bin\mysql.exe`) <= 0 {
		t.Fatal("MySQL 8.4 did not sort newer than 8.0")
	}
	if naturalPathCompare("same-01", "same-1") != 0 {
		t.Fatal("equivalent zero-padded numeric runs did not compare equally")
	}
}

func restoreTestInspection(format Format, engine config.DBType) *Inspection {
	confidence := ConfidenceExact
	if format == FormatPostgresSQL || format == FormatMySQLSQL || format == FormatSQLiteSQL || format == FormatGenericSQL {
		confidence = ConfidenceStrong
	}
	if format == FormatGenericSQL {
		confidence = ConfidenceAmbiguous
	}
	if format == FormatUnknown {
		confidence = ConfidenceUnknown
	}
	return &Inspection{
		Path:       filepath.Join(os.TempDir(), "dbterm-restore-test-artifact"),
		Size:       10,
		SHA256:     strings.Repeat("0", 64),
		Format:     format,
		Engine:     engine,
		Confidence: confidence,
		Evidence:   []string{"fixture"},
	}
}

func withRestoreInspection(mutate func(*Inspection)) *Inspection {
	value := restoreTestInspection(FormatPostgresCustom, config.PostgreSQL)
	mutate(value)
	return value
}

func restorePostgresTarget() config.ConnectionConfig {
	return config.ConnectionConfig{
		Type: config.PostgreSQL, Host: "db.example", Port: "5432", User: "restore_user",
		Database: "target_db", Password: "s3cr3t-postgres-value", SSLMode: "require",
	}
}

func restoreMySQLTarget() config.ConnectionConfig {
	return config.ConnectionConfig{
		Type: config.MySQL, Host: "db.example", Port: "3306", User: "restore_user",
		Database: "target_db", Password: "s3cr3t-mysql-value",
	}
}

func createRestoreSQLiteDatabase(t *testing.T, path, statements string) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(statements); err != nil {
		_ = database.Close()
		t.Fatalf("create SQLite fixture: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func queryRestoreSQLiteString(t *testing.T, path, query string) string {
	t.Helper()
	database, err := sql.Open("sqlite", sqliteFileDSN(path, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var value string
	if err := database.QueryRow(query).Scan(&value); err != nil {
		t.Fatalf("query SQLite fixture: %v", err)
	}
	return value
}

func assertRestoreSQLiteQueryFails(t *testing.T, path, query string) {
	t.Helper()
	database, err := sql.Open("sqlite", sqliteFileDSN(path, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.QueryRow(query).Scan(new(string)); err == nil {
		t.Fatalf("query unexpectedly succeeded: %s", query)
	}
}

func mustReadRestoreFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func environmentValue(environment []string, name string) string {
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func requireSQLite3ForRestoreTest(t *testing.T) {
	t.Helper()
	if _, err := requireClientTool("sqlite3"); err != nil {
		t.Skipf("sqlite3 integration test skipped: %v", err)
	}
}
