package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestBundleRestoreDefaultsToEmbeddedDatabaseOnly(t *testing.T) {
	t.Parallel()
	inspection := createRestoreBundleFixture(t)
	targetPath := filepath.Join(t.TempDir(), "target.sqlite3")
	target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
	plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{Mode: RestoreModeClean})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.IncludedFileSets) != 1 || len(plan.FileSetTargets) != 0 || len(plan.Options.FileSetTargets) != 0 {
		t.Fatalf("database-only bundle plan = %+v", plan)
	}
	if !containsString(plan.Warnings, "database-only") {
		t.Fatalf("bundle plan warnings = %v, want database-only warning", plan.Warnings)
	}
	if err := ExecuteRestore(context.Background(), plan, nil); err != nil {
		t.Fatalf("ExecuteRestore() database-only bundle error = %v", err)
	}
	if got := queryRestoreSQLiteString(t, targetPath, `SELECT value FROM restored`); got != "from bundle" {
		t.Fatalf("restored embedded database value = %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(targetPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "settings.json" || entry.Name() == "avatar.txt" {
			t.Fatalf("database-only restore unexpectedly extracted %s", entry.Name())
		}
	}
}

func TestBundleRestorePublishesSelectedFilesToExplicitRoot(t *testing.T) {
	t.Parallel()
	inspection := createRestoreBundleFixture(t)
	directory := t.TempDir()
	targetPath := filepath.Join(directory, "target.sqlite3")
	fileRoot := filepath.Join(directory, "isolated-files")
	target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
	plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{
		Mode:           RestoreModeClean,
		FileSetTargets: []RestoreFileSetTarget{{Label: "application", Root: fileRoot}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.FileSetTargets) != 1 || plan.FileSetTargets[0].Root != fileRoot || plan.FileSetTargets[0].FileCount != 2 {
		t.Fatalf("file-set restore plan = %+v", plan.FileSetTargets)
	}
	if err := ExecuteRestore(context.Background(), plan, nil); err != nil {
		t.Fatalf("ExecuteRestore() bundle files error = %v", err)
	}
	if got := string(mustReadRestoreFile(t, filepath.Join(fileRoot, "settings.json"))); got != `{"enabled":true}` {
		t.Fatalf("settings restore = %q", got)
	}
	if got := string(mustReadRestoreFile(t, filepath.Join(fileRoot, "uploads", "avatar.txt"))); got != "avatar" {
		t.Fatalf("nested file restore = %q", got)
	}
}

func TestBundleRestoreCollisionStopsBeforeDatabaseRestore(t *testing.T) {
	t.Parallel()
	inspection := createRestoreBundleFixture(t)
	directory := t.TempDir()
	targetPath := filepath.Join(directory, "target.sqlite3")
	createRestoreSQLiteDatabase(t, targetPath, `CREATE TABLE original(value TEXT); INSERT INTO original VALUES ('preserved');`)
	fileRoot := filepath.Join(directory, "isolated-files")
	if err := os.MkdirAll(fileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	collision := filepath.Join(fileRoot, "settings.json")
	if err := os.WriteFile(collision, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
	plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{
		Mode:           RestoreModeClean,
		FileSetTargets: []RestoreFileSetTarget{{Label: "application", Root: fileRoot}},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = ExecuteRestore(context.Background(), plan, nil)
	if err == nil || !strings.Contains(err.Error(), "overwrite is disabled") {
		t.Fatalf("collision error = %v", err)
	}
	if got := queryRestoreSQLiteString(t, targetPath, `SELECT value FROM original`); got != "preserved" {
		t.Fatalf("database changed despite file collision preflight: %q", got)
	}
	if got := string(mustReadRestoreFile(t, collision)); got != "existing" {
		t.Fatalf("collision was overwritten without approval: %q", got)
	}
}

func TestBundleRestoreExplicitOverwriteReplacesOnlyRegularFiles(t *testing.T) {
	t.Parallel()
	inspection := createRestoreBundleFixture(t)
	directory := t.TempDir()
	targetPath := filepath.Join(directory, "target.sqlite3")
	fileRoot := filepath.Join(directory, "isolated-files")
	if err := os.MkdirAll(fileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fileRoot, "settings.json"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
	plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{
		Mode: RestoreModeClean, OverwriteFileSetFiles: true,
		FileSetTargets: []RestoreFileSetTarget{{Label: "application", Root: fileRoot}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ExecuteRestore(context.Background(), plan, nil); err != nil {
		t.Fatal(err)
	}
	if got := string(mustReadRestoreFile(t, filepath.Join(fileRoot, "settings.json"))); got != `{"enabled":true}` {
		t.Fatalf("explicit overwrite result = %q", got)
	}
}

func TestBundleRestoreRejectsTraversalReservedNamesAndUnsafeTargets(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, relative := range []string{"../escape", "safe/../../escape", "C:/alternate", "uploads/CON.txt", "trailing./file"} {
		if _, err := restoreTargetPath(root, relative); err == nil {
			t.Errorf("restoreTargetPath(%q) unexpectedly succeeded", relative)
		}
	}

	existingDirectory := filepath.Join(root, "directory-collision")
	if err := os.Mkdir(existingDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	file := stagedRestoreFile{label: "application", targetRoot: root, targetPath: existingDirectory}
	if err := preflightRestoreFilePublications([]stagedRestoreFile{file}, true); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("directory collision error = %v", err)
	}

	link := filepath.Join(root, "link-target")
	if err := os.Symlink(filepath.Join(root, "missing"), link); err == nil {
		file.targetPath = link
		if err := preflightRestoreFilePublications([]stagedRestoreFile{file}, true); err == nil || !strings.Contains(err.Error(), "link") {
			t.Fatalf("link collision error = %v", err)
		}
	}
}

func TestBundleRestoreRejectsTraversalManifestAndHardlinkEntry(t *testing.T) {
	t.Parallel()
	native := createRunnerSQLiteFixture(t, t.TempDir(), "native.sqlite3")
	nativeBytes := mustReadRestoreFile(t, native)
	nativeDigest := sha256.Sum256(nativeBytes)
	fileDigest := sha256.Sum256([]byte("data"))
	baseManifest := DBTermBundleManifest{
		SchemaVersion: DBTermBundleSchemaVersion,
		Database: DBTermBundleDatabase{
			Engine: config.SQLite, Format: string(FormatSQLiteDatabase), Path: "dbterm/database/native.sqlite3",
			SizeBytes: int64(len(nativeBytes)), SHA256: hex.EncodeToString(nativeDigest[:]),
		},
		FileSets: []DBTermBundleFileSet{{
			Label: "files", Required: true, FileCount: 1, SizeBytes: 4,
			Files: []DBTermBundleFile{{Path: "safe.txt", SizeBytes: 4, SHA256: hex.EncodeToString(fileDigest[:])}},
		}},
	}

	traversal := baseManifest
	traversal.FileSets = append([]DBTermBundleFileSet(nil), baseManifest.FileSets...)
	traversal.FileSets[0].Files = []DBTermBundleFile{{Path: "../escape", SizeBytes: 4, SHA256: hex.EncodeToString(fileDigest[:])}}
	traversalPath := writeRawRestoreBundle(t, traversal, nativeBytes, tar.TypeReg)
	assertUnsafeBundleRestoreRejected(t, traversalPath, "traversal")

	hardlinkPath := writeRawRestoreBundle(t, baseManifest, nativeBytes, tar.TypeLink)
	assertUnsafeBundleRestoreRejected(t, hardlinkPath, "path, type, and size")

	symlinkPath := writeRawRestoreBundle(t, baseManifest, nativeBytes, tar.TypeSymlink)
	assertUnsafeBundleRestoreRejected(t, symlinkPath, "path, type, and size")
}

func TestBundleRestorePlanEnforcesSelectedLimitsAndCopiesTargets(t *testing.T) {
	t.Parallel()
	inspection := createRestoreBundleFixture(t)
	target := config.ConnectionConfig{Type: config.SQLite, FilePath: filepath.Join(t.TempDir(), "target.sqlite3")}
	root := filepath.Join(t.TempDir(), "files")
	options := RestoreOptions{
		Mode: RestoreModeClean, MaxFileSetFiles: 1,
		FileSetTargets: []RestoreFileSetTarget{{Label: "application", Root: root}},
	}
	if _, err := BuildRestorePlan(inspection, &target, options); err == nil || !strings.Contains(err.Error(), "file restore limit") {
		t.Fatalf("file count limit error = %v", err)
	}
	options.MaxFileSetFiles = 10
	options.MaxFileSetBytes = 1
	if _, err := BuildRestorePlan(inspection, &target, options); err == nil || !strings.Contains(err.Error(), "byte restore limit") {
		t.Fatalf("byte limit error = %v", err)
	}

	options.MaxFileSetBytes = 1 << 20
	plan, err := BuildRestorePlan(inspection, &target, options)
	if err != nil {
		t.Fatal(err)
	}
	options.FileSetTargets[0].Root = filepath.Join(t.TempDir(), "mutated")
	inspection.FileSets[0].Label = "mutated"
	if plan.FileSetTargets[0].Root != root || plan.Options.FileSetTargets[0].Root != root || plan.IncludedFileSets[0].Label != "application" {
		t.Fatal("restore plan retained mutable file-set inputs")
	}
}

func TestBundleRestoreMaterializesEverySupportedEmbeddedDatabaseFormat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		format Format
		engine config.DBType
		data   []byte
	}{
		{name: "PostgreSQL custom", format: FormatPostgresCustom, engine: config.PostgreSQL, data: append([]byte("PGDMP"), 1, 15, 0, 4)},
		{name: "PostgreSQL tar", format: FormatPostgresTar, engine: config.PostgreSQL, data: postgresTarFixture(t)},
		{name: "PostgreSQL SQL", format: FormatPostgresSQL, engine: config.PostgreSQL, data: []byte("-- PostgreSQL database dump\nSET client_encoding = 'UTF8';\n")},
		{name: "MySQL SQL", format: FormatMySQLSQL, engine: config.MySQL, data: []byte("-- MySQL dump 10.13\nCREATE TABLE `items` (`id` int);\n")},
		{name: "SQLite database", format: FormatSQLiteDatabase, engine: config.SQLite, data: sqliteFixture(512)},
		{name: "SQLite SQL", format: FormatSQLiteSQL, engine: config.SQLite, data: []byte("PRAGMA foreign_keys=OFF;\nBEGIN TRANSACTION;\nCREATE TABLE items(id INTEGER);\nCOMMIT;\n")},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			nativeDigest := sha256.Sum256(test.data)
			fileDigest := sha256.Sum256([]byte("data"))
			manifest := DBTermBundleManifest{
				SchemaVersion: DBTermBundleSchemaVersion,
				Database: DBTermBundleDatabase{
					Engine: test.engine, Format: string(test.format), Path: "dbterm/database/native",
					SizeBytes: int64(len(test.data)), SHA256: hex.EncodeToString(nativeDigest[:]),
				},
				FileSets: []DBTermBundleFileSet{{
					Label: "files", Required: true, FileCount: 1, SizeBytes: 4,
					Files: []DBTermBundleFile{{Path: "safe.txt", SizeBytes: 4, SHA256: hex.EncodeToString(fileDigest[:])}},
				}},
			}
			artifactPath := writeRawRestoreBundle(t, manifest, test.data, tar.TypeReg)
			outer := mustReadRestoreFile(t, artifactPath)
			outerDigest := sha256.Sum256(outer)
			inspection := &Inspection{
				Path: artifactPath, Size: int64(len(outer)), SHA256: hex.EncodeToString(outerDigest[:]),
				Format: FormatDBTermBundle, DatabaseFormat: test.format, Engine: test.engine, Confidence: ConfidenceExact,
				FileSets: []ManifestFileSet{{Label: "files", FileCount: 1, SizeBytes: 4, Consistency: FileSetConsistencyBestEffort}},
			}
			payload, err := materializeRestorePayload(context.Background(), inspection, RestoreOptions{})
			if err != nil {
				t.Fatalf("materializeRestorePayload() error = %v", err)
			}
			defer payload.cleanup()
			if got := mustReadRestoreFile(t, payload.path); !bytes.Equal(got, test.data) {
				t.Fatalf("database client payload differs from embedded %s bytes", test.format)
			}
			if len(payload.restoreFiles) != 0 {
				t.Fatalf("database-only materialization staged %d bundled files", len(payload.restoreFiles))
			}
		})
	}
}

func createRestoreBundleFixture(t *testing.T) *Inspection {
	t.Helper()
	native := filepath.Join(t.TempDir(), "native.sqlite3")
	createRestoreSQLiteDatabase(t, native, `CREATE TABLE restored(value TEXT); INSERT INTO restored VALUES ('from bundle');`)
	root := t.TempDir()
	writeFileSetTestFile(t, root, "settings.json", `{"enabled":true}`)
	writeFileSetTestFile(t, root, "uploads/avatar.txt", "avatar")
	cfg := &config.ConnectionConfig{Name: "bundle", Type: config.SQLite, FilePath: native}
	plan, err := PlanFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := buildDBTermBundle(context.Background(), t.TempDir(), native, cfg, plan, []FileSet{{
		Label: "application", Root: root, Include: []string{"**"}, Required: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(context.Background(), bundle.path, InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return inspection
}

func writeRawRestoreBundle(t *testing.T, manifest DBTermBundleManifest, native []byte, fileType byte) string {
	t.Helper()
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range []struct {
		header tar.Header
		data   []byte
	}{
		{header: tar.Header{Name: dbtermBundleManifestPath, Mode: 0o600, Typeflag: tar.TypeReg, Size: int64(len(manifestBytes))}, data: manifestBytes},
		{header: tar.Header{Name: manifest.Database.Path, Mode: 0o600, Typeflag: tar.TypeReg, Size: int64(len(native))}, data: native},
		{header: tar.Header{Name: "dbterm/files/files/safe.txt", Mode: 0o600, Typeflag: fileType, Size: 4}, data: []byte("data")},
	} {
		if entry.header.Typeflag == tar.TypeLink || entry.header.Typeflag == tar.TypeSymlink {
			entry.header.Size = 0
			entry.header.Linkname = "elsewhere"
			entry.data = nil
		}
		if err := writer.WriteHeader(&entry.header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "unsafe.dbterm")
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertUnsafeBundleRestoreRejected(t *testing.T, artifactPath, want string) {
	t.Helper()
	data := mustReadRestoreFile(t, artifactPath)
	digest := sha256.Sum256(data)
	inspection := &Inspection{
		Path: artifactPath, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]),
		Format: FormatDBTermBundle, DatabaseFormat: FormatSQLiteDatabase, Engine: config.SQLite,
		Confidence: ConfidenceExact,
		FileSets:   []ManifestFileSet{{Label: "files", FileCount: 1, SizeBytes: 4, Consistency: FileSetConsistencyBestEffort}},
	}
	targetPath := filepath.Join(t.TempDir(), "target.sqlite3")
	createRestoreSQLiteDatabase(t, targetPath, `CREATE TABLE original(value TEXT); INSERT INTO original VALUES ('safe');`)
	target := config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
	plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{Mode: RestoreModeClean})
	if err != nil {
		t.Fatal(err)
	}
	err = ExecuteRestore(context.Background(), plan, nil)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("unsafe bundle restore error = %v, want %q", err, want)
	}
	if got := queryRestoreSQLiteString(t, targetPath, `SELECT value FROM original`); got != "safe" {
		t.Fatalf("unsafe bundle changed target database: %q", got)
	}
}
