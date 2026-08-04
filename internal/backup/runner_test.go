package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"
	"github.com/shreyam1008/dbterm/config"
)

func TestRunnerSQLiteZstdAgeRoundTrip(t *testing.T) {
	isolateBackupState(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.sqlite3")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT); INSERT INTO users(name) VALUES ('Ada');`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	job := Job{
		ID: "job_test", Name: "encrypted", ConnectionID: "conn_test", Enabled: true,
		Destination: dir, FilenameTemplate: "{connection}_{timestamp}_{run}",
		Compression: CompressionZstd, CompressionLevel: 3,
		Encryption: EncryptionAge, AgeRecipient: identity.Recipient().String(),
		Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 2}, TimeoutMinutes: 5,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	cfg := &config.ConnectionConfig{ID: "conn_test", Name: "local", Type: config.SQLite, FilePath: source}
	artifact, err := (Runner{}).Run(context.Background(), job, cfg, "run_1234567890abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if !artifact.Verified || artifact.Size == 0 {
		t.Fatalf("artifact = %#v", artifact)
	}

	file, err := os.Open(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := age.Decrypt(file, identity)
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := zstd.NewReader(decrypted)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 16)
	if _, err := io.ReadFull(decoder, header); err != nil {
		t.Fatal(err)
	}
	decoder.Close()
	_ = file.Close()
	if string(header) != "SQLite format 3\x00" {
		t.Fatalf("decoded header = %q", header)
	}

	payload, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if got := hex.EncodeToString(digest[:]); got != artifact.SHA256 {
		t.Fatalf("checksum = %s, want %s", artifact.SHA256, got)
	}
}

func TestRunnerZipUsesFinalArtifactNameForEntry(t *testing.T) {
	isolateBackupState(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.sqlite3")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(id INTEGER);`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	job := Job{
		ID: "job_zip", Name: "zip", ConnectionID: "conn", Destination: dir,
		FilenameTemplate: "nightly_{run}", Compression: CompressionZip, CompressionLevel: 3,
		Encryption: EncryptionNone, Schedule: Schedule{Kind: ScheduleManual},
		Retention: Retention{KeepLast: 1}, TimeoutMinutes: 5,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	cfg := &config.ConnectionConfig{ID: "conn", Name: "local", Type: config.SQLite, FilePath: source}
	artifact, err := (Runner{}).Run(context.Background(), job, cfg, "run_1234567890abcdef")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != 1 || archive.File[0].Name != "nightly_1234567890.sqlite3" {
		t.Fatalf("ZIP entries = %#v, want the final native artifact name", archive.File)
	}
}

func TestRunnerRefusesExistingArtifact(t *testing.T) {
	isolateBackupState(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.sqlite3")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(id INTEGER);`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	job := Job{
		ID: "job_test", Name: "fixed", ConnectionID: "conn", Destination: dir,
		FilenameTemplate: "fixed", Compression: CompressionNone, Encryption: EncryptionNone,
		Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 1}, TimeoutMinutes: 5,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	cfg := &config.ConnectionConfig{ID: "conn", Name: "local", Type: config.SQLite, FilePath: source}
	if _, err := (Runner{}).Run(context.Background(), job, cfg, "run_one"); err != nil {
		t.Fatal(err)
	}
	if _, err := (Runner{}).Run(context.Background(), job, cfg, "run_two"); err == nil {
		t.Fatal("expected no-clobber error")
	}
}

func TestRunnerReportsStructuredDumpAndExactWrapProgress(t *testing.T) {
	isolateBackupState(t)
	directory := t.TempDir()
	source := filepath.Join(directory, "progress.sqlite3")
	database, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE payload(data BLOB); INSERT INTO payload(data) VALUES (zeroblob(2097152));`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	job := Job{
		ID: "job_progress", Name: "progress", ConnectionID: "conn_progress", Destination: directory,
		FilenameTemplate: "progress_{run}", Compression: CompressionZstd, CompressionLevel: 3,
		Encryption: EncryptionNone, Schedule: Schedule{Kind: ScheduleManual},
		Retention: Retention{KeepLast: 2}, TimeoutMinutes: 5,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	connection := &config.ConnectionConfig{ID: "conn_progress", Name: "progress source", Type: config.SQLite, FilePath: source}
	var mutex sync.Mutex
	var events []ProgressEvent
	artifact, err := (Runner{Progress: func(event ProgressEvent) {
		mutex.Lock()
		events = append(events, event)
		mutex.Unlock()
	}}).Run(context.Background(), job, connection, "run_progress")
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Size == 0 {
		t.Fatal("runner produced an empty artifact")
	}
	mutex.Lock()
	defer mutex.Unlock()
	seen := make(map[string]bool)
	var dumpBytes int64
	var finalWrap ProgressEvent
	for _, event := range events {
		seen[event.Phase] = true
		if event.Elapsed < 0 {
			t.Fatalf("negative elapsed progress: %#v", event)
		}
		if event.Phase == "dump" && event.CurrentBytes > dumpBytes {
			dumpBytes = event.CurrentBytes
		}
		if event.Phase == "wrap" {
			finalWrap = event
		}
	}
	for _, phase := range []string{"preflight", "dump", "verify", "wrap", "publish"} {
		if !seen[phase] {
			t.Errorf("missing %q progress event in %#v", phase, events)
		}
	}
	if dumpBytes == 0 {
		t.Fatalf("native dump never reported a staged size: %#v", events)
	}
	if finalWrap.TotalBytes == 0 || finalWrap.CurrentBytes != finalWrap.TotalBytes {
		t.Fatalf("final wrapping progress is not exact: %#v", finalWrap)
	}
}

func TestCreateNativeBackupUsesAndCleansPrivateStateStage(t *testing.T) {
	isolateBackupState(t)
	directory := t.TempDir()
	source := filepath.Join(directory, "source.sqlite3")
	database, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE t(v TEXT); INSERT INTO t VALUES ('safe');`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "backup.sqlite3")
	cfg := &config.ConnectionConfig{Type: config.SQLite, FilePath: source}
	if err := CreateNativeBackup(context.Background(), cfg, output, NativeOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := validateSQLiteDatabaseFile(context.Background(), output); err != nil {
		t.Fatalf("published native backup is invalid: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(output)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("published SQLite backup mode = %04o, want 0600", got)
		}
	}
	root, err := privateNativeStageRoot()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("private staging directories remain after success: %v", entries)
	}
}

