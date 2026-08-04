package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestApplyRetentionDeletesArtifactsAndMarksRunsPruned(t *testing.T) {
	stateDir := t.TempDir()
	destination := t.TempDir()
	store, err := OpenStore(filepath.Join(stateDir, "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	job := Job{
		Name: "retention", ConnectionID: "conn", Enabled: true, Destination: destination,
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual},
		Retention: Retention{KeepLast: 2},
	}
	if err := job.ApplyDefaults(now.Add(-5 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertJob(ctx, &job); err != nil {
		t.Fatal(err)
	}

	paths := make([]string, 4)
	for index := range paths {
		paths[index] = filepath.Join(destination, "backup-"+string(rune('1'+index))+".sql")
		if err := os.WriteFile(paths[index], []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		startedAt := now.Add(time.Duration(index-4) * 24 * time.Hour)
		if _, err := store.ClaimJob(ctx, job.ID, "retention-test", startedAt); err != nil {
			t.Fatal(err)
		}
		run, err := store.StartRun(ctx, job.ID, TriggerManual, startedAt)
		if err != nil {
			t.Fatal(err)
		}
		run.Status = RunSucceeded
		run.FinishedAt = startedAt.Add(time.Minute)
		run.Artifact = Artifact{Path: paths[index], Size: 6, Verified: true, CreatedAt: run.FinishedAt}
		if err := store.FinishRun(ctx, &run, "retention-test"); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := ApplyRetention(ctx, store, job, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed %d artifacts, want 2: %#v", len(removed), removed)
	}
	for index, path := range paths {
		_, statErr := os.Stat(path)
		if index < 2 && !os.IsNotExist(statErr) {
			t.Fatalf("expired artifact %s still exists (stat error %v)", path, statErr)
		}
		if index >= 2 && statErr != nil {
			t.Fatalf("retained artifact %s: %v", path, statErr)
		}
	}

	runs, err := store.ListRuns(ctx, job.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 4 {
		t.Fatalf("history has %d runs, want 4", len(runs))
	}
	pruned := make(map[string]Artifact, 2)
	for _, run := range runs {
		if !run.Artifact.PrunedAt.IsZero() {
			pruned[run.Artifact.Path] = run.Artifact
		}
	}
	for _, path := range paths[:2] {
		artifact, ok := pruned[path]
		if !ok {
			t.Errorf("run for removed artifact %s was not marked pruned", path)
			continue
		}
		if artifact.PruneReason != "retention" || !artifact.PrunedAt.Equal(now) {
			t.Errorf("prune metadata for %s = %#v", path, artifact)
		}
	}
	for _, path := range paths[2:] {
		if _, ok := pruned[path]; ok {
			t.Errorf("retained artifact %s was marked pruned", path)
		}
	}

	removed, err = ApplyRetention(ctx, store, job, now.Add(time.Hour))
	if err != nil || len(removed) != 0 {
		t.Fatalf("second ApplyRetention() = %#v, %v; want idempotent no-op", removed, err)
	}
}

func TestVerifyRecordedArtifactForPruneRefusesChangedContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.dump")
	if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	exists, err := verifyRecordedArtifactForPrune(context.Background(), path, Artifact{
		Path: path, Size: 6, SHA256: strings.Repeat("0", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("verification = (%t, %v), want checksum refusal", exists, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("changed artifact was modified: %v", statErr)
	}
}

func TestApplyRetentionConsidersArtifactsBeyondHistoryViewLimit(t *testing.T) {
	stateDir := t.TempDir()
	destination := t.TempDir()
	store, err := OpenStore(filepath.Join(stateDir, "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	job := Job{
		Name: "large retention history", ConnectionID: "conn", Enabled: true, Destination: destination,
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual},
		Retention: Retention{KeepLast: 1500},
	}
	if err := store.UpsertJob(ctx, &job); err != nil {
		t.Fatal(err)
	}

	const runCount = 1505
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < runCount; index++ {
		path := filepath.Join(destination, "artifact-"+strconv.Itoa(index)+".dump")
		if index < 5 {
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
		}
		finishedAt := now.Add(time.Duration(index-runCount) * time.Minute)
		run := Run{
			ID: "run_bulk_" + strconv.Itoa(index), JobID: job.ID, Trigger: TriggerScheduled,
			Status: RunSucceeded, StartedAt: finishedAt.Add(-time.Second), FinishedAt: finishedAt,
			Artifact: Artifact{Path: path, Size: 1, Verified: true, CreatedAt: finishedAt},
		}
		payload, err := json.Marshal(run)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO backup_runs
			(id, job_id, trigger_kind, status, started_at, finished_at, run_json)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, run.ID, run.JobID, run.Trigger, run.Status,
			formatTime(run.StartedAt), formatTime(run.FinishedAt), payload); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	removed, err := ApplyRetention(ctx, store, job, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 5 {
		t.Fatalf("removed %d artifacts, want 5 beyond the bounded history view", len(removed))
	}
	for index := 0; index < 5; index++ {
		path := filepath.Join(destination, "artifact-"+strconv.Itoa(index)+".dump")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expired artifact %s still exists (stat error %v)", path, err)
		}
		var payload []byte
		if err := store.db.QueryRowContext(ctx, `SELECT run_json FROM backup_runs WHERE id = ?`, "run_bulk_"+strconv.Itoa(index)).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var run Run
		if err := json.Unmarshal(payload, &run); err != nil {
			t.Fatal(err)
		}
		if run.Artifact.PrunedAt.IsZero() || run.Artifact.PruneReason != "retention" {
			t.Fatalf("run %d prune metadata = %#v", index, run.Artifact)
		}
	}
}

func TestApplyRetentionMaxTotalBytesKeepsNewestWithinCeiling(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	destination := t.TempDir()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	job := Job{
		Name: "size only", ConnectionID: "conn", Enabled: true, Destination: destination,
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual},
		Retention: Retention{MaxTotalBytes: 8}, TimeoutMinutes: 5,
	}
	if err := job.ApplyDefaults(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if job.Retention.KeepLast != 0 {
		t.Fatalf("size-only ApplyDefaults set KeepLast=%d, want 0", job.Retention.KeepLast)
	}
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}

	paths := make([]string, 4)
	for index := range paths {
		paths[index] = addRetentionRun(t, store, job, destination, now.Add(time.Duration(index-4)*time.Hour), 4, index)
	}
	removed, err := ApplyRetention(context.Background(), store, job, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || removed[0] != paths[0] || removed[1] != paths[1] {
		t.Fatalf("removed = %#v, want oldest-first %#v", removed, paths[:2])
	}
	for index, path := range paths {
		_, statErr := os.Stat(path)
		if index < 2 && !os.IsNotExist(statErr) {
			t.Fatalf("old artifact %s remains: %v", path, statErr)
		}
		if index >= 2 && statErr != nil {
			t.Fatalf("new artifact %s was not retained: %v", path, statErr)
		}
	}
}

func TestApplyRetentionAlwaysKeepsOversizedNewestArtifact(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	destination := t.TempDir()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	job := Job{
		Name: "oversized newest", ConnectionID: "conn", Enabled: true, Destination: destination,
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual},
		Retention: Retention{MaxTotalBytes: 2}, TimeoutMinutes: 5,
	}
	if err := job.ApplyDefaults(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	oldPath := addRetentionRun(t, store, job, destination, now.Add(-2*time.Hour), 1, 1)
	newPath := addRetentionRun(t, store, job, destination, now.Add(-time.Hour), 5, 2)
	removed, err := ApplyRetention(context.Background(), store, job, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != oldPath {
		t.Fatalf("removed = %#v, want only %s", removed, oldPath)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("oversized newest artifact was removed: %v", err)
	}
}

func addRetentionRun(t *testing.T, store *Store, job Job, destination string, startedAt time.Time, size int, suffix int) string {
	t.Helper()
	path := filepath.Join(destination, "size-artifact-"+strconv.Itoa(suffix)+".dump")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner := "size-retention-test"
	if _, err := store.ClaimJob(ctx, job.ID, owner, startedAt); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartRun(ctx, job.ID, TriggerManual, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = RunSucceeded
	run.FinishedAt = startedAt.Add(time.Minute)
	run.Artifact = Artifact{Path: path, Size: int64(size), Verified: true, CreatedAt: run.FinishedAt}
	if err := store.FinishRun(ctx, &run, owner); err != nil {
		t.Fatal(err)
	}
	return path
}
