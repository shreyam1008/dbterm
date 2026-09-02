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
	"github.com/shreyam1008/dbterm/internal/config"
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
	decodedPayload, err := io.ReadAll(decoder)
	if err != nil {
		t.Fatal(err)
	}
	decoder.Close()
	_ = file.Close()
	if !strings.HasPrefix(string(decodedPayload), "SQLite format 3\x00") {
		t.Fatalf("decoded header = %q", decodedPayload[:min(16, len(decodedPayload))])
	}
	if artifact.PublicationState != ArtifactPublicationComplete {
		t.Fatalf("artifact publication state = %q, want complete", artifact.PublicationState)
	}

	payload, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if got := hex.EncodeToString(digest[:]); got != artifact.SHA256 {
		t.Fatalf("checksum = %s, want %s", artifact.SHA256, got)
	}
	manifest, err := ReadArtifactManifest(artifact.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ArtifactID != artifact.ID || manifest.RunID != "run_1234567890abcdef" || manifest.JobID != job.ID {
		t.Fatalf("manifest identity = %#v, artifact = %#v", manifest, artifact)
	}
	if manifest.SizeBytes != artifact.Size || manifest.SHA256 != artifact.SHA256 || manifest.Format != artifact.Format {
		t.Fatalf("manifest final-byte metadata = %#v, artifact = %#v", manifest, artifact)
	}
	if manifest.Encryption != EncryptionSchemeAgeX25519V1 || manifest.Compression != CompressionZstd {
		t.Fatalf("manifest wrapper metadata = %#v", manifest)
	}
	locked, err := Inspect(context.Background(), artifact.Path, InspectOptions{})
	if err != nil {
		t.Fatalf("inspect encrypted managed artifact without identity: %v", err)
	}
	if !locked.Locked || locked.Manifest == nil || locked.Manifest.ArtifactID != artifact.ID {
		t.Fatalf("locked inspection did not retain verified manifest: %#v", locked)
	}
}

func TestRunnerUsesExactOnDemandFilenameAndPublishesManifest(t *testing.T) {
	isolateBackupState(t)
	destination := t.TempDir()
	source := createRunnerSQLiteFixture(t, t.TempDir(), "exact-source.sqlite3")
	job := runnerSQLiteJob(destination, "ignored_{run}", "job_exact")
	cfg := &config.ConnectionConfig{ID: job.ConnectionID, Name: "exact source", Type: config.SQLite, FilePath: source}

	artifact, err := (Runner{OutputFilename: "operator-choice.backup"}).Run(context.Background(), job, cfg, "run_exact")
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(artifact.Path); got != "operator-choice.backup" {
		t.Fatalf("artifact basename = %q", got)
	}
	if _, err := ReadArtifactManifest(artifact.ManifestPath); err != nil {
		t.Fatalf("read completion manifest: %v", err)
	}
}

func TestRunnerRejectsUnsafeExactOnDemandFilename(t *testing.T) {
	isolateBackupState(t)
	source := createRunnerSQLiteFixture(t, t.TempDir(), "unsafe-source.sqlite3")
	job := runnerSQLiteJob(t.TempDir(), "ignored_{run}", "job_unsafe")
	cfg := &config.ConnectionConfig{ID: job.ConnectionID, Name: "unsafe source", Type: config.SQLite, FilePath: source}
	for _, name := range []string{"../escape.sqlite3", `nested\\escape.sqlite3`, ".", ".."} {
		t.Run(strings.ReplaceAll(name, "\\", "_"), func(t *testing.T) {
			if _, err := (Runner{OutputFilename: name}).Run(context.Background(), job, cfg, "run_unsafe"); err == nil || !strings.Contains(err.Error(), "one basename") {
				t.Fatalf("Runner.Run() error = %v, want safe basename refusal", err)
			}
		})
	}
}

