package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestExecuteClaimedJobKeepsLeaseWhileTerminalPostProcessingRuns(t *testing.T) {
	for _, stage := range []string{"after-success copies", "retention"} {
		t.Run(stage, func(t *testing.T) {
			isolateBackupState(t)
			source := createRunnerSQLiteFixture(t, t.TempDir(), "lease-source.sqlite3")
			store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			job := runnerSQLiteJob(t.TempDir(), "lease_{run}", "job_post_processing_"+strings.ReplaceAll(stage, " ", "_"))
			job.TimeoutMinutes = 5
			if err := store.UpsertJob(context.Background(), &job); err != nil {
				t.Fatal(err)
			}
			const owner = "post-processing-owner"
			if _, err := store.ClaimJob(context.Background(), job.ID, owner, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			connections := &config.Store{Connections: []config.ConnectionConfig{{
				ID: job.ConnectionID, Name: "lease source", Type: config.SQLite, FilePath: source,
			}}}
			entered := make(chan struct{})
			release := make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			block := func(ctx context.Context) error {
				close(entered)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			operations := backupPostProcessingOperations{
				listAfterSuccess: func(context.Context, *Store, string) ([]CopyJob, error) { return nil, nil },
				runAfterSuccess: func(ctx context.Context, _ *Store, _ []CopyJob, _ ProgressFunc) error {
					if stage == "after-success copies" {
						return block(ctx)
					}
					return nil
				},
				applyRetention: func(ctx context.Context, _ *Store, _ Job, _ time.Time) ([]string, error) {
					if stage == "retention" {
						return nil, block(ctx)
					}
					return nil, nil
				},
				timeout: func(Job, []CopyJob) time.Duration { return 5 * time.Minute },
			}
			type executionResult struct {
				run Run
				err error
			}
			result := make(chan executionResult, 1)
			go func() {
				run, runErr := executeClaimedJobWithPostProcessing(context.Background(), store, connections, job, owner, TriggerManual, nil, nil, operations)
				result <- executionResult{run: run, err: runErr}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("backup did not reach blocking post-processing stage")
			}

			history, err := store.ListRuns(context.Background(), job.ID, 1)
			if err != nil || len(history) != 1 || history[0].Status != RunSucceeded || history[0].FinishedAt.IsZero() {
				t.Fatalf("terminal backup was not visible during %s: %#v, %v", stage, history, err)
			}
			if _, err := store.ClaimJob(context.Background(), job.ID, "competitor", time.Now().UTC()); !errors.Is(err, ErrJobBusy) {
				t.Fatalf("competing claim during %s = %v, want ErrJobBusy", stage, err)
			}
			changed := job
			changed.Retention.KeepLast++
			if err := store.UpsertJob(context.Background(), &changed); !errors.Is(err, ErrJobBusy) {
				t.Fatalf("policy edit during %s = %v, want ErrJobBusy", stage, err)
			}
			close(release)
			released = true
			select {
			case completed := <-result:
				if completed.err != nil || completed.run.Status != RunSucceeded {
					t.Fatalf("backup completion after %s = %#v, %v", stage, completed.run, completed.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("backup did not leave post-processing")
			}
			if _, err := store.ClaimJob(context.Background(), job.ID, "competitor", time.Now().UTC()); err != nil {
				t.Fatalf("backup lease remained held after %s: %v", stage, err)
			}
		})
	}
}

func TestBackupPostProcessingTimeoutIncludesCopiesAndCaps(t *testing.T) {
	job := Job{TimeoutMinutes: 5}
	copies := []CopyJob{{TimeoutMinutes: 10}, {TimeoutMinutes: 20}}
	if got, want := backupPostProcessingTimeout(job, copies), 35*time.Minute; got != want {
		t.Fatalf("backupPostProcessingTimeout() = %s, want %s", got, want)
	}
	copies = []CopyJob{{TimeoutMinutes: 24 * 60}, {TimeoutMinutes: 24 * 60}}
	if got := backupPostProcessingTimeout(job, copies); got != maximumBackupPostProcessingTimeout {
		t.Fatalf("capped backupPostProcessingTimeout() = %s, want %s", got, maximumBackupPostProcessingTimeout)
	}
}

func TestExecuteClaimedJobDurablyRecordsArtifactWhenManifestPublicationFails(t *testing.T) {
	isolateBackupState(t)
	destination := t.TempDir()
	source := createRunnerSQLiteFixture(t, t.TempDir(), "orphan-source.sqlite3")
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job := runnerSQLiteJob(destination, "tracked_orphan", "job_tracked_orphan")
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	connections := &config.Store{Connections: []config.ConnectionConfig{{
		ID: job.ConnectionID, Name: "orphan source", Type: config.SQLite, FilePath: source,
	}}}
	const owner = "orphan-test-owner"
	claimed, err := store.ClaimJob(context.Background(), job.ID, owner, time.Now().UTC())
	if err != nil || claimed.ID != job.ID {
		t.Fatalf("claim job = %#v, %v", claimed, err)
	}
	manifestPath := filepath.Join(destination, "tracked_orphan.sqlite3"+ArtifactManifestSuffix)
	run, err := executeClaimedJobWithProgressAndNotifier(
		context.Background(), store, connections, job, owner, TriggerManual,
		func(event ProgressEvent) {
			if event.Phase == "publish" && event.Message == "artifact durable; publishing completion manifest last" {
				if writeErr := os.WriteFile(manifestPath, []byte("competitor"), 0o600); writeErr != nil {
					t.Errorf("create competing manifest: %v", writeErr)
				}
			}
		}, nil,
	)
	if err == nil || run.Status != RunFailed {
		t.Fatalf("executeClaimedJob() = %#v, %v; want failed run", run, err)
	}
	if !run.Artifact.Verified || strings.TrimSpace(run.Artifact.Path) == "" {
		t.Fatalf("failed run lost published artifact metadata: %#v", run)
	}
	stored, listErr := store.ListRuns(context.Background(), job.ID, 10)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(stored) != 1 || stored[0].Artifact.Path != run.Artifact.Path || !stored[0].Artifact.Verified {
		t.Fatalf("durable failed run lost orphan artifact: %#v", stored)
	}
}

func TestExecuteClaimedJobPersistsAndNotifiesRetentionFailureSeparately(t *testing.T) {
	isolateBackupState(t)
	destination := t.TempDir()
	source := createRunnerSQLiteFixture(t, t.TempDir(), "retention-source.sqlite3")
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	job := runnerSQLiteJob(destination, "retention_{run}", "job_retention_warning")
	job.Retention.KeepLast = 1
	job.Notification = EmailNotification{
		Policy: NotificationFailure, SMTPHost: "localhost", SMTPPort: 25, TLSMode: SMTPTLSNone,
		From: "dbterm@example.com", Recipients: []string{"alerts@example.com"},
	}
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}

	const oldOwner = "retention-old-owner"
	if _, err := store.ClaimJob(context.Background(), job.ID, oldOwner, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	oldRun, err := store.StartRun(context.Background(), job.ID, TriggerManual, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(destination, "old.sqlite3")
	oldPayload := []byte("old-but-changed")
	if err := os.WriteFile(oldPath, oldPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	oldRun.Status = RunSucceeded
	oldRun.FinishedAt = now.Add(-2*time.Hour + time.Minute)
	oldRun.Artifact = Artifact{
		Path: oldPath, Size: int64(len(oldPayload)), SHA256: strings.Repeat("0", 64),
		Verified: true, PublicationState: ArtifactPublicationComplete, CreatedAt: oldRun.StartedAt,
	}
	if err := store.FinishRun(context.Background(), &oldRun, oldOwner); err != nil {
		t.Fatal(err)
	}

	const owner = "retention-current-owner"
	if _, err := store.ClaimJob(context.Background(), job.ID, owner, now); err != nil {
		t.Fatal(err)
	}
	connections := &config.Store{Connections: []config.ConnectionConfig{{
		ID: job.ConnectionID, Name: "retention source", Type: config.SQLite, FilePath: source,
	}}}
	notified := false
	run, runErr := executeClaimedJobWithNotifier(context.Background(), store, connections, job, owner, TriggerScheduled, nil,
		func(ctx context.Context, _ Job, notifiedRun Run) error {
			notified = strings.Contains(notifiedRun.RetentionError, "SHA-256")
			history, listErr := store.ListRuns(ctx, job.ID, 1)
			if listErr != nil || len(history) != 1 || history[0].RetentionError == "" || !history[0].NotificationAttempted {
				t.Errorf("retention warning was not durable before notification: %#v, %v", history, listErr)
			}
			return nil
		})
	if runErr != nil || run.Status != RunSucceeded {
		t.Fatalf("executeClaimedJobWithNotifier() = %#v, %v; backup itself should succeed", run, runErr)
	}
	if run.RetentionError == "" || !notified {
		t.Fatalf("retention warning/notifier = %q/%t", run.RetentionError, notified)
	}
	history, err := store.ListRuns(context.Background(), job.ID, 1)
	if err != nil || len(history) != 1 || history[0].RetentionError == "" || !history[0].NotificationSent {
		t.Fatalf("stored retention notification outcome = %#v, %v", history, err)
	}
}

func TestRunAgentReconcilesStaleRunsOnStartup(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now().UTC()
	job := Job{
		Name: "interrupted", ConnectionID: "conn", Enabled: true, Destination: t.TempDir(),
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual},
		Retention: Retention{KeepLast: 1}, TimeoutMinutes: 1,
	}
	if err := job.ApplyDefaults(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertJob(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimJob(ctx, job.ID, "dead-owner", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartRun(ctx, job.ID, TriggerManual, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	agentCtx, cancel := context.WithCancel(context.Background())
	var messages []string
	err = RunAgent(agentCtx, store, time.Hour, func(message string) {
		messages = append(messages, message)
		if strings.Contains(message, "recovered 1 interrupted") {
			cancel()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(messages, "\n"), "recovered 1 interrupted") {
		t.Fatalf("agent messages = %#v", messages)
	}
	runs, err := store.ListRuns(context.Background(), job.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != RunFailed || runs[0].FinishedAt.IsZero() {
		t.Fatalf("recovered runs = %#v", runs)
	}
}

func TestRunDueClaimsEachJobImmediatelyBeforeExecution(t *testing.T) {
	t.Setenv("DBTERM_CONFIG_DIR", t.TempDir())
	t.Setenv("DBTERM_STATE_DIR", t.TempDir())
	t.Setenv("DBTERM_STATE_DIR", t.TempDir())
	connectionStore, err := config.LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.sqlite3")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT); INSERT INTO items(name) VALUES ('one');`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	connection := config.ConnectionConfig{ID: "conn_agent_drain", Name: "agent drain", Type: config.SQLite, FilePath: source}
	if err := connectionStore.Add(connection); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	// Deliberately place this due cycle well behind the wall clock. The second
	// job must still be evaluated against the cycle start after the first job
	// completes; using time.Now between jobs would misclassify and skip it.
	now := time.Now().UTC().Add(-10 * time.Minute)
	newJob := func(name string, due time.Time) Job {
		t.Helper()
		job := Job{
			Name: name, ConnectionID: connection.ID, Enabled: true, Destination: t.TempDir(),
			Compression: CompressionNone,
			Schedule:    Schedule{Kind: ScheduleInterval, EveryMinutes: 5, RunMissedOnWake: false},
			Retention:   Retention{KeepLast: 1}, TimeoutMinutes: 5,
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
	newJob("first due", now.Add(-time.Minute))
	second := newJob("second due", now.Add(-30*time.Second))

	var observationErr error
	firstStarted := false
	secondStarted := false
	secondWasPreclaimed := false
	secondLeaseWasFresh := false
	err = RunDue(ctx, store, "drain-owner", now, func(message string) {
		if strings.Contains(message, `starting "first due"`) {
			firstStarted = true
			var leaseOwner sql.NullString
			if err := store.db.QueryRowContext(ctx, `SELECT lease_owner FROM backup_jobs WHERE id = ?`, second.ID).Scan(&leaseOwner); err != nil {
				observationErr = err
			} else {
				secondWasPreclaimed = leaseOwner.Valid && leaseOwner.String != ""
			}
		}
		if strings.Contains(message, `starting "second due"`) {
			secondStarted = true
			var leaseUntilRaw string
			if err := store.db.QueryRowContext(ctx, `SELECT lease_until FROM backup_jobs WHERE id = ?`, second.ID).Scan(&leaseUntilRaw); err != nil {
				observationErr = err
			} else if leaseUntil, parseErr := time.Parse(time.RFC3339Nano, leaseUntilRaw); parseErr != nil {
				observationErr = fmt.Errorf("parse second lease_until %q: %w", leaseUntilRaw, parseErr)
			} else {
				// Timeout is five minutes and the safety margin is ten. Even
				// though the due cycle is ten minutes old, the claimed job must
				// receive a fresh lease from its actual execution time.
				secondLeaseWasFresh = leaseUntil.After(time.Now().UTC().Add(10 * time.Minute))
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if observationErr != nil {
		t.Fatal(observationErr)
	}
	if !firstStarted || !secondStarted {
		t.Fatalf("start observations: first=%t second=%t", firstStarted, secondStarted)
	}
	if secondWasPreclaimed {
		t.Fatal("second due job was leased before the first job began execution")
	}
	if !secondLeaseWasFresh {
		t.Fatal("second due job did not receive a fresh full-duration lease")
	}
	runs, err := store.ListRuns(ctx, "", 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("ListRuns() = %#v, %v", runs, err)
	}
}

func TestAgentHealthRejectsImplausiblyFutureHeartbeat(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	if err := store.SetMeta(ctx, agentHeartbeatKey, now.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	status, err := AgentHealth(ctx, store, now)
	if err != nil {
		t.Fatal(err)
	}
	if status.Healthy {
		t.Fatalf("future heartbeat reported healthy: %#v", status)
	}
	if err := store.SetMeta(ctx, agentHeartbeatKey, now.Add(agentFutureClockTolerance/2).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	status, err = AgentHealth(ctx, store, now)
	if err != nil || !status.Healthy {
		t.Fatalf("small clock skew status = %#v, %v", status, err)
	}
}

func TestExecuteClaimedJobPersistsNotificationOutcomeAfterTerminalRun(t *testing.T) {
	tests := []struct {
		name             string
		policy           NotificationPolicy
		notificationErr  error
		wantCalled       bool
		wantSent         bool
		wantHistoryError bool
	}{
		{name: "sent", policy: NotificationSuccess, wantCalled: true, wantSent: true},
		{name: "delivery failed", policy: NotificationSuccess, notificationErr: fmt.Errorf("rejected private-user@example.com private-password"), wantCalled: true, wantHistoryError: true},
		{name: "policy disabled", policy: NotificationNever},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			isolateBackupState(t)
			source := filepath.Join(t.TempDir(), "source.sqlite3")
			database, err := sql.Open("sqlite", source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TABLE items(id INTEGER PRIMARY KEY, value TEXT); INSERT INTO items(value) VALUES ('safe');`); err != nil {
				_ = database.Close()
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}

			store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			now := time.Now().UTC()
			job := Job{
				Name: "notification history", ConnectionID: "conn_notification", Enabled: true, Destination: t.TempDir(),
				Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 2}, TimeoutMinutes: 5,
				Notification: EmailNotification{Policy: test.policy},
			}
			if test.policy != NotificationNever {
				job.Notification.SMTPHost = "localhost"
				job.Notification.SMTPPort = 25
				job.Notification.TLSMode = SMTPTLSNone
				job.Notification.Username = "private-user@example.com"
				job.Notification.Password = "private-password"
				job.Notification.From = "private-user@example.com"
				job.Notification.Recipients = []string{"alerts@example.com"}
			}
			if err := job.ApplyDefaults(now); err != nil {
				t.Fatal(err)
			}
			if err := store.UpsertJob(context.Background(), &job); err != nil {
				t.Fatal(err)
			}
			owner := "notification-test-owner"
			if _, err := store.ClaimJob(context.Background(), job.ID, owner, now); err != nil {
				t.Fatal(err)
			}
			if err := store.SetMeta(context.Background(), agentHeartbeatKey, now.Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			if err := store.SetMeta(context.Background(), agentPIDKey, "123"); err != nil {
				t.Fatal(err)
			}

			connections := &config.Store{Connections: []config.ConnectionConfig{{
				ID: "conn_notification", Name: "notification source", Type: config.SQLite, FilePath: source,
			}}}
			called := false
			durableBeforeSMTP := false
			leaseReleasedBeforeSMTP := false
			activityVisible := false
			notifier := func(ctx context.Context, notifiedJob Job, notifiedRun Run) error {
				called = true
				runs, listErr := store.ListRuns(ctx, job.ID, 10)
				if listErr == nil && len(runs) == 1 && runs[0].Status == RunSucceeded && !runs[0].FinishedAt.IsZero() && runs[0].NotificationAttempted && !runs[0].NotificationSent {
					durableBeforeSMTP = true
				}
				status, healthErr := AgentHealth(ctx, store, time.Now())
				activityVisible = healthErr == nil && status.Activity != nil && status.Activity.RunID == notifiedRun.ID && status.Activity.Phase == "notification"
				const smtpObserver = "smtp-observer"
				if _, claimErr := store.ClaimJob(ctx, job.ID, smtpObserver, time.Now().UTC()); claimErr == nil {
					leaseReleasedBeforeSMTP = true
					if releaseErr := store.ReleaseJob(ctx, job.ID, smtpObserver); releaseErr != nil {
						t.Errorf("release SMTP observer lease: %v", releaseErr)
					}
				}
				return test.notificationErr
			}
			var messages []string
			run, runErr := executeClaimedJobWithNotifier(context.Background(), store, connections, job, owner, TriggerScheduled, func(message string) {
				messages = append(messages, message)
			}, notifier)
			if runErr != nil {
				t.Fatalf("executeClaimedJobWithNotifier() = %v", runErr)
			}
			if run.Status != RunSucceeded {
				t.Fatalf("notification changed backup status: %#v", run)
			}
			if called != test.wantCalled {
				t.Fatalf("notifier called=%t, want %t", called, test.wantCalled)
			}
			if test.wantCalled && (!durableBeforeSMTP || !leaseReleasedBeforeSMTP || !activityVisible) {
				t.Fatalf("before-SMTP state: durable=%t lease-released=%t activity=%t", durableBeforeSMTP, leaseReleasedBeforeSMTP, activityVisible)
			}
			history, err := store.ListRuns(context.Background(), job.ID, 10)
			if err != nil || len(history) != 1 {
				t.Fatalf("ListRuns() = %#v, %v", history, err)
			}
			stored := history[0]
			if stored.NotificationAttempted != test.wantCalled || stored.NotificationSent != test.wantSent {
				t.Fatalf("notification history = %#v", stored)
			}
			if test.wantHistoryError {
				if stored.NotificationError == "" || strings.Contains(stored.NotificationError, "private-user@example.com") || strings.Contains(stored.NotificationError, "private-password") {
					t.Fatalf("unsafe notification history error %q", stored.NotificationError)
				}
			} else if stored.NotificationError != "" {
				t.Fatalf("unexpected notification history error %q", stored.NotificationError)
			}
			if joined := strings.Join(messages, "\n"); strings.Contains(joined, "private-user@example.com") || strings.Contains(joined, "private-password") {
				t.Fatalf("progress leaked SMTP credentials: %s", joined)
			}
			status, err := AgentHealth(context.Background(), store, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if status.Activity != nil {
				t.Fatalf("agent activity was not cleared after run: %#v", status.Activity)
			}
		})
	}
}

func TestAgentHealthIgnoresStaleActivity(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if err := store.SetMeta(context.Background(), agentHeartbeatKey, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	stale := AgentActivity{JobID: "job", RunID: "run", Phase: "dump", StartedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-agentHealthWindow - time.Second)}
	payload, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetMeta(context.Background(), agentActivityKey, string(payload)); err != nil {
		t.Fatal(err)
	}
	status, err := AgentHealth(context.Background(), store, now)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Healthy || status.Activity != nil {
		t.Fatalf("stale activity status = %#v", status)
	}
}

func TestFailedBackupKeepsOriginalErrorWhenFailureNotificationAlsoFails(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	job := Job{
		Name: "missing source", ConnectionID: "missing-connection", Enabled: true, Destination: t.TempDir(),
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 1}, TimeoutMinutes: 5,
		Notification: EmailNotification{
			Policy: NotificationFailure, SMTPHost: "localhost", SMTPPort: 25, TLSMode: SMTPTLSNone,
			Username: "failure-user@example.com", Password: "failure-password", From: "failure-user@example.com",
			Recipients: []string{"alerts@example.com"},
		},
	}
	if err := job.ApplyDefaults(now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	owner := "failed-notification-owner"
	if _, err := store.ClaimJob(context.Background(), job.ID, owner, now); err != nil {
		t.Fatal(err)
	}
	durableBeforeNotification := false
	run, runErr := executeClaimedJobWithNotifier(context.Background(), store, &config.Store{}, job, owner, TriggerScheduled, nil,
		func(ctx context.Context, _ Job, notifiedRun Run) error {
			runs, listErr := store.ListRuns(ctx, job.ID, 1)
			durableBeforeNotification = listErr == nil && len(runs) == 1 && runs[0].Status == RunFailed && runs[0].NotificationAttempted && runs[0].ID == notifiedRun.ID
			return fmt.Errorf("SMTP rejected failure-user@example.com failure-password")
		})
	if runErr == nil || !strings.Contains(runErr.Error(), "no longer exists") {
		t.Fatalf("run error = %v, want original missing-connection failure", runErr)
	}
	if run.Status != RunFailed || !durableBeforeNotification {
		t.Fatalf("failed run/ordering = %#v, durable=%t", run, durableBeforeNotification)
	}
	history, err := store.ListRuns(context.Background(), job.ID, 1)
	if err != nil || len(history) != 1 {
		t.Fatalf("ListRuns() = %#v, %v", history, err)
	}
	stored := history[0]
	if !stored.NotificationAttempted || stored.NotificationSent || stored.NotificationError == "" {
		t.Fatalf("failed notification outcome = %#v", stored)
	}
	if strings.Contains(stored.NotificationError, "failure-user@example.com") || strings.Contains(stored.NotificationError, "failure-password") {
		t.Fatalf("failed notification history leaked credentials: %q", stored.NotificationError)
	}
}
