package backup

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreJobRunLifecycleAndLease(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state", "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 8, 4, 4, 0, 0, 0, time.UTC)
	job := Job{
		Name:         "nightly",
		ConnectionID: "conn_test",
		Enabled:      true,
		Destination:  t.TempDir(),
		Compression:  CompressionGzip,
		Schedule:     Schedule{Kind: ScheduleInterval, EveryMinutes: 60},
		Retention:    Retention{KeepLast: 2},
	}
	if err := job.ApplyDefaults(now); err != nil {
		t.Fatal(err)
	}
	job.NextRunAt = now.Add(-time.Minute)
	if err := store.UpsertJob(ctx, &job); err != nil {
		t.Fatal(err)
	}

	claimed, err := store.ClaimDueJobs(ctx, now, "agent-a", 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDueJobs() = %#v, %v", claimed, err)
	}
	if _, err := store.ClaimJob(ctx, job.ID, "agent-b", now); !errors.Is(err, ErrJobBusy) {
		t.Fatalf("second claim error = %v, want ErrJobBusy", err)
	}

	run, err := store.StartRun(ctx, job.ID, TriggerScheduled, now)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = RunSucceeded
	run.Artifact = Artifact{Path: "/tmp/example.zst", Size: 42, SHA256: "abc", Verified: true}
	run.FinishedAt = now.Add(time.Minute)
	if err := store.FinishRun(ctx, &run, "agent-a"); err != nil {
		t.Fatal(err)
	}

	stored, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastRunAt.IsZero() || !stored.NextRunAt.After(run.FinishedAt) {
		t.Fatalf("stored times = last %s next %s", stored.LastRunAt, stored.NextRunAt)
	}
	runs, err := store.ListRuns(ctx, job.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].Status != RunSucceeded {
		t.Fatalf("ListRuns() = %#v, %v", runs, err)
	}
}

func TestStoreListLatestVerifiedUnprunedRunsPerJob(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	base := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	newJob := func(name string) Job {
		t.Helper()
		job := Job{
			Name: name, ConnectionID: "conn_" + name, Destination: t.TempDir(),
			Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual},
			Retention: Retention{KeepLast: 2},
		}
		if err := job.ApplyDefaults(base); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertJob(ctx, &job); err != nil {
			t.Fatal(err)
		}
		return job
	}
	finishSuccess := func(job Job, finishedAt time.Time, path string, pruned bool, mutate ...func(*Artifact)) Run {
		t.Helper()
		owner := "owner_" + path
		if _, err := store.ClaimJob(ctx, job.ID, owner, finishedAt.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		run, err := store.StartRun(ctx, job.ID, TriggerManual, finishedAt.Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		run.Status = RunSucceeded
		run.FinishedAt = finishedAt
		run.Artifact = Artifact{Path: path, Verified: true, PublicationState: ArtifactPublicationComplete}
		for _, apply := range mutate {
			apply(&run.Artifact)
		}
		if pruned {
			run.Artifact.PrunedAt = finishedAt.Add(time.Minute)
			run.Artifact.PruneReason = "test retention"
		}
		if err := store.FinishRun(ctx, &run, owner); err != nil {
			t.Fatal(err)
		}
		return run
	}

	firstJob := newJob("first")
	firstRetained := finishSuccess(firstJob, base.Add(time.Hour), "first-retained", false)
	finishSuccess(firstJob, base.Add(2*time.Hour), "first-newer-pruned", true)
	finishSuccess(firstJob, base.Add(3*time.Hour), "first-newer-unverified", false, func(artifact *Artifact) {
		artifact.Verified = false
	})
	finishSuccess(firstJob, base.Add(4*time.Hour), "first-newer-incomplete", false, func(artifact *Artifact) {
		artifact.PublicationState = ArtifactPublicationArtifactOnly
	})
	secondJob := newJob("second")
	finishSuccess(secondJob, base.Add(30*time.Minute), "second-older", false)
	secondLatest := finishSuccess(secondJob, base.Add(3*time.Hour), "second-latest", false)

	runs, err := store.ListLatestVerifiedUnprunedRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("ListLatestVerifiedUnprunedRuns() returned %d runs, want one for each of two jobs: %#v", len(runs), runs)
	}
	byJob := make(map[string]Run, len(runs))
	for _, run := range runs {
		if _, exists := byJob[run.JobID]; exists {
			t.Fatalf("ListLatestVerifiedUnprunedRuns() returned more than one run for job %s", run.JobID)
		}
		byJob[run.JobID] = run
	}
	if got := byJob[firstJob.ID]; got.ID != firstRetained.ID {
		t.Fatalf("first job latest retained run = %q, want older unpruned run %q", got.ID, firstRetained.ID)
	}
	if got := byJob[secondJob.ID]; got.ID != secondLatest.ID {
		t.Fatalf("second job latest retained run = %q, want %q", got.ID, secondLatest.ID)
	}
}