func TestCreateNativeBackupCancellationAfterDumpDoesNotPublish(t *testing.T) {
	isolateBackupState(t)
	directory := t.TempDir()
	source := filepath.Join(directory, "source.sqlite3")
	database, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE t(v TEXT); INSERT INTO t VALUES ('safe');`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	output := filepath.Join(directory, "canceled.sqlite3")
	err = CreateNativeBackup(ctx, &config.ConnectionConfig{Type: config.SQLite, FilePath: source}, output, NativeOptions{
		Progress: func(event ProgressEvent) {
			if event.Phase == "dump" && event.Message == "engine-native backup created" {
				cancel()
			}
		},
	})
	cancel()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateNativeBackup() error = %v, want cancellation", err)
	}
	if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
		t.Fatalf("canceled backup published output: %v", statErr)
	}
}

func TestRunnerCancellationAfterWrappingDoesNotPublish(t *testing.T) {
	isolateBackupState(t)
	directory := t.TempDir()
	source := filepath.Join(directory, "source.sqlite3")
	database, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE payload(data BLOB); INSERT INTO payload VALUES (zeroblob(2097152));`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	job := Job{
		ID: "job_cancel_wrap", Name: "cancel wrap", ConnectionID: "conn_cancel_wrap", Destination: directory,
		FilenameTemplate: "cancel_after_wrap", Compression: CompressionGzip, CompressionLevel: 3,
		Encryption: EncryptionNone, Schedule: Schedule{Kind: ScheduleManual},
		Retention: Retention{KeepLast: 1}, TimeoutMinutes: 5,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	connection := &config.ConnectionConfig{ID: job.ConnectionID, Name: "source", Type: config.SQLite, FilePath: source}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = (Runner{Progress: func(event ProgressEvent) {
		if event.Phase == "wrap" && event.TotalBytes > 0 && event.CurrentBytes == event.TotalBytes {
			cancel()
		}
	}}).Run(ctx, job, connection, "run_cancel_wrap")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Runner.Run() error = %v, want cancellation", err)
	}
	final := filepath.Join(directory, "cancel_after_wrap.sqlite3.gz")
	if _, statErr := os.Lstat(final); !os.IsNotExist(statErr) {
		t.Fatalf("runner published artifact after wrap cancellation: %v", statErr)
	}
	matches, globErr := filepath.Glob(filepath.Join(directory, ".dbterm-*.partial"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("runner left private partial artifacts: %v", matches)
	}
}

func TestCreateNativeBackupMissingSQLiteSourceRemainsAbsent(t *testing.T) {
	isolateBackupState(t)
	directory := t.TempDir()
	source := filepath.Join(directory, "missing.sqlite3")
	output := filepath.Join(directory, "backup.sqlite3")
	err := CreateNativeBackup(context.Background(), &config.ConnectionConfig{Type: config.SQLite, FilePath: source}, output, NativeOptions{})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("CreateNativeBackup() error = %v, want missing-source error", err)
	}
	if _, statErr := os.Lstat(source); !os.IsNotExist(statErr) {
		t.Fatalf("missing SQLite source was created: %v", statErr)
	}
	if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
		t.Fatalf("backup was published for missing SQLite source: %v", statErr)
	}
}

