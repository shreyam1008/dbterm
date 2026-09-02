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
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

var (
	ErrJobBusy      = errors.New("backup job is already running")
	ErrJobLeaseLost = errors.New("backup job lease was lost")
)

// missedRunGrace keeps normal scheduler jitter from being mistaken for an
// agent wake. The default agent polls every 30 seconds, so two minutes allows
// several delayed polls before RunMissedOnWake is consulted.
const missedRunGrace = 2 * time.Minute

const staleRunRecoveryError = "The dbterm process stopped before this run recorded completion, and no active job lease remains. Run the backup again; if it repeats, inspect the backup agent log."

type Store struct {
	db   *sql.DB
	path string
}

func OpenStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("backup store path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create backup state directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open backup store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, path: filepath.Clean(path)}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		_ = db.Close()
		return nil, fmt.Errorf("protect backup store permissions: %w", err)
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("backup store is not open")
	}
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE IF NOT EXISTS backup_jobs (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			connection_id TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			next_run_at TEXT,
			lease_owner TEXT,
			lease_until TEXT,
			job_json BLOB NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS backup_jobs_due_idx ON backup_jobs(enabled, next_run_at, lease_until)`,
		`CREATE TABLE IF NOT EXISTS backup_runs (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL,
			trigger_kind TEXT NOT NULL,
			status TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT,
			run_json BLOB NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS backup_runs_job_idx ON backup_runs(job_id, started_at DESC)`,
		`CREATE TABLE IF NOT EXISTS backup_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT OR IGNORE INTO backup_meta(key, value) VALUES ('schema_version', '1')`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize backup store: %w", err)
		}
	}
	return s.migrateCopyCatalog(ctx)
}

func (s *Store) UpsertJob(ctx context.Context, job *Job) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("backup store is not open")
	}
	now := time.Now().UTC()
	if err := job.ApplyDefaults(now); err != nil {
		return err
	}
	if job.Enabled && job.NextRunAt.IsZero() {
		next, ok, err := job.Schedule.Next(now)
		if err != nil {
			return err
		}
		if ok {
			job.NextRunAt = next
		}
	}
	if !job.Enabled {
		job.NextRunAt = time.Time{}
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode backup job: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin backup job save: %w", err)
	}
	defer tx.Rollback()
	if err := refuseEnabledDependentCopyDestinationChange(ctx, tx, job); err != nil {
		return err
	}

	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM backup_jobs WHERE id <> ? ORDER BY id`, job.ID)
	if err != nil {
		return fmt.Errorf("check backup job name: %w", err)
	}
	for rows.Next() {
		var existingID string
		var existingName string
		if err := rows.Scan(&existingID, &existingName); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read backup job name: %w", err)
		}
		if strings.EqualFold(existingName, job.Name) {
			_ = rows.Close()
			return fmt.Errorf("backup job name %q is already used by job %s; choose a unique name", job.Name, existingID)
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close backup job name check: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("scan backup job names: %w", err)
	}

	result, err := tx.ExecContext(ctx, `INSERT INTO backup_jobs
		(id, name, connection_id, enabled, next_run_at, lease_owner, lease_until, job_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		name=excluded.name, connection_id=excluded.connection_id, enabled=excluded.enabled,
		next_run_at=excluded.next_run_at, job_json=excluded.job_json, updated_at=excluded.updated_at
		WHERE backup_jobs.lease_until IS NULL OR backup_jobs.lease_until <= ?`,
		job.ID, job.Name, job.ConnectionID, boolInt(job.Enabled), formatNullableTime(job.NextRunAt), payload,
		formatTime(job.CreatedAt), formatTime(job.UpdatedAt), formatTime(now))
	if err != nil {
		return fmt.Errorf("save backup job: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm backup job save: %w", err)
	}
	if updated != 1 {
		return ErrJobBusy
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit backup job save: %w", err)
	}
	return nil
}

