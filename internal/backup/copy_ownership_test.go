package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCopyOwnershipKeepsCompletedArtifactFromFailedBatch(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	source := t.TempDir()
	destination := t.TempDir()
	base := time.Now().UTC().Add(-2 * time.Hour)
	writeCopyRunnerFixture(t, source, "first.sqlite", "artifact_first", "producer", "source_job", base, false)
	writeCopyRunnerFixture(t, source, "second.sqlite", "artifact_second", "producer", "source_job", base.Add(time.Hour), true)
	job := localCopyRunnerJob(t, source, destination)
	job.Enabled = true
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}

	run, err := RunCopyJobNow(context.Background(), store, job.ID, nil)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("failed batch error = %v, want SHA-256 failure", err)
	}
	if run.Status != RunFailed || len(run.Artifacts) != 1 || run.Artifacts[0].ArtifactID != "artifact_first" || run.Artifacts[0].PublicationState != ArtifactPublicationComplete {
		t.Fatalf("failed batch lost its completed ownership result: %+v", run)
	}
	owned, err := store.listOwnedUnprunedCopyArtifacts(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 1 || owned[0].Artifact.ArtifactID != "artifact_first" {
		t.Fatalf("owned recovery points after failed batch = %+v", owned)
	}
	runs, err := store.ListCopyRuns(context.Background(), job.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectCopyArtifactForInspection(runs, "artifact_first")
	if err != nil || selected.ArtifactID != "artifact_first" {
		t.Fatalf("select completed failed-batch artifact = %+v, %v", selected, err)
	}

	// A later successful batch must apply this job's retention policy to the
	// completed recovery point even though its original aggregate run failed.
	job.Retention = Retention{KeepLast: 1}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(source, "second.sqlite"), filepath.Join(source, "second.sqlite") + ArtifactManifestSuffix} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	writeCopyRunnerFixture(t, source, "new.sqlite", "artifact_new", "producer", "source_job", base.Add(2*time.Hour), false)
	if _, err := RunCopyJobNow(context.Background(), store, job.ID, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(destination, "first.sqlite"), filepath.Join(destination, "first.sqlite") + ArtifactManifestSuffix} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("failed-run recovery point was not retention-managed at %s: %v", path, err)
		}
	}
	storedFailed, err := store.GetCopyRun(context.Background(), run.ID)
	if err != nil || len(storedFailed.Artifacts) != 1 || storedFailed.Artifacts[0].PrunedAt.IsZero() {
		t.Fatalf("failed-run recovery point was not durably marked pruned: %+v, %v", storedFailed, err)
	}
}

func TestCopyOwnershipAdoptsReverifiedExistingArtifactOnce(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	writeCopyRunnerFixture(t, source, "existing.sqlite", "artifact_existing", "producer", "source_job", time.Now().UTC().Add(-time.Hour), false)
	job := localCopyRunnerJob(t, source, destination)
	if _, err := (CopyRunner{}).Run(context.Background(), job); err != nil {
		t.Fatalf("prepare destination outside catalog: %v", err)
	}

	store, err := OpenStore(filepath.Join(t.TempDir(), "recreated.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job.Enabled = true
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	adoption, err := RunCopyJobNow(context.Background(), store, job.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if adoption.AlreadyPresent != 1 || adoption.BytesCopied != 0 || len(adoption.Artifacts) != 1 || !adoption.Artifacts[0].AlreadyPresent || adoption.Artifacts[0].PublicationState != ArtifactPublicationComplete {
		t.Fatalf("fresh catalog did not adopt exactly one reverified copy: %+v", adoption)
	}
	second, err := RunCopyJobNow(context.Background(), store, job.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.AlreadyPresent != 1 || len(second.Artifacts) != 0 {
		t.Fatalf("repeat observation duplicated durable ownership: %+v", second)
	}
	owned, err := store.listOwnedUnprunedCopyArtifacts(context.Background(), job.ID)
	if err != nil || len(owned) != 1 || owned[0].Artifact.ArtifactID != "artifact_existing" {
		t.Fatalf("active catalog ownership = %+v, %v", owned, err)
	}
}

func TestCopyOwnershipFreshCatalogDoesNotAdoptUnrelatedDestinationFiles(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	created := time.Now().UTC().Add(-time.Hour)
	writeCopyRunnerFixture(t, source, "wanted.sqlite", "artifact_wanted", "producer", "source_job", created, false)
	job := localCopyRunnerJob(t, source, destination)
	if _, err := (CopyRunner{}).Run(context.Background(), job); err != nil {
		t.Fatalf("prepare expected destination copy: %v", err)
	}
	writeCopyRunnerFixture(t, destination, "unrelated.sqlite", "artifact_unrelated", "other_producer", "other_job", created, false)

	store, err := OpenStore(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job.Enabled = true
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	run, err := RunCopyJobNow(context.Background(), store, job.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Artifacts) != 1 || run.Artifacts[0].ArtifactID != "artifact_wanted" {
		t.Fatalf("fresh catalog adopted an unrelated destination file: %+v", run.Artifacts)
	}
	owned, err := store.listOwnedUnprunedCopyArtifacts(context.Background(), job.ID)
	if err != nil || len(owned) != 1 || owned[0].Artifact.ArtifactID != "artifact_wanted" {
		t.Fatalf("fresh catalog ownership = %+v, %v", owned, err)
	}
}

func TestCopyOwnershipNeverMakesIncompletePublicationEligible(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job := localCopyRunnerJob(t, t.TempDir(), t.TempDir())
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := store.ClaimCopyJob(context.Background(), job.ID, "owner", now); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartCopyRun(context.Background(), job.ID, CopyTriggerManual, now)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = RunFailed
	run.Error = "manifest publication failed"
	run.FinishedAt = now.Add(time.Minute)
	run.Artifacts = []CopyArtifactResult{{
		ArtifactID: "artifact_incomplete", Source: filepath.Join(job.Source.Location, "incomplete.sqlite"), Destination: filepath.Join(job.Destination.Location, "incomplete.sqlite"),
		SizeBytes: 8, SHA256: strings.Repeat("a", 64), Verification: CopyVerificationSHA256Format,
		VerifiedAt: now.Add(30 * time.Second), PublicationState: ArtifactPublicationArtifactOnly,
	}}
	if err := store.FinishCopyRun(context.Background(), &run, "owner"); err != nil {
		t.Fatal(err)
	}
	owned, err := store.listOwnedUnprunedCopyArtifacts(context.Background(), job.ID)
	if err != nil || len(owned) != 0 {
		t.Fatalf("incomplete publication became retention-eligible: %+v, %v", owned, err)
	}
	runs, err := store.ListCopyRuns(context.Background(), job.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SelectCopyArtifactForInspection(runs, "artifact_incomplete"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete publication inspection error = %v", err)
	}
}