func TestTerminalRunJSONUpdatesMergeAfterConcurrentChange(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "backups.db")
	firstStore, err := OpenStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	secondStore, err := OpenStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()

	ctx := context.Background()
	now := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	job := Job{
		Name: "concurrent outcomes", ConnectionID: "conn", Destination: t.TempDir(),
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 1},
	}
	if err := job.ApplyDefaults(now); err != nil {
		t.Fatal(err)
	}
	if err := firstStore.UpsertJob(ctx, &job); err != nil {
		t.Fatal(err)
	}
	const owner = "outcome-owner"
	if _, err := firstStore.ClaimJob(ctx, job.ID, owner, now); err != nil {
		t.Fatal(err)
	}
	run, err := firstStore.StartRun(ctx, job.ID, TriggerManual, now)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = RunSucceeded
	run.FinishedAt = now.Add(time.Minute)
	run.Artifact = Artifact{Path: "backup.dump", Verified: true, PublicationState: ArtifactPublicationComplete}
	if err := firstStore.FinishRun(ctx, &run, owner); err != nil {
		t.Fatal(err)
	}

	interleaved := false
	prunedAt := now.Add(2 * time.Minute)
	err = firstStore.updateTerminalRunJSON(ctx, run.ID, "interleaved prune", func(current *Run) error {
		current.Artifact.PrunedAt = prunedAt
		current.Artifact.PruneReason = "retention"
		if !interleaved {
			interleaved = true
			return secondStore.recordRunNotification(ctx, run.ID, true, true, "")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := firstStore.ListRuns(ctx, job.ID, 1)
	if err != nil || len(stored) != 1 {
		t.Fatalf("ListRuns() = %#v, %v", stored, err)
	}
	got := stored[0]
	if !got.NotificationAttempted || !got.NotificationSent || got.Artifact.PrunedAt.IsZero() || got.Artifact.PruneReason != "retention" {
		t.Fatalf("concurrent terminal updates did not merge: %#v", got)
	}
}

func TestJobRejectsFilenameTraversal(t *testing.T) {
	job := Job{
		Name: "bad", ConnectionID: "conn", Destination: t.TempDir(),
		FilenameTemplate: "../escape", Compression: CompressionNone,
		Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 1},
	}
	if err := job.ApplyDefaults(time.Now()); err == nil {
		t.Fatal("expected filename traversal validation error")
	}
}

func TestClaimDueJobsSkipsMissedRunWhenCatchUpDisabled(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	job := Job{
		Name: "skip missed", ConnectionID: "conn", Enabled: true, Destination: t.TempDir(),
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleInterval, EveryMinutes: 15},
		Retention: Retention{KeepLast: 1},
	}
	if err := job.ApplyDefaults(now); err != nil {
		t.Fatal(err)
	}
	job.NextRunAt = now.Add(-47 * time.Minute)
	if err := store.UpsertJob(ctx, &job); err != nil {
		t.Fatal(err)
	}

	claimed, err := store.ClaimDueJobs(ctx, now, "agent", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %d jobs, want missed run to be skipped", len(claimed))
	}
	stored, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 4, 10, 13, 0, 0, time.UTC)
	if !stored.NextRunAt.Equal(want) {
		t.Fatalf("next run = %s, want %s", stored.NextRunAt, want)
	}
}

