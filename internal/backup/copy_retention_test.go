package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCopyRetentionPlanRefusesNewRecoveryPointAfterPreview(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	source := t.TempDir()
	destination := t.TempDir()
	base := time.Now().Add(-3 * time.Hour).UTC()
	writeCopyRunnerFixture(t, source, "old.sqlite", "artifact_old", "producer", "source_job", base, false)
	writeCopyRunnerFixture(t, source, "new.sqlite", "artifact_new", "producer", "source_job", base.Add(time.Hour), false)
	job := localCopyRunnerJob(t, source, destination)
	job.Retention = Retention{KeepLast: 10}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	if _, err := RunCopyJobNow(context.Background(), store, job.ID, nil); err != nil {
		t.Fatal(err)
	}

	previewJob := job
	previewJob.Retention = Retention{KeepLast: 1}
	preview, err := PreviewCopyRetention(context.Background(), store, previewJob, base.Add(2*time.Hour))
	if err != nil || len(preview) != 1 || preview[0].ArtifactID != "artifact_old" {
		t.Fatalf("initial preview = %+v, err %v", preview, err)
	}

	writeCopyRunnerFixture(t, source, "newest.sqlite", "artifact_newest", "producer", "source_job", base.Add(2*time.Hour), false)
	if _, err := RunCopyJobNow(context.Background(), store, job.ID, nil); err != nil {
		t.Fatal(err)
	}
	removed, err := ApplyCopyRetentionPlan(context.Background(), store, previewJob, base.Add(3*time.Hour), preview)
	if !errors.Is(err, ErrCopyRetentionPlanChanged) || len(removed) != 0 {
		t.Fatalf("changed-plan apply = removed %v, err %v", removed, err)
	}
	for _, name := range []string{"old.sqlite", "new.sqlite", "newest.sqlite"} {
		if _, err := os.Stat(filepath.Join(destination, name)); err != nil {
			t.Fatalf("changed-plan apply removed %s: %v", name, err)
		}
	}
}

func TestCopyRetentionKeepsNewestVerifiedRecoveryPoints(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	source := t.TempDir()
	destination := t.TempDir()
	base := time.Now().Add(-3 * time.Hour).UTC()
	writeCopyRunnerFixture(t, source, "old.sqlite", "artifact_old", "producer", "source_job", base, false)
	writeCopyRunnerFixture(t, source, "middle.sqlite", "artifact_middle", "producer", "source_job", base.Add(time.Hour), false)
	writeCopyRunnerFixture(t, source, "new.sqlite", "artifact_new", "producer", "source_job", base.Add(2*time.Hour), false)
	job := localCopyRunnerJob(t, source, destination)
	job.Enabled = true
	job.Retention = Retention{KeepLast: 2}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	if _, err := RunCopyJobNow(context.Background(), store, job.ID, nil); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"middle.sqlite", "new.sqlite"} {
		if _, err := os.Stat(filepath.Join(destination, name)); err != nil {
			t.Fatalf("new retained recovery point %s is missing: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(destination, name+ArtifactManifestSuffix)); err != nil {
			t.Fatalf("new retained manifest %s is missing: %v", name, err)
		}
	}
	for _, path := range []string{
		filepath.Join(destination, "old.sqlite"),
		filepath.Join(destination, "old.sqlite"+ArtifactManifestSuffix),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("expired copy still exists at %s: %v", path, err)
		}
	}
	latest, ok, err := store.LatestCopyRun(context.Background(), job.ID)
	if err != nil || !ok {
		t.Fatalf("latest copy run: found=%t err=%v", ok, err)
	}
	pruned := 0
	for _, artifact := range latest.Artifacts {
		if artifact.ArtifactID == "artifact_old" {
			if artifact.PrunedAt.IsZero() || artifact.PruneReason != "retention" {
				t.Fatalf("expired copy was not durably marked pruned: %+v", artifact)
			}
			pruned++
		}
	}
	if pruned != 1 {
		t.Fatalf("pruned artifact records = %d, want 1: %+v", pruned, latest.Artifacts)
	}
}

func TestCopyRetentionRefusesChangedArtifactAndPreservesManifest(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	source := t.TempDir()
	destination := t.TempDir()
	base := time.Now().Add(-2 * time.Hour).UTC()
	writeCopyRunnerFixture(t, source, "old.sqlite", "artifact_old", "producer", "source_job", base, false)
	job := localCopyRunnerJob(t, source, destination)
	job.Enabled = true
	job.Retention = Retention{KeepLast: 10}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	if _, err := RunCopyJobNow(context.Background(), store, job.ID, nil); err != nil {
		t.Fatal(err)
	}
	writeCopyRunnerFixture(t, source, "new.sqlite", "artifact_new", "producer", "source_job", base.Add(time.Hour), false)
	// Copy the new point without selecting the old one for retention yet.
	if _, err := RunCopyJobNow(context.Background(), store, job.ID, nil); err != nil {
		t.Fatal(err)
	}

	oldPath := filepath.Join(destination, "old.sqlite")
	file, err := os.OpenFile(oldPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("changed"), 0); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	job.Retention = Retention{KeepLast: 1}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyCopyRetention(context.Background(), store, job, time.Now()); err == nil {
		t.Fatal("changed copied artifact was deleted instead of refused")
	}
	for _, path := range []string{oldPath, oldPath + ArtifactManifestSuffix} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retention did not preserve changed recovery-point component %s: %v", path, err)
		}
	}
}
