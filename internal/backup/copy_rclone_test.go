package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestCopyRunnerRcloneCopiesAllFilteredArtifactsOldestFirstWithoutMutatingSource(t *testing.T) {
	remoteRoot, sourceDirectory := prepareRcloneCopySource(t)
	destination := t.TempDir()
	created := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	writeCopyRunnerFixture(t, sourceDirectory, "new.sqlite", "artifact_new", "producer_one", "job_one", created.Add(time.Hour), false)
	writeCopyRunnerFixture(t, sourceDirectory, "old.sqlite", "artifact_old", "producer_one", "job_one", created, false)
	writeCopyRunnerFixture(t, sourceDirectory, "other-job.sqlite", "artifact_other_job", "producer_one", "job_two", created, false)
	writeCopyRunnerFixture(t, sourceDirectory, "other-producer.sqlite", "artifact_other_producer", "producer_two", "job_one", created, false)
	otherFormat := writeCopyRunnerFixture(t, sourceDirectory, "other-format.sqlite", "artifact_other_format", "producer_one", "job_one", created, false)
	rewriteRcloneCopyManifest(t, otherFormat, func(manifest *ArtifactManifest) {
		manifest.Engine = config.PostgreSQL
		manifest.Format = string(FormatPostgresCustom)
	})
	if err := os.WriteFile(filepath.Join(sourceDirectory, "orphan.sqlite"), []byte("artifact without a completion manifest"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotCopyDirectory(t, sourceDirectory)

	job := rcloneCopyRunnerJob(t, destination)
	job.ArtifactFilter = CopyArtifactFilter{
		ProducerID: "producer_one", JobID: "job_one",
		Formats: []string{string(FormatSQLiteDatabase)},
	}
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	var copied []string
	outcome, err := (CopyRunner{Progress: func(event ProgressEvent) {
		if event.Phase == "copy" && strings.HasPrefix(event.Message, "copied and verified ") {
			copied = append(copied, strings.TrimPrefix(event.Message, "copied and verified "))
		}
	}}).Run(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Discovered != 2 || outcome.AlreadyPresent != 0 || len(outcome.Artifacts) != 2 {
		t.Fatalf("unexpected rclone copy outcome: %+v", outcome)
	}
	if got := strings.Join(copied, ","); got != "old.sqlite,new.sqlite" {
		t.Fatalf("copy order = %q, want oldest first", got)
	}
	if after := snapshotCopyDirectory(t, sourceDirectory); !reflect.DeepEqual(after, before) {
		t.Fatalf("rclone pull mutated source under %s", remoteRoot)
	}
	for _, name := range []string{"old.sqlite", "new.sqlite"} {
		manifest, err := ReadArtifactManifestForArtifactRequired(filepath.Join(destination, name))
		if err != nil {
			t.Fatal(err)
		}
		if manifest.ArtifactID != "artifact_"+strings.TrimSuffix(name, ".sqlite") {
			t.Fatalf("unexpected copied manifest: %+v", manifest)
		}
	}
	for _, name := range []string{"other-job.sqlite", "other-producer.sqlite", "other-format.sqlite", "orphan.sqlite"} {
		if _, err := os.Lstat(filepath.Join(destination, name)); !os.IsNotExist(err) {
			t.Fatalf("filtered or unpublished artifact %s reached destination: %v", name, err)
		}
	}
}

func TestRclonePullInvokesLocalPublicationGuardForArtifactStage(t *testing.T) {
	_, sourceDirectory := prepareRcloneCopySource(t)
	writeCopyRunnerFixture(t, sourceDirectory, "guarded.sqlite", "artifact_rclone_guard", "producer_one", "job_one", time.Now().UTC(), false)
	destination := t.TempDir()
	job := rcloneCopyRunnerJob(t, destination)
	guardedArtifactStage := false
	runner := CopyRunner{LocalPublicationGuard: func(_ context.Context, localPath string, phase CopyLocalPublicationPhase) error {
		if phase == CopyLocalStageCreated && strings.HasPrefix(filepath.Base(localPath), ".dbterm-publish-") {
			guardedArtifactStage = true
			return os.ErrPermission
		}
		return nil
	}}
	_, err := runner.Run(context.Background(), job)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("rclone local-publication guard error = %v", err)
	}
	if !guardedArtifactStage {
		t.Fatal("rclone pull did not guard its local artifact stage")
	}
	assertCopyDirectoryEmpty(t, destination)
}

func TestCopyRunnerRcloneStrictSidecars(t *testing.T) {
	t.Run("missing sidecar is ignored", func(t *testing.T) {
		_, sourceDirectory := prepareRcloneCopySource(t)
		destination := t.TempDir()
		if err := os.WriteFile(filepath.Join(sourceDirectory, "orphan.sqlite"), []byte("not published"), 0o600); err != nil {
			t.Fatal(err)
		}
		outcome, err := (CopyRunner{}).Run(context.Background(), rcloneCopyRunnerJob(t, destination))
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Discovered != 0 || outcome.BytesCopied != 0 || len(outcome.Artifacts) != 0 {
			t.Fatalf("artifact without strict sidecar was discovered: %+v", outcome)
		}
		assertCopyDirectoryEmpty(t, destination)
	})

	t.Run("malformed strict sidecar fails closed", func(t *testing.T) {
		_, sourceDirectory := prepareRcloneCopySource(t)
		destination := t.TempDir()
		if err := os.WriteFile(filepath.Join(sourceDirectory, "broken.sqlite"+ArtifactManifestSuffix), []byte("{not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := (CopyRunner{}).Run(context.Background(), rcloneCopyRunnerJob(t, destination))
		if err == nil || !strings.Contains(err.Error(), "manifest") {
			t.Fatalf("malformed sidecar error = %v", err)
		}
		assertCopyDirectoryEmpty(t, destination)
	})

	t.Run("completion manifest without artifact fails closed", func(t *testing.T) {
		_, sourceDirectory := prepareRcloneCopySource(t)
		destination := t.TempDir()
		manifestPath := writeCopyRunnerFixture(t, sourceDirectory, "missing.sqlite", "artifact_missing", "producer_one", "job_one", time.Now(), false)
		if err := os.Remove(strings.TrimSuffix(manifestPath, ArtifactManifestSuffix)); err != nil {
			t.Fatal(err)
		}
		_, err := (CopyRunner{}).Run(context.Background(), rcloneCopyRunnerJob(t, destination))
		if err == nil || !strings.Contains(err.Error(), "has no readable artifact") {
			t.Fatalf("missing artifact error = %v", err)
		}
		assertCopyDirectoryEmpty(t, destination)
	})
}

func TestCopyRunnerRcloneRejectsChecksumMismatchWithoutPublishing(t *testing.T) {
	_, sourceDirectory := prepareRcloneCopySource(t)
	destination := t.TempDir()
	writeCopyRunnerFixture(t, sourceDirectory, "bad.sqlite", "artifact_bad", "producer_one", "job_one", time.Now(), true)

	_, err := (CopyRunner{}).Run(context.Background(), rcloneCopyRunnerJob(t, destination))
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("checksum mismatch error = %v", err)
	}
	assertCopyDirectoryEmpty(t, destination)
}

func TestCopyRunnerRcloneRejectsRemoteObjectChangedDuringTransfer(t *testing.T) {
	_, sourceDirectory := prepareRcloneCopySource(t)
	destination := t.TempDir()
	writeCopyRunnerFixture(t, sourceDirectory, "changing.sqlite", "artifact_changing", "producer_one", "job_one", time.Now(), false)
	remoteArtifact := filepath.Join(sourceDirectory, "changing.sqlite")
	initial, err := os.Stat(remoteArtifact)
	if err != nil {
		t.Fatal(err)
	}
	var changed atomic.Bool
	mutationResult := make(chan error, 1)
	runner := CopyRunner{Progress: func(event ProgressEvent) {
		if event.Message == "pulling verified artifact from rclone" && event.CurrentBytes > 0 && changed.CompareAndSwap(false, true) {
			mutationResult <- os.Chtimes(remoteArtifact, initial.ModTime(), initial.ModTime().Add(time.Hour))
		}
	}}
	_, err = runner.Run(context.Background(), rcloneCopyRunnerJob(t, destination))
	if !changed.Load() {
		t.Fatal("test did not change remote object metadata during the stream")
	}
	if mutationErr := <-mutationResult; mutationErr != nil {
		t.Fatalf("change remote object metadata: %v", mutationErr)
	}
	if err == nil || !strings.Contains(err.Error(), "changed during transfer") {
		t.Fatalf("changed remote object error = %v", err)
	}
	assertCopyDirectoryEmpty(t, destination)
}

func TestCopyRunnerRcloneNoOpUsesArtifactIdentityNotFilenameOrModTime(t *testing.T) {
	_, sourceDirectory := prepareRcloneCopySource(t)
	destination := t.TempDir()
	writeCopyRunnerFixture(t, sourceDirectory, "original.sqlite", "artifact_stable", "producer_one", "job_one", time.Now(), false)
	job := rcloneCopyRunnerJob(t, destination)
	if _, err := (CopyRunner{}).Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}

	originalArtifact := filepath.Join(sourceDirectory, "original.sqlite")
	originalManifest := artifactManifestPath(originalArtifact)
	renamedArtifact := filepath.Join(sourceDirectory, "renamed.sqlite")
	renamedManifest := artifactManifestPath(renamedArtifact)
	if err := os.Rename(originalArtifact, renamedArtifact); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(originalManifest, renamedManifest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(renamedArtifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(renamedArtifact, info.ModTime(), info.ModTime().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	outcome, err := (CopyRunner{}).Run(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Discovered != 1 || outcome.AlreadyPresent != 1 || outcome.BytesCopied != 0 || len(outcome.Artifacts) != 1 || !outcome.Artifacts[0].AlreadyPresent {
		t.Fatalf("identity no-op outcome = %+v", outcome)
	}
	if _, err := os.Stat(filepath.Join(destination, "original.sqlite")); err != nil {
		t.Fatalf("original identity copy missing: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(destination, "renamed.sqlite")); !os.IsNotExist(err) {
		t.Fatalf("filename change created a duplicate: %v", err)
	}
}

func TestCopyRunnerRcloneRefusesChangedIdentityAtExistingFilename(t *testing.T) {
	_, sourceDirectory := prepareRcloneCopySource(t)
	destination := t.TempDir()
	manifestPath := writeCopyRunnerFixture(t, sourceDirectory, "reused.sqlite", "artifact_original", "producer_one", "job_one", time.Now(), false)
	job := rcloneCopyRunnerJob(t, destination)
	if _, err := (CopyRunner{}).Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	destinationArtifact := filepath.Join(destination, "reused.sqlite")
	before, err := os.ReadFile(destinationArtifact)
	if err != nil {
		t.Fatal(err)
	}

	rewriteRcloneCopyManifest(t, manifestPath, func(manifest *ArtifactManifest) {
		manifest.ArtifactID = "artifact_replacement"
		manifest.RunID = "run_artifact_replacement"
	})
	_, err = (CopyRunner{}).Run(context.Background(), job)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("changed identity collision error = %v", err)
	}
	after, readErr := os.ReadFile(destinationArtifact)
	if readErr != nil || !bytes.Equal(after, before) {
		t.Fatalf("changed identity overwrote immutable destination: read=%v", readErr)
	}
	manifest, readErr := ReadArtifactManifestForArtifactRequired(destinationArtifact)
	if readErr != nil || manifest.ArtifactID != "artifact_original" {
		t.Fatalf("destination manifest changed: manifest=%+v err=%v", manifest, readErr)
	}
}

func TestCopyRunnerKeepsLocalToRclonePushDisabled(t *testing.T) {
	source := t.TempDir()
	toolLookedUp := false
	originalFinder := findRcloneTool
	findRcloneTool = func(string) (string, error) {
		toolLookedUp = true
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { findRcloneTool = originalFinder })
	job := CopyJob{
		ID: "copy_disabled_push", Name: "disabled push", Mode: CopyModePush,
		Source:      CopyEndpoint{Kind: CopyEndpointLocal, Location: source},
		Destination: CopyEndpoint{Kind: CopyEndpointRclone, Location: "rclone://archive/team/backups"},
		Trigger:     CopyTriggerManual, Schedule: Schedule{Kind: ScheduleManual},
		Verification: CopyVerificationSHA256Format, Retention: Retention{KeepLast: 14}, TimeoutMinutes: 5,
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := (CopyRunner{}).Run(context.Background(), job)
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("local-to-rclone push error = %v", err)
	}
	if toolLookedUp {
		t.Fatal("disabled local-to-rclone push invoked rclone")
	}
}

func prepareRcloneCopySource(t *testing.T) (string, string) {
	t.Helper()
	remoteRoot := t.TempDir()
	sourceDirectory := filepath.Join(remoteRoot, "team", "backups")
	if err := os.MkdirAll(sourceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := writeFakeRclone(t)
	originalFinder := findRcloneTool
	findRcloneTool = func(string) (string, error) { return tool, nil }
	t.Cleanup(func() { findRcloneTool = originalFinder })
	t.Setenv("DBTERM_TEST_RCLONE_ROOT", remoteRoot)
	return remoteRoot, sourceDirectory
}

func rcloneCopyRunnerJob(t *testing.T, destination string) CopyJob {
	t.Helper()
	job := CopyJob{
		ID: "copy_rclone", Name: "rclone vault pull", Mode: CopyModePull,
		Source:      CopyEndpoint{Kind: CopyEndpointRclone, Location: "rclone://archive/team/backups"},
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: destination},
		Trigger:     CopyTriggerManual, Schedule: Schedule{Kind: ScheduleManual},
		Verification: CopyVerificationSHA256Format, Retention: Retention{KeepLast: 14}, TimeoutMinutes: 5,
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	return job
}

func rewriteRcloneCopyManifest(t *testing.T, path string, change func(*ArtifactManifest)) {
	t.Helper()
	manifest, err := ReadArtifactManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	change(manifest)
	var encoded bytes.Buffer
	if err := EncodeArtifactManifest(&encoded, *manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func snapshotCopyDirectory(t *testing.T, directory string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = data
	}
	return result
}

func assertCopyDirectoryEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("copy failure left destination files: %v", entryNames(entries))
	}
}
