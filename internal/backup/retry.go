package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

func backupRetrySettings(job Job) (int, int) {
	initial, maximum := job.RetryInitialSeconds, job.RetryMaxSeconds
	if initial == 0 {
		initial = 2
	}
	if maximum == 0 {
		maximum = 60
	}
	return initial, maximum
}

// Native clients report many errors through stderr. Use a narrow allowlist,
// after rejecting permanent failures, rather than retrying every exit status.
func backupErrorRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, permanent := range []string{"authentication", "password", "permission", "access denied", "certificate", "no such", "does not exist", "no longer exists", "invalid", "unsupported", "requires", "checksum", "sha-256", "no space", "disk full", "read-only", "collision", "already exists", "symlink"} {
		if strings.Contains(message, permanent) {
			return false
		}
	}
	for _, transient := range []string{"connection refused", "connection reset", "connection timed out", "connection to server was lost", "server closed the connection", "lost connection", "broken pipe", "unexpected eof", "network is unreachable", "temporary failure in name resolution", "temporarily unavailable", "database is locked", "database is busy"} {
		if strings.Contains(message, transient) {
			return true
		}
	}
	return false
}

type backupGenerationAttempt func(context.Context, ProgressFunc) (Artifact, error)

// All attempts and waits share the original run deadline and job lease. Once
// publication starts, no automatic rerun is safe, even if no path was returned.
func runBackupAttempts(ctx context.Context, job Job, report ProgressFunc, attempt backupGenerationAttempt, wait func(context.Context, time.Duration) error) (Artifact, []BackupAttempt, error) {
	maximumAttempts := job.MaxAttempts
	if maximumAttempts < 1 {
		maximumAttempts = 1
	}
	initial, maximum := backupRetrySettings(job)
	var history []BackupAttempt
	var artifact Artifact
	var err error
	for number := 1; number <= maximumAttempts; number++ {
		if ctx.Err() != nil {
			return artifact, history, ctx.Err()
		}
		entry := BackupAttempt{Number: number, StartedAt: time.Now().UTC(), Phase: "preflight"}
		var mutex sync.Mutex
		publicationStarted := false
		artifact, err = attempt(ctx, func(event ProgressEvent) {
			mutex.Lock()
			if event.Phase != "" {
				entry.Phase = event.Phase
			}
			publicationStarted = publicationStarted || event.Phase == "publish"
			mutex.Unlock()
			if report != nil {
				report(event)
			}
		})
		mutex.Lock()
		entry.FinishedAt = time.Now().UTC()
		if err != nil {
			entry.Error = err.Error()
		}
		history = append(history, entry)
		published := publicationStarted
		mutex.Unlock()
		if err == nil || number == maximumAttempts || published || artifact.Path != "" || artifact.PublicationState != "" || !backupErrorRetryable(err) {
			return artifact, history, err
		}
		delay := copyRetryDelay(CopyJob{RetryInitialSeconds: initial, RetryMaxSeconds: maximum}, number)
		if report != nil {
			report(ProgressEvent{Phase: "retry", Message: fmt.Sprintf("backup attempt %d/%d failed; retrying in %s: %v", number, maximumAttempts, delay.Round(time.Millisecond), err)})
		}
		if waitErr := wait(ctx, delay); waitErr != nil {
			return artifact, history, waitErr
		}
	}
	return artifact, history, err
}

func waitBackupRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
