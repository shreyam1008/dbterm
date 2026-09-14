package backup

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestBackupRetriesPreserveSafetyBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, phase, message string
		artifact             Artifact
		attempts, want       int
	}{
		{name: "transient dump", phase: "dump", message: "connection reset by peer", attempts: 3, want: 3},
		{name: "locked SQLite", phase: "dump", message: "database is locked", attempts: 3, want: 3},
		{name: "legacy plan", phase: "dump", message: "connection refused", want: 1},
		{name: "bad password", phase: "dump", message: "connection refused: password authentication failed", attempts: 3, want: 1},
		{name: "arbitrary native error", phase: "dump", message: "exit status 1", attempts: 3, want: 1},
		{name: "full destination", phase: "wrap", message: "no space left on device", attempts: 3, want: 1},
		{name: "publish begun", phase: "publish", message: "connection reset", attempts: 3, want: 1},
		{name: "artifact returned", phase: "dump", message: "connection reset", artifact: Artifact{Path: "orphan"}, attempts: 3, want: 1},
		{name: "uncertain publication", phase: "wrap", message: "connection reset", artifact: Artifact{PublicationState: ArtifactPublicationUncertain}, attempts: 3, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls, waits := 0, 0
			_, history, err := runBackupAttempts(context.Background(), Job{MaxAttempts: test.attempts}, nil, func(ctx context.Context, progress ProgressFunc) (Artifact, error) {
				calls++
				progress(ProgressEvent{Phase: test.phase})
				return test.artifact, errors.New(test.message)
			}, func(ctx context.Context, delay time.Duration) error {
				waits++
				if delay <= 0 || delay > time.Minute {
					t.Fatalf("invalid backoff %s", delay)
				}
				return nil
			})
			if err == nil || calls != test.want || len(history) != test.want || waits != test.want-1 {
				t.Fatalf("calls=%d waits=%d history=%d err=%v", calls, waits, len(history), err)
			}
			for i, entry := range history {
				if entry.Number != i+1 || entry.Error != test.message || entry.FinishedAt.Before(entry.StartedAt) {
					t.Fatalf("bad attempt %#v", entry)
				}
			}
		})
	}
}

func TestBackupRetrySucceedsAndSharesDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	deadline, _ := ctx.Deadline()
	calls := 0
	artifact, history, err := runBackupAttempts(ctx, Job{MaxAttempts: 3}, nil, func(attemptCtx context.Context, progress ProgressFunc) (Artifact, error) {
		if got, _ := attemptCtx.Deadline(); got != deadline {
			t.Fatal("retry reset the run deadline")
		}
		calls++
		progress(ProgressEvent{Phase: "dump"})
		if calls == 1 {
			return Artifact{}, errors.New("connection reset")
		}
		return Artifact{Path: "complete", PublicationState: ArtifactPublicationComplete}, nil
	}, func(waitCtx context.Context, _ time.Duration) error {
		if got, _ := waitCtx.Deadline(); got != deadline {
			t.Fatal("wait lost run deadline")
		}
		return nil
	})
	if err != nil || artifact.Path != "complete" || len(history) != 2 || history[1].Error != "" {
		t.Fatalf("%#v %#v %v", artifact, history, err)
	}
}

func TestBackupRetryCancellationStopsBeforeNextAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	_, history, err := runBackupAttempts(ctx, Job{MaxAttempts: 3}, nil, func(context.Context, ProgressFunc) (Artifact, error) {
		calls++
		return Artifact{}, errors.New("connection refused")
	}, func(waitCtx context.Context, delay time.Duration) error {
		cancel()
		return waitBackupRetry(waitCtx, delay)
	})
	if !errors.Is(err, context.Canceled) || calls != 1 || len(history) != 1 {
		t.Fatalf("calls %d, history %#v, err %v", calls, history, err)
	}
}

func TestBackupRetryPolicyAndAttemptsPersistThroughRealGeneration(t *testing.T) {
	isolateBackupState(t)
	source := createRunnerSQLiteFixture(t, t.TempDir(), "retry-source.sqlite3")
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job := runnerSQLiteJob(t.TempDir(), "retry_{run}", "job_retry")
	job.MaxAttempts, job.RetryInitialSeconds, job.RetryMaxSeconds = 3, 4, 10
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimJob(context.Background(), job.ID, "retry-test", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if claimed.MaxAttempts != 3 || claimed.RetryInitialSeconds != 4 || claimed.RetryMaxSeconds != 10 {
		t.Fatalf("policy changed: %#v", claimed)
	}
	connections := &config.Store{Connections: []config.ConnectionConfig{{ID: job.ConnectionID, Name: "fixture", Type: config.SQLite, FilePath: source}}}
	run, err := executeClaimedJobWithProgressAndNotifier(context.Background(), store, connections, claimed, "retry-test", TriggerManual, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.ListRuns(context.Background(), job.ID, 1)
	if err != nil || len(saved) != 1 || len(saved[0].Attempts) != 1 || saved[0].Status != RunSucceeded || saved[0].Artifact.Path != run.Artifact.Path {
		t.Fatalf("saved=%#v err=%v", saved, err)
	}
}

func TestBackupRetryDefaultsDoNotEnableLegacyPlanRetries(t *testing.T) {
	job := runnerSQLiteJob(t.TempDir(), "legacy", "legacy")
	job.MaxAttempts, job.RetryInitialSeconds, job.RetryMaxSeconds = 0, 0, 0
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	if job.MaxAttempts != 1 {
		t.Fatalf("legacy attempts=%d", job.MaxAttempts)
	}
	job.RetryInitialSeconds, job.RetryMaxSeconds = 61, 60
	if err := job.Validate(); err == nil {
		t.Fatal("invalid backoff accepted")
	}
}