func TestSetJobEnabledCanDisableButNotReenableLegacyRcloneJob(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	job := Job{
		ID: "job_legacy_rclone", Name: "legacy remote", ConnectionID: "conn", Enabled: true,
		Destination: "rclone://vault/dbterm", FilenameTemplate: DefaultFilenameTemplate,
		Compression: CompressionZstd, Encryption: EncryptionNone,
		Schedule:  Schedule{Kind: ScheduleInterval, EveryMinutes: 30},
		Retention: Retention{KeepLast: 2}, TimeoutMinutes: 30,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), NextRunAt: now,
	}
	payload, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `INSERT INTO backup_jobs
		(id, name, connection_id, enabled, next_run_at, job_json, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?, ?)`, job.ID, job.Name, job.ConnectionID,
		formatTime(job.NextRunAt), payload, formatTime(job.CreatedAt), formatTime(job.UpdatedAt)); err != nil {
		t.Fatal(err)
	}

	if err := store.SetJobEnabled(context.Background(), job.ID, false); err != nil {
		t.Fatalf("disable legacy rclone job: %v", err)
	}
	disabled, err := store.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled || !disabled.NextRunAt.IsZero() {
		t.Fatalf("disabled job = %#v, want disabled with no next run", disabled)
	}
	if err := store.SetJobEnabled(context.Background(), job.ID, true); !errors.Is(err, ErrRcloneBackupPublicationDisabled) {
		t.Fatalf("re-enable legacy rclone job error = %v, want ErrRcloneBackupPublicationDisabled", err)
	}
}

func TestClaimDueJobsRunsMissedRunWhenCatchUpEnabled(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	job := Job{
		Name: "catch up", ConnectionID: "conn", Enabled: true, Destination: t.TempDir(),
		Compression: CompressionNone,
		Schedule:    Schedule{Kind: ScheduleInterval, EveryMinutes: 15, RunMissedOnWake: true},
		Retention:   Retention{KeepLast: 1},
	}
	if err := job.ApplyDefaults(now); err != nil {
		t.Fatal(err)
	}
	job.NextRunAt = now.Add(-47 * time.Minute)
	if err := store.UpsertJob(ctx, &job); err != nil {
		t.Fatal(err)
	}

	claimed, err := store.ClaimDueJobs(ctx, now, "agent", 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDueJobs() = %#v, %v; want one catch-up run", claimed, err)
	}
}

func TestClaimDueJobsAllowsNormalPollingJitter(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	job := Job{
		Name: "normal poll", ConnectionID: "conn", Enabled: true, Destination: t.TempDir(),
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleInterval, EveryMinutes: 15},
		Retention: Retention{KeepLast: 1},
	}
	if err := job.ApplyDefaults(now); err != nil {
		t.Fatal(err)
	}
	job.NextRunAt = now.Add(-missedRunGrace / 2)
	if err := store.UpsertJob(ctx, &job); err != nil {
		t.Fatal(err)
	}

	claimed, err := store.ClaimDueJobs(ctx, now, "agent", 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDueJobs() = %#v, %v; want normally due job", claimed, err)
	}
}

func TestClaimDueJobsSkipsMisfireAndFindsLaterDueJob(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	newJob := func(name string, due time.Time, runMissed bool) Job {
		t.Helper()
		job := Job{
			Name: name, ConnectionID: "conn", Enabled: true, Destination: t.TempDir(),
			Compression: CompressionNone,
			Schedule:    Schedule{Kind: ScheduleInterval, EveryMinutes: 15, RunMissedOnWake: runMissed},
			Retention:   Retention{KeepLast: 1},
		}
		if err := job.ApplyDefaults(now); err != nil {
			t.Fatal(err)
		}
		job.NextRunAt = due
		if err := store.UpsertJob(ctx, &job); err != nil {
			t.Fatal(err)
		}
		return job
	}
	skipped := newJob("skip first", now.Add(-time.Hour), false)
	runnable := newJob("run second", now.Add(-time.Minute), false)

	claimed, err := store.ClaimDueJobs(ctx, now, "agent", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != runnable.ID {
		t.Fatalf("ClaimDueJobs() = %#v, want later runnable job %s", claimed, runnable.ID)
	}
	advanced, err := store.GetJob(ctx, skipped.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !advanced.NextRunAt.After(now) {
		t.Fatalf("skipped job next run = %s, want future occurrence", advanced.NextRunAt)
	}
}

func TestUpsertJobRejectsDuplicateNameCaseInsensitively(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	first := Job{
		Name: "Nightly Backup", ConnectionID: "conn", Destination: t.TempDir(),
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 1},
	}
	if err := store.UpsertJob(ctx, &first); err != nil {
		t.Fatal(err)
	}
	duplicate := first
	duplicate.ID = ""
	duplicate.Name = "nightly backup"
	if err := store.UpsertJob(ctx, &duplicate); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("duplicate UpsertJob() error = %v", err)
	}
	jobs, err := store.ListJobs(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("ListJobs() = %#v, %v", jobs, err)
	}
}