func TestCreateNativeBackupRejectsSQLiteSourceSymlink(t *testing.T) {
	isolateBackupState(t)
	directory := t.TempDir()
	realSource := filepath.Join(directory, "real.sqlite3")
	database, err := sql.Open("sqlite", realSource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE t(v TEXT);`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	linkedSource := filepath.Join(directory, "linked.sqlite3")
	if err := os.Symlink(realSource, linkedSource); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	output := filepath.Join(directory, "backup.sqlite3")
	err = CreateNativeBackup(context.Background(), &config.ConnectionConfig{Type: config.SQLite, FilePath: linkedSource}, output, NativeOptions{})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("CreateNativeBackup() error = %v, want symbolic-link rejection", err)
	}
	if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
		t.Fatalf("backup was published from symbolic-link source: %v", statErr)
	}
}

func TestPrivateNativeStageCleanupIsScoped(t *testing.T) {
	isolateBackupState(t)
	root, err := privateNativeStageRoot()
	if err != nil {
		t.Fatal(err)
	}
	oldStage := filepath.Join(root, privateStagePrefix+"old")
	if err := os.Mkdir(oldStage, 0o700); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(oldStage, "native.dump")
	if err := os.WriteFile(oldFile, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-privateStageMaxAge - time.Hour)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldStage, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, "do-not-remove")
	if err := os.Mkdir(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}
	stage, err := newPrivateNativeStage(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage)
	if _, err := os.Stat(oldStage); !os.IsNotExist(err) {
		t.Fatalf("stale stage was not removed: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("cleanup touched unrelated state directory: %v", err)
	}
	if filepath.Dir(stage) != root {
		t.Fatalf("stage = %s, want child of %s", stage, root)
	}
}

func isolateBackupState(t *testing.T) {
	t.Helper()
	t.Setenv("DBTERM_STATE_DIR", t.TempDir())
}

type alwaysFailWriter struct{}

func (alwaysFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestZstdCompressionCloseErrorPropagates(t *testing.T) {
	writer, closeWriter, err := compressionWriter(alwaysFailWriter{}, CompressionZstd, 3, "backup.dump")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write([]byte("content that remains buffered until finalization"))
	if err := closeWriter(); err == nil {
		t.Fatal("zstd close error was discarded")
	}
}

func TestCredentialFilesRejectRecordBreakingValues(t *testing.T) {
	directory := t.TempDir()
	if _, _, err := writePGPassFile(directory, &config.ConnectionConfig{
		Type: config.PostgreSQL, Host: "localhost", Port: "5432", Database: "app", User: "user", Password: "line1\nline2",
	}); err == nil {
		t.Fatal("pgpass accepted a password containing a line break")
	}
	if _, _, err := writeMySQLDefaultsFile(directory, "before\x00after"); err == nil {
		t.Fatal("MySQL option file accepted a password containing NUL")
	}
}