// refuseEnabledDependentCopyDestinationChange prevents an after-success (or
// otherwise linked) copy from silently continuing to read its old,
// materialized source after its producer is moved. Operators must stop the
// dependent policy, update and re-prove that route, then enable it again.
func refuseEnabledDependentCopyDestinationChange(ctx context.Context, tx *sql.Tx, job *Job) error {
	var payload []byte
	err := tx.QueryRowContext(ctx, `SELECT job_json FROM backup_jobs WHERE id = ?`, job.ID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load stored backup destination: %w", err)
	}
	var stored Job
	if err := json.Unmarshal(payload, &stored); err != nil {
		return fmt.Errorf("decode stored backup destination: %w", err)
	}
	if stored.Destination == job.Destination {
		return nil
	}
	var copyID, copyName string
	err = tx.QueryRowContext(ctx, `SELECT id, name FROM copy_jobs
		WHERE source_backup_job_id = ? AND enabled = 1 ORDER BY name COLLATE NOCASE, id LIMIT 1`, job.ID).Scan(&copyID, &copyName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check dependent copy jobs: %w", err)
	}
	return fmt.Errorf("backup destination cannot change while dependent copy job %q (%s) is enabled; disable it, update its source to the new destination, run a real transfer to re-prove the route, then enable it again", copyName, copyID)
}

func (s *Store) ListJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT job_json, next_run_at FROM backup_jobs ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, fmt.Errorf("list backup jobs: %w", err)
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list backup jobs: %w", err)
	}
	return jobs, nil
}

func (s *Store) GetJob(ctx context.Context, idOrName string) (Job, error) {
	idOrName = strings.TrimSpace(idOrName)
	row := s.db.QueryRowContext(ctx, `SELECT job_json, next_run_at FROM backup_jobs WHERE id = ?`, idOrName)
	job, err := scanJob(row)
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT job_json, next_run_at FROM backup_jobs ORDER BY id`)
	if err != nil {
		return Job{}, fmt.Errorf("find backup job by name: %w", err)
	}
	defer rows.Close()
	var matches []Job
	for rows.Next() {
		candidate, scanErr := scanJob(rows)
		if scanErr != nil {
			return Job{}, scanErr
		}
		if strings.EqualFold(candidate.Name, idOrName) {
			matches = append(matches, candidate)
		}
	}
	if err := rows.Err(); err != nil {
		return Job{}, fmt.Errorf("scan backup jobs by name: %w", err)
	}
	if len(matches) == 0 {
		return Job{}, fmt.Errorf("backup job %q was not found", idOrName)
	}
	if len(matches) > 1 {
		ids := make([]string, len(matches))
		for index := range matches {
			ids[index] = matches[index].ID
		}
		return Job{}, fmt.Errorf("backup job name %q is ambiguous across IDs %s; use a job ID", idOrName, strings.Join(ids, ", "))
	}
	return matches[0], nil
}

func (s *Store) DeleteJob(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM backup_jobs
		WHERE id = ? AND (lease_until IS NULL OR lease_until <= ?)`, strings.TrimSpace(id), formatTime(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("delete backup job: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		if _, getErr := s.GetJob(ctx, id); getErr == nil {
			return ErrJobBusy
		}
		return fmt.Errorf("backup job %q was not found", id)
	}
	return nil
}

func (s *Store) SetJobEnabled(ctx context.Context, id string, enabled bool) error {
	job, err := s.GetJob(ctx, id)
	if err != nil {
		return err
	}
	// Disabling is a fail-safe state transition and must remain possible for a
	// legacy job whose former destination or settings are no longer accepted.
	// Enabling still goes through full validation below.
	if !enabled {
		job.Enabled = false
		job.NextRunAt = time.Time{}
		job.UpdatedAt = time.Now().UTC()
		payload, marshalErr := json.Marshal(job)
		if marshalErr != nil {
			return fmt.Errorf("encode disabled backup job: %w", marshalErr)
		}
		result, updateErr := s.db.ExecContext(ctx, `UPDATE backup_jobs
			SET enabled = 0, next_run_at = NULL, job_json = ?, updated_at = ? WHERE id = ?`,
			payload, formatTime(job.UpdatedAt), job.ID)
		if updateErr != nil {
			return fmt.Errorf("disable backup job: %w", updateErr)
		}
		updated, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("confirm disabled backup job: %w", rowsErr)
		}
		if updated != 1 {
			return fmt.Errorf("backup job %q was not found", job.ID)
		}
		return nil
	}
	job.Enabled = enabled
	job.NextRunAt = time.Time{}
	return s.UpsertJob(ctx, &job)
}

