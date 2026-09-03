package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestLocalEncryptedBundleCopyRestoreEndToEnd(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DBTERM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("DBTERM_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("DBTERM_LOG_DIR", filepath.Join(root, "logs"))

	sourceDatabase := filepath.Join(root, "source", "registration.sqlite3")
	if err := os.MkdirAll(filepath.Dir(sourceDatabase), 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", sourceDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		CREATE TABLE registrations(id INTEGER PRIMARY KEY, name TEXT NOT NULL, balance INTEGER NOT NULL);
		INSERT INTO registrations(name, balance) VALUES ('Ada', 125), ('Grace', 250), ('Sundaram', 375);
	`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	applicationRoot := filepath.Join(root, "source", "application")
	writeLocalRehearsalFile(t, applicationRoot, "settings.json", []byte(`{"site":"rehearsal","enabled":true}`))
	writeLocalRehearsalFile(t, applicationRoot, "photos/profile.bin", []byte{0, 1, 2, 3, 5, 8, 13, 21})
	writeLocalRehearsalFile(t, applicationRoot, "ignored.tmp", []byte("excluded"))

	identityPath := filepath.Join(root, "keys", "age-identity.txt")
	recipient, err := GenerateAgeIdentity(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	const connectionID = "conn_local_rehearsal"
	connections, err := config.LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	connections.Connections = []config.ConnectionConfig{{
		ID: connectionID, Name: "local rehearsal source", Type: config.SQLite, FilePath: sourceDatabase,
	}}
	if err := connections.Save(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenDefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	producerRoot := filepath.Join(root, "producer")
	vaultRoot := filepath.Join(root, "vault")
	for _, directory := range []string{producerRoot, vaultRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	job := Job{
		Name: "local encrypted bundle", ConnectionID: connectionID,
		Destination: producerRoot, FilenameTemplate: "rehearsal_{run}",
		Compression: CompressionZstd, CompressionLevel: 3,
		Encryption: EncryptionAge, AgeRecipient: recipient,
		FileSets: []FileSet{{
			Label: "application", Root: applicationRoot, Include: []string{"**"}, Exclude: []string{"*.tmp"}, Required: true,
		}},
		Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 3}, TimeoutMinutes: 5,
	}
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	run, err := RunJobNow(context.Background(), store, job.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunSucceeded || !run.Artifact.Verified || run.Artifact.PublicationState != ArtifactPublicationComplete {
		t.Fatalf("backup run did not produce a complete verified recovery point: %+v", run)
	}
	manifest, err := ReadArtifactManifest(run.Artifact.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ArtifactID != run.Artifact.ID || manifest.RunID != run.ID || manifest.JobID != job.ID ||
		!manifest.Encrypted || manifest.Encryption != EncryptionSchemeAgeX25519V1 ||
		manifest.Format != string(FormatDBTermBundle) || len(manifest.FileSets) != 1 || manifest.FileSets[0].FileCount != 2 {
		t.Fatalf("portable completion manifest does not describe the encrypted bundle: %+v", manifest)
	}

	copyJob := CopyJob{
		Name: "local rehearsal vault", Mode: CopyModePush,
		Source: CopyEndpoint{Kind: CopyEndpointLocal}, SourceBackupJobID: job.ID,
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: vaultRoot},
		Trigger:     CopyTriggerManual, Schedule: Schedule{Kind: ScheduleManual},
		Verification: CopyVerificationSHA256Format, Retention: Retention{KeepLast: 3}, TimeoutMinutes: 5,
	}
	if err := store.UpsertCopyJob(context.Background(), &copyJob); err != nil {
		t.Fatal(err)
	}
	copyRun, err := RunCopyJobNow(context.Background(), store, copyJob.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if copyRun.Status != RunSucceeded || copyRun.BytesCopied != run.Artifact.Size || len(copyRun.Artifacts) != 1 {
		t.Fatalf("copy run did not transfer exactly one complete artifact: %+v", copyRun)
	}
	copied := copyRun.Artifacts[0]
	if copied.ArtifactID != run.Artifact.ID || copied.SHA256 != run.Artifact.SHA256 || copied.PublicationState != ArtifactPublicationComplete {
		t.Fatalf("vault result is not bound to the producer artifact: %+v", copied)
	}
	if got := localRehearsalSHA256(t, copied.Destination); got != localRehearsalSHA256(t, run.Artifact.Path) || got != run.Artifact.SHA256 {
		t.Fatalf("vault checksum = %s, want producer/catalog checksum %s", got, run.Artifact.SHA256)
	}

	inspection, err := Inspect(context.Background(), copied.Destination, InspectOptions{
		AgeIdentityPath: identityPath, MaxDecodedBytes: 1 << 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Locked || inspection.Confidence != ConfidenceExact || inspection.Format != FormatDBTermBundle ||
		inspection.DatabaseFormat != FormatSQLiteDatabase || len(inspection.FileSets) != 1 || inspection.FileSets[0].FileCount != 2 {
		t.Fatalf("vault inspection did not resolve the encrypted SQLite bundle exactly: %+v", inspection)
	}

	restoreDatabase := filepath.Join(root, "restore", "registration.sqlite3")
	restoreFiles := filepath.Join(root, "restore", "application")
	target := config.ConnectionConfig{Name: "isolated restore", Type: config.SQLite, FilePath: restoreDatabase}
	plan, err := BuildRestorePlan(inspection, &target, RestoreOptions{
		Mode: RestoreModeClean, StopOnError: true, SingleTransaction: true, AgeIdentityPath: identityPath,
		FileSetTargets: []RestoreFileSetTarget{{Label: "application", Root: restoreFiles}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ExecuteRestore(context.Background(), plan, nil); err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open("sqlite", restoreDatabase)
	if err != nil {
		t.Fatal(err)
	}
	var rows, balance int
	if err := restored.QueryRow(`SELECT COUNT(*), SUM(balance) FROM registrations`).Scan(&rows, &balance); err != nil {
		_ = restored.Close()
		t.Fatal(err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	if rows != 3 || balance != 750 {
		t.Fatalf("restored database rows = %d, balance = %d", rows, balance)
	}
	for _, relative := range []string{"settings.json", "photos/profile.bin"} {
		if source, restored := localRehearsalSHA256(t, filepath.Join(applicationRoot, filepath.FromSlash(relative))), localRehearsalSHA256(t, filepath.Join(restoreFiles, filepath.FromSlash(relative))); source != restored {
			t.Fatalf("restored file %s checksum = %s, want %s", relative, restored, source)
		}
	}
	if _, err := os.Lstat(filepath.Join(restoreFiles, "ignored.tmp")); !os.IsNotExist(err) {
		t.Fatalf("excluded file was restored: %v", err)
	}

	beforeNoClobber := localRehearsalSHA256(t, restoreDatabase)
	err = ExecuteRestore(context.Background(), plan, nil)
	if err == nil || !strings.Contains(err.Error(), "already exists and overwrite is disabled") {
		t.Fatalf("second restore error = %v, want file-set no-clobber refusal", err)
	}
	if after := localRehearsalSHA256(t, restoreDatabase); after != beforeNoClobber {
		t.Fatalf("no-clobber refusal changed the restored database: before %s, after %s", beforeNoClobber, after)
	}
}

func writeLocalRehearsalFile(t *testing.T, root, relative string, contents []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func localRehearsalSHA256(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(digest.Sum(nil))
}
