package backup

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// RunCopyJobNow claims and executes one copy job. Copy runs are durable and
// independent from backup-generation runs.
func RunCopyJobNow(ctx context.Context, store *Store, idOrName string, emit func(string)) (CopyRun, error) {
	return RunCopyJobNowWithProgress(ctx, store, idOrName, progressFromEmitter(emit))
}

func RunCopyJobNowWithProgress(ctx context.Context, store *Store, idOrName string, progress ProgressFunc) (CopyRun, error) {
	if store == nil {
		return CopyRun{}, fmt.Errorf("backup store is required")
	}
	if _, err := store.ReconcileStaleCopyRuns(ctx, time.Now().UTC()); err != nil {
		return CopyRun{}, fmt.Errorf("recover interrupted copy runs: %w", err)
	}
	owner, err := NewID("copy-manual")
	if err != nil {
		return CopyRun{}, err
	}
	job, err := store.ClaimCopyJob(ctx, idOrName, owner, time.Now().UTC())
	if err != nil {
		return CopyRun{}, err
	}
	return executeClaimedCopyJob(ctx, store, job, owner, CopyTriggerManual, progress)
}

// RunDueCopies drains timed copy jobs serially using the same agent owner.
func RunDueCopies(ctx context.Context, store *Store, owner string, now time.Time, emit func(string)) error {
	if store == nil {
		return fmt.Errorf("backup store is required")
	}
	if emit == nil {
		emit = func(string) {}
	}
	cycleNow := now.UTC()
	reconciled, err := store.ReconcileStaleCopyRuns(ctx, cycleNow)
	if err != nil {
		return fmt.Errorf("recover interrupted copy runs: %w", err)
	}
	if reconciled > 0 {
		emit(fmt.Sprintf("recovered %d interrupted copy run(s); review copy history and rerun them if needed", reconciled))
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		jobs, err := store.ClaimDueCopyJobs(ctx, cycleNow, owner, 1)
		if err != nil {
			return err
		}
		if len(jobs) == 0 {
			return nil
		}
		job := jobs[0]
		if _, err := executeClaimedCopyJob(ctx, store, job, owner, CopyTriggerTimed, progressFromEmitter(emit)); err != nil {
			emit(fmt.Sprintf("copy %q failed: %v", job.Name, err))
		}
	}
}

// RunAfterSuccessCopies runs producer-owned copies bound to one successful
// backup stream. Errors are returned to the caller but never rewrite the
// already-durable backup run as failed.
func RunAfterSuccessCopies(ctx context.Context, store *Store, backupJobID string, progress ProgressFunc) error {
	jobs, err := store.ListEnabledAfterSuccessCopyJobs(ctx, backupJobID)
	if err != nil {
		return err
	}
	return runAfterSuccessCopyJobs(ctx, store, jobs, progress)
}

// runAfterSuccessCopyJobs executes the exact candidate set captured while the
// producer backup job still owns its lease. Every candidate is reloaded again
// under its own copy-job lease before transport begins.
func runAfterSuccessCopyJobs(ctx context.Context, store *Store, jobs []CopyJob, progress ProgressFunc) error {
	var failures []error
	for _, candidate := range jobs {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		owner, idErr := NewID("copy-after")
		if idErr != nil {
			failures = append(failures, idErr)
			continue
		}
		job, claimErr := store.ClaimCopyJob(ctx, candidate.ID, owner, time.Now().UTC())
		if claimErr != nil {
			failures = append(failures, fmt.Errorf("claim copy %q: %w", candidate.Name, claimErr))
			continue
		}
		if _, runErr := executeClaimedCopyJob(ctx, store, job, owner, CopyTriggerAfterSuccess, progress); runErr != nil {
			failures = append(failures, fmt.Errorf("copy %q: %w", candidate.Name, runErr))
		}
	}
	return errors.Join(failures...)
}

func executeClaimedCopyJob(parent context.Context, store *Store, job CopyJob, owner string, trigger CopyTrigger, progress ProgressFunc) (CopyRun, error) {
	return executeClaimedCopyJobWithVolumeOperations(parent, store, job, owner, trigger, progress, defaultCopyVolumeOperations())
}

type copyRetentionApplier func(context.Context, *Store, CopyJob, time.Time) ([]string, error)

func executeClaimedCopyJobWithVolumeOperations(parent context.Context, store *Store, job CopyJob, owner string, trigger CopyTrigger, progress ProgressFunc, volumeOperations copyVolumeOperations) (CopyRun, error) {
	return executeClaimedCopyJobWithPostProcessing(parent, store, job, owner, trigger, progress, volumeOperations, ApplyCopyRetention, copyRetentionTimeout(job))
}

