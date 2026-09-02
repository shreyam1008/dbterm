package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrCopyVolumeBusy      = errors.New("copy destination volume is already in use")
	ErrCopyVolumeLeaseLost = errors.New("copy destination volume lease was lost")
)

type CopyVolumeLease struct {
	VolumeKey string
	Owner     string
	JobID     string
	RunID     string
	Until     time.Time
}

func (store *Store) ClaimCopyVolumeLease(ctx context.Context, volumeKey, owner, jobID, runID string, now, until time.Time) (CopyVolumeLease, error) {
	if store == nil || store.db == nil {
		return CopyVolumeLease{}, fmt.Errorf("backup store is not open")
	}
	volumeKey = strings.TrimSpace(volumeKey)
	owner = strings.TrimSpace(owner)
	jobID = strings.TrimSpace(jobID)
	runID = strings.TrimSpace(runID)
	if volumeKey == "" || owner == "" || jobID == "" || runID == "" {
		return CopyVolumeLease{}, fmt.Errorf("copy volume key, owner, job ID, and run ID are required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	until = until.UTC()
	if !until.After(now) {
		return CopyVolumeLease{}, fmt.Errorf("copy volume lease expiry must be after its acquisition time")
	}
	result, err := store.db.ExecContext(ctx, `INSERT INTO copy_volume_leases
		(volume_key, owner, job_id, run_id, lease_until, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(volume_key) DO UPDATE SET
			owner=excluded.owner, job_id=excluded.job_id, run_id=excluded.run_id,
			lease_until=excluded.lease_until, updated_at=excluded.updated_at
		WHERE copy_volume_leases.lease_until <= excluded.updated_at OR
			(copy_volume_leases.owner = excluded.owner AND copy_volume_leases.job_id = excluded.job_id AND copy_volume_leases.run_id = excluded.run_id)`,
		volumeKey, owner, jobID, runID, formatTime(until), formatTime(now))
	if err != nil {
		return CopyVolumeLease{}, fmt.Errorf("claim copy destination volume: %w", err)
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return CopyVolumeLease{}, fmt.Errorf("confirm copy destination volume claim: %w", err)
	}
	if claimed != 1 {
		return CopyVolumeLease{}, ErrCopyVolumeBusy
	}
	return CopyVolumeLease{VolumeKey: volumeKey, Owner: owner, JobID: jobID, RunID: runID, Until: until}, nil
}

func (store *Store) RenewCopyVolumeLease(ctx context.Context, lease *CopyVolumeLease, now, until time.Time) error {
	if store == nil || store.db == nil {
		return fmt.Errorf("backup store is not open")
	}
	if lease == nil || strings.TrimSpace(lease.VolumeKey) == "" || strings.TrimSpace(lease.Owner) == "" || strings.TrimSpace(lease.JobID) == "" || strings.TrimSpace(lease.RunID) == "" {
		return fmt.Errorf("copy destination volume lease is required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	until = until.UTC()
	if !until.After(now) {
		return fmt.Errorf("copy volume lease expiry must be after renewal time")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE copy_volume_leases SET lease_until = ?, updated_at = ?
		WHERE volume_key = ? AND owner = ? AND job_id = ? AND run_id = ? AND lease_until > ?`,
		formatTime(until), formatTime(now), lease.VolumeKey, lease.Owner, lease.JobID, lease.RunID, formatTime(now))
	if err != nil {
		return fmt.Errorf("renew copy destination volume: %w", err)
	}
	renewed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm copy destination volume renewal: %w", err)
	}
	if renewed != 1 {
		return ErrCopyVolumeLeaseLost
	}
	lease.Until = until
	return nil
}

func (store *Store) ReleaseCopyVolumeLease(ctx context.Context, lease CopyVolumeLease) error {
	if store == nil || store.db == nil {
		return fmt.Errorf("backup store is not open")
	}
	if strings.TrimSpace(lease.VolumeKey) == "" || strings.TrimSpace(lease.Owner) == "" || strings.TrimSpace(lease.JobID) == "" || strings.TrimSpace(lease.RunID) == "" {
		return fmt.Errorf("copy destination volume lease is required")
	}
	result, err := store.db.ExecContext(ctx, `DELETE FROM copy_volume_leases
		WHERE volume_key = ? AND owner = ? AND job_id = ? AND run_id = ?`,
		lease.VolumeKey, lease.Owner, lease.JobID, lease.RunID)
	if err != nil {
		return fmt.Errorf("release copy destination volume: %w", err)
	}
	released, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm copy destination volume release: %w", err)
	}
	if released != 1 {
		return ErrCopyVolumeLeaseLost
	}
	return nil
}

func (store *Store) recordCopyRunWarnings(ctx context.Context, runID string, warnings []string) error {
	if len(warnings) == 0 {
		return nil
	}
	return store.updateTerminalCopyRunJSON(ctx, runID, "copy destination volume warnings", func(run *CopyRun) error {
		for _, warning := range warnings {
			warning = strings.TrimSpace(warning)
			if warning == "" {
				continue
			}
			alreadyRecorded := false
			for _, existing := range run.Warnings {
				if existing == warning {
					alreadyRecorded = true
					break
				}
			}
			if !alreadyRecorded {
				run.Warnings = append(run.Warnings, warning)
			}
		}
		return nil
	})
}
