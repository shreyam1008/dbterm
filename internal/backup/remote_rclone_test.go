package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestMain(testingMain *testing.M) {
	if os.Getenv("DBTERM_TEST_RCLONE_HELPER") == "1" {
		os.Exit(runFakeRclone(os.Args[1:], os.Stdout))
	}
	if os.Getenv("DBTERM_TEST_PG_DUMP_HELPER") == "1" {
		os.Exit(runFakePostgresDump(os.Args[1:], os.Stdout))
	}
	os.Exit(testingMain.Run())
}

func TestRcloneLegacyArtifactCanStillBeVerifiedReadOnly(t *testing.T) {
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
	stored := filepath.Join(remoteRoot, "team", "backups", "orders.dump")
	if err := os.WriteFile(stored, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	exists, err := verifyRcloneArtifactForPrune(context.Background(), object, Artifact{
		Path: object.String(), Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]),
	})
	if err != nil || !exists {
		t.Fatalf("verify remote artifact = %t, %v", exists, err)
	}
	if got, readErr := os.ReadFile(stored); readErr != nil || string(got) != string(payload) {
		t.Fatalf("read-only verification changed remote object = %q, %v", got, readErr)
	}
}

func TestRclonePublicationFailsClosedBeforeToolLookupOrMutation(t *testing.T) {
	remoteRoot := t.TempDir()
	finalPath := filepath.Join(remoteRoot, "team", "backups", "orders.dump")
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const existing = "independently published object"
	if err := os.WriteFile(finalPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	object, err := parseDestination("rclone://archive/team/backups/orders.dump")
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(stage, []byte("new backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	toolLookedUp := false
	originalFinder := findRcloneTool
	findRcloneTool = func(string) (string, error) {
		toolLookedUp = true
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { findRcloneTool = originalFinder })
	t.Setenv("DBTERM_TEST_RCLONE_ROOT", remoteRoot)

	err = publishRcloneNoReplace(context.Background(), stage, object, int64(len("new backup")), nil)
	if err == nil || !strings.Contains(err.Error(), "rclone backup publication is disabled") {
		t.Fatalf("publishRcloneNoReplace() error = %v, want explicit fail-closed error", err)
	}
	if toolLookedUp {
		t.Fatal("fail-closed rclone publication looked up or invoked rclone")
	}
	got, readErr := os.ReadFile(finalPath)
	if readErr != nil || string(got) != existing {
		t.Fatalf("existing remote object = %q, %v; want untouched sentinel", got, readErr)
	}
	entries, readErr := os.ReadDir(filepath.Dir(finalPath))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(finalPath) {
		t.Fatalf("disabled publication created remote objects: %v", entryNames(entries))
	}
}

func TestCleanupStaleRclonePublicationPartialsIsAgeAndNamespaceScoped(t *testing.T) {
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
	directory := filepath.Join(remoteRoot, "team", "backups")
	old := ".dbterm-upload_aaaaaaaaaaaaaaaaaaaaaaaa.partial"
	fresh := ".dbterm-upload_bbbbbbbbbbbbbbbbbbbbbbbb.partial"
	unrelated := ".dbterm-upload_not-an-id.partial"
	for _, name := range []string{old, fresh, unrelated} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := time.Now().UTC().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(directory, old), cutoff.Add(-time.Hour), cutoff.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(directory, fresh), cutoff.Add(time.Hour), cutoff.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(directory, unrelated), cutoff.Add(-time.Hour), cutoff.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	removed, err := cleanupStaleRclonePublicationPartials(context.Background(), destination, cutoff)
	if err != nil || removed != 1 {
		t.Fatalf("cleanupStaleRclonePublicationPartials() = %d, %v; want 1, nil", removed, err)
	}
	if _, err := os.Stat(filepath.Join(directory, old)); !os.IsNotExist(err) {
		t.Fatalf("old private upload remains: %v", err)
	}
	for _, name := range []string{fresh, unrelated} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("scoped cleanup removed %s: %v", name, err)
		}
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestRunnerRejectsRcloneDestinationBeforeBackupOrToolLookup(t *testing.T) {
	isolateBackupState(t)
	toolLookedUp := false
	originalFinder := findRcloneTool
	findRcloneTool = func(string) (string, error) {
		toolLookedUp = true
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { findRcloneTool = originalFinder })

	source := filepath.Join(t.TempDir(), "missing.sqlite3")
	job := Job{
		ID: "job_remote", Name: "offsite", ConnectionID: "conn_local", Destination: "rclone://archive/team/backups",
		FilenameTemplate: "orders_{run}", Compression: CompressionNone, Encryption: EncryptionNone,
		Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 2}, TimeoutMinutes: 5,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	connection := &config.ConnectionConfig{ID: "conn_local", Name: "orders", Type: config.SQLite, FilePath: source}
	_, err := (Runner{}).Run(context.Background(), job, connection, "run_1234567890abcdef")
	if err == nil || !strings.Contains(err.Error(), "rclone backup publication is disabled") {
		t.Fatalf("Runner.Run() error = %v, want explicit fail-closed error", err)
	}
	if toolLookedUp {
		t.Fatal("rejected rclone job looked up or invoked rclone")
	}
	if _, statErr := os.Lstat(source); !os.IsNotExist(statErr) {
		t.Fatalf("rejected rclone job touched database source: %v", statErr)
	}
}

func TestCreateNativeBackupRejectsRcloneBeforeToolLookup(t *testing.T) {
	toolLookedUp := false
	originalFinder := findRcloneTool
	findRcloneTool = func(string) (string, error) {
		toolLookedUp = true
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { findRcloneTool = originalFinder })

	connection := &config.ConnectionConfig{Type: config.SQLite, FilePath: filepath.Join(t.TempDir(), "missing.sqlite3")}
	err := CreateNativeBackup(context.Background(), connection, "rclone://archive/team/backups/orders.sqlite3", NativeOptions{})
	if err == nil || !strings.Contains(err.Error(), "rclone backup publication is disabled") {
		t.Fatalf("CreateNativeBackup() error = %v, want explicit fail-closed error", err)
	}
	if toolLookedUp {
		t.Fatal("rejected native rclone backup looked up or invoked rclone")
	}
}

func TestPostgresBackupUsesConfiguredRemoteSourceHost(t *testing.T) {
	tool, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argumentsPath := filepath.Join(t.TempDir(), "arguments")
	t.Setenv("DBTERM_TEST_PG_DUMP_HELPER", "1")
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

func runFakePostgresDump(arguments []string, stdout io.Writer) int {
	argumentsPath := os.Getenv("DBTERM_TEST_DATABASE_ARGUMENTS")
	if argumentsPath == "" {
		return 2
	}
	data := []byte(strings.Join(arguments, "\n") + "\n")
	if err := os.WriteFile(argumentsPath, data, 0o600); err != nil {
		return 2
	}
	if _, err := io.WriteString(stdout, "PGDMPremote-source-fixture"); err != nil {
		return 2
	}
	return 0
}

func writeFakeRclone(t *testing.T) string {
	t.Helper()
	tool, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBTERM_TEST_RCLONE_HELPER", "1")
	return tool
}

func runFakeRclone(arguments []string, stdout io.Writer) int {
	if len(arguments) > 0 && arguments[0] == "--ask-password=false" {
		arguments = arguments[1:]
	}
	if len(arguments) == 0 {
		return 9
	}

	command := arguments[0]
	arguments = arguments[1:]
	remotePath := func(index int) (string, bool) {
		if index >= len(arguments) {
			return "", false
		}
		_, objectPath, ok := strings.Cut(arguments[index], ":")
		root := os.Getenv("DBTERM_TEST_RCLONE_ROOT")
		if !ok || root == "" {
			return "", false
		}
		return filepath.Join(root, filepath.FromSlash(objectPath)), true
	}

	switch command {
	case "mkdir":
		target, ok := remotePath(0)
		if !ok || os.MkdirAll(target, 0o700) != nil {
			return 2
		}
		return 0
	case "lsjson":
		target, ok := remotePath(0)
		if !ok {
			return 2
		}
		info, err := os.Stat(target)
		if err == nil && info.Mode().IsRegular() {
			item := rcloneObject{Path: filepath.Base(target), Name: filepath.Base(target), Size: info.Size(), ModTime: info.ModTime().UTC().Format(time.RFC3339Nano)}
			if json.NewEncoder(stdout).Encode(item) != nil {
				return 2
			}
			return 0
		}
		if containsArgument(arguments, "--stat") {
			return 3
		}
		items := []rcloneObject{}
		entries, readErr := os.ReadDir(target)
		if readErr != nil && !os.IsNotExist(readErr) {
			return 2
		}
		for _, entry := range entries {
			entryInfo, infoErr := entry.Info()
			if infoErr == nil && entryInfo.Mode().IsRegular() {
				items = append(items, rcloneObject{Path: entry.Name(), Name: entry.Name(), Size: entryInfo.Size(), ModTime: entryInfo.ModTime().UTC().Format(time.RFC3339Nano)})
			}
		}
		if json.NewEncoder(stdout).Encode(items) != nil {
			return 2
		}
		return 0
	case "cat":
		target, ok := remotePath(0)
		if !ok {
			return 2
		}
		file, err := os.Open(target)
		if err != nil {
			return 3
		}
		_, copyErr := io.Copy(stdout, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return 2
		}
		return 0
	case "deletefile":
		target, ok := remotePath(0)
		if !ok {
			return 2
		}
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return 2
		}
		return 0
	default:
		return 9
	}
}

func containsArgument(arguments []string, expected string) bool {
	for _, argument := range arguments {
		if argument == expected {
			return true
		}
	}
	return false
}
