package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestValidateImportTarget(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		if _, err := validateImportTarget(nil); err == nil {
			t.Fatal("validateImportTarget(nil) expected error, got nil")
		}
	})

	t.Run("unsupported type", func(t *testing.T) {
		cfg := &config.ConnectionConfig{Type: config.SQLite, Database: "local.db"}
		if _, err := validateImportTarget(cfg); err == nil {
			t.Fatal("validateImportTarget(sqlite) expected error, got nil")
		}
	})

	t.Run("missing database", func(t *testing.T) {
		cfg := &config.ConnectionConfig{Type: config.PostgreSQL, Host: "localhost"}
		if _, err := validateImportTarget(cfg); err == nil {
			t.Fatal("validateImportTarget(missing database) expected error, got nil")
		}
	})

	t.Run("valid mysql target clones and trims", func(t *testing.T) {
		cfg := &config.ConnectionConfig{
			Type:     config.MySQL,
			Host:     "  localhost  ",
			Port:     " 3306 ",
			User:     " root ",
			Database: " appdb ",
		}

		got, err := validateImportTarget(cfg)
		if err != nil {
			t.Fatalf("validateImportTarget(valid) error = %v", err)
		}
		if got == cfg {
			t.Fatal("validateImportTarget should return cloned config, got original pointer")
		}
		if got.Host != "localhost" || got.Port != "3306" || got.User != "root" || got.Database != "appdb" {
			t.Fatalf("validateImportTarget trim mismatch: got host=%q port=%q user=%q db=%q", got.Host, got.Port, got.User, got.Database)
		}
	})
}

func TestImportClientRequirementForType(t *testing.T) {
	postgresReq, err := importClientRequirementForType(config.PostgreSQL)
	if err != nil {
		t.Fatalf("importClientRequirementForType(PostgreSQL) error = %v", err)
	}
	if postgresReq.binary != "psql" {
		t.Fatalf("PostgreSQL binary = %q, want %q", postgresReq.binary, "psql")
	}

	mysqlReq, err := importClientRequirementForType(config.MySQL)
	if err != nil {
		t.Fatalf("importClientRequirementForType(MySQL) error = %v", err)
	}
	if mysqlReq.binary != "mysql" {
		t.Fatalf("MySQL binary = %q, want %q", mysqlReq.binary, "mysql")
	}

	if _, err := importClientRequirementForType(config.SQLite); err == nil {
		t.Fatal("importClientRequirementForType(SQLite) expected error, got nil")
	}
}

func TestImportClientSetupHint(t *testing.T) {
	hint := importClientSetupHint("psql")
	if strings.TrimSpace(hint) == "" {
		t.Fatal("importClientSetupHint returned empty hint")
	}

	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		if !strings.Contains(strings.ToLower(hint), "psql") && !strings.Contains(strings.ToLower(hint), "postgres") {
			t.Fatalf("importClientSetupHint(%q) missing expected keyword: %q", "psql", hint)
		}
	}
}

func TestResolveImportSQLPath(t *testing.T) {
	tmp := t.TempDir()

	filePath := filepath.Join(tmp, "dump.sql")
	if err := os.WriteFile(filePath, []byte("select 1;\n"), 0o600); err != nil {
		t.Fatalf("write temp sql file: %v", err)
	}

	resolved, err := resolveImportSQLPath(filePath)
	if err != nil {
		t.Fatalf("resolveImportSQLPath(valid) error = %v", err)
	}
	if !filepath.IsAbs(resolved) {
		t.Fatalf("resolveImportSQLPath should return absolute path, got %q", resolved)
	}

	if _, err := resolveImportSQLPath(filepath.Join(tmp, "missing.sql")); err == nil {
		t.Fatal("resolveImportSQLPath(missing) expected error, got nil")
	}

	if _, err := resolveImportSQLPath(tmp); err == nil {
		t.Fatal("resolveImportSQLPath(directory) expected error, got nil")
	}
}