func (s *Store) ClaimDueJobs(ctx context.Context, now time.Time, owner string, limit int) ([]Job, error) {
	return s.claimDueJobsAt(ctx, now, now, owner, limit)
}

// claimDueJobsAt separates the scheduler's fixed due/missed-run reference from
// the wall-clock time used to acquire a lease. A serialized drain can spend a
// long time on an earlier job; later jobs must still be evaluated against the
// same due cycle without receiving a lease that has already aged or expired.
func (s *Store) claimDueJobsAt(ctx context.Context, dueNow, leaseNow time.Time, owner string, limit int) ([]Job, error) {
	if limit < 1 {
		limit = 1
	}
	if strings.TrimSpace(owner) == "" {
		return nil, fmt.Errorf("lease owner is required")
	}
	dueNow = dueNow.UTC()
	leaseNow = leaseNow.UTC()
	claimed := make([]Job, 0, limit)
	for len(claimed) < limit {
		var id string
		err := s.db.QueryRowContext(ctx, `SELECT id FROM backup_jobs
			WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ?
			AND (lease_until IS NULL OR lease_until <= ?)
			ORDER BY next_run_at, id LIMIT 1`, formatTime(dueNow), formatTime(leaseNow)).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return claimed, fmt.Errorf("find due backup job: %w", err)
		}
		job, getErr := s.GetJob(ctx, id)
		if getErr != nil {
			return claimed, getErr
		}
		if !job.Schedule.RunMissedOnWake && dueNow.Sub(job.NextRunAt) > missedRunGrace {
			if _, advanceErr := s.advanceMissedJob(ctx, job, dueNow, leaseNow); advanceErr != nil {
				return claimed, advanceErr
			}
			continue
		}
		job, claimErr := s.claimJob(ctx, id, owner, leaseNow)
		if errors.Is(claimErr, ErrJobBusy) {
			continue
		}
		if claimErr != nil {
			return claimed, claimErr
		}
		claimed = append(claimed, job)
	}
	return claimed, nil
}

