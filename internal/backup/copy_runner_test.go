package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestCopyRunnerLocalCopiesAllMissingOldestFirst(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	created := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	writeCopyRunnerFixture(t, source, "new.sqlite", "artifact_new", "producer_one", "job_one", created.Add(time.Hour), false)
	writeCopyRunnerFixture(t, source, "old.sqlite", "artifact_old", "producer_one", "job_one", created, false)

	var copied []string
	job := localCopyRunnerJob(t, source, destination)
	outcome, err := (CopyRunner{Now: func() time.Time { return created.Add(2 * time.Hour) }, Progress: func(event ProgressEvent) {
		if event.Phase == "copy" && strings.HasPrefix(event.Message, "copied and verified ") {
			copied = append(copied, strings.TrimPrefix(event.Message, "copied and verified "))
		}
	}}).Run(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Discovered != 2 || outcome.AlreadyPresent != 0 || len(outcome.Artifacts) != 2 {
		t.Fatalf("unexpected copy outcome: %+v", outcome)
	}
	if strings.Join(copied, ",") != "old.sqlite,new.sqlite" {
		t.Fatalf("copy order = %v, want oldest first", copied)
	}
	for _, name := range []string{"old.sqlite", "new.sqlite"} {
		if _, err := os.Stat(filepath.Join(source, name)); err != nil {
			t.Fatalf("source artifact was removed: %v", err)
		}
		manifest, err := ReadArtifactManifestForArtifactRequired(filepath.Join(destination, name))
		if err != nil {
			t.Fatal(err)
		}
		if manifest.ArtifactID != "artifact_"+strings.TrimSuffix(name, ".sqlite") {
			t.Fatalf("unexpected copied manifest: %+v", manifest)
		}
	}

	second, err := (CopyRunner{}).Run(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if second.Discovered != 2 || second.AlreadyPresent != 2 || second.BytesCopied != 0 || len(second.Artifacts) != 2 {
		t.Fatalf("second run should be a verified no-op: %+v", second)
	}
	for _, artifact := range second.Artifacts {
		if !artifact.AlreadyPresent || artifact.PublicationState != ArtifactPublicationComplete || artifact.ManifestSize < 1 || !validCopySHA256(artifact.ManifestSHA256) {
			t.Fatalf("verified no-op did not produce durable ownership evidence: %+v", artifact)
		}
	}
}

func TestCopyRunnerLocalPublicationGuardCoversEveryBoundary(t *testing.T) {
	source, destination := t.TempDir(), t.TempDir()
	writeCopyRunnerFixture(t, source, "guarded.sqlite", "artifact_guarded", "producer", "source_job", time.Now().UTC(), false)
	job := localCopyRunnerJob(t, source, destination)
	type call struct {
		phase CopyLocalPublicationPhase
		path  string
	}
	var calls []call
	runner := CopyRunner{LocalPublicationGuard: func(_ context.Context, localPath string, phase CopyLocalPublicationPhase) error {
		if _, err := os.Lstat(localPath); err != nil {
			t.Fatalf("guard %s received missing path %s: %v", phase, localPath, err)
		}
		calls = append(calls, call{phase: phase, path: filepath.Base(localPath)})
		return nil
	}}
	if _, err := runner.Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}

	var stageCreated, beforeArtifact, beforeManifest int
	for _, invocation := range calls {
		switch invocation.phase {
		case CopyLocalStageCreated:
			stageCreated++
			if !strings.HasSuffix(invocation.path, ".partial") {
				t.Errorf("stage-created guard path = %q, want private partial", invocation.path)
			}
		case CopyLocalBeforeArtifactPublish:
			beforeArtifact++
			if !strings.HasPrefix(invocation.path, ".dbterm-publish-") {
				t.Errorf("before-artifact guard path = %q, want artifact partial", invocation.path)
			}
		case CopyLocalBeforeManifestPublish:
			beforeManifest++
			if invocation.path != "guarded.sqlite" {
				t.Errorf("before-manifest guard path = %q, want published artifact", invocation.path)
			}
		default:
			t.Errorf("unexpected local publication phase %q", invocation.phase)
		}
	}
	if stageCreated != 3 || beforeArtifact < 1 || beforeManifest < 1 {
		t.Fatalf("local publication guard calls = %+v, want probe + artifact + manifest stages and both publication boundaries", calls)
	}
}

func TestCopyRunnerLocalRefusesSamePhysicalSourceAndDestination(t *testing.T) {
	directory := t.TempDir()
	err := ensureDistinctLocalCopyDirectories(directory, directory)
	if err == nil || !strings.Contains(err.Error(), "same physical directory") {
		t.Fatalf("same-directory copy error = %v", err)
	}
}

func TestCopyRunnerLocalRejectsChecksumMismatchWithoutPublishing(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	writeCopyRunnerFixture(t, source, "bad.sqlite", "artifact_bad", "producer_one", "job_one", time.Now(), true)

	_, err := (CopyRunner{}).Run(context.Background(), localCopyRunnerJob(t, source, destination))
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("expected checksum failure, got %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(destination, "bad.sqlite")); !os.IsNotExist(statErr) {
		t.Fatalf("checksum failure published an artifact: %v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(destination, "bad.sqlite"+ArtifactManifestSuffix)); !os.IsNotExist(statErr) {
		t.Fatalf("checksum failure published a manifest: %v", statErr)
	}
}

func TestCopyRunnerLocalDetectsCorruptedExistingCopy(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	writeCopyRunnerFixture(t, source, "copy.sqlite", "artifact_copy", "producer_one", "job_one", time.Now(), false)
	job := localCopyRunnerJob(t, source, destination)
	if _, err := (CopyRunner{}).Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(destination, "copy.sqlite")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = (CopyRunner{}).Run(context.Background(), job)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("expected existing-copy corruption failure, got %v", err)
	}
}

func TestCopyRunnerLocalFiltersAndIgnoresUnpublishedArtifacts(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	writeCopyRunnerFixture(t, source, "wanted.sqlite", "artifact_wanted", "producer_one", "job_wanted", time.Now(), false)
	writeCopyRunnerFixture(t, source, "other.sqlite", "artifact_other", "producer_one", "job_other", time.Now(), false)
	if err := os.WriteFile(filepath.Join(source, "partial.sqlite"), []byte("not published"), 0o600); err != nil {
		t.Fatal(err)
	}

	job := localCopyRunnerJob(t, source, destination)
	job.ArtifactFilter.JobID = "job_wanted"
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	outcome, err := (CopyRunner{}).Run(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Discovered != 1 || len(outcome.Artifacts) != 1 || outcome.Artifacts[0].ArtifactID != "artifact_wanted" {
		t.Fatalf("filter outcome = %+v", outcome)
	}
	for _, name := range []string{"other.sqlite", "partial.sqlite"} {
		if _, err := os.Lstat(filepath.Join(destination, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected destination file %s: %v", name, err)
		}
	}
}

func TestCopyRunnerLocalRefusesArtifactOnlyCollision(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	writeCopyRunnerFixture(t, source, "copy.sqlite", "artifact_copy", "producer_one", "job_one", time.Now(), false)
	collision := filepath.Join(destination, "copy.sqlite")
	if err := os.WriteFile(collision, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := (CopyRunner{}).Run(context.Background(), localCopyRunnerJob(t, source, destination))
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("expected collision failure, got %v", err)
	}
	data, readErr := os.ReadFile(collision)
	if readErr != nil || string(data) != "unrelated" {
		t.Fatalf("collision was changed: data=%q err=%v", data, readErr)
	}
}

func TestCopyRunnerLocalReconcilesVerifiedArtifactOnlyPublication(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	writeCopyRunnerFixture(t, source, "copy.sqlite", "artifact_copy", "producer_one", "job_one", time.Now(), false)
	data, err := os.ReadFile(filepath.Join(source, "copy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "copy.sqlite"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	outcome, err := (CopyRunner{}).Run(context.Background(), localCopyRunnerJob(t, source, destination))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.BytesCopied != 0 || len(outcome.Artifacts) != 1 || !outcome.Artifacts[0].Reconciled || outcome.Artifacts[0].PublicationState != ArtifactPublicationComplete {
		t.Fatalf("reconciled outcome = %+v", outcome)
	}
	manifest, err := ReadArtifactManifestForArtifactRequired(filepath.Join(destination, "copy.sqlite"))
	if err != nil || manifest.ArtifactID != "artifact_copy" {
		t.Fatalf("reconciled completion manifest = %+v, %v", manifest, err)
	}
}

func TestCopyRunnerLocalRejectsManifestSymlink(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	manifestPath := writeCopyRunnerFixture(t, source, "copy.sqlite", "artifact_copy", "producer_one", "job_one", time.Now(), false)
	realPath := manifestPath + ".real"
	if err := os.Rename(manifestPath, realPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, manifestPath); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	_, err := (CopyRunner{}).Run(context.Background(), localCopyRunnerJob(t, source, destination))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic-link") {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
}

func localCopyRunnerJob(t *testing.T, source, destination string) CopyJob {
	t.Helper()
	job := CopyJob{
		ID: "copy_local", Name: "local vault", Mode: CopyModePull,
		Source:      CopyEndpoint{Kind: CopyEndpointLocal, Location: source},
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: destination},
		Trigger:     CopyTriggerManual, Schedule: Schedule{Kind: ScheduleManual},
		Verification: CopyVerificationSHA256Format, Retention: Retention{KeepLast: 14},
		TimeoutMinutes: 5, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	return job
}

func writeCopyRunnerFixture(t *testing.T, directory, name, artifactID, producerID, jobID string, created time.Time, wrongChecksum bool) string {
	t.Helper()
	artifactPath := createRunnerSQLiteFixture(t, directory, name)
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	checksum := hex.EncodeToString(digest[:])
	if wrongChecksum {
		checksum = strings.Repeat("f", 64)
	}
	manifest := ArtifactManifest{
		SchemaVersion: ArtifactManifestSchemaVersion,
		ArtifactID:    artifactID, RunID: "run_" + artifactID, JobID: jobID,
		CreatedAt: created.UTC(), ProducerID: producerID, DBTermVersion: "test",
		Engine: config.SQLite, Format: string(FormatSQLiteDatabase),
		Compression: CompressionNone, Encryption: EncryptionSchemeNone, Encrypted: false,
		SizeBytes: int64(len(data)), SHA256: checksum,
		Verification: ArtifactVerificationPassed, VerificationLevel: ArtifactVerificationBasic,
		FileSets: []ManifestFileSet{}, Warnings: []string{},
	}
	var encoded bytes.Buffer
	if err := EncodeArtifactManifest(&encoded, manifest); err != nil {
		t.Fatal(err)
	}
	manifestPath := artifactManifestPath(artifactPath)
	if err := os.WriteFile(manifestPath, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return manifestPath
}

func ReadArtifactManifestForArtifactRequired(path string) (*ArtifactManifest, error) {
	manifest, present, err := ReadArtifactManifestForArtifact(path)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, os.ErrNotExist
	}
	return manifest, nil
}
