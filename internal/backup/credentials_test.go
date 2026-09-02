package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestCredentialFilesArePrivateAndCleanedUp(t *testing.T) {
	directory := t.TempDir()

	pgPath, pgCleanup, err := writePGPassFile(directory, &config.ConnectionConfig{
		Type:     config.PostgreSQL,
		Host:     "db.example",
		Port:     "5432",
		Database: "orders",
		User:     "backup",
		Password: `colon:and\slash`,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertBackupPrivateFile(t, pgPath)
	pgData, err := os.ReadFile(pgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(pgData), "db.example:5432:orders:backup:colon\\:and\\\\slash\n"; got != want {
		t.Fatalf("unexpected pgpass contents: got %q, want %q", got, want)
	}
	pgCleanup()
	assertCredentialFileRemoved(t, pgPath)

	mysqlPath, mysqlCleanup, err := writeMySQLDefaultsFile(directory, "quote\" and\\slash\ttab")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(mysqlPath) != ".cnf" {
		t.Fatalf("MySQL credential file lost its .cnf suffix: %q", mysqlPath)
	}
	assertBackupPrivateFile(t, mysqlPath)
	mysqlData, err := os.ReadFile(mysqlPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(mysqlData), "[client]\npassword=\"quote\\\" and\\\\slash\\ttab\"\n"; got != want {
		t.Fatalf("unexpected MySQL option file contents: got %q, want %q", got, want)
	}
	mysqlCleanup()
	assertCredentialFileRemoved(t, mysqlPath)

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".dbterm-pgpass-") || strings.HasPrefix(entry.Name(), ".dbterm-my-") {
			t.Fatalf("credential cleanup left %q behind", entry.Name())
		}
	}
}

func assertCredentialFileRemoved(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("credential cleanup did not remove %q: %v", path, err)
	}
}