func TestRunnerRejectsReservedExactOutputFilename(t *testing.T) {
	isolateBackupState(t)
	destination := t.TempDir()
	source := createRunnerSQLiteFixture(t, t.TempDir(), "reserved-source.sqlite3")
	job := runnerSQLiteJob(destination, "ignored_{run}", "job_reserved")
	cfg := &config.ConnectionConfig{ID: job.ConnectionID, Name: "reserved source", Type: config.SQLite, FilePath: source}
	for _, reserved := range []string{
		".dbterm-artifact-operator.partial",
		".dbterm-keep.partial",
		".dbterm-upload_00112233445566778899aabb.partial",
	} {
		if _, err := (Runner{OutputFilename: reserved}).Run(context.Background(), job, cfg, "run_reserved"); err == nil || !strings.Contains(err.Error(), "reserved private-partial namespace") {
			t.Fatalf("Runner.Run(%q) error = %v, want reserved private-partial refusal", reserved, err)
		}
		if _, err := os.Lstat(filepath.Join(destination, reserved)); !os.IsNotExist(err) {
			t.Fatalf("runner created reserved output %q despite validation failure: %v", reserved, err)
		}
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

func TestRunnerRefusesExistingCompletionManifestBeforeDump(t *testing.T) {
	isolateBackupState(t)
	directory := t.TempDir()
	source := filepath.Join(directory, "source.sqlite3")
	database, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE t(id INTEGER);`); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()
	job := Job{
		ID: "job_manifest_collision", Name: "fixed", ConnectionID: "conn", Destination: directory,
		FilenameTemplate: "fixed", Compression: CompressionNone, Encryption: EncryptionNone,
		Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 1}, TimeoutMinutes: 5,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	cfg := &config.ConnectionConfig{ID: "conn", Name: "local", Type: config.SQLite, FilePath: source}
	manifestPath := filepath.Join(directory, "fixed.sqlite3"+ArtifactManifestSuffix)
	if err := os.WriteFile(manifestPath, []byte("reserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = (Runner{}).Run(context.Background(), job, cfg, "run_collision")
	if err == nil || !strings.Contains(err.Error(), "manifest already exists") {
		t.Fatalf("Runner.Run() error = %v, want manifest no-clobber refusal", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "fixed.sqlite3")); !os.IsNotExist(err) {
		t.Fatalf("runner published artifact despite sidecar collision: %v", err)
	}
}

func TestRunnerFinishesCompletionManifestWhenCancellationLosesPublicationBoundary(t *testing.T) {
	isolateBackupState(t)
	directory := t.TempDir()
	source := createRunnerSQLiteFixture(t, directory, "cancel-boundary.sqlite3")
	job := runnerSQLiteJob(directory, "cancel_boundary", "job_cancel_boundary")
	connection := &config.ConnectionConfig{ID: job.ConnectionID, Name: "local", Type: config.SQLite, FilePath: source}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	artifact, err := (Runner{Progress: func(event ProgressEvent) {
		if event.Phase == "publish" && event.Message == "artifact durable; publishing completion manifest last" {
			cancel()
		}
	}}).Run(ctx, job, connection, "run_cancel_boundary")
	if err != nil {
		t.Fatalf("Runner.Run() after publication boundary = %v", err)
	}
	if _, err := os.Stat(artifact.Path); err != nil {
		t.Fatalf("published artifact missing: %v", err)
	}
	if _, err := os.Stat(artifact.ManifestPath); err != nil {
		t.Fatalf("completion manifest missing after boundary cancellation: %v", err)
	}
}

func TestRunnerReportsTrackedOrphanWhenManifestPublicationCollidesAtBoundary(t *testing.T) {
	isolateBackupState(t)
	directory := t.TempDir()
	source := createRunnerSQLiteFixture(t, directory, "manifest-boundary.sqlite3")
	job := runnerSQLiteJob(directory, "manifest_boundary", "job_manifest_boundary")
	connection := &config.ConnectionConfig{ID: job.ConnectionID, Name: "local", Type: config.SQLite, FilePath: source}
	finalPath := filepath.Join(directory, "manifest_boundary.sqlite3")
	manifestPath := finalPath + ArtifactManifestSuffix
	competitor := []byte("do not replace")
	artifact, err := (Runner{Progress: func(event ProgressEvent) {
		if event.Phase == "publish" && event.Message == "artifact durable; publishing completion manifest last" {
			if writeErr := os.WriteFile(manifestPath, competitor, 0o600); writeErr != nil {
				t.Errorf("create competing manifest: %v", writeErr)
			}
		}
	}}).Run(context.Background(), job, connection, "run_manifest_boundary")
	if err == nil || !strings.Contains(err.Error(), "copy scanners will ignore the orphan artifact") {
		t.Fatalf("Runner.Run() error = %v, want tracked orphan warning", err)
	}
	if artifact.Path != finalPath || !artifact.Verified || artifact.ManifestPath != manifestPath {
		t.Fatalf("returned orphan artifact = %#v", artifact)
	}
	if _, statErr := os.Stat(finalPath); statErr != nil {
		t.Fatalf("completed orphan artifact was not preserved: %v", statErr)
	}
	if got, readErr := os.ReadFile(manifestPath); readErr != nil || string(got) != string(competitor) {
		t.Fatalf("competing manifest = %q, %v", got, readErr)
	}
}

func TestMarkManifestPublicationUncertainTracksCrossedBoundary(t *testing.T) {
	artifact := Artifact{PublicationState: ArtifactPublicationArtifactOnly}
	boundaryErr := &publicationBoundaryError{path: "backup.dbterm.json", err: errors.New("durability check failed")}
	if !markManifestPublicationUncertain(&artifact, boundaryErr) {
		t.Fatal("crossed manifest publication boundary was not detected")
	}
	if artifact.PublicationState != ArtifactPublicationUncertain {
		t.Fatalf("publication state = %q, want uncertain", artifact.PublicationState)
	}

	artifact.PublicationState = ArtifactPublicationArtifactOnly
	if markManifestPublicationUncertain(&artifact, errors.New("pre-publication failure")) {
		t.Fatal("ordinary manifest failure was reported as a crossed boundary")
	}
	if artifact.PublicationState != ArtifactPublicationArtifactOnly {
		t.Fatalf("ordinary failure changed publication state to %q", artifact.PublicationState)
	}
}

func createRunnerSQLiteFixture(t *testing.T, directory, name string) string {
	t.Helper()
	source := filepath.Join(directory, name)
	database, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE t(id INTEGER); INSERT INTO t VALUES (1);`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return source
}

func runnerSQLiteJob(destination, template, id string) Job {
	return Job{
		ID: id, Name: id, ConnectionID: "conn_" + id, Destination: destination,
		FilenameTemplate: template, Compression: CompressionNone, Encryption: EncryptionNone,
		Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 1}, TimeoutMinutes: 5,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
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
	if _, statErr := os.Lstat(final + ArtifactManifestSuffix); !os.IsNotExist(statErr) {
		t.Fatalf("runner published manifest after wrap cancellation: %v", statErr)
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

func TestCleanupStalePartialsOnlyRemovesOwnedNamespaces(t *testing.T) {
	directory := t.TempDir()
	oldTime := time.Now().Add(-72 * time.Hour)
	cutoff := time.Now().Add(-48 * time.Hour)
	owned := []string{
		".dbterm-artifact-00112233445566778899aabb.partial",
		".dbterm-manifest-00112233445566778899aabc.partial",
		".dbterm-publish-00112233445566778899aabd.partial",
	}
	preserved := []string{
		".dbterm-keep.partial",
		".dbterm-artifact-old.partial",
		".dbterm-write-test-old.partial",
		".dbterm-artifact-old.partial.extra",
		"ordinary.partial",
	}
	for _, name := range append(append([]string{}, owned...), preserved...) {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}
	freshOwned := filepath.Join(directory, ".dbterm-artifact-00112233445566778899aabe.partial")
	if err := os.WriteFile(freshOwned, []byte("fresh"), 0o600); err != nil {
		t.Fatal(err)
	}

	removed, err := cleanupStalePartials(directory, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if removed != len(owned) {
		t.Fatalf("cleanupStalePartials() removed %d files, want %d", removed, len(owned))
	}
	for _, name := range owned {
		if _, err := os.Lstat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			t.Errorf("owned stale partial %q was not removed: %v", name, err)
		}
	}
	for _, name := range append(preserved, filepath.Base(freshOwned)) {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Errorf("cleanup removed preserved file %q: %v", name, err)
		}
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

func TestArtifactFilenameIsPortableAcrossSupportedOperatingSystems(t *testing.T) {
	valid := []string{"orders_20260903.sql.zst", "prod.dbterm.zst.age", "报告.sqlite3"}
	for _, name := range valid {
		if err := validateExactArtifactFilename(name); err != nil {
			t.Errorf("portable filename %q rejected: %v", name, err)
		}
	}
	invalid := []string{
		"CON.sql", "prn.sqlite3", "AUX.dbterm.zst", "nul", "COM1.zip", "lpt9.age",
		"orders:latest.sql", "orders?.sql", "orders.sql.", "orders.sql ", "orders\x1f.sql",
	}
	for _, name := range invalid {
		if err := validateExactArtifactFilename(name); err == nil {
			t.Errorf("non-portable filename %q was accepted", name)
		}
	}
}
