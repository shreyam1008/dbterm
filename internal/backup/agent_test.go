package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

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
			activityVisible := false
			notifier := func(ctx context.Context, notifiedJob Job, notifiedRun Run) error {
				called = true
				runs, listErr := store.ListRuns(ctx, job.ID, 10)
				if listErr == nil && len(runs) == 1 && runs[0].Status == RunSucceeded && !runs[0].FinishedAt.IsZero() && runs[0].NotificationAttempted && !runs[0].NotificationSent {
					durableBeforeSMTP = true
				}
				status, healthErr := AgentHealth(ctx, store, time.Now())
				activityVisible = healthErr == nil && status.Activity != nil && status.Activity.RunID == notifiedRun.ID && status.Activity.Phase == "notification"
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
			if test.wantCalled && (!durableBeforeSMTP || !activityVisible) {
				t.Fatalf("before-SMTP state: durable=%t activity=%t", durableBeforeSMTP, activityVisible)
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
