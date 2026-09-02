package backup

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStageCopyArtifactForInspectionLocalSuccessInspectAndCleanup(t *testing.T) {
	job, result, _ := localCopyInspectionFixture(t)
	stagingRoot := filepath.Join(t.TempDir(), "private-inspection")

	staged, err := StageCopyArtifactForInspection(context.Background(), job, result, CopyInspectionStageOptions{
		Location: CopyInspectionDestination, StagingRoot: stagingRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	stageDirectory := filepath.Dir(staged.Path)
	if !pathWithin(stagingRoot, staged.Path) {
		t.Fatalf("staged path %q is not below %q", staged.Path, stagingRoot)
	}
	if staged.Manifest.ArtifactID != result.ArtifactID || staged.Origin != result.Destination {
		t.Fatalf("unexpected staged identity: %+v", staged)
	}
	inspection, err := staged.Inspect(context.Background(), InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Format != FormatSQLiteDatabase || inspection.Manifest == nil || inspection.Manifest.ArtifactID != result.ArtifactID {
		t.Fatalf("unexpected staged inspection: %+v", inspection)
	}
	if err := staged.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stageDirectory); !os.IsNotExist(err) {
		t.Fatalf("private stage still exists after cleanup: %v", err)
	}
	if err := staged.Cleanup(); err != nil {
		t.Fatalf("idempotent cleanup failed: %v", err)
	}
}

func TestStageCopyArtifactForInspectionLocalRejectsTamperAndCleansFailure(t *testing.T) {
	t.Run("artifact bytes", func(t *testing.T) {
		job, result, _ := localCopyInspectionFixture(t)
		file, err := os.OpenFile(result.Destination, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("tamper"); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		stagingRoot := filepath.Join(t.TempDir(), "private-inspection")
		_, err = StageCopyArtifactForInspection(context.Background(), job, result, CopyInspectionStageOptions{StagingRoot: stagingRoot})
		if err == nil || !strings.Contains(err.Error(), "exactly") {
			t.Fatalf("tampered artifact error = %v", err)
		}
		assertEmptyInspectionStageRoot(t, stagingRoot)
	})

	t.Run("manifest bytes", func(t *testing.T) {
		job, result, _ := localCopyInspectionFixture(t)
		file, err := os.OpenFile(result.ManifestPath, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(" \n"); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		stagingRoot := filepath.Join(t.TempDir(), "private-inspection")
		_, err = StageCopyArtifactForInspection(context.Background(), job, result, CopyInspectionStageOptions{StagingRoot: stagingRoot})
		if err == nil || !strings.Contains(err.Error(), "sidecar identity recorded at copy time") {
			t.Fatalf("tampered manifest error = %v", err)
		}
		assertEmptyInspectionStageRoot(t, stagingRoot)
	})

	t.Run("missing manifest", func(t *testing.T) {
		job, result, _ := localCopyInspectionFixture(t)
		if err := os.Remove(result.ManifestPath); err != nil {
			t.Fatal(err)
		}
		_, err := StageCopyArtifactForInspection(context.Background(), job, result, CopyInspectionStageOptions{StagingRoot: filepath.Join(t.TempDir(), "private-inspection")})
		if err == nil || !strings.Contains(err.Error(), "completion manifest") {
			t.Fatalf("missing manifest error = %v", err)
		}
	})

	t.Run("catalog identity", func(t *testing.T) {
		job, result, _ := localCopyInspectionFixture(t)
		result.ArtifactID = "artifact_different"
		_, err := StageCopyArtifactForInspection(context.Background(), job, result, CopyInspectionStageOptions{StagingRoot: filepath.Join(t.TempDir(), "private-inspection")})
		if err == nil || !strings.Contains(err.Error(), "catalog artifact identity") {
			t.Fatalf("catalog identity error = %v", err)
		}
	})
}

func TestStageCopyArtifactForInspectionSourceRejectsCopyTimeManifestMismatch(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	manifestPath := writeCopyRunnerFixture(t, source, "source.sqlite", "artifact_source_manifest", "producer", "job", time.Now(), false)
	artifactPath := strings.TrimSuffix(manifestPath, ArtifactManifestSuffix)
	manifest, err := ReadArtifactManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	job := CopyJob{
		Name: "source-side inspection", Mode: CopyModePush,
		Source:      CopyEndpoint{Kind: CopyEndpointLocal, Location: source},
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: destination},
		Trigger:     CopyTriggerManual, Verification: CopyVerificationSHA256Format,
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	result := copyInspectionResult(*manifest, manifestBytes, artifactPath,
		filepath.Join(destination, "source.sqlite"), filepath.Join(destination, "source.sqlite"+ArtifactManifestSuffix))
	staged, err := StageCopyArtifactForInspection(context.Background(), job, result, CopyInspectionStageOptions{
		Location: CopyInspectionSource, StagingRoot: filepath.Join(t.TempDir(), "private-inspection"),
	})
	if err != nil {
		t.Fatalf("inspect unchanged source sidecar: %v", err)
	}
	if err := staged.Cleanup(); err != nil {
		t.Fatal(err)
	}

	file, err := os.OpenFile(manifestPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(" \n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	stagingRoot := filepath.Join(t.TempDir(), "private-inspection")
	_, err = StageCopyArtifactForInspection(context.Background(), job, result, CopyInspectionStageOptions{
		Location: CopyInspectionSource, StagingRoot: stagingRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "sidecar identity recorded at copy time") {
		t.Fatalf("source-side manifest mismatch error = %v", err)
	}
	assertEmptyInspectionStageRoot(t, stagingRoot)
}

func TestStageCopyArtifactForInspectionRejectsTraversalPartialAndUnsupportedTopology(t *testing.T) {
	job, result, _ := localCopyInspectionFixture(t)
	t.Run("outside endpoint", func(t *testing.T) {
		outside := filepath.Join(filepath.Dir(job.Destination.Location), "outside.sqlite")
		changed := result
		changed.Destination = outside
		changed.ManifestPath = artifactManifestPath(outside)
		_, err := StageCopyArtifactForInspection(context.Background(), job, changed, CopyInspectionStageOptions{})
		if err == nil || !strings.Contains(err.Error(), "direct child") {
			t.Fatalf("traversal error = %v", err)
		}
	})
	t.Run("partial", func(t *testing.T) {
		changed := result
		changed.Destination = filepath.Join(job.Destination.Location, "artifact.sqlite.partial")
		changed.ManifestPath = artifactManifestPath(changed.Destination)
		_, err := StageCopyArtifactForInspection(context.Background(), job, changed, CopyInspectionStageOptions{})
		if err == nil || !strings.Contains(err.Error(), "partial") {
			t.Fatalf("partial error = %v", err)
		}
	})
	t.Run("rclone destination", func(t *testing.T) {
		changedJob := job
		changedJob.Mode = CopyModePush
		changedJob.Destination = CopyEndpoint{Kind: CopyEndpointRclone, Location: "rclone://archive/vault"}
		changed := result
		changed.Destination = "rclone://archive/vault/artifact.sqlite"
		changed.ManifestPath = changed.Destination + ArtifactManifestSuffix
		_, err := StageCopyArtifactForInspection(context.Background(), changedJob, changed, CopyInspectionStageOptions{})
		if err == nil || !strings.Contains(err.Error(), "only for a pull job source") {
			t.Fatalf("unsupported topology error = %v", err)
		}
	})
}

func TestStageCopyArtifactForInspectionSFTPDestination(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSFTP)
	server.makeDirectory(t, endpoint, "/vault")
	fixtureDirectory := t.TempDir()
	manifestPath := writeCopyRunnerFixture(t, fixtureDirectory, "remote.sqlite", "artifact_remote_inspect", "producer_one", "job_one", time.Now(), false)
	artifactPath := strings.TrimSuffix(manifestPath, ArtifactManifestSuffix)
	artifactBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	client := server.client(t, endpoint)
	if err := writeSFTPTestFile(client.client, "/vault/remote.sqlite", artifactBytes); err != nil {
		t.Fatal(err)
	}
	if err := writeSFTPTestFile(client.client, "/vault/remote.sqlite"+ArtifactManifestSuffix, manifestBytes); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()

	manifest, err := ReadArtifactManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	job := CopyJob{
		Name: "SFTP inspection", Mode: CopyModePush,
		Source: CopyEndpoint{Kind: CopyEndpointLocal, Location: fixtureDirectory}, Destination: endpoint,
		Trigger: CopyTriggerManual, Schedule: Schedule{Kind: ScheduleManual}, Verification: CopyVerificationSHA256Format,
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	result := copyInspectionResult(*manifest, manifestBytes, artifactPath,
		sftpDisplayPath(endpoint, "remote.sqlite"), sftpDisplayPath(endpoint, "remote.sqlite"+ArtifactManifestSuffix))
	staged, err := StageCopyArtifactForInspection(context.Background(), job, result, CopyInspectionStageOptions{StagingRoot: filepath.Join(t.TempDir(), "private-inspection")})
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Cleanup()
	if _, err := staged.Inspect(context.Background(), InspectOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestStageCopyArtifactForInspectionSFTPRejectsHostKeyMismatch(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSFTP)
	server.makeDirectory(t, endpoint, "/vault")
	job, result, manifest := localCopyInspectionFixture(t)
	job.Mode = CopyModePush
	job.Destination = endpoint
	job.Destination.PinnedHostKey = "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, sha256.Size))
	artifactName := filepath.Base(result.Destination)
	result.Destination = sftpDisplayPath(job.Destination, artifactName)
	result.ManifestPath = sftpDisplayPath(job.Destination, artifactName+ArtifactManifestSuffix)
	result.ManifestSize = int64(len(mustEncodeInspectionManifest(t, manifest)))
	manifestDigest := sha256.Sum256(mustEncodeInspectionManifest(t, manifest))
	result.ManifestSHA256 = hex.EncodeToString(manifestDigest[:])

	_, err := StageCopyArtifactForInspection(context.Background(), job, result, CopyInspectionStageOptions{StagingRoot: filepath.Join(t.TempDir(), "private-inspection")})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "host key") {
		t.Fatalf("host-key mismatch error = %v", err)
	}
}

func TestStageCopyArtifactForInspectionRcloneSource(t *testing.T) {
	_, sourceDirectory := prepareRcloneCopySource(t)
	manifestPath := writeCopyRunnerFixture(t, sourceDirectory, "remote.sqlite", "artifact_rclone_inspect", "producer_one", "job_one", time.Now(), false)
	manifest, err := ReadArtifactManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	job := CopyJob{
		Name: "rclone inspection", Mode: CopyModePull,
		Source:      CopyEndpoint{Kind: CopyEndpointRclone, Location: "rclone://archive/team/backups"},
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: destination},
		Trigger:     CopyTriggerManual, Schedule: Schedule{Kind: ScheduleManual}, Verification: CopyVerificationSHA256Format,
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	result := copyInspectionResult(*manifest, manifestBytes, "rclone://archive/team/backups/remote.sqlite",
		filepath.Join(destination, "remote.sqlite"), filepath.Join(destination, "remote.sqlite"+ArtifactManifestSuffix))
	staged, err := StageCopyArtifactForInspection(context.Background(), job, result, CopyInspectionStageOptions{
		Location: CopyInspectionSource, StagingRoot: filepath.Join(t.TempDir(), "private-inspection"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Cleanup()
	if _, err := staged.Inspect(context.Background(), InspectOptions{}); err != nil {
		t.Fatal(err)
	}
}

func localCopyInspectionFixture(t *testing.T) (CopyJob, CopyArtifactResult, ArtifactManifest) {
	t.Helper()
	source := t.TempDir()
	destination := t.TempDir()
	manifestPath := writeCopyRunnerFixture(t, destination, "artifact.sqlite", "artifact_inspect", "producer_one", "job_one", time.Now(), false)
	artifactPath := strings.TrimSuffix(manifestPath, ArtifactManifestSuffix)
	manifest, err := ReadArtifactManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	job := CopyJob{
		Name: "local inspection", Mode: CopyModePush,
		Source: CopyEndpoint{Kind: CopyEndpointLocal, Location: source}, Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: destination},
		Trigger: CopyTriggerManual, Schedule: Schedule{Kind: ScheduleManual}, Verification: CopyVerificationSHA256Format,
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	return job, copyInspectionResult(*manifest, manifestBytes, filepath.Join(source, "artifact.sqlite"), artifactPath, manifestPath), *manifest
}

func copyInspectionResult(manifest ArtifactManifest, manifestBytes []byte, source, destination, manifestPath string) CopyArtifactResult {
	manifestDigest := sha256.Sum256(manifestBytes)
	return CopyArtifactResult{
		ArtifactID: manifest.ArtifactID, Source: source, Destination: destination,
		SourceCreatedAt: manifest.CreatedAt, SizeBytes: manifest.SizeBytes, SHA256: manifest.SHA256,
		Verification: CopyVerificationSHA256Format, VerifiedAt: time.Now().UTC(),
		ManifestPath: manifestPath, ManifestSize: int64(len(manifestBytes)), ManifestSHA256: hex.EncodeToString(manifestDigest[:]),
		PublicationState: ArtifactPublicationComplete,
	}
}

func mustEncodeInspectionManifest(t *testing.T, manifest ArtifactManifest) []byte {
	t.Helper()
	var encoded strings.Builder
	if err := EncodeArtifactManifest(&encoded, manifest); err != nil {
		t.Fatal(err)
	}
	return []byte(encoded.String())
}

func assertEmptyInspectionStageRoot(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed inspection left private staging entries: %+v", entries)
	}
}