func executeClaimedCopyJobWithPostProcessing(parent context.Context, store *Store, job CopyJob, owner string, trigger CopyTrigger, progress ProgressFunc, volumeOperations copyVolumeOperations, applyRetention copyRetentionApplier, retentionTimeout time.Duration) (CopyRun, error) {
	if applyRetention == nil {
		applyRetention = ApplyCopyRetention
	}
	if retentionTimeout <= 0 {
		retentionTimeout = copyRetentionTimeout(job)
	}
	postProcessingLeaseDuration := retentionTimeout + 30*time.Minute
	preparedJob, err := store.prepareCopyJobForExecution(parent, job.ID, owner, trigger, time.Now().UTC())
	if err != nil {
		// A failed proof check atomically disables the job and clears this lease.
		// Other preparation failures still get a best-effort lease release.
		_ = store.ReleaseCopyJob(context.Background(), job.ID, owner)
		return CopyRun{}, err
	}
	job = preparedJob
	run, err := store.StartCopyRun(parent, job.ID, trigger, time.Now().UTC())
	if err != nil {
		_ = store.ReleaseCopyJob(context.Background(), job.ID, owner)
		return CopyRun{}, err
	}
	leaseHeld := true
	defer func() {
		if leaseHeld {
			_ = store.ReleaseCopyJob(context.Background(), job.ID, owner)
		}
	}()
	effectiveProgress := progress
	if trigger != CopyTriggerManual {
		activity := newCopyAgentActivityRecorder(store, job, run)
		defer activity.Clear()
		effectiveProgress = combineProgress(progress, activity.Report)
	}
	report := func(event ProgressEvent) {
		if event.Elapsed == 0 {
			event.Elapsed = time.Since(run.StartedAt)
		}
		if effectiveProgress != nil {
			effectiveProgress(event)
		}
	}
	report(ProgressEvent{Phase: "copy", Message: fmt.Sprintf("starting copy %q", job.Name)})

	ctx, cancel := context.WithTimeout(parent, time.Duration(job.TimeoutMinutes)*time.Minute)
	var volumeSession *copyVolumeSession
	if job.DestinationVolume != nil {
		report(ProgressEvent{Phase: "volume", Message: "claiming and verifying the configured destination volume"})
		volumeSession, err = prepareCopyDestinationVolume(ctx, store, job, run.ID, volumeOperations)
	}
	runner := CopyRunner{Store: store, Progress: report}
	if volumeSession != nil {
		runner.LocalPublicationGuard = volumeSession.guardLocalPublication
	}
	var outcome CopyOutcome
	runErr := err
	maxAttempts := job.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	for attempt := 1; runErr == nil && attempt <= maxAttempts; attempt++ {
		attemptOutcome, attemptErr := runner.Run(ctx, job)
		outcome = mergeCopyOutcomes(outcome, attemptOutcome)
		runErr = attemptErr
		if attemptErr == nil || attempt >= maxAttempts || !copyErrorRetryable(attemptErr) {
			break
		}
		delay := copyRetryDelay(job, attempt)
		report(ProgressEvent{Phase: "retry", Message: fmt.Sprintf("copy attempt %d failed; retrying in %s: %v", attempt, delay.Round(time.Millisecond), attemptErr)})
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			runErr = ctx.Err()
		case <-timer.C:
		}
		if ctx.Err() != nil {
			runErr = ctx.Err()
			break
		}
	}
	if runErr == nil && volumeSession != nil {
		report(ProgressEvent{Phase: "volume", Message: "rechecking destination volume identity after transfer"})
		runErr = volumeSession.verifyAfterTransfer(ctx)
	}
	cancel()
	run.Discovered = outcome.Discovered
	run.AlreadyPresent = outcome.AlreadyPresent
	run.BytesCopied = outcome.BytesCopied
	run.NewestSourceAt = outcome.NewestSourceAt
	run.Artifacts = outcome.Artifacts
	run.Warnings = append(run.Warnings, outcome.Warnings...)
	run.FinishedAt = time.Now().UTC()
	if job.ExpectedFreshnessMinutes > 0 {
		threshold := time.Duration(job.ExpectedFreshnessMinutes) * time.Minute
		switch {
		case outcome.NewestSourceAt.IsZero():
			run.Warnings = append(run.Warnings, fmt.Sprintf("no producer artifact was found; expected freshness is %s", threshold))
		case run.FinishedAt.After(outcome.NewestSourceAt) && run.FinishedAt.Sub(outcome.NewestSourceAt) > threshold:
			age := run.FinishedAt.Sub(outcome.NewestSourceAt).Round(time.Minute)
			run.Warnings = append(run.Warnings, fmt.Sprintf("newest producer artifact is %s old; expected freshness is %s", age, threshold))
		}
	}
	if runErr != nil {
		run.Error = runErr.Error()
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) || errors.Is(parent.Err(), context.Canceled) {
			run.Status = RunCanceled
		} else {
			run.Status = RunFailed
		}
	} else {
		run.Status = RunSucceeded
	}
	if finishErr := store.recordCopyRunTerminal(context.Background(), &run, owner); finishErr != nil {
		if volumeSession != nil {
			releaseContext, releaseCancel := context.WithTimeout(context.Background(), copyVolumeReleaseTimeout)
			warnings := volumeSession.release(releaseContext, true)
			releaseCancel()
			for _, warning := range warnings {
				report(ProgressEvent{Phase: "volume", Message: warning})
			}
		}
		if runErr != nil {
			return run, fmt.Errorf("%v; recording copy run failed: %w", runErr, finishErr)
		}
		return run, finishErr
	}
	jobLeaseHealthy := true
	var postProcessingErr error
	leaseRenewedAt := time.Now().UTC()
	renewContext, renewCancel := context.WithTimeout(context.Background(), 5*time.Second)
	renewErr := store.RenewCopyJobLease(renewContext, job.ID, owner, leaseRenewedAt, leaseRenewedAt.Add(postProcessingLeaseDuration))
	renewCancel()
	if renewErr != nil {
		jobLeaseHealthy = false
		postProcessingErr = fmt.Errorf("renew copy-job lease for post-processing: %w", renewErr)
		warning := "copy-job lease was lost after transfer validity was recorded; destructive retention was skipped: " + renewErr.Error()
		run.Warnings = append(run.Warnings, warning)
		report(ProgressEvent{Phase: "retention", Message: warning})
		warningContext, warningCancel := context.WithTimeout(context.Background(), 5*time.Second)
		warningErr := store.recordCopyRunWarnings(warningContext, run.ID, []string{warning})
		warningCancel()
		if warningErr != nil {
			report(ProgressEvent{Phase: "retention", Message: "copy-job lease warning could not be added to copy history: " + warningErr.Error()})
		}
	}
	if runErr == nil {
		transferred := 0
		adopted := 0
		for _, artifact := range run.Artifacts {
			if artifact.PublicationState != ArtifactPublicationComplete {
				continue
			}
			if artifact.AlreadyPresent {
				adopted++
			} else {
				transferred++
			}
		}
		message := fmt.Sprintf("copy complete: %d transferred, %d already present, %d bytes", transferred, run.AlreadyPresent, run.BytesCopied)
		if adopted > 0 {
			message += fmt.Sprintf("; %d verified existing artifact(s) added to this catalog", adopted)
		}
		if len(run.Warnings) > 0 {
			message += "; warning: " + strings.Join(run.Warnings, "; ")
		}
		report(ProgressEvent{Phase: "copy", Message: message, CurrentBytes: run.BytesCopied, TotalBytes: run.BytesCopied})
	}
	volumeLeaseHealthy := true
	if volumeSession != nil && runErr == nil {
		renewContext, renewCancel := context.WithTimeout(context.Background(), 5*time.Second)
		renewErr := volumeSession.renewForPostProcessing(renewContext, postProcessingLeaseDuration)
		renewCancel()
		if renewErr != nil {
			volumeLeaseHealthy = false
			warning := "destination volume lease was lost after the copy; retention and power management were skipped: " + renewErr.Error()
			run.Warnings = append(run.Warnings, warning)
			report(ProgressEvent{Phase: "volume", Message: warning})
			warningContext, warningCancel := context.WithTimeout(context.Background(), 5*time.Second)
			warningErr := store.recordCopyRunWarnings(warningContext, run.ID, []string{warning})
			warningCancel()
			if warningErr != nil {
				report(ProgressEvent{Phase: "volume", Message: "destination volume lease warning could not be added to copy history: " + warningErr.Error()})
			}
		}
	}
	if runErr == nil && jobLeaseHealthy && volumeLeaseHealthy && (job.Destination.Kind == CopyEndpointLocal || job.Destination.Kind == CopyEndpointSSH || job.Destination.Kind == CopyEndpointSFTP) {
		report(ProgressEvent{Phase: "retention", Message: "applying copy retention policy"})
		// Retention receives a deadline strictly before both renewed leases. It
		// therefore cannot continue deleting after another owner could claim the
		// copy job or destination volume.
		retentionContext, cancelRetention := context.WithTimeout(context.Background(), retentionTimeout)
		removed, retentionErr := applyRetention(retentionContext, store, job, run.FinishedAt)
		cancelRetention()
		if retentionErr != nil {
			run.RetentionError = retentionErr.Error()
			report(ProgressEvent{Phase: "retention", Message: "copy succeeded, but retention cleanup failed: " + retentionErr.Error()})
		} else if len(removed) > 0 {
			report(ProgressEvent{Phase: "retention", Message: fmt.Sprintf("copy retention removed %d expired recovery point(s)", len(removed))})
		} else {
			report(ProgressEvent{Phase: "retention", Message: "copy retention complete; no recovery points removed"})
		}
		retentionCtx, cancelRetentionRecord := context.WithTimeout(context.Background(), 5*time.Second)
		recordErr := store.recordCopyRunRetentionOutcome(retentionCtx, run.ID, run.RetentionError)
		cancelRetentionRecord()
		if recordErr != nil {
			report(ProgressEvent{Phase: "retention", Message: "copy retention finished, but its outcome could not be added to history: " + recordErr.Error()})
		}
	}
	if volumeSession != nil && !volumeSession.leaseLost {
		report(ProgressEvent{Phase: "volume", Message: "releasing the configured destination volume"})
		releaseContext, releaseCancel := context.WithTimeout(context.Background(), copyVolumeReleaseTimeout)
		volumeWarnings := volumeSession.release(releaseContext, true)
		releaseCancel()
		if len(volumeWarnings) > 0 {
			run.Warnings = append(run.Warnings, volumeWarnings...)
			for _, warning := range volumeWarnings {
				report(ProgressEvent{Phase: "volume", Message: warning})
			}
			warningContext, warningCancel := context.WithTimeout(context.Background(), 5*time.Second)
			warningErr := store.recordCopyRunWarnings(warningContext, run.ID, volumeWarnings)
			warningCancel()
			if warningErr != nil {
				report(ProgressEvent{Phase: "volume", Message: "destination volume warning could not be added to copy history: " + warningErr.Error()})
			}
		} else {
			report(ProgressEvent{Phase: "volume", Message: "destination volume released safely"})
		}
	}
	// Copy notification is deliberately post-commit. SMTP failure cannot turn a
	// verified copy into a failed copy or a failed transfer into an unrecorded
	// event.
	if job.Notification.ShouldNotifyCopyRun(run) {
		report(ProgressEvent{Phase: "notification", Message: "sending copy email notification"})
		run.NotificationAttempted = true
		attemptCtx, cancelAttempt := context.WithTimeout(context.Background(), 5*time.Second)
		attemptErr := store.recordCopyRunNotification(attemptCtx, run.ID, true, false, "")
		cancelAttempt()
		if attemptErr != nil {
			report(ProgressEvent{Phase: "notification", Message: "copy email is continuing, but the attempt marker could not be added to history: " + attemptErr.Error()})
		}
		notificationCtx, cancelNotification := context.WithTimeout(context.Background(), smtpOperationTimeout)
		notificationErr := SendCopyRunNotification(notificationCtx, job, run)
		cancelNotification()
		if notificationErr != nil {
			safeErr := redactSMTPError(notificationErr, job.Notification)
			run.NotificationError = safeErr.Error()
			report(ProgressEvent{Phase: "notification", Message: "copy run was recorded, but email notification failed: " + safeErr.Error()})
		} else {
			run.NotificationSent = true
			report(ProgressEvent{Phase: "notification", Message: "copy email notification sent"})
		}
		outcomeCtx, cancelOutcome := context.WithTimeout(context.Background(), 5*time.Second)
		outcomeErr := store.recordCopyRunNotification(outcomeCtx, run.ID, run.NotificationAttempted, run.NotificationSent, run.NotificationError)
		cancelOutcome()
		if outcomeErr != nil {
			report(ProgressEvent{Phase: "notification", Message: "copy email was handled, but its outcome could not be added to history: " + outcomeErr.Error()})
		}
	}
	releaseContext, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	releaseErr := store.ReleaseCopyJob(releaseContext, job.ID, owner)
	releaseCancel()
	if releaseErr == nil {
		leaseHeld = false
	}
	if runErr != nil {
		if postProcessingErr != nil || releaseErr != nil {
			return run, errors.Join(runErr, postProcessingErr, releaseErr)
		}
		return run, runErr
	}
	if postProcessingErr != nil {
		if releaseErr != nil {
			return run, errors.Join(fmt.Errorf("copy was durably recorded, but post-processing ownership was lost: %w", postProcessingErr), releaseErr)
		}
		return run, fmt.Errorf("copy was durably recorded, but post-processing ownership was lost: %w", postProcessingErr)
	}
	if releaseErr != nil {
		return run, fmt.Errorf("copy completed, but release copy-job lease after post-processing: %w", releaseErr)
	}
	return run, nil
}