func (s *Store) advanceMissedJob(ctx context.Context, job Job, dueNow, leaseNow time.Time) (bool, error) {
	previousRunAt := job.NextRunAt.UTC()
	next, ok, err := job.Schedule.AdvancePast(previousRunAt, dueNow)
	if err != nil {
		return false, fmt.Errorf("advance missed backup job %q: %w", job.Name, err)
	}
	if ok {
		job.NextRunAt = next
	} else {
		job.NextRunAt = time.Time{}
	}
	job.UpdatedAt = leaseNow.UTC()
	payload, err := json.Marshal(job)
	if err != nil {
		return false, fmt.Errorf("encode advanced backup job %q: %w", job.Name, err)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE backup_jobs
		SET next_run_at = ?, job_json = ?, updated_at = ?
		WHERE id = ? AND enabled = 1 AND next_run_at = ?
		AND (lease_until IS NULL OR lease_until <= ?)`,
		formatNullableTime(job.NextRunAt), payload, formatTime(job.UpdatedAt), job.ID,
		formatTime(previousRunAt), formatTime(leaseNow))
	if err != nil {
		return false, fmt.Errorf("skip missed backup job %q: %w", job.Name, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("confirm skipped backup job %q: %w", job.Name, err)
	}
	return updated > 0, nil
}

func (s *Store) ClaimJob(ctx context.Context, idOrName, owner string, now time.Time) (Job, error) {
	job, err := s.GetJob(ctx, idOrName)
	if err != nil {
		return Job{}, err
	}
	return s.claimJob(ctx, job.ID, owner, now.UTC())
}

func (s *Store) claimJob(ctx context.Context, id, owner string, now time.Time) (Job, error) {
	job, err := s.GetJob(ctx, id)
	if err != nil {
		return Job{}, err
	}
	leaseFor := time.Duration(job.TimeoutMinutes+10) * time.Minute
	result, err := s.db.ExecContext(ctx, `UPDATE backup_jobs SET lease_owner = ?, lease_until = ?
		WHERE id = ? AND (lease_until IS NULL OR lease_until <= ? OR lease_owner = ?)`,
		owner, formatTime(now.Add(leaseFor)), job.ID, formatTime(now), owner)
	if err != nil {
		return Job{}, fmt.Errorf("claim backup job: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return Job{}, ErrJobBusy
	}
	return job, nil
}

func (s *Store) StartRun(ctx context.Context, jobID string, trigger Trigger, now time.Time) (Run, error) {
	id, err := NewID("run")
	if err != nil {
		return Run{}, err
	}
	run := Run{ID: id, JobID: jobID, Trigger: trigger, Status: RunRunning, StartedAt: now.UTC()}
	payload, _ := json.Marshal(run)
	_, err = s.db.ExecContext(ctx, `INSERT INTO backup_runs(id, job_id, trigger_kind, status, started_at, run_json)
		VALUES (?, ?, ?, ?, ?, ?)`, run.ID, run.JobID, run.Trigger, run.Status, formatTime(run.StartedAt), payload)
	if err != nil {
		return Run{}, fmt.Errorf("start backup run: %w", err)
	}
	return run, nil
}

func (s *Store) FinishRun(ctx context.Context, run *Run, leaseOwner string) error {
	return s.finishRun(ctx, run, leaseOwner, true)
}

// recordRunTerminal durably records backup validity while retaining the
// backup-job lease. The agent uses this boundary so after-success copy dispatch
// and producer retention cannot overlap a second run or a policy edit. SMTP is
// deliberately attempted only after the lease is released.
func (s *Store) recordRunTerminal(ctx context.Context, run *Run, leaseOwner string) error {
	return s.finishRun(ctx, run, leaseOwner, false)
}

func (s *Store) finishRun(ctx context.Context, run *Run, leaseOwner string, releaseLease bool) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("backup store is not open")
	}
	if run == nil {
		return fmt.Errorf("backup run is required")
	}
	leaseOwner = strings.TrimSpace(leaseOwner)
	if leaseOwner == "" {
		return fmt.Errorf("backup job lease owner is required")
	}
	if run.FinishedAt.IsZero() {
		run.FinishedAt = time.Now().UTC()
	} else {
		run.FinishedAt = run.FinishedAt.UTC()
	}
	if run.Status == RunRunning || run.Status == "" {
		if run.Error == "" {
			run.Status = RunSucceeded
		} else {
			run.Status = RunFailed
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("finish backup run: %w", err)
	}
	defer tx.Rollback()

	var payload []byte
	var nextRaw sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT job_json, next_run_at FROM backup_jobs WHERE id = ?`, run.JobID).Scan(&payload, &nextRaw); err != nil {
		return fmt.Errorf("load completed backup job: %w", err)
	}
	var job Job
	if err := json.Unmarshal(payload, &job); err != nil {
		return fmt.Errorf("decode completed backup job: %w", err)
	}
	job.LastRunAt = run.FinishedAt.UTC()
	job.UpdatedAt = run.FinishedAt.UTC()
	if run.Trigger == TriggerScheduled && job.Enabled {
		next, ok, nextErr := job.Schedule.Next(run.FinishedAt)
		if nextErr != nil {
			return nextErr
		}
		if ok {
			job.NextRunAt = next
		} else {
			job.NextRunAt = time.Time{}
		}
	} else if parsed, ok := parseNullableTime(nextRaw); ok {
		job.NextRunAt = parsed
	}
	jobPayload, _ := json.Marshal(job)
	runPayload, _ := json.Marshal(run)
	result, err := tx.ExecContext(ctx, `UPDATE backup_runs SET status = ?, finished_at = ?, run_json = ?
		WHERE id = ? AND job_id = ? AND status = ?`, run.Status, formatTime(run.FinishedAt), runPayload,
		run.ID, run.JobID, RunRunning)
	if err != nil {
		return fmt.Errorf("update backup run: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm backup run update: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("backup run %q is not running", run.ID)
	}
	jobUpdate := `UPDATE backup_jobs SET job_json = ?, next_run_at = ?, updated_at = ?
		WHERE id = ? AND lease_owner = ?`
	if releaseLease {
		jobUpdate = `UPDATE backup_jobs SET lease_owner = NULL, lease_until = NULL,
			job_json = ?, next_run_at = ?, updated_at = ? WHERE id = ? AND lease_owner = ?`
	}
	result, err = tx.ExecContext(ctx, jobUpdate,
		jobPayload, formatNullableTime(job.NextRunAt), formatTime(job.UpdatedAt), job.ID, leaseOwner)
	if err != nil {
		return fmt.Errorf("update completed backup job: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm completed backup job update: %w", err)
	}
	if count != 1 {
		return ErrJobLeaseLost
	}
	return tx.Commit()
}

// ReconcileStaleRuns closes history entries abandoned by a crashed process.
// A running entry is stale only when its job no longer has an unexpired lease;
// the conditional update repeats that check inside the transaction so an
// actively leased run is never rewritten.
func (s *Store) ReconcileStaleRuns(ctx context.Context, now time.Time) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("backup store is not open")
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin stale backup recovery: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT r.id, r.job_id, r.run_json
		FROM backup_runs r
		WHERE r.status = ? AND NOT EXISTS (
			SELECT 1 FROM backup_jobs j
			WHERE j.id = r.job_id AND j.lease_until IS NOT NULL AND j.lease_until > ?
		)
		ORDER BY r.started_at, r.id`, RunRunning, formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("find stale backup runs: %w", err)
	}
	var staleRuns []Run
	for rows.Next() {
		var runID string
		var jobID string
		var payload []byte
		if err := rows.Scan(&runID, &jobID, &payload); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("read stale backup run: %w", err)
		}
		var run Run
		if err := json.Unmarshal(payload, &run); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("decode stale backup run: %w", err)
		}
		// Indexed columns are authoritative if an older or partially written
		// JSON payload omitted identifiers.
		run.ID = runID
		run.JobID = jobID
		staleRuns = append(staleRuns, run)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close stale backup scan: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("scan stale backup runs: %w", err)
	}

	reconciled := 0
	for _, run := range staleRuns {
		run.Status = RunFailed
		run.FinishedAt = now
		run.Error = staleRunRecoveryError
		payload, err := json.Marshal(run)
		if err != nil {
			return 0, fmt.Errorf("encode recovered backup run %q: %w", run.ID, err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE backup_runs
			SET status = ?, finished_at = ?, run_json = ?
			WHERE id = ? AND status = ? AND NOT EXISTS (
				SELECT 1 FROM backup_jobs j
				WHERE j.id = backup_runs.job_id AND j.lease_until IS NOT NULL AND j.lease_until > ?
			)`, RunFailed, formatTime(now), payload, run.ID, RunRunning, formatTime(now))
		if err != nil {
			return 0, fmt.Errorf("recover stale backup run %q: %w", run.ID, err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("confirm stale backup recovery %q: %w", run.ID, err)
		}
		if updated == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE backup_jobs
			SET lease_owner = NULL, lease_until = NULL
			WHERE id = ? AND (lease_until IS NULL OR lease_until <= ?)`, run.JobID, formatTime(now)); err != nil {
			return 0, fmt.Errorf("release expired lease for recovered run %q: %w", run.ID, err)
		}
		reconciled++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit stale backup recovery: %w", err)
	}
	return reconciled, nil
}

func (s *Store) ReleaseJob(ctx context.Context, jobID, leaseOwner string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("backup store is not open")
	}
	jobID = strings.TrimSpace(jobID)
	leaseOwner = strings.TrimSpace(leaseOwner)
	if jobID == "" || leaseOwner == "" {
		return fmt.Errorf("backup job ID and lease owner are required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE backup_jobs SET lease_owner = NULL, lease_until = NULL
		WHERE id = ? AND lease_owner = ?`, jobID, leaseOwner)
	if err != nil {
		return fmt.Errorf("release backup job: %w", err)
	}
	released, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm backup job release: %w", err)
	}
	if released != 1 {
		return ErrJobLeaseLost
	}
	return nil
}

