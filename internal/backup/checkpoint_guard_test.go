package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shreyam1008/dbterm/internal/config"
)

const backupCheckpointToken = "0123456789abcdef01234567"

func TestSQLiteBackupRejectsSourceCheckpointBeforeSnapshot(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sqlite3")
	source := backupCheckpointSQLiteFixture(t, sourcePath, true)
	output := filepath.Join(root, "output.sqlite3")
	err := runSQLiteSnapshot(context.Background(), &config.ConnectionConfig{Type: config.SQLite, FilePath: sourcePath}, output)
	assertCheckpointBackupRefusal(t, err)
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("rejected backup created snapshot: %v", err)
	}
	var count int
	if err := source.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name=?`, "__dbterm_cp_"+backupCheckpointToken).Scan(&count); err != nil || count != 1 {
		t.Fatalf("backup modified the source checkpoint: count %d, error %v", count, err)
	}
}

func TestSQLiteBackupRejectsCheckpointInsideCapturedSnapshot(t *testing.T) {
	root := t.TempDir()
	source := backupCheckpointSQLiteFixture(t, filepath.Join(root, "source.sqlite3"), true)
	snapshot := filepath.Join(root, "captured.sqlite3")
	if _, err := source.Exec(`VACUUM INTO ` + sqliteLiteral(snapshot)); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`DROP TRIGGER checkpoint_capture; DROP TABLE "__dbterm_cp_` + backupCheckpointToken + `";`); err != nil {
		t.Fatal(err)
	}
	// Even if the source journal disappears before a post-capture check, the
	// immutable captured database still contains its recording objects.
	if err := checkBackupSourceJournals(context.Background(), source, config.SQLite); err != nil {
		t.Fatal(err)
	}
	assertCheckpointBackupRefusal(t, verifyNativeBackup(context.Background(), &config.ConnectionConfig{Type: config.SQLite}, snapshot))
}

func TestTursoLogicalBackupRejectsCheckpointInFrozenSchema(t *testing.T) {
	source := backupCheckpointSQLiteFixture(t, filepath.Join(t.TempDir(), "source.sqlite3"), true)
	output := filepath.Join(t.TempDir(), "dump.sql")
	assertCheckpointBackupRefusal(t, writeSQLiteCompatibleDump(context.Background(), source, config.Turso, output))
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("rejected logical backup kept partial SQL: %v", err)
	}
}

func TestSQLiteBackupRejectsOrphanedCheckpointTrigger(t *testing.T) {
	source := backupCheckpointSQLiteFixture(t, filepath.Join(t.TempDir(), "source.sqlite3"), false)
	name := "__dbterm_cp_" + backupCheckpointToken + "_0_insert"
	if _, err := source.Exec(`CREATE TRIGGER "` + name + `" AFTER INSERT ON users BEGIN INSERT INTO missing_journal(value) VALUES(NEW.name); END;`); err != nil {
		t.Fatal(err)
	}
	assertCheckpointBackupRefusal(t, checkBackupSourceJournals(context.Background(), source, config.SQLite))
	output := filepath.Join(t.TempDir(), "dump.sql")
	assertCheckpointBackupRefusal(t, writeSQLiteCompatibleDump(context.Background(), source, config.Turso, output))
}

func TestSQLiteBackupAllowsSimilarUserTableNames(t *testing.T) {
	source := backupCheckpointSQLiteFixture(t, filepath.Join(t.TempDir(), "source.sqlite3"), false)
	if _, err := source.Exec(`CREATE TABLE __dbterm_cp_notes(value TEXT);`); err != nil {
		t.Fatal(err)
	}
	if err := checkBackupSourceJournals(context.Background(), source, config.SQLite); err != nil {
		t.Fatalf("similar user name was treated as checkpoint state: %v", err)
	}
}

func TestMySQLBackupDetectsCapturedCheckpointDeclarations(t *testing.T) {
	name := "_dbterm_cp_" + backupCheckpointToken + "_events"
	for _, declaration := range []string{
		"CREATE TABLE `" + name + "` (seq BIGINT);",
		"CREATE TABLE IF NOT EXISTS `" + name + "` (seq BIGINT);",
		"CREATE TABLE `app`.`" + name + "` (seq BIGINT);",
		"CREATE TABLE app." + name + " (seq BIGINT);",
		"/*!50003 CREATE TABLE `" + name + "` (seq BIGINT) */;",
		"CREATE TRIGGER `_dbterm_cp_" + backupCheckpointToken + "_0_i` AFTER INSERT ON users FOR EACH ROW SET @x=1;",
		"/*!50003 CREATE*/ /*!50017 DEFINER=`root`@`localhost`*/ /*!50003 TRIGGER `_dbterm_cp_" + backupCheckpointToken + "_0_i` AFTER INSERT ON users FOR EACH ROW SET @x=1 */;",
	} {
		path := writeInspectionFixture(t, "mysql.sql", []byte("-- MySQL dump 10.13\n"+declaration+"\n"))
		assertCheckpointBackupRefusal(t, checkMySQLBackupJournals(context.Background(), path, "app"))
	}
	ordinary := "-- MySQL dump 10.13\n" +
		"-- CREATE TABLE `" + name + "` (seq BIGINT);\n" +
		"CREATE TABLE users(note TEXT DEFAULT '" + name + "');\n" +
		"INSERT INTO users VALUES ('CREATE TABLE `" + name + "` (seq BIGINT);');\n"
	path := writeInspectionFixture(t, "ordinary.sql", []byte(ordinary))
	if err := checkMySQLBackupJournals(context.Background(), path, "app"); err != nil {
		t.Fatalf("journal-like user values/comments were rejected: %v", err)
	}
}

func TestPostgresBackupDetectsCapturedCheckpointTOC(t *testing.T) {
	schema := "dbterm_cp_" + backupCheckpointToken
	for _, toc := range []string{
		"5; 2615 2200 SCHEMA - " + schema + " owner\n",
		"21; 2620 16801 TRIGGER public users " + schema + "_0 owner\n",
		"22; 2620 16802 TRIGGER public users " + schema + "_0_truncate owner\n",
	} {
		assertCheckpointBackupRefusal(t, scanPostgresBackupTOC(strings.NewReader(toc)))
	}
	ordinary := "; SCHEMA - " + schema + " owner\n" +
		"5; 2615 2200 SCHEMA - dbterm_cp_notes " + schema + "\n" +
		"21; 2620 16801 TRIGGER public users dbterm_cp_notes_0 owner\n"
	if err := scanPostgresBackupTOC(strings.NewReader(ordinary)); err != nil {
		t.Fatalf("similar user names or TOC owner were rejected: %v", err)
	}
}

func backupCheckpointSQLiteFixture(t *testing.T, path string, withJournal bool) *sql.DB {
	t.Helper()
	source, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	source.SetMaxOpenConns(1)
	if _, err := source.Exec(`CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT); INSERT INTO users VALUES (1, 'Ada');`); err != nil {
		t.Fatal(err)
	}
	if withJournal {
		if _, err := source.Exec(`CREATE TABLE "__dbterm_cp_` + backupCheckpointToken + `"(seq INTEGER PRIMARY KEY, value TEXT);
		CREATE TRIGGER checkpoint_capture AFTER INSERT ON users BEGIN
		INSERT INTO "__dbterm_cp_` + backupCheckpointToken + `"(value) VALUES (NEW.name); END;`); err != nil {
			t.Fatal(err)
		}
	}
	return source
}

func assertCheckpointBackupRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "finish or discard") || !strings.Contains(err.Error(), "cleanup") {
		t.Fatalf("backup error = %v, want actionable checkpoint cleanup refusal", err)
	}
}
