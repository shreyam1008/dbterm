package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCopyJobNowPersistsIndependentSuccessfulRun(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	source := t.TempDir()
	destination := t.TempDir()
	writeCopyRunnerFixture(t, source, "copy.sqlite", "artifact_copy", "producer_one", "job_one", time.Now().Add(-time.Minute), false)
	job := localCopyRunnerJob(t, source, destination)
	job.Enabled = true
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}

	run, err := RunCopyJobNow(context.Background(), store, job.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunSucceeded || run.Discovered != 1 || run.AlreadyPresent != 0 || len(run.Artifacts) != 1 {
		t.Fatalf("unexpected first copy run: %+v", run)
	}
	stored, err := store.GetCopyRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != RunSucceeded || len(stored.Artifacts) != 1 || stored.Artifacts[0].PublicationState != ArtifactPublicationComplete {
		t.Fatalf("stored copy run lost verification evidence: %+v", stored)
	}

	second, err := RunCopyJobNow(context.Background(), store, job.Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != RunSucceeded || second.Discovered != 1 || second.AlreadyPresent != 1 || second.BytesCopied != 0 || len(second.Artifacts) != 0 {
		t.Fatalf("verified existing copy should be a successful no-op: %+v", second)
	}
}

func TestManualRealCopyUnlocksTimedAutomationEndToEnd(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := t.TempDir()
	destination := t.TempDir()
	writeCopyRunnerFixture(t, source, "proof.sqlite", "artifact_proof_e2e", "producer", "source_job", time.Now().Add(-time.Minute), false)
	job := localCopyRunnerJob(t, source, destination)
	job.Enabled = false
	job.Trigger = CopyTriggerTimed
	job.Schedule = Schedule{Kind: ScheduleInterval, EveryMinutes: 30}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCopyJobEnabled(context.Background(), job.ID, true); !errors.Is(err, ErrCopyJobTransferProofRequired) {
		t.Fatalf("unproved timed copy enable error = %v", err)
	}
	run, err := RunCopyJobNow(context.Background(), store, job.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunSucceeded || run.BytesCopied <= 0 || len(run.Artifacts) != 1 {
		t.Fatalf("manual proof run = %+v", run)
	}
	proved, err := store.GetCopyJob(context.Background(), job.ID)
	if err != nil || !proved.HasCurrentTransferProof() || !proved.TransferProofAt.Equal(run.FinishedAt) {
		t.Fatalf("stored end-to-end proof = %+v, %v", proved, err)
	}
	if err := store.SetCopyJobEnabled(context.Background(), job.ID, true); err != nil {
		t.Fatalf("real manual transfer did not unlock timed automation: %v", err)
	}
	enabled, err := store.GetCopyJob(context.Background(), job.ID)
	if err != nil || !enabled.Enabled || enabled.NextRunAt.IsZero() {
		t.Fatalf("enabled proved copy = %+v, %v", enabled, err)
	}
	dueAt := time.Now().UTC()
	enabled.NextRunAt = dueAt.Add(-time.Second)
	if err := store.UpsertCopyJob(context.Background(), &enabled); err != nil {
		t.Fatal(err)
	}
	if err := RunDueCopies(context.Background(), store, "proved-agent", dueAt, nil); err != nil {
		t.Fatalf("proved timed execution: %v", err)
	}
	runs, err := store.ListCopyRuns(context.Background(), job.ID, 10)
	if err != nil || len(runs) != 2 || runs[0].Trigger != CopyTriggerTimed || runs[0].Status != RunSucceeded {
		t.Fatalf("automatic run after manual proof = %+v, %v", runs, err)
	}
}

func TestTimedCopyInjectedWithoutProofDisablesBeforeRunOrTransport(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := t.TempDir()
	destination := t.TempDir()
	writeCopyRunnerFixture(t, source, "blocked.sqlite", "artifact_blocked_timed", "producer", "job", time.Now(), false)
	job := localCopyRunnerJob(t, source, destination)
	job.Enabled = false
	job.Trigger = CopyTriggerTimed
	job.Schedule = Schedule{Kind: ScheduleInterval, EveryMinutes: 30, RunMissedOnWake: true}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	injectEnabledCopyJobForTest(t, store, job, now.Add(-time.Second))
	var emitted []string
	if err := RunDueCopies(context.Background(), store, "proof-guard-agent", now, func(message string) { emitted = append(emitted, message) }); err != nil {
		t.Fatal(err)
	}
	assertCopyProofBlockedWithoutTransport(t, store, job.ID, destination)
	if len(emitted) != 1 || !strings.Contains(emitted[0], "disabled") || !strings.Contains(emitted[0], "run it manually") {
		t.Fatalf("proof-blocked timed message = %#v", emitted)
	}
	if err := RunDueCopies(context.Background(), store, "proof-guard-agent", now.Add(time.Minute), func(message string) { emitted = append(emitted, message) }); err != nil {
		t.Fatal(err)
	}
	if len(emitted) != 1 {
		t.Fatalf("disabled proof-blocked copy was retried in a hot loop: %#v", emitted)
	}
}

func TestAfterSuccessCopyInjectedWithoutProofDisablesBeforeRunOrTransport(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	source := t.TempDir()
	destination := t.TempDir()
	backupJob := Job{
		Name: "after-success producer", ConnectionID: "conn", Destination: source,
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 2},
	}
	if err := store.UpsertJob(ctx, &backupJob); err != nil {
		t.Fatal(err)
	}
	writeCopyRunnerFixture(t, source, "blocked.sqlite", "artifact_blocked_after", "producer", backupJob.ID, time.Now(), false)
	job := localCopyRunnerJob(t, source, destination)
	job.Enabled = false
	job.Mode = CopyModePush
	job.Trigger = CopyTriggerAfterSuccess
	job.SourceBackupJobID = backupJob.ID
	if err := store.UpsertCopyJob(ctx, &job); err != nil {
		t.Fatal(err)
	}
	injectEnabledCopyJobForTest(t, store, job, time.Time{})
	err = RunAfterSuccessCopies(ctx, store, backupJob.ID, nil)
	if !errors.Is(err, ErrCopyJobTransferProofRequired) || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("injected after-success proof error = %v", err)
	}
	assertCopyProofBlockedWithoutTransport(t, store, job.ID, destination)
	if err := RunAfterSuccessCopies(ctx, store, backupJob.ID, nil); err != nil {
		t.Fatalf("disabled after-success copy ran again: %v", err)
	}
}