// RenewJobLease extends only the current owner's still-live lease. It never
// revives an expired lease because another process may already be entitled to
// claim the job and begin a new backup.
func (s *Store) RenewJobLease(ctx context.Context, jobID, leaseOwner string, now, leaseUntil time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("backup store is not open")
	}
	jobID = strings.TrimSpace(jobID)
	leaseOwner = strings.TrimSpace(leaseOwner)
	if jobID == "" || leaseOwner == "" {
		return fmt.Errorf("backup job ID and lease owner are required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	leaseUntil = leaseUntil.UTC()
	if leaseUntil.IsZero() || !leaseUntil.After(now) {
		return fmt.Errorf("renewed backup job lease must expire after the renewal time")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE backup_jobs SET lease_until = ?
		WHERE id = ? AND lease_owner = ? AND lease_until IS NOT NULL AND lease_until > ?`,
		formatTime(leaseUntil), jobID, leaseOwner, formatTime(now))
	if err != nil {
		return fmt.Errorf("renew backup job lease: %w", err)
	}
	renewed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm backup job lease renewal: %w", err)
	}
	if renewed != 1 {
		return ErrJobLeaseLost
	}
	return nil
}

func (s *Store) ListRuns(ctx context.Context, jobID string, limit int) ([]Run, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	query := `SELECT run_json FROM backup_runs`
	args := []any{}
	if strings.TrimSpace(jobID) != "" {
		query += ` WHERE job_id = ?`
		args = append(args, strings.TrimSpace(jobID))
	}
	query += ` ORDER BY started_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list backup runs: %w", err)
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var run Run
		if err := json.Unmarshal(payload, &run); err != nil {
			return nil, fmt.Errorf("decode backup run: %w", err)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// listSuccessfulUnprunedRuns returns the complete retention working set. It is
// deliberately separate from the bounded history-view API: applying a LIMIT
// before filtering pruned artifacts can strand old files forever.
func (s *Store) listSuccessfulUnprunedRuns(ctx context.Context, jobID string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_json FROM backup_runs
		WHERE job_id = ? AND status = ? ORDER BY started_at DESC`, strings.TrimSpace(jobID), RunSucceeded)
	if err != nil {
		return nil, fmt.Errorf("list successful backup artifacts for retention: %w", err)
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("read successful backup artifact for retention: %w", err)
		}
		var run Run
		if err := json.Unmarshal(payload, &run); err != nil {
			return nil, fmt.Errorf("decode successful backup artifact for retention: %w", err)
		}
		if run.Status == RunSucceeded && strings.TrimSpace(run.Artifact.Path) != "" && run.Artifact.PrunedAt.IsZero() {
			runs = append(runs, run)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan successful backup artifacts for retention: %w", err)
	}
	return runs, nil
}

// ListLatestVerifiedUnprunedRuns returns at most one retained recovery point
// per job without applying the bounded activity-history limit. UI protection
// summaries must not forget an inactive job merely because other jobs produced
// more recent run rows.
func (s *Store) ListLatestVerifiedUnprunedRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_json FROM backup_runs
		WHERE status = ? ORDER BY finished_at DESC, started_at DESC`, RunSucceeded)
	if err != nil {
		return nil, fmt.Errorf("list latest successful backup artifacts: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{})
	var runs []Run
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("read latest successful backup artifact: %w", err)
		}
		var run Run
		if err := json.Unmarshal(payload, &run); err != nil {
			return nil, fmt.Errorf("decode latest successful backup artifact: %w", err)
		}
		if run.Status != RunSucceeded || !run.Artifact.Verified || strings.TrimSpace(run.Artifact.Path) == "" || !run.Artifact.PrunedAt.IsZero() {
			continue
		}
		if run.Artifact.PublicationState != "" && run.Artifact.PublicationState != ArtifactPublicationComplete {
			continue
		}
		if _, exists := seen[run.JobID]; exists {
			continue
		}
		seen[run.JobID] = struct{}{}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan latest successful backup artifacts: %w", err)
	}
	return runs, nil
}

func (s *Store) LatestRun(ctx context.Context, jobID string) (Run, bool, error) {
	runs, err := s.ListRuns(ctx, jobID, 1)
	if err != nil || len(runs) == 0 {
		return Run{}, false, err
	}
	return runs[0], true, nil
}

func (s *Store) recordRunRetentionOutcome(ctx context.Context, runID, retentionError string) error {
	return s.updateTerminalRunJSON(ctx, runID, "retention outcome", func(run *Run) error {
		run.RetentionError = boundedNotificationError(retentionError)
		return nil
	})
}

// recordRunNotification updates only notification fields in the existing run
// JSON. It intentionally leaves the indexed terminal status, timestamps, job
// schedule, and lease untouched because SMTP is always attempted after the
// backup transaction has committed.
func (s *Store) recordRunNotification(ctx context.Context, runID string, attempted, sent bool, notificationError string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("backup run ID is required")
	}
	if sent && !attempted {
		return fmt.Errorf("a notification cannot be sent without being attempted")
	}
	return s.updateTerminalRunJSON(ctx, runID, "notification outcome", func(run *Run) error {
		run.NotificationAttempted = attempted
		run.NotificationSent = sent
		run.NotificationError = boundedNotificationError(notificationError)
		if sent {
			run.NotificationError = ""
		}
		return nil
	})
}

// updateTerminalRunJSON applies a narrow read-modify-write patch with an
// optimistic comparison against the exact JSON bytes that were read. This
// preserves independent retention and notification fields even when multiple
// readers update terminal history concurrently.
func (s *Store) updateTerminalRunJSON(ctx context.Context, runID, action string, mutate func(*Run) error) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("backup store is not open")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("backup run ID is required")
	}
	if mutate == nil {
		return fmt.Errorf("backup run %s update is required", action)
	}
	const maximumAttempts = 16
	for attempt := 0; attempt < maximumAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("record backup run %s: %w", action, err)
		}
		var original []byte
		var indexedStatus RunStatus
		if err := s.db.QueryRowContext(ctx, `SELECT status, run_json FROM backup_runs WHERE id = ?`, runID).Scan(&indexedStatus, &original); err != nil {
			return fmt.Errorf("load backup run %s: %w", action, err)
		}
		if indexedStatus == RunRunning || indexedStatus == "" {
			return fmt.Errorf("cannot record %s for a non-terminal backup run", action)
		}
		var run Run
		if err := json.Unmarshal(original, &run); err != nil {
			return fmt.Errorf("decode backup run %s: %w", action, err)
		}
		if err := mutate(&run); err != nil {
			return err
		}
		updatedPayload, err := json.Marshal(run)
		if err != nil {
			return fmt.Errorf("encode backup run %s: %w", action, err)
		}
		result, err := s.db.ExecContext(ctx, `UPDATE backup_runs SET run_json = ? WHERE id = ? AND status = ? AND run_json = ?`,
			updatedPayload, runID, indexedStatus, original)
		if err != nil {
			return fmt.Errorf("record backup run %s: %w", action, err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("confirm backup run %s: %w", action, err)
		}
		if updated == 1 {
			return nil
		}
	}
	return fmt.Errorf("backup run changed repeatedly while recording %s", action)
}