func TestIsPostgresArchiveDumpUsesContent(t *testing.T) {
	tmp := t.TempDir()
	archiveWithSQLName := filepath.Join(tmp, "archive.sql")
	if err := os.WriteFile(archiveWithSQLName, []byte("PGDMP\x01\x0f\x00"), 0o600); err != nil {
		t.Fatalf("write PostgreSQL archive fixture: %v", err)
	}
	got, err := isPostgresArchiveDump(archiveWithSQLName)
	if err != nil {
		t.Fatalf("isPostgresArchiveDump(archive) error = %v", err)
	}
	if !got {
		t.Fatal("PostgreSQL archive content should be detected regardless of .sql extension")
	}

	plainSQLWithDumpName := filepath.Join(tmp, "plain.dump")
	plainSQL := "--\n-- PostgreSQL database dump\n--\nCREATE TABLE widgets (id integer);\n"
	if err := os.WriteFile(plainSQLWithDumpName, []byte(plainSQL), 0o600); err != nil {
		t.Fatalf("write PostgreSQL SQL fixture: %v", err)
	}
	got, err = isPostgresArchiveDump(plainSQLWithDumpName)
	if err != nil {
		t.Fatalf("isPostgresArchiveDump(SQL) error = %v", err)
	}
	if got {
		t.Fatal("plain SQL must not be treated as an archive merely because it uses .dump")
	}

	if _, err := isPostgresArchiveDump(filepath.Join(tmp, "missing.dump")); err == nil {
		t.Fatal("missing import file should return an inspection error")
	}
}

func TestPostgresArchiveImportArgsNeverCleanImplicitly(t *testing.T) {
	cfg := &config.ConnectionConfig{
		Type: config.PostgreSQL, Host: "localhost", Port: "5432", User: "postgres", Database: "appdb",
	}
	args := postgresArchiveImportArgs(cfg, "/tmp/app.dump", true)
	joined := " " + strings.Join(args, " ") + " "
	for _, forbidden := range []string{" --clean ", " --if-exists "} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("archive import arguments contain destructive flag %q: %v", strings.TrimSpace(forbidden), args)
		}
	}
	if !strings.Contains(joined, " --exit-on-error ") {
		t.Fatalf("archive import arguments should retain stop-on-error: %v", args)
	}
}

func TestMapImportCommandError(t *testing.T) {
	t.Run("nil run error", func(t *testing.T) {
		if err := mapImportCommandError(context.Background(), "psql", "", nil); err != nil {
			t.Fatalf("mapImportCommandError(nil) = %v, want nil", err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := mapImportCommandError(ctx, "psql", "", errors.New("signal killed"))
		if !errors.Is(err, errImportCancelled) {
			t.Fatalf("mapImportCommandError(cancelled) = %v, want errImportCancelled", err)
		}
	})

	t.Run("deadline exceeded", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()

		err := mapImportCommandError(ctx, "mysql", "", errors.New("timeout"))
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("mapImportCommandError(deadline) = %v, want timeout message", err)
		}
	})

	t.Run("fallback to tail output", func(t *testing.T) {
		err := mapImportCommandError(context.Background(), "mysql", "ERROR 1064 (42000)", errors.New("exit status 1"))
		if err == nil || !strings.Contains(err.Error(), "ERROR 1064") {
			t.Fatalf("mapImportCommandError(tail) = %v, want tail detail", err)
		}
	})
}

func TestSameImportConnection(t *testing.T) {
	left := &config.ConnectionConfig{
		Type:     config.PostgreSQL,
		Host:     "localhost",
		Port:     "",
		User:     "postgres",
		Database: "appdb",
	}
	right := &config.ConnectionConfig{
		Type:     config.PostgreSQL,
		Host:     "LOCALHOST",
		Port:     "5432",
		User:     "postgres",
		Database: "appdb",
	}

	if !sameImportConnection(left, right) {
		t.Fatal("sameImportConnection should match equivalent PostgreSQL configs")
	}
}

func TestImportRunState(t *testing.T) {
	app := &App{}
	cancelCalls := 0

	if !app.beginImportRun(func() { cancelCalls++ }, nil) {
		t.Fatal("beginImportRun expected true on first call")
	}
	if app.beginImportRun(func() {}, nil) {
		t.Fatal("beginImportRun expected false when already running")
	}
	if !app.isImportRunning() {
		t.Fatal("isImportRunning expected true after begin")
	}

	if !app.requestImportCancel() {
		t.Fatal("requestImportCancel expected true on first cancel request")
	}
	if cancelCalls != 1 {
		t.Fatalf("cancel function called %d times, want 1", cancelCalls)
	}
	if app.requestImportCancel() {
		t.Fatal("requestImportCancel expected false after cancel already requested")
	}

	app.finishImportRun()
	if app.isImportRunning() {
		t.Fatal("isImportRunning expected false after finish")
	}
}
