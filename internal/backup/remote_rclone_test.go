//go:build !windows

package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestRclonePublishVerifyAndDelete(t *testing.T) {
	remoteRoot := t.TempDir()
	tool := writeFakeRclone(t)
	originalFinder := findRcloneTool
	findRcloneTool = func(string) (string, error) { return tool, nil }
	t.Cleanup(func() { findRcloneTool = originalFinder })
	t.Setenv("DBTERM_TEST_RCLONE_ROOT", remoteRoot)

	destination, err := parseDestination("rclone://archive/team/backups")
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureRcloneDestination(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	objectValue, err := destination.join("orders.dump")
	if err != nil {
		t.Fatal(err)
	}
	object, err := parseDestination(objectValue)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("verified remote backup")
	stage := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(stage, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishRcloneNoReplace(context.Background(), stage, object, int64(len(payload)), nil); err != nil {
		t.Fatal(err)
	}
	if err := publishRcloneNoReplace(context.Background(), stage, object, int64(len(payload)), nil); err == nil {
		t.Fatal("second remote publication replaced an existing artifact")
	}
	digest := sha256.Sum256(payload)
	exists, err := verifyRcloneArtifactForPrune(context.Background(), object, Artifact{
		Path: object.String(), Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]),
	})
	if err != nil || !exists {
		t.Fatalf("verify remote artifact = %t, %v", exists, err)
	}
	if err := deleteRcloneArtifact(context.Background(), object); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := inspectRcloneObject(context.Background(), object); err != nil || exists {
		t.Fatalf("deleted remote artifact = exists %t, error %v", exists, err)
	}
}

func TestRunnerPublishesLocalSQLiteSourceToRcloneDestination(t *testing.T) {
	isolateBackupState(t)
	remoteRoot := t.TempDir()
	tool := writeFakeRclone(t)
	originalFinder := findRcloneTool
	findRcloneTool = func(string) (string, error) { return tool, nil }
	t.Cleanup(func() { findRcloneTool = originalFinder })
	t.Setenv("DBTERM_TEST_RCLONE_ROOT", remoteRoot)

	source := filepath.Join(t.TempDir(), "orders.sqlite3")
	database, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE orders(id INTEGER PRIMARY KEY, total INTEGER); INSERT INTO orders(total) VALUES (42);`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	job := Job{
		ID: "job_remote", Name: "offsite", ConnectionID: "conn_local", Destination: "rclone://archive/team/backups",
		FilenameTemplate: "orders_{run}", Compression: CompressionNone, Encryption: EncryptionNone,
		Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 2}, TimeoutMinutes: 5,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	connection := &config.ConnectionConfig{ID: "conn_local", Name: "orders", Type: config.SQLite, FilePath: source}
	artifact, err := (Runner{}).Run(context.Background(), job, connection, "run_1234567890abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Path != "rclone://archive/team/backups/orders_1234567890.sqlite3" || !artifact.Verified {
		t.Fatalf("remote artifact = %#v", artifact)
	}
	stored := filepath.Join(remoteRoot, "team", "backups", "orders_1234567890.sqlite3")
	prefix := make([]byte, 16)
	file, err := os.Open(stored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Read(prefix); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	if string(prefix) != "SQLite format 3\x00" {
		t.Fatalf("remote SQLite header = %q", prefix)
	}
}

func TestPostgresBackupUsesConfiguredRemoteSourceHost(t *testing.T) {
	toolDirectory := t.TempDir()
	tool := filepath.Join(toolDirectory, "pg_dump")
	argumentsPath := filepath.Join(toolDirectory, "arguments")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$DBTERM_TEST_DATABASE_ARGUMENTS"
printf 'PGDMPremote-source-fixture'
`
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	originalFinder := findRestoreTool
	findRestoreTool = func(string) (string, error) { return tool, nil }
	t.Cleanup(func() { findRestoreTool = originalFinder })
	t.Setenv("DBTERM_TEST_DATABASE_ARGUMENTS", argumentsPath)

	output := filepath.Join(t.TempDir(), "remote.dump")
	connection := &config.ConnectionConfig{
		Type: config.PostgreSQL, Host: "db.example.internal", Port: "5433",
		User: "backup", Password: "secret", Database: "orders", SSLMode: "require",
	}
	if err := runPostgresDump(context.Background(), connection, output, 6); err != nil {
		t.Fatal(err)
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	argumentText := string(arguments)
	for _, expected := range []string{"--host\ndb.example.internal\n", "--port\n5433\n", "orders\n"} {
		if !strings.Contains(argumentText, expected) {
			t.Fatalf("pg_dump arguments %q do not contain %q", argumentText, expected)
		}
	}
}

func writeFakeRclone(t *testing.T) string {
	t.Helper()
	tool := filepath.Join(t.TempDir(), "rclone")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--ask-password=false" ]; then shift; fi
command_name="${1:-}"
shift
to_local() {
  remote_value="$1"
  object_path="${remote_value#*:}"
  printf '%s/%s' "$DBTERM_TEST_RCLONE_ROOT" "$object_path"
}
case "$command_name" in
  mkdir)
    target="$(to_local "$1")"
    mkdir -p "$target"
    ;;
  lsjson)
    target="$(to_local "$1")"
    stat_mode=false
    for argument in "$@"; do
      if [ "$argument" = "--stat" ]; then stat_mode=true; fi
    done
    if [ -f "$target" ]; then
      size="$(wc -c < "$target" | tr -d ' ')"
      name="$(basename "$target")"
      printf '{"Path":"%s","Name":"%s","Size":%s,"ModTime":"2026-08-14T00:00:00Z","IsDir":false}\n' "$name" "$name" "$size"
    elif [ "$stat_mode" = true ]; then
      exit 3
    else
      printf '[]\n'
    fi
    ;;
  copyto)
    source_path="$1"
    target="$(to_local "$2")"
    if [ -e "$target" ]; then exit 4; fi
    mkdir -p "$(dirname "$target")"
    cp "$source_path" "$target"
    ;;
  cat)
    target="$(to_local "$1")"
    cat "$target"
    ;;
  deletefile)
    target="$(to_local "$1")"
    rm -f "$target"
    ;;
  *)
    exit 9
    ;;
esac
`
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return tool
}