func copyRetentionTimeout(job CopyJob) time.Duration {
	minutes := job.TimeoutMinutes
	if minutes < 1 {
		minutes = DefaultTimeoutMinutes
	}
	return time.Duration(minutes) * time.Minute
}

func mergeCopyOutcomes(accumulated, next CopyOutcome) CopyOutcome {
	if next.Discovered > accumulated.Discovered {
		accumulated.Discovered = next.Discovered
	}
	if next.AlreadyPresent > accumulated.AlreadyPresent {
		accumulated.AlreadyPresent = next.AlreadyPresent
	}
	accumulated.BytesCopied += next.BytesCopied
	if next.NewestSourceAt.After(accumulated.NewestSourceAt) {
		accumulated.NewestSourceAt = next.NewestSourceAt
	}
	for _, warning := range next.Warnings {
		found := false
		for _, existing := range accumulated.Warnings {
			if existing == warning {
				found = true
				break
			}
		}
		if !found {
			accumulated.Warnings = append(accumulated.Warnings, warning)
		}
	}
	for _, artifact := range next.Artifacts {
		replaced := false
		for index := range accumulated.Artifacts {
			if accumulated.Artifacts[index].ArtifactID == artifact.ArtifactID && accumulated.Artifacts[index].Destination == artifact.Destination {
				// A later retry will observe bytes published by an earlier attempt
				// as already present. Preserve the stronger fact that this run
				// actually transferred them once both records are complete.
				existing := accumulated.Artifacts[index]
				if existing.PublicationState != ArtifactPublicationComplete ||
					(artifact.PublicationState == ArtifactPublicationComplete && (existing.AlreadyPresent || !artifact.AlreadyPresent)) {
					accumulated.Artifacts[index] = artifact
				}
				replaced = true
				break
			}
		}
		if !replaced {
			accumulated.Artifacts = append(accumulated.Artifacts, artifact)
		}
	}
	return accumulated
}

func copyErrorRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, permanent := range []string{
		"host key mismatch", "authentication", "private identity", "sha-256", "checksum", "format",
		"collision", "already exists", "outside", "symlink", "symbolic", "unsupported", "invalid", "requires",
	} {
		if strings.Contains(message, permanent) {
			return false
		}
	}
	for _, transient := range []string{
		"connection", "connect", "timeout", "timed out", "broken pipe", "connection reset", "unexpected eof",
		"temporarily unavailable", "network", "i/o", "exit status",
	} {
		if strings.Contains(message, transient) {
			return true
		}
	}
	return false
}

func copyRetryDelay(job CopyJob, failedAttempt int) time.Duration {
	initialSeconds := job.RetryInitialSeconds
	if initialSeconds < 1 {
		initialSeconds = 2
	}
	maximumSeconds := job.RetryMaxSeconds
	if maximumSeconds < initialSeconds {
		maximumSeconds = 60
	}
	initial := time.Duration(initialSeconds) * time.Second
	maximum := time.Duration(maximumSeconds) * time.Second
	delay := initial
	for step := 1; step < failedAttempt && delay < maximum; step++ {
		if delay > maximum/2 {
			delay = maximum
			break
		}
		delay *= 2
	}
	if delay > maximum {
		delay = maximum
	}
	// Full jitter in the upper half prevents synchronized fleets while avoiding
	// near-zero retry storms. A failed entropy read safely falls back to base.
	half := delay / 2
	if half <= 0 {
		return delay
	}
	var random [8]byte
	if _, err := cryptorand.Read(random[:]); err != nil {
		return delay
	}
	return half + time.Duration(binary.LittleEndian.Uint64(random[:])%uint64(half+1))
}
