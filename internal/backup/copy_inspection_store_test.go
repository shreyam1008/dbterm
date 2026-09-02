package backup

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestListCopyArtifactsForInspectionReturnsOwnedUnprunedNewestFirst(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	source, destination := t.TempDir(), t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	writeCopyRunnerFixture(t, source, "older.sqlite", "artifact_older", "producer", "source_job", now.Add(-2*time.Hour), false)
	writeCopyRunnerFixture(t, source, "newer.sqlite", "artifact_newer", "producer", "source_job", now.Add(-time.Hour), false)
	job := localCopyRunnerJob(t, source, destination)
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	run, err := RunCopyJobNow(context.Background(), store, job.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	artifacts, err := store.ListCopyArtifactsForInspection(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 || artifacts[0].ArtifactID != "artifact_newer" || artifacts[1].ArtifactID != "artifact_older" {
		t.Fatalf("inspection recovery points = %+v, want newest then older", artifacts)
	}

	olderDestination := filepath.Join(destination, "older.sqlite")
	if err := store.MarkCopyArtifactPruned(context.Background(), run.ID, "artifact_older", olderDestination, "test", now); err != nil {
		t.Fatal(err)
	}
	artifacts, err = store.ListCopyArtifactsForInspection(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].ArtifactID != "artifact_newer" {
		t.Fatalf("inspection recovery points after prune = %+v, want only newer", artifacts)
	}
}
