package backup

import (
	"context"
	"fmt"
	"time"
)

// PreviewCopyRetentionWithVolume prepares and positively identifies a
// configured local destination volume while holding its durable lease. Release
// warnings are returned separately because a completed preview remains valid.
func PreviewCopyRetentionWithVolume(ctx context.Context, store *Store, job CopyJob, now time.Time) ([]CopyRetentionCandidate, []string, error) {
	return withPreparedCopyDestinationVolume(ctx, store, job, "retention-preview", defaultCopyVolumeOperations(), func(actionContext context.Context) ([]CopyRetentionCandidate, error) {
		return PreviewCopyRetention(actionContext, store, job, now)
	})
}

// ApplyCopyRetentionWithVolume prevents a configured removable destination
// from being pruned through its bare mount-point directory. The exact volume
// is prepared, leased, and rechecked around the existing contained retention
// implementation.
func ApplyCopyRetentionWithVolume(ctx context.Context, store *Store, job CopyJob, now time.Time) ([]string, []string, error) {
	return withPreparedCopyDestinationVolume(ctx, store, job, "retention", defaultCopyVolumeOperations(), func(actionContext context.Context) ([]string, error) {
		return ApplyCopyRetention(actionContext, store, job, now)
	})
}

// ApplyCopyRetentionPlanWithVolume holds the copy-job lease while it verifies
// that the plan still exactly matches the earlier preview and while it applies
// that plan. This prevents a concurrent manual or scheduled copy from changing
// the retention window between comparison and deletion.
func ApplyCopyRetentionPlanWithVolume(ctx context.Context, store *Store, job CopyJob, now time.Time, expected []CopyRetentionCandidate) (removed []string, warnings []string, returnErr error) {
	if store == nil {
		return nil, nil, fmt.Errorf("backup store is required")
	}
	owner, err := NewID("copy-retention")
	if err != nil {
		return nil, nil, fmt.Errorf("create copy-retention lease owner: %w", err)
	}
	claimed, err := store.ClaimCopyJob(ctx, job.ID, owner, time.Now().UTC())
	if err != nil {
		return nil, nil, fmt.Errorf("claim copy job for retention: %w", err)
	}
	defer func() {
		releaseContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		releaseErr := store.ReleaseCopyJob(releaseContext, claimed.ID, owner)
		cancel()
		if releaseErr != nil {
			if returnErr == nil {
				returnErr = fmt.Errorf("release copy job after retention: %w", releaseErr)
			} else {
				returnErr = fmt.Errorf("%w; copy-job lease release also failed: %v", returnErr, releaseErr)
			}
		}
	}()
	return withPreparedCopyDestinationVolume(ctx, store, claimed, "retention", defaultCopyVolumeOperations(), func(actionContext context.Context) ([]string, error) {
		return ApplyCopyRetentionPlan(actionContext, store, claimed, now, expected)
	})
}

// StageCopyArtifactForInspectionWithVolume safely stages a destination copy
// from a configured local volume. The volume may be released after staging
// because StagedCopyArtifact owns independently verified bytes in private
// local storage. Source inspection does not touch the destination volume.
func StageCopyArtifactForInspectionWithVolume(ctx context.Context, store *Store, job CopyJob, result CopyArtifactResult, options CopyInspectionStageOptions) (*StagedCopyArtifact, []string, error) {
	location := options.Location
	if location == "" {
		location = CopyInspectionDestination
	}
	action := func(actionContext context.Context) (*StagedCopyArtifact, error) {
		return StageCopyArtifactForInspection(actionContext, job, result, options)
	}
	if location != CopyInspectionDestination || job.DestinationVolume == nil {
		staged, err := action(ctx)
		return staged, nil, err
	}
	return withPreparedCopyDestinationVolume(ctx, store, job, "inspection", defaultCopyVolumeOperations(), action)
}

func withPreparedCopyDestinationVolume[T any](ctx context.Context, store *Store, job CopyJob, operation string, operations copyVolumeOperations, action func(context.Context) (T, error)) (result T, warnings []string, returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if job.DestinationVolume == nil {
		result, returnErr = action(ctx)
		return result, nil, returnErr
	}
	owner, err := NewID("copy-volume-" + operation)
	if err != nil {
		return result, nil, fmt.Errorf("create %s destination-volume owner: %w", operation, err)
	}
	session, err := prepareCopyDestinationVolume(ctx, store, job, owner, operations)
	if err != nil {
		return result, nil, fmt.Errorf("prepare destination volume for %s: %w", operation, err)
	}
	// Keep every standalone inspection/retention operation within the volume
	// lease acquired by prepareCopyDestinationVolume. Without this bound, a
	// caller using context.Background could outlive the durable lease and race a
	// second job on the same removable disk.
	timeoutMinutes := job.TimeoutMinutes
	if timeoutMinutes <= 0 {
		timeoutMinutes = DefaultTimeoutMinutes
	}
	actionContext, cancelAction := context.WithTimeout(ctx, time.Duration(timeoutMinutes)*time.Minute)
	result, returnErr = action(actionContext)
	if returnErr == nil {
		if err := session.verifyAfterTransfer(actionContext); err != nil {
			returnErr = fmt.Errorf("recheck destination volume after %s: %w", operation, err)
		}
	}
	cancelAction()
	releaseContext, cancel := context.WithTimeout(context.Background(), copyVolumeReleaseTimeout)
	warnings = session.release(releaseContext, returnErr == nil)
	cancel()
	return result, warnings, returnErr
}