func TestGetJobRejectsAmbiguousLegacyName(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	first := Job{
		ID: "job_legacy_one", Name: "Legacy Nightly", ConnectionID: "conn", Destination: t.TempDir(),
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 1},
	}
	if err := store.UpsertJob(ctx, &first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "job_legacy_two"
	second.Name = "legacy nightly"
	second.CreatedAt = first.CreatedAt.Add(time.Second)
	second.UpdatedAt = second.CreatedAt
	payload, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO backup_jobs
		(id, name, connection_id, enabled, next_run_at, job_json, created_at, updated_at)
		VALUES (?, ?, ?, 0, NULL, ?, ?, ?)`, second.ID, second.Name, second.ConnectionID, payload,
		formatTime(second.CreatedAt), formatTime(second.UpdatedAt)); err != nil {
		t.Fatal(err)
	}

	if _, err := store.GetJob(ctx, "LEGACY NIGHTLY"); err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), first.ID) || !strings.Contains(err.Error(), second.ID) {
		t.Fatalf("ambiguous GetJob() error = %v", err)
	}
	if byID, err := store.GetJob(ctx, first.ID); err != nil || byID.ID != first.ID {
		t.Fatalf("GetJob(exact ID) = %#v, %v", byID, err)
	}
}

func TestReconcileStaleRunsLeavesActiveLeaseUntouched(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	newRunningJob := func(name, owner string, claimedAt time.Time) (Job, Run) {
		t.Helper()
		job := Job{
			Name: name, ConnectionID: "conn", Enabled: true, Destination: t.TempDir(),
			Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual},
			Retention: Retention{KeepLast: 1}, TimeoutMinutes: 30,
		}
		if err := job.ApplyDefaults(claimedAt); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertJob(ctx, &job); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ClaimJob(ctx, job.ID, owner, claimedAt); err != nil {
			t.Fatal(err)
		}
		run, err := store.StartRun(ctx, job.ID, TriggerManual, claimedAt)
		if err != nil {
			t.Fatal(err)
		}
		return job, run
	}

	activeJob, activeRun := newRunningJob("active", "active-owner", now)
	staleJob, staleRun := newRunningJob("stale", "dead-owner", now.Add(-2*time.Hour))
	completedJob, completedRun := newRunningJob("completed", "completed-owner", now.Add(-3*time.Hour))
	completedRun.Status = RunSucceeded
	completedRun.FinishedAt = now.Add(-2*time.Hour - time.Minute)
	if err := store.FinishRun(ctx, &completedRun, "completed-owner"); err != nil {
		t.Fatal(err)
	}

	count, err := store.ReconcileStaleRuns(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reconciled %d runs, want only the stale run", count)
	}

	assertRun := func(jobID, runID string, wantStatus RunStatus) Run {
		t.Helper()
		runs, err := store.ListRuns(ctx, jobID, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if run.ID == runID {
				if run.Status != wantStatus {
					t.Fatalf("run %s status = %s, want %s", runID, run.Status, wantStatus)
				}
				return run
			}
		}
		t.Fatalf("run %s was not found", runID)
		return Run{}
	}

	active := assertRun(activeJob.ID, activeRun.ID, RunRunning)
	if !active.FinishedAt.IsZero() || active.Error != "" {
		t.Fatalf("active run was modified: %#v", active)
	}
	if _, err := store.ClaimJob(ctx, activeJob.ID, "contender", now); !errors.Is(err, ErrJobBusy) {
		t.Fatalf("active job lease changed during recovery: %v", err)
	}
	stale := assertRun(staleJob.ID, staleRun.ID, RunFailed)
	if !stale.FinishedAt.Equal(now) || !strings.Contains(stale.Error, "Run the backup again") || !strings.Contains(stale.Error, "agent log") {
		t.Fatalf("stale run recovery is not actionable: %#v", stale)
	}
	completed := assertRun(completedJob.ID, completedRun.ID, RunSucceeded)
	if !completed.FinishedAt.Equal(completedRun.FinishedAt) || completed.Error != "" {
		t.Fatalf("completed run was modified: %#v", completed)
	}
	if _, err := store.ClaimJob(ctx, staleJob.ID, "replacement-owner", now); err != nil {
		t.Fatalf("claim recovered job: %v", err)
	}
}
