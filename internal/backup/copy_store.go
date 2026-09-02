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
	"time"
)

var (
	ErrCopyJobBusy                  = errors.New("copy job is already running")
	ErrCopyJobLeaseLost             = errors.New("copy job lease was lost")
	ErrCopyJobTransferProofRequired = errors.New("copy job requires a successful real transfer before automatic enablement")
)

const staleCopyRunRecoveryError = "The dbterm process stopped before this copy run recorded completion, and no active copy-job lease remains. Run the copy again; if it repeats, inspect the backup agent log."

// migrateCopyCatalog adds the copy subsystem without rewriting backup job or
// run rows. It is idempotent so every process can safely call OpenStore while
// rolling forward an existing catalog.
func (store *Store) migrateCopyCatalog(ctx context.Context) error {
	if store == nil || store.db == nil {
		return fmt.Errorf("backup store is not open")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin copy catalog migration: %w", err)
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS copy_jobs (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			mode TEXT NOT NULL CHECK(mode IN ('push', 'pull')),
			trigger_kind TEXT NOT NULL CHECK(trigger_kind IN ('manual', 'after_success', 'timed')),
			source_backup_job_id TEXT,
			next_run_at TEXT,
			lease_owner TEXT,
			lease_until TEXT,
			job_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS copy_jobs_name_nocase_idx ON copy_jobs(name COLLATE NOCASE)`,
		`CREATE INDEX IF NOT EXISTS copy_jobs_due_idx ON copy_jobs(enabled, trigger_kind, next_run_at, lease_until)`,
		`CREATE INDEX IF NOT EXISTS copy_jobs_after_success_idx ON copy_jobs(enabled, trigger_kind, source_backup_job_id)`,
		`CREATE TABLE IF NOT EXISTS copy_runs (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL,
			trigger_kind TEXT NOT NULL CHECK(trigger_kind IN ('manual', 'after_success', 'timed')),
			status TEXT NOT NULL CHECK(status IN ('running', 'succeeded', 'failed', 'canceled')),
			started_at TEXT NOT NULL,
			finished_at TEXT,
			run_json BLOB NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS copy_runs_job_idx ON copy_runs(job_id, started_at DESC)`,
		`CREATE TABLE IF NOT EXISTS copy_volume_leases (
			volume_key TEXT PRIMARY KEY,
			owner TEXT NOT NULL,
			job_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			lease_until TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS copy_volume_leases_expiry_idx ON copy_volume_leases(lease_until)`,
		`INSERT INTO backup_meta(key, value) VALUES ('schema_version', '3')
			ON CONFLICT(key) DO UPDATE SET value = CASE
				WHEN CAST(backup_meta.value AS INTEGER) < 3 THEN '3'
				ELSE backup_meta.value
			END`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate copy catalog: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit copy catalog migration: %w", err)
	}
	return nil
}

func (store *Store) UpsertCopyJob(ctx context.Context, job *CopyJob) error {
	if store == nil || store.db == nil {
		return fmt.Errorf("backup store is not open")
	}
	now := time.Now().UTC()
	if err := job.ApplyDefaults(now); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin copy job save: %w", err)
	}
	defer tx.Rollback()
	if err := materializeCopySourceBackupDestination(ctx, tx, job); err != nil {
		return err
	}
	if err := preserveStoredCopyTransferProof(ctx, tx, job); err != nil {
		return err
	}
	if err := requireCurrentCopyTransferProof(*job); err != nil {
		return err
	}
	if job.Enabled && job.Trigger == CopyTriggerTimed && job.NextRunAt.IsZero() {
		next, ok, err := job.Schedule.Next(now)
		if err != nil {
			return err
		}
		if ok {
			job.NextRunAt = next
		}
	}
	if !job.Enabled || job.Trigger != CopyTriggerTimed {
		job.NextRunAt = time.Time{}
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode copy job: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, name, enabled, job_json FROM copy_jobs WHERE id <> ? ORDER BY id`, job.ID)
	if err != nil {
		return fmt.Errorf("check copy job name: %w", err)
	}
	for rows.Next() {
		var existingID, existingName string
		var existingEnabled int
		var existingPayload []byte
		if err := rows.Scan(&existingID, &existingName, &existingEnabled, &existingPayload); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read copy job name: %w", err)
		}
		if strings.EqualFold(existingName, job.Name) {
			_ = rows.Close()
			return fmt.Errorf("copy job name %q is already used by job %s; choose a unique name", job.Name, existingID)
		}
		if job.Enabled && existingEnabled != 0 {
			var existing CopyJob
			if err := json.Unmarshal(existingPayload, &existing); err != nil {
				_ = rows.Close()
				return fmt.Errorf("decode existing copy topology %s: %w", existingID, err)
			}
			if copyJobsConflict(*job, existing) {
				_ = rows.Close()
				return fmt.Errorf("copy topology conflicts with enabled job %q (%s); one artifact stream must have exactly one transfer owner", existing.Name, existing.ID)
			}
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close copy job name check: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("scan copy job names: %w", err)
	}

	result, err := tx.ExecContext(ctx, `INSERT INTO copy_jobs
		(id, name, enabled, mode, trigger_kind, source_backup_job_id, next_run_at, lease_owner, lease_until, job_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, enabled=excluded.enabled, mode=excluded.mode,
			trigger_kind=excluded.trigger_kind, source_backup_job_id=excluded.source_backup_job_id,
			next_run_at=excluded.next_run_at,
			job_json=excluded.job_json, updated_at=excluded.updated_at
		WHERE copy_jobs.lease_until IS NULL OR copy_jobs.lease_until <= ?`,
		job.ID, job.Name, boolInt(job.Enabled), job.Mode, job.Trigger, nullableCopyText(job.SourceBackupJobID), formatNullableTime(job.NextRunAt), payload,
		formatTime(job.CreatedAt), formatTime(job.UpdatedAt), formatTime(now))
	if err != nil {
		return fmt.Errorf("save copy job: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm copy job save: %w", err)
	}
	if updated != 1 {
		return ErrCopyJobBusy
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit copy job save: %w", err)
	}
	return nil
}

// materializeCopySourceBackupDestination binds a copy policy to the local
// directory used by its source backup job at save time. The stable backup job
// ID continues to select the artifact stream and after-success trigger, while
// the stored path prevents a later backup-policy edit from silently changing
// what an already-proved copy job reads. An explicitly configured copy source
// remains authoritative.
func materializeCopySourceBackupDestination(ctx context.Context, tx *sql.Tx, job *CopyJob) error {
	if job == nil || strings.TrimSpace(job.SourceBackupJobID) == "" {
		return nil
	}
	backupJob, err := scanJob(tx.QueryRowContext(ctx, `SELECT job_json, next_run_at FROM backup_jobs WHERE id = ?`, job.SourceBackupJobID))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("source backup job %q was not found", job.SourceBackupJobID)
	}
	if err != nil {
		return fmt.Errorf("load source backup job %q: %w", job.SourceBackupJobID, err)
	}
	normalizedDestination, err := NormalizeBackupDestination(backupJob.Destination)
	if err != nil {
		return fmt.Errorf("source backup job %q has an invalid destination: %w", job.SourceBackupJobID, err)
	}
	if IsRemoteBackupDestination(normalizedDestination) {
		return fmt.Errorf("source backup job %q does not publish to a local directory", job.SourceBackupJobID)
	}
	if strings.TrimSpace(job.Source.Location) != "" {
		return nil
	}
	source, err := normalizeCopyEndpoint(CopyEndpoint{Kind: CopyEndpointLocal, Location: normalizedDestination}, false)
	if err != nil {
		return fmt.Errorf("materialize source backup job %q destination: %w", job.SourceBackupJobID, err)
	}
	job.Source = source
	return job.Validate()
}

// preserveStoredCopyTransferProof makes transfer proof store-issued. Public
// callers may carry proof fields while editing a job, but cannot manufacture a
// proof for a new policy or replace the proof recorded by FinishCopyRun.
func preserveStoredCopyTransferProof(ctx context.Context, tx *sql.Tx, job *CopyJob) error {
	var payload []byte
	err := tx.QueryRowContext(ctx, `SELECT job_json FROM copy_jobs WHERE id = ?`, job.ID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		job.TransferProofAt = time.Time{}
		job.TransferProofFingerprint = ""
		return nil
	}
	if err != nil {
		return fmt.Errorf("load stored copy transfer proof: %w", err)
	}
	var stored CopyJob
	if err := json.Unmarshal(payload, &stored); err != nil {
		return fmt.Errorf("decode stored copy transfer proof: %w", err)
	}
	job.TransferProofAt = stored.TransferProofAt
	job.TransferProofFingerprint = strings.ToLower(strings.TrimSpace(stored.TransferProofFingerprint))
	return nil
}

func (store *Store) ListCopyJobs(ctx context.Context) ([]CopyJob, error) {
	if store == nil || store.db == nil {
		return nil, fmt.Errorf("backup store is not open")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT job_json, mode, trigger_kind, next_run_at
		FROM copy_jobs ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, fmt.Errorf("list copy jobs: %w", err)
	}
	defer rows.Close()
	var jobs []CopyJob
	for rows.Next() {
		job, err := scanCopyJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list copy jobs: %w", err)
	}
	return jobs, nil
}

// ListEnabledAfterSuccessCopyJobs returns the locally owned push policies that
// should be considered after one backup job has durably completed. The caller
// must still acquire each copy-job lease before starting a run.
func (store *Store) ListEnabledAfterSuccessCopyJobs(ctx context.Context, backupJobID string) ([]CopyJob, error) {
	if store == nil || store.db == nil {
		return nil, fmt.Errorf("backup store is not open")
	}
	backupJobID = strings.TrimSpace(backupJobID)
	if backupJobID == "" {
		return nil, fmt.Errorf("source backup job ID is required")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT job_json, mode, trigger_kind, next_run_at
		FROM copy_jobs WHERE enabled = 1 AND trigger_kind = ? AND source_backup_job_id = ?
		ORDER BY name COLLATE NOCASE, id`, CopyTriggerAfterSuccess, backupJobID)
	if err != nil {
		return nil, fmt.Errorf("list after-success copy jobs: %w", err)
	}
	defer rows.Close()
	var jobs []CopyJob
	for rows.Next() {
		job, err := scanCopyJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list after-success copy jobs: %w", err)
	}
	return jobs, nil
}

func (store *Store) GetCopyJob(ctx context.Context, idOrName string) (CopyJob, error) {
	if store == nil || store.db == nil {
		return CopyJob{}, fmt.Errorf("backup store is not open")
	}
	idOrName = strings.TrimSpace(idOrName)
	if idOrName == "" {
		return CopyJob{}, fmt.Errorf("copy job ID or name is required")
	}
	job, err := scanCopyJob(store.db.QueryRowContext(ctx, `SELECT job_json, mode, trigger_kind, next_run_at
		FROM copy_jobs WHERE id = ?`, idOrName))
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CopyJob{}, err
	}
	rows, err := store.db.QueryContext(ctx, `SELECT job_json, mode, trigger_kind, next_run_at FROM copy_jobs ORDER BY id`)
	if err != nil {
		return CopyJob{}, fmt.Errorf("find copy job by name: %w", err)
	}
	defer rows.Close()
	var matches []CopyJob
	for rows.Next() {
		candidate, err := scanCopyJob(rows)
		if err != nil {
			return CopyJob{}, err
		}
		if strings.EqualFold(candidate.Name, idOrName) {
			matches = append(matches, candidate)
		}
	}
	if err := rows.Err(); err != nil {
		return CopyJob{}, fmt.Errorf("scan copy jobs by name: %w", err)
	}
	if len(matches) == 0 {
		return CopyJob{}, fmt.Errorf("copy job %q was not found", idOrName)
	}
	if len(matches) > 1 {
		ids := make([]string, len(matches))
		for index := range matches {
			ids[index] = matches[index].ID
		}
		return CopyJob{}, fmt.Errorf("copy job name %q is ambiguous across IDs %s; use a job ID", idOrName, strings.Join(ids, ", "))
	}
	return matches[0], nil
}

func (store *Store) DeleteCopyJob(ctx context.Context, idOrName string) error {
	job, err := store.GetCopyJob(ctx, idOrName)
	if err != nil {
		return err
	}
	result, err := store.db.ExecContext(ctx, `DELETE FROM copy_jobs
		WHERE id = ? AND (lease_until IS NULL OR lease_until <= ?)`, job.ID, formatTime(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("delete copy job: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm copy job deletion: %w", err)
	}
	if deleted != 1 {
		return ErrCopyJobBusy
	}
	return nil
}

func (store *Store) SetCopyJobEnabled(ctx context.Context, idOrName string, enabled bool) error {
	job, err := store.GetCopyJob(ctx, idOrName)
	if err != nil {
		return err
	}
	if enabled {
		job.Enabled = true
		job.NextRunAt = time.Time{}
		return store.UpsertCopyJob(ctx, &job)
	}
	job.Enabled = false
	job.NextRunAt = time.Time{}
	job.UpdatedAt = time.Now().UTC()
	payload, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode disabled copy job: %w", err)
	}
	result, err := store.db.ExecContext(ctx, `UPDATE copy_jobs
		SET enabled = 0, next_run_at = NULL, job_json = ?, updated_at = ? WHERE id = ?`,
		payload, formatTime(job.UpdatedAt), job.ID)
	if err != nil {
		return fmt.Errorf("disable copy job: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm disabled copy job: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("copy job %q was not found", job.ID)
	}
	return nil
}

func (store *Store) ClaimDueCopyJobs(ctx context.Context, now time.Time, owner string, limit int) ([]CopyJob, error) {
	if store == nil || store.db == nil {
		return nil, fmt.Errorf("backup store is not open")
	}
	if strings.TrimSpace(owner) == "" {
		return nil, fmt.Errorf("copy job lease owner is required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	if limit < 1 {
		limit = 1
	}
	now = now.UTC()
	claimed := make([]CopyJob, 0, limit)
	for len(claimed) < limit {
		var id string
		err := store.db.QueryRowContext(ctx, `SELECT id FROM copy_jobs
			WHERE enabled = 1 AND trigger_kind = ? AND next_run_at IS NOT NULL AND next_run_at <= ?
			AND (lease_until IS NULL OR lease_until <= ?)
			ORDER BY next_run_at, id LIMIT 1`, CopyTriggerTimed, formatTime(now), formatTime(now)).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return claimed, fmt.Errorf("find due copy job: %w", err)
		}
		job, err := store.GetCopyJob(ctx, id)
		if err != nil {
			return claimed, err
		}
		if !job.Schedule.RunMissedOnWake && now.Sub(job.NextRunAt) > missedRunGrace {
			advanced, err := store.advanceMissedCopyJob(ctx, job, now)
			if err != nil {
				return claimed, err
			}
			if advanced {
				continue
			}
		}
		job, err = store.claimCopyJob(ctx, job.ID, owner, now)
		if errors.Is(err, ErrCopyJobBusy) {
			continue
		}
		if err != nil {
			return claimed, err
		}
		claimed = append(claimed, job)
	}
	return claimed, nil
}

func (store *Store) advanceMissedCopyJob(ctx context.Context, job CopyJob, now time.Time) (bool, error) {
	previous := job.NextRunAt.UTC()
	next, ok, err := job.Schedule.AdvancePast(previous, now)
	if err != nil {
		return false, fmt.Errorf("advance missed copy job %q: %w", job.Name, err)
	}
	if ok {
		job.NextRunAt = next
	} else {
		job.NextRunAt = time.Time{}
	}
	job.UpdatedAt = now.UTC()
	payload, err := json.Marshal(job)
	if err != nil {
		return false, fmt.Errorf("encode advanced copy job %q: %w", job.Name, err)
	}
	result, err := store.db.ExecContext(ctx, `UPDATE copy_jobs
		SET next_run_at = ?, job_json = ?, updated_at = ?
		WHERE id = ? AND enabled = 1 AND trigger_kind = ? AND next_run_at = ?
		AND (lease_until IS NULL OR lease_until <= ?)`,
		formatNullableTime(job.NextRunAt), payload, formatTime(job.UpdatedAt), job.ID, CopyTriggerTimed,
		formatTime(previous), formatTime(now))
	if err != nil {
		return false, fmt.Errorf("skip missed copy job %q: %w", job.Name, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("confirm skipped copy job %q: %w", job.Name, err)
	}
	return updated == 1, nil
}

func (store *Store) ClaimCopyJob(ctx context.Context, idOrName, owner string, now time.Time) (CopyJob, error) {
	job, err := store.GetCopyJob(ctx, idOrName)
	if err != nil {
		return CopyJob{}, err
	}
	return store.claimCopyJob(ctx, job.ID, owner, now)
}

func (store *Store) claimCopyJob(ctx context.Context, id, owner string, now time.Time) (CopyJob, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return CopyJob{}, fmt.Errorf("copy job lease owner is required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	job, err := store.GetCopyJob(ctx, id)
	if err != nil {
		return CopyJob{}, err
	}
	leaseFor := time.Duration(job.TimeoutMinutes+10) * time.Minute
	result, err := store.db.ExecContext(ctx, `UPDATE copy_jobs SET lease_owner = ?, lease_until = ?
		WHERE id = ? AND (lease_until IS NULL OR lease_until <= ? OR lease_owner = ?)`,
		owner, formatTime(now.Add(leaseFor)), job.ID, formatTime(now), owner)
	if err != nil {
		return CopyJob{}, fmt.Errorf("claim copy job: %w", err)
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return CopyJob{}, fmt.Errorf("confirm copy job claim: %w", err)
	}
	if claimed != 1 {
		return CopyJob{}, ErrCopyJobBusy
	}
	return job, nil
}

func (store *Store) ReleaseCopyJob(ctx context.Context, jobID, leaseOwner string) error {
	if store == nil || store.db == nil {
		return fmt.Errorf("backup store is not open")
	}
	jobID = strings.TrimSpace(jobID)
	leaseOwner = strings.TrimSpace(leaseOwner)
	if jobID == "" || leaseOwner == "" {
		return fmt.Errorf("copy job ID and lease owner are required")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE copy_jobs SET lease_owner = NULL, lease_until = NULL
		WHERE id = ? AND lease_owner = ?`, jobID, leaseOwner)
	if err != nil {
		return fmt.Errorf("release copy job: %w", err)
	}
	released, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm copy job release: %w", err)
	}
	if released != 1 {
		return ErrCopyJobLeaseLost
	}
	return nil
}

// RenewCopyJobLease extends an existing live lease only for its current owner.
// It never revives an expired lease: once expiry permits another process to
// claim the job, the former owner must not continue into retention or other
// post-processing that assumes exclusive ownership.
func (store *Store) RenewCopyJobLease(ctx context.Context, jobID, leaseOwner string, now, leaseUntil time.Time) error {
	if store == nil || store.db == nil {
		return fmt.Errorf("backup store is not open")
	}
	jobID = strings.TrimSpace(jobID)
	leaseOwner = strings.TrimSpace(leaseOwner)
	if jobID == "" || leaseOwner == "" {
		return fmt.Errorf("copy job ID and lease owner are required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	leaseUntil = leaseUntil.UTC()
	if leaseUntil.IsZero() || !leaseUntil.After(now) {
		return fmt.Errorf("renewed copy job lease must expire after the renewal time")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE copy_jobs SET lease_until = ?
		WHERE id = ? AND lease_owner = ? AND lease_until IS NOT NULL AND lease_until > ?`,
		formatTime(leaseUntil), jobID, leaseOwner, formatTime(now))
	if err != nil {
		return fmt.Errorf("renew copy job lease: %w", err)
	}
	renewed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm copy job lease renewal: %w", err)
	}
	if renewed != 1 {
		return ErrCopyJobLeaseLost
	}
	return nil
}

func (store *Store) StartCopyRun(ctx context.Context, jobID string, trigger CopyTrigger, now time.Time) (CopyRun, error) {
	if store == nil || store.db == nil {
		return CopyRun{}, fmt.Errorf("backup store is not open")
	}
	trigger = CopyTrigger(strings.ToLower(strings.TrimSpace(string(trigger))))
	if trigger == "" {
		trigger = CopyTriggerManual
	}
	if now.IsZero() {
		now = time.Now()
	}
	job, err := store.prepareCopyJobForExecution(ctx, jobID, "", trigger, now)
	if err != nil {
		return CopyRun{}, err
	}
	id, err := NewID("copyrun")
	if err != nil {
		return CopyRun{}, err
	}
	run := CopyRun{
		ID: id, JobID: job.ID, Trigger: trigger, Status: RunRunning, StartedAt: now.UTC(),
		RequiredVerification: job.Verification, Artifacts: []CopyArtifactResult{}, Warnings: []string{},
	}
	payload, err := json.Marshal(run)
	if err != nil {
		return CopyRun{}, fmt.Errorf("encode copy run: %w", err)
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO copy_runs
		(id, job_id, trigger_kind, status, started_at, run_json) VALUES (?, ?, ?, ?, ?, ?)`,
		run.ID, run.JobID, run.Trigger, run.Status, formatTime(run.StartedAt), payload)
	if err != nil {
		return CopyRun{}, fmt.Errorf("start copy run: %w", err)
	}
	return run, nil
}

// prepareCopyJobForExecution is the final fail-closed gate before any copy-run
// record, volume preparation, or transfer. Automatic jobs are reloaded under a
// transaction so execution never trusts the earlier scheduler snapshot. A
// missing or stale real-transfer proof atomically disables the policy, clears
// its next occurrence and lease, and returns an actionable error without
// creating a CopyRun. Manual execution deliberately bypasses only the proof
// requirement so it can establish a new proof.
func (store *Store) prepareCopyJobForExecution(ctx context.Context, jobID, leaseOwner string, trigger CopyTrigger, now time.Time) (CopyJob, error) {
	if store == nil || store.db == nil {
		return CopyJob{}, fmt.Errorf("backup store is not open")
	}
	jobID = strings.TrimSpace(jobID)
	leaseOwner = strings.TrimSpace(leaseOwner)
	trigger = CopyTrigger(strings.ToLower(strings.TrimSpace(string(trigger))))
	if jobID == "" {
		return CopyJob{}, fmt.Errorf("copy job ID is required")
	}
	if trigger == "" {
		trigger = CopyTriggerManual
	}
	if trigger != CopyTriggerManual && trigger != CopyTriggerAfterSuccess && trigger != CopyTriggerTimed {
		return CopyJob{}, fmt.Errorf("unsupported copy run trigger %q", trigger)
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return CopyJob{}, fmt.Errorf("begin copy execution proof check: %w", err)
	}
	defer tx.Rollback()

	var payload []byte
	var mode CopyMode
	var storedTrigger CopyTrigger
	var enabled int
	var nextRaw, storedLeaseOwner, leaseUntilRaw sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT job_json, mode, trigger_kind, enabled, next_run_at, lease_owner, lease_until
		FROM copy_jobs WHERE id = ?`, jobID).Scan(&payload, &mode, &storedTrigger, &enabled, &nextRaw, &storedLeaseOwner, &leaseUntilRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return CopyJob{}, fmt.Errorf("copy job %q was not found", jobID)
	}
	if err != nil {
		return CopyJob{}, fmt.Errorf("load copy job for execution: %w", err)
	}
	var job CopyJob
	if err := json.Unmarshal(payload, &job); err != nil {
		return CopyJob{}, fmt.Errorf("decode copy job for execution: %w", err)
	}
	job.applyCompatibilityDefaults()
	job.ID = jobID
	job.Mode = mode
	job.Trigger = storedTrigger
	job.Enabled = enabled != 0
	if parsed, ok := parseNullableTime(nextRaw); ok {
		job.NextRunAt = parsed
	} else {
		job.NextRunAt = time.Time{}
	}
	if leaseOwner != "" {
		leaseUntil, live := parseNullableTime(leaseUntilRaw)
		if !storedLeaseOwner.Valid || storedLeaseOwner.String != leaseOwner || !live || !leaseUntil.After(now) {
			return CopyJob{}, ErrCopyJobLeaseLost
		}
	}
	if trigger == CopyTriggerManual {
		if err := tx.Commit(); err != nil {
			return CopyJob{}, fmt.Errorf("commit manual copy execution check: %w", err)
		}
		return job, nil
	}
	if trigger != job.Trigger {
		return CopyJob{}, fmt.Errorf("copy run trigger %q does not match job trigger %q", trigger, job.Trigger)
	}
	if !job.Enabled {
		return CopyJob{}, fmt.Errorf("disabled copy job cannot start an automatic %q run", trigger)
	}
	if job.HasCurrentTransferProof() {
		if err := tx.Commit(); err != nil {
			return CopyJob{}, fmt.Errorf("commit copy execution proof check: %w", err)
		}
		return job, nil
	}

	job.Enabled = false
	job.NextRunAt = time.Time{}
	job.UpdatedAt = now
	payload, err = json.Marshal(job)
	if err != nil {
		return CopyJob{}, fmt.Errorf("encode proof-blocked copy job: %w", err)
	}
	query := `UPDATE copy_jobs SET enabled = 0, next_run_at = NULL, lease_owner = NULL, lease_until = NULL,
		job_json = ?, updated_at = ? WHERE id = ? AND enabled = 1`
	arguments := []any{payload, formatTime(job.UpdatedAt), job.ID}
	if leaseOwner != "" {
		query += ` AND lease_owner = ? AND lease_until IS NOT NULL AND lease_until > ?`
		arguments = append(arguments, leaseOwner, formatTime(now))
	}
	result, err := tx.ExecContext(ctx, query, arguments...)
	if err != nil {
		return CopyJob{}, fmt.Errorf("disable copy job with missing transfer proof: %w", err)
	}
	disabled, err := result.RowsAffected()
	if err != nil {
		return CopyJob{}, fmt.Errorf("confirm proof-blocked copy disable: %w", err)
	}
	if disabled != 1 {
		if leaseOwner != "" {
			return CopyJob{}, ErrCopyJobLeaseLost
		}
		return CopyJob{}, fmt.Errorf("copy job %q changed during its execution proof check", job.ID)
	}
	if err := tx.Commit(); err != nil {
		return CopyJob{}, fmt.Errorf("commit proof-blocked copy disable: %w", err)
	}
	return job, fmt.Errorf("%w: automatic %s execution was blocked and the copy job was disabled; run it manually with the current source, destination, volume, filters, and verification, then enable it again", ErrCopyJobTransferProofRequired, trigger)
}

func (store *Store) FinishCopyRun(ctx context.Context, run *CopyRun, leaseOwner string) error {
	return store.finishCopyRun(ctx, run, leaseOwner, true)
}

// recordCopyRunTerminal durably records transfer validity while retaining the
// copy-job lease. The agent uses this boundary so retention, volume release,
// and notification post-processing cannot overlap a second run of the job.
func (store *Store) recordCopyRunTerminal(ctx context.Context, run *CopyRun, leaseOwner string) error {
	return store.finishCopyRun(ctx, run, leaseOwner, false)
}

func (store *Store) finishCopyRun(ctx context.Context, run *CopyRun, leaseOwner string, releaseLease bool) error {
	if store == nil || store.db == nil {
		return fmt.Errorf("backup store is not open")
	}
	if run == nil {
		return fmt.Errorf("copy run is required")
	}
	leaseOwner = strings.TrimSpace(leaseOwner)
	if leaseOwner == "" {
		return fmt.Errorf("copy job lease owner is required")
	}
	if run.FinishedAt.IsZero() {
		run.FinishedAt = time.Now().UTC()
	} else {
		run.FinishedAt = run.FinishedAt.UTC()
	}
	if run.Status == "" || run.Status == RunRunning {
		if strings.TrimSpace(run.Error) == "" {
			run.Status = RunSucceeded
		} else {
			run.Status = RunFailed
		}
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("finish copy run: %w", err)
	}
	defer tx.Rollback()
	var indexedJobID string
	var indexedTrigger CopyTrigger
	var indexedStatus RunStatus
	var indexedStartedAt string
	if err := tx.QueryRowContext(ctx, `SELECT job_id, trigger_kind, status, started_at
		FROM copy_runs WHERE id = ?`, run.ID).Scan(&indexedJobID, &indexedTrigger, &indexedStatus, &indexedStartedAt); err != nil {
		return fmt.Errorf("load active copy run: %w", err)
	}
	if indexedStatus != RunRunning {
		return fmt.Errorf("copy run %q is not running", run.ID)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, indexedStartedAt)
	if err != nil {
		return fmt.Errorf("decode active copy run start time: %w", err)
	}
	run.JobID = indexedJobID
	run.Trigger = indexedTrigger
	run.StartedAt = startedAt.UTC()
	var jobPayload []byte
	var nextRaw sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT job_json, next_run_at FROM copy_jobs WHERE id = ?`, run.JobID).Scan(&jobPayload, &nextRaw); err != nil {
		return fmt.Errorf("load completed copy job: %w", err)
	}
	var job CopyJob
	if err := json.Unmarshal(jobPayload, &job); err != nil {
		return fmt.Errorf("decode completed copy job: %w", err)
	}
	job.applyCompatibilityDefaults()
	run.RequiredVerification = job.Verification
	if err := validateTerminalCopyRun(*run); err != nil {
		return err
	}
	for _, artifact := range run.Artifacts {
		if err := artifact.validate(job.Verification, run.Status == RunSucceeded); err != nil {
			return err
		}
		if artifact.VerifiedAt.Before(run.StartedAt) || artifact.VerifiedAt.After(run.FinishedAt) {
			return fmt.Errorf("copied artifact verification time is outside the copy run")
		}
	}
	run.Artifacts, err = filterPreviouslyOwnedCopyArtifacts(ctx, tx, run.JobID, run.ID, run.Artifacts)
	if err != nil {
		return err
	}
	if copyRunProvesTransfer(*run) {
		fingerprint, fingerprintErr := job.TransferConfigurationFingerprint()
		if fingerprintErr != nil {
			return fmt.Errorf("record copy transfer proof: %w", fingerprintErr)
		}
		job.TransferProofAt = run.FinishedAt
		job.TransferProofFingerprint = fingerprint
	}
	job.LastRunAt = run.FinishedAt
	job.UpdatedAt = run.FinishedAt
	if run.Trigger == CopyTriggerTimed && job.Enabled && job.Trigger == CopyTriggerTimed {
		next, ok, err := job.Schedule.Next(run.FinishedAt)
		if err != nil {
			return err
		}
		if ok {
			job.NextRunAt = next
		} else {
			job.NextRunAt = time.Time{}
		}
	} else if parsed, ok := parseNullableTime(nextRaw); ok {
		job.NextRunAt = parsed
	} else {
		job.NextRunAt = time.Time{}
	}
	jobPayload, err = json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode completed copy job: %w", err)
	}
	runPayload, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("encode completed copy run: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE copy_runs SET status = ?, finished_at = ?, run_json = ?
		WHERE id = ? AND job_id = ? AND status = ?`, run.Status, formatTime(run.FinishedAt), runPayload,
		run.ID, run.JobID, RunRunning)
	if err != nil {
		return fmt.Errorf("update copy run: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm copy run update: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("copy run %q is not running", run.ID)
	}
	jobUpdate := `UPDATE copy_jobs SET job_json = ?, next_run_at = ?, updated_at = ? WHERE id = ? AND lease_owner = ?`
	if releaseLease {
		jobUpdate = `UPDATE copy_jobs SET lease_owner = NULL, lease_until = NULL,
			job_json = ?, next_run_at = ?, updated_at = ? WHERE id = ? AND lease_owner = ?`
	}
	result, err = tx.ExecContext(ctx, jobUpdate,
		jobPayload, formatNullableTime(job.NextRunAt), formatTime(job.UpdatedAt), job.ID, leaseOwner)
	if err != nil {
		return fmt.Errorf("update completed copy job: %w", err)
	}
	released, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm completed copy job update: %w", err)
	}
	if released != 1 {
		return ErrCopyJobLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit completed copy run: %w", err)
	}
	return nil
}

func requireCurrentCopyTransferProof(job CopyJob) error {
	if !job.Enabled || job.Trigger == CopyTriggerManual {
		return nil
	}
	if job.HasCurrentTransferProof() {
		return nil
	}
	return fmt.Errorf("%w: run this copy manually with the current source, destination, volume, filters, and verification, then enable it; the read-only endpoint test does not count", ErrCopyJobTransferProofRequired)
}

func copyRunProvesTransfer(run CopyRun) bool {
	if run.Status != RunSucceeded || run.BytesCopied <= 0 {
		return false
	}
	for _, artifact := range run.Artifacts {
		if artifact.PublicationState == ArtifactPublicationComplete && !artifact.AlreadyPresent && !artifact.Reconciled {
			return true
		}
	}
	return false
}

// filterPreviouslyOwnedCopyArtifacts keeps CopyRun as an append-only history
// while ensuring an immutable recovery point has one active ownership record
// per job and destination. A reverified destination encountered after catalog
// recreation is retained; later no-op observations are represented by the
// AlreadyPresent counter without creating ambiguous inspection records.
func filterPreviouslyOwnedCopyArtifacts(ctx context.Context, tx *sql.Tx, jobID, currentRunID string, artifacts []CopyArtifactResult) ([]CopyArtifactResult, error) {
	type ownershipKey struct {
		artifactID  string
		destination string
	}
	previous := make(map[ownershipKey]CopyArtifactResult)
	rows, err := tx.QueryContext(ctx, `SELECT id, status, run_json FROM copy_runs
		WHERE job_id = ? AND id <> ? AND status <> ? ORDER BY started_at DESC, id DESC`, jobID, currentRunID, RunRunning)
	if err != nil {
		return nil, fmt.Errorf("load existing copy artifact ownership: %w", err)
	}
	for rows.Next() {
		var runID string
		var indexedStatus RunStatus
		var payload []byte
		if err := rows.Scan(&runID, &indexedStatus, &payload); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("read existing copy artifact ownership: %w", err)
		}
		var prior CopyRun
		if err := json.Unmarshal(payload, &prior); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("decode existing copy run %s while recording ownership: %w", runID, err)
		}
		if prior.ID != runID || prior.JobID != jobID || prior.Status != indexedStatus {
			_ = rows.Close()
			return nil, fmt.Errorf("copy run %s ownership metadata does not match its catalog index", runID)
		}
		for _, artifact := range prior.Artifacts {
			if artifact.PublicationState != ArtifactPublicationComplete || !artifact.PrunedAt.IsZero() {
				continue
			}
			key := ownershipKey{artifactID: artifact.ArtifactID, destination: artifact.Destination}
			if existing, ok := previous[key]; ok && !sameCopyArtifactOwnership(existing, artifact) {
				_ = rows.Close()
				return nil, fmt.Errorf("copy artifact %q has conflicting active catalog ownership at %s", artifact.ArtifactID, artifact.Destination)
			}
			previous[key] = artifact
		}
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close existing copy artifact ownership: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan existing copy artifact ownership: %w", err)
	}

	filtered := make([]CopyArtifactResult, 0, len(artifacts))
	current := make(map[ownershipKey]CopyArtifactResult, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.PublicationState != ArtifactPublicationComplete || !artifact.PrunedAt.IsZero() {
			filtered = append(filtered, artifact)
			continue
		}
		key := ownershipKey{artifactID: artifact.ArtifactID, destination: artifact.Destination}
		if duplicate, ok := current[key]; ok {
			if !sameCopyArtifactOwnership(duplicate, artifact) {
				return nil, fmt.Errorf("copy run contains conflicting ownership for artifact %q at %s", artifact.ArtifactID, artifact.Destination)
			}
			continue
		}
		current[key] = artifact
		if owned, ok := previous[key]; ok {
			if !sameCopyArtifactOwnership(owned, artifact) {
				return nil, fmt.Errorf("copy artifact %q conflicts with active catalog ownership at %s", artifact.ArtifactID, artifact.Destination)
			}
			continue
		}
		filtered = append(filtered, artifact)
	}
	return filtered, nil
}

func sameCopyArtifactOwnership(left, right CopyArtifactResult) bool {
	return left.ArtifactID == right.ArtifactID && left.Destination == right.Destination &&
		left.SizeBytes == right.SizeBytes && strings.EqualFold(left.SHA256, right.SHA256) &&
		left.SourceCreatedAt.Equal(right.SourceCreatedAt) && left.ManifestPath == right.ManifestPath &&
		left.ManifestSize == right.ManifestSize && strings.EqualFold(left.ManifestSHA256, right.ManifestSHA256)
}

func validateTerminalCopyRun(run CopyRun) error {
	if strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.JobID) == "" {
		return fmt.Errorf("copy run ID and job ID are required")
	}
	switch run.Trigger {
	case CopyTriggerManual, CopyTriggerAfterSuccess, CopyTriggerTimed:
	default:
		return fmt.Errorf("unsupported copy run trigger %q", run.Trigger)
	}
	switch run.Status {
	case RunSucceeded:
		if strings.TrimSpace(run.Error) != "" {
			return fmt.Errorf("successful copy run cannot contain an error")
		}
	case RunFailed:
		if strings.TrimSpace(run.Error) == "" {
			return fmt.Errorf("failed copy run requires an error")
		}
	case RunCanceled:
	default:
		return fmt.Errorf("copy run must have a terminal status")
	}
	if run.StartedAt.IsZero() || run.FinishedAt.Before(run.StartedAt) {
		return fmt.Errorf("copy run timestamps are invalid")
	}
	if run.Discovered < 0 || run.AlreadyPresent < 0 || run.BytesCopied < 0 {
		return fmt.Errorf("copy run counters cannot be negative")
	}
	return nil
}

func (store *Store) ListCopyRuns(ctx context.Context, jobID string, limit int) ([]CopyRun, error) {
	if store == nil || store.db == nil {
		return nil, fmt.Errorf("backup store is not open")
	}
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	query := `SELECT run_json, id, job_id, trigger_kind, status, started_at, finished_at FROM copy_runs`
	args := []any{}
	if strings.TrimSpace(jobID) != "" {
		query += ` WHERE job_id = ?`
		args = append(args, strings.TrimSpace(jobID))
	}
	query += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list copy runs: %w", err)
	}
	defer rows.Close()
	var runs []CopyRun
	for rows.Next() {
		run, err := scanCopyRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list copy runs: %w", err)
	}
	return runs, nil
}

func (store *Store) GetCopyRun(ctx context.Context, runID string) (CopyRun, error) {
	if store == nil || store.db == nil {
		return CopyRun{}, fmt.Errorf("backup store is not open")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return CopyRun{}, fmt.Errorf("copy run ID is required")
	}
	run, err := scanCopyRun(store.db.QueryRowContext(ctx, `SELECT run_json, id, job_id, trigger_kind, status, started_at, finished_at
		FROM copy_runs WHERE id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return CopyRun{}, fmt.Errorf("copy run %q was not found", runID)
	}
	return run, err
}

func (store *Store) LatestCopyRun(ctx context.Context, jobID string) (CopyRun, bool, error) {
	runs, err := store.ListCopyRuns(ctx, jobID, 1)
	if err != nil || len(runs) == 0 {
		return CopyRun{}, false, err
	}
	return runs[0], true, nil
}

func (store *Store) ReconcileStaleCopyRuns(ctx context.Context, now time.Time) (int, error) {
	if store == nil || store.db == nil {
		return 0, fmt.Errorf("backup store is not open")
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin stale copy recovery: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT r.id, r.job_id, r.run_json FROM copy_runs r
		WHERE r.status = ? AND NOT EXISTS (
			SELECT 1 FROM copy_jobs j WHERE j.id = r.job_id
			AND j.lease_until IS NOT NULL AND j.lease_until > ?
		) ORDER BY r.started_at, r.id`, RunRunning, formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("find stale copy runs: %w", err)
	}
	var stale []CopyRun
	for rows.Next() {
		var id, jobID string
		var payload []byte
		if err := rows.Scan(&id, &jobID, &payload); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("read stale copy run: %w", err)
		}
		var run CopyRun
		if err := json.Unmarshal(payload, &run); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("decode stale copy run: %w", err)
		}
		run.ID = id
		run.JobID = jobID
		stale = append(stale, run)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close stale copy scan: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("scan stale copy runs: %w", err)
	}

	reconciled := 0
	for _, run := range stale {
		run.Status = RunFailed
		run.FinishedAt = now
		run.Error = staleCopyRunRecoveryError
		payload, err := json.Marshal(run)
		if err != nil {
			return 0, fmt.Errorf("encode recovered copy run %q: %w", run.ID, err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE copy_runs SET status = ?, finished_at = ?, run_json = ?
			WHERE id = ? AND status = ? AND NOT EXISTS (
				SELECT 1 FROM copy_jobs j WHERE j.id = copy_runs.job_id
				AND j.lease_until IS NOT NULL AND j.lease_until > ?
			)`, RunFailed, formatTime(now), payload, run.ID, RunRunning, formatTime(now))
		if err != nil {
			return 0, fmt.Errorf("recover stale copy run %q: %w", run.ID, err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("confirm stale copy recovery %q: %w", run.ID, err)
		}
		if updated != 1 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE copy_jobs SET lease_owner = NULL, lease_until = NULL
			WHERE id = ? AND (lease_until IS NULL OR lease_until <= ?)`, run.JobID, formatTime(now)); err != nil {
			return 0, fmt.Errorf("release expired lease for recovered copy run %q: %w", run.ID, err)
		}
		reconciled++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit stale copy recovery: %w", err)
	}
	return reconciled, nil
}

func scanCopyJob(row rowScanner) (CopyJob, error) {
	var payload []byte
	var mode CopyMode
	var trigger CopyTrigger
	var nextRaw sql.NullString
	if err := row.Scan(&payload, &mode, &trigger, &nextRaw); err != nil {
		return CopyJob{}, err
	}
	var job CopyJob
	if err := json.Unmarshal(payload, &job); err != nil {
		return CopyJob{}, fmt.Errorf("decode copy job: %w", err)
	}
	job.applyCompatibilityDefaults()
	job.Mode = mode
	job.Trigger = trigger
	if parsed, ok := parseNullableTime(nextRaw); ok {
		job.NextRunAt = parsed
	} else {
		job.NextRunAt = time.Time{}
	}
	return job, nil
}

func scanCopyRun(row rowScanner) (CopyRun, error) {
	var payload []byte
	var id, jobID string
	var trigger CopyTrigger
	var status RunStatus
	var startedRaw string
	var finishedRaw sql.NullString
	if err := row.Scan(&payload, &id, &jobID, &trigger, &status, &startedRaw, &finishedRaw); err != nil {
		return CopyRun{}, err
	}
	var run CopyRun
	if err := json.Unmarshal(payload, &run); err != nil {
		return CopyRun{}, fmt.Errorf("decode copy run: %w", err)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, startedRaw)
	if err != nil {
		return CopyRun{}, fmt.Errorf("decode copy run start time: %w", err)
	}
	run.ID = id
	run.JobID = jobID
	run.Trigger = trigger
	run.Status = status
	run.StartedAt = startedAt.UTC()
	if finishedAt, ok := parseNullableTime(finishedRaw); ok {
		run.FinishedAt = finishedAt
	} else {
		run.FinishedAt = time.Time{}
	}
	return run, nil
}

func nullableCopyText(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func copyJobsConflict(left, right CopyJob) bool {
	sameRoute := copyTopologyEndpointsEqual(left.Source, left.SourceBackupJobID, nil, right.Source, right.SourceBackupJobID, nil) &&
		copyTopologyEndpointsEqual(left.Destination, "", left.DestinationVolume, right.Destination, "", right.DestinationVolume)
	reversedRoute := copyTopologyEndpointsEqual(left.Source, left.SourceBackupJobID, nil, right.Destination, "", right.DestinationVolume) &&
		copyTopologyEndpointsEqual(left.Destination, "", left.DestinationVolume, right.Source, right.SourceBackupJobID, nil)
	return (sameRoute || reversedRoute) && copyArtifactFiltersOverlap(left, right)
}

func copyTopologyEndpointsEqual(left CopyEndpoint, leftSourceBackupJobID string, leftVolume *CopyDestinationVolume, right CopyEndpoint, rightSourceBackupJobID string, rightVolume *CopyDestinationVolume) bool {
	if copyTopologyEndpoint(left, leftSourceBackupJobID) == copyTopologyEndpoint(right, rightSourceBackupJobID) {
		return true
	}
	if left.Kind != CopyEndpointLocal || right.Kind != CopyEndpointLocal ||
		strings.TrimSpace(left.Location) == "" || strings.TrimSpace(right.Location) == "" {
		return false
	}
	leftInfo, leftErr := os.Stat(left.Location)
	rightInfo, rightErr := os.Stat(right.Location)
	if leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo) {
		return true
	}
	return copyManagedVolumeEndpointsEqual(left, leftVolume, right, rightVolume)
}

func copyManagedVolumeEndpointsEqual(left CopyEndpoint, leftVolume *CopyDestinationVolume, right CopyEndpoint, rightVolume *CopyDestinationVolume) bool {
	if left.Kind != CopyEndpointLocal || right.Kind != CopyEndpointLocal || leftVolume == nil || rightVolume == nil ||
		leftVolume.Mode != CopyVolumeManagedLinuxBlockDevice || rightVolume.Mode != CopyVolumeManagedLinuxBlockDevice ||
		strings.TrimSpace(leftVolume.FilesystemUUID) == "" || !strings.EqualFold(leftVolume.FilesystemUUID, rightVolume.FilesystemUUID) {
		return false
	}
	leftRelative, leftErr := filepath.Rel(leftVolume.MountPoint, left.Location)
	rightRelative, rightErr := filepath.Rel(rightVolume.MountPoint, right.Location)
	if leftErr != nil || rightErr != nil || copyPathTraverses(leftRelative) || copyPathTraverses(rightRelative) {
		return false
	}
	return filepath.ToSlash(filepath.Clean(leftRelative)) == filepath.ToSlash(filepath.Clean(rightRelative))
}

func copyTopologyEndpoint(endpoint CopyEndpoint, sourceBackupJobID string) string {
	if normalized, err := normalizeCopyEndpoint(endpoint, strings.TrimSpace(sourceBackupJobID) != ""); err == nil {
		endpoint = normalized
	}
	value := strings.TrimSpace(endpoint.Location)
	if value == "" && strings.TrimSpace(sourceBackupJobID) != "" {
		return "backup-job:" + strings.TrimSpace(sourceBackupJobID)
	}
	if endpoint.Kind == CopyEndpointLocal {
		// Windows volume paths are case-insensitive in the normal deployment,
		// while lowering Unix paths would incorrectly merge distinct streams.
		if len(value) >= 2 && value[1] == ':' {
			value = strings.ToLower(value)
		}
	}
	return string(endpoint.Kind) + ":" + value
}

func copyArtifactFiltersOverlap(left, right CopyJob) bool {
	leftJob := strings.TrimSpace(left.ArtifactFilter.JobID)
	if leftJob == "" {
		leftJob = strings.TrimSpace(left.SourceBackupJobID)
	}
	rightJob := strings.TrimSpace(right.ArtifactFilter.JobID)
	if rightJob == "" {
		rightJob = strings.TrimSpace(right.SourceBackupJobID)
	}
	if !copyOptionalFilterValueOverlaps(left.ArtifactFilter.ProducerID, right.ArtifactFilter.ProducerID) ||
		!copyOptionalFilterValueOverlaps(leftJob, rightJob) {
		return false
	}
	if len(left.ArtifactFilter.Formats) == 0 || len(right.ArtifactFilter.Formats) == 0 {
		return true
	}
	formats := make(map[string]struct{}, len(left.ArtifactFilter.Formats))
	for _, format := range left.ArtifactFilter.Formats {
		formats[strings.ToLower(strings.TrimSpace(format))] = struct{}{}
	}
	for _, format := range right.ArtifactFilter.Formats {
		if _, exists := formats[strings.ToLower(strings.TrimSpace(format))]; exists {
			return true
		}
	}
	return false
}

func copyOptionalFilterValueOverlaps(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left == "" || right == "" || left == right
}