func TestChangedCopyConfigurationDisablesWithoutTimedHotLoop(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	source := t.TempDir()
	provedDestination := t.TempDir()
	changedDestination := t.TempDir()
	writeCopyRunnerFixture(t, source, "proof.sqlite", "artifact_changed_config", "producer", "job", time.Now(), false)
	job := localCopyRunnerJob(t, source, provedDestination)
	job.Enabled = false
	job.Trigger = CopyTriggerTimed
	job.Schedule = Schedule{Kind: ScheduleInterval, EveryMinutes: 30, RunMissedOnWake: true}
	if err := store.UpsertCopyJob(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := RunCopyJobNow(ctx, store, job.ID, nil); err != nil {
		t.Fatalf("manual proof run: %v", err)
	}
	proved, err := store.GetCopyJob(ctx, job.ID)
	if err != nil || !proved.HasCurrentTransferProof() {
		t.Fatalf("proved job = %+v, %v", proved, err)
	}
	proved.Destination.Location = changedDestination
	now := time.Now().UTC()
	injectEnabledCopyJobForTest(t, store, proved, now.Add(-time.Second))
	var emitted []string
	if err := RunDueCopies(ctx, store, "changed-config-agent", now, func(message string) { emitted = append(emitted, message) }); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetCopyJob(ctx, job.ID)
	if err != nil || stored.Enabled || !stored.NextRunAt.IsZero() || stored.HasCurrentTransferProof() {
		t.Fatalf("stale changed-config job was not durably blocked: %+v, %v", stored, err)
	}
	entries, err := os.ReadDir(changedDestination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("changed unproved destination received files: %#v, %v", entries, err)
	}
	runs, err := store.ListCopyRuns(ctx, job.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].Trigger != CopyTriggerManual {
		t.Fatalf("proof-blocked changed config created a pretend run: %+v, %v", runs, err)
	}
	if len(emitted) != 1 || !strings.Contains(emitted[0], "disabled") {
		t.Fatalf("changed-config proof message = %#v", emitted)
	}
	if err := RunDueCopies(ctx, store, "changed-config-agent", now.Add(time.Minute), func(message string) { emitted = append(emitted, message) }); err != nil {
		t.Fatal(err)
	}
	if len(emitted) != 1 {
		t.Fatalf("changed-config copy retried after fail-closed disable: %#v", emitted)
	}
}

func TestRunCopyJobNowRecordsFailureAndReleasesLease(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	source := t.TempDir()
	destination := t.TempDir()
	writeCopyRunnerFixture(t, source, "bad.sqlite", "artifact_bad", "producer_one", "job_one", time.Now(), true)
	job := localCopyRunnerJob(t, source, destination)
	job.Enabled = true
	smtpServer := startSMTPTestServer(t, "")
	job.Notification = EmailNotification{
		Policy: NotificationFailure, SMTPHost: smtpServer.host, SMTPPort: smtpServer.port, TLSMode: SMTPTLSNone,
		From: "dbterm@example.test", Recipients: []string{"operator@example.test"},
	}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}

	run, err := RunCopyJobNow(context.Background(), store, job.ID, nil)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("expected checksum failure, got run=%+v err=%v", run, err)
	}
	if run.Status != RunFailed || !strings.Contains(run.Error, "SHA-256") {
		t.Fatalf("failed copy run was not preserved independently: %+v", run)
	}
	stored, err := store.GetCopyRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != RunFailed || stored.Error == "" || !stored.NotificationAttempted || !stored.NotificationSent || stored.NotificationError != "" {
		t.Fatalf("stored failure = %+v", stored)
	}
	select {
	case message := <-smtpServer.message:
		if !strings.Contains(message, "Copy failed") || !strings.Contains(message, "SHA-256") {
			t.Fatalf("unexpected copy failure email:\n%s", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("copy failure notification was not delivered")
	}
	// The terminal write must release the durable lease so an operator can
	// correct the source and retry immediately.
	if _, err := store.ClaimCopyJob(context.Background(), job.ID, "retry-owner", time.Now()); err != nil {
		t.Fatalf("copy lease remained stuck after failure: %v", err)
	}
}

func TestCopyRetryClassificationAndBoundedJitter(t *testing.T) {
	if copyErrorRetryable(errors.New("SSH host key mismatch")) {
		t.Fatal("host-key mismatch must require human review, not automatic retry")
	}
	if copyErrorRetryable(errors.New("artifact SHA-256 mismatch")) {
		t.Fatal("integrity failure must not be retried as a network fault")
	}
	if !copyErrorRetryable(errors.New("connection reset by peer")) {
		t.Fatal("transient connection failure should be retried")
	}
	job := CopyJob{RetryInitialSeconds: 2, RetryMaxSeconds: 5}
	for attempt, bounds := range []struct{ minimum, maximum time.Duration }{
		{time.Second, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{2500 * time.Millisecond, 5 * time.Second},
	} {
		delay := copyRetryDelay(job, attempt+1)
		if delay < bounds.minimum || delay > bounds.maximum {
			t.Fatalf("attempt %d retry delay %s outside [%s,%s]", attempt+1, delay, bounds.minimum, bounds.maximum)
		}
	}
}

func TestMergeCopyOutcomesReplacesUncertainBoundaryAfterRecovery(t *testing.T) {
	first := CopyOutcome{Discovered: 1, Artifacts: []CopyArtifactResult{{
		ArtifactID: "artifact", Destination: "vault/artifact", PublicationState: ArtifactPublicationUncertain,
	}}}
	second := CopyOutcome{Discovered: 1, Artifacts: []CopyArtifactResult{{
		ArtifactID: "artifact", Destination: "vault/artifact", PublicationState: ArtifactPublicationComplete, Reconciled: true,
	}}}
	merged := mergeCopyOutcomes(first, second)
	if len(merged.Artifacts) != 1 || merged.Artifacts[0].PublicationState != ArtifactPublicationComplete || !merged.Artifacts[0].Reconciled {
		t.Fatalf("merged retry outcome = %+v", merged)
	}
}

func TestMergeCopyOutcomesPreservesTransferWhenRetrySeesSameArtifactPresent(t *testing.T) {
	transferred := CopyOutcome{Discovered: 2, BytesCopied: 8, Artifacts: []CopyArtifactResult{{
		ArtifactID: "artifact", Destination: "vault/artifact", PublicationState: ArtifactPublicationComplete,
	}}}
	retry := CopyOutcome{Discovered: 2, AlreadyPresent: 1, Artifacts: []CopyArtifactResult{{
		ArtifactID: "artifact", Destination: "vault/artifact", PublicationState: ArtifactPublicationComplete, AlreadyPresent: true,
	}}}
	merged := mergeCopyOutcomes(transferred, retry)
	if len(merged.Artifacts) != 1 || merged.Artifacts[0].AlreadyPresent || merged.BytesCopied != 8 || merged.AlreadyPresent != 1 {
		t.Fatalf("merged retry lost transfer ownership evidence: %+v", merged)
	}
}

func injectEnabledCopyJobForTest(t *testing.T, store *Store, job CopyJob, nextRunAt time.Time) {
	t.Helper()
	job.Enabled = true
	job.NextRunAt = nextRunAt.UTC()
	job.UpdatedAt = time.Now().UTC()
	payload, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `UPDATE copy_jobs
		SET enabled = 1, next_run_at = ?, job_json = ?, updated_at = ? WHERE id = ?`,
		formatNullableTime(job.NextRunAt), payload, formatTime(job.UpdatedAt), job.ID); err != nil {
		t.Fatal(err)
	}
}

func assertCopyProofBlockedWithoutTransport(t *testing.T, store *Store, jobID, destination string) {
	t.Helper()
	job, err := store.GetCopyJob(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Enabled || !job.NextRunAt.IsZero() || job.HasCurrentTransferProof() {
		t.Fatalf("proof-blocked copy state = %+v", job)
	}
	runs, err := store.ListCopyRuns(context.Background(), jobID, 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("proof-blocked copy created run history: %+v, %v", runs, err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("proof-blocked copy transferred files: %#v, %v", entries, err)
	}
}