func boundedNotificationError(message string) string {
	const maximumBytes = 4096
	message = strings.TrimSpace(message)
	if len(message) <= maximumBytes {
		return message
	}
	message = message[:maximumBytes]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return strings.TrimSpace(message) + "…"
}

func (s *Store) MarkArtifactPruned(ctx context.Context, runID, reason string, at time.Time) error {
	return s.updateTerminalRunJSON(ctx, runID, "pruned artifact", func(run *Run) error {
		run.Artifact.PrunedAt = at.UTC()
		run.Artifact.PruneReason = strings.TrimSpace(reason)
		return nil
	})
}

func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("backup metadata key is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO backup_meta(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("save backup metadata: %w", err)
	}
	return nil
}

func (s *Store) GetMeta(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM backup_meta WHERE key = ?`, strings.TrimSpace(key)).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read backup metadata: %w", err)
	}
	return value, true, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner) (Job, error) {
	var payload []byte
	var nextRaw sql.NullString
	if err := row.Scan(&payload, &nextRaw); err != nil {
		return Job{}, err
	}
	var job Job
	if err := json.Unmarshal(payload, &job); err != nil {
		return Job{}, fmt.Errorf("decode backup job: %w", err)
	}
	if parsed, ok := parseNullableTime(nextRaw); ok {
		job.NextRunAt = parsed
	} else {
		job.NextRunAt = time.Time{}
	}
	return job, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func formatNullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatTime(value)
}

func parseNullableTime(value sql.NullString) (time.Time, bool) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	return parsed, err == nil
}
