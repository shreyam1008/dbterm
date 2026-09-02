package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type copyRetentionEntry struct {
	RunID      string
	JobID      string
	FinishedAt time.Time
	Index      int
	Artifact   CopyArtifactResult
}

// listOwnedUnprunedCopyArtifacts deliberately has no UI-style LIMIT:
// retention must see every completed artifact owned by this exact copy job.
// A batch can fail after publishing one or more individually verified copies,
// so ownership is determined by the artifact publication state, not by the
// aggregate CopyRun status.
func (store *Store) listOwnedUnprunedCopyArtifacts(ctx context.Context, jobID string) ([]copyRetentionEntry, error) {
	if store == nil || store.db == nil {
		return nil, fmt.Errorf("backup store is not open")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("copy job ID is required")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id, status, finished_at, run_json FROM copy_runs
		WHERE job_id = ? AND status <> ? ORDER BY finished_at DESC, started_at DESC, id DESC`, jobID, RunRunning)
	if err != nil {
		return nil, fmt.Errorf("list owned copy artifacts for retention: %w", err)
	}
	defer rows.Close()
	var entries []copyRetentionEntry
	for rows.Next() {
		var runID string
		var indexedStatus RunStatus
		var finishedRaw string
		var payload []byte
		if err := rows.Scan(&runID, &indexedStatus, &finishedRaw, &payload); err != nil {
			return nil, fmt.Errorf("read owned copy artifact for retention: %w", err)
		}
		finishedAt, err := time.Parse(time.RFC3339Nano, finishedRaw)
		if err != nil {
			return nil, fmt.Errorf("decode owned copy finish time: %w", err)
		}
		var run CopyRun
		if err := json.Unmarshal(payload, &run); err != nil {
			return nil, fmt.Errorf("decode owned copy run for retention: %w", err)
		}
		if run.ID != runID || run.Status != indexedStatus || run.Status == RunRunning || run.JobID != jobID {
			return nil, fmt.Errorf("copy run %s ownership metadata does not match its catalog index", runID)
		}
		for index, artifact := range run.Artifacts {
			if artifact.PublicationState != ArtifactPublicationComplete || !artifact.PrunedAt.IsZero() || strings.TrimSpace(artifact.Destination) == "" {
				continue
			}
			entries = append(entries, copyRetentionEntry{
				RunID: runID, JobID: jobID, FinishedAt: finishedAt.UTC(), Index: index, Artifact: artifact,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan owned copy artifacts for retention: %w", err)
	}
	return entries, nil
}

func (store *Store) MarkCopyArtifactPruned(ctx context.Context, runID, artifactID, destination, reason string, at time.Time) error {
	artifactID = strings.TrimSpace(artifactID)
	destination = strings.TrimSpace(destination)
	reason = strings.TrimSpace(reason)
	if artifactID == "" || destination == "" || reason == "" {
		return fmt.Errorf("copy artifact ID, destination, and prune reason are required")
	}
	if at.IsZero() {
		at = time.Now()
	}
	return store.updateTerminalCopyRunJSON(ctx, runID, "pruned copy artifact", func(run *CopyRun) error {
		match := -1
		for index := range run.Artifacts {
			artifact := run.Artifacts[index]
			if artifact.ArtifactID == artifactID && artifact.Destination == destination {
				if match >= 0 {
					return fmt.Errorf("copy run contains duplicate artifact identity %q at %s", artifactID, destination)
				}
				match = index
			}
		}
		if match < 0 {
			return fmt.Errorf("copy artifact %q at %s was not found in run %s", artifactID, destination, run.ID)
		}
		run.Artifacts[match].PrunedAt = at.UTC()
		run.Artifacts[match].PruneReason = reason
		return nil
	})
}

func (store *Store) recordCopyRunRetentionOutcome(ctx context.Context, runID, retentionError string) error {
	return store.updateTerminalCopyRunJSON(ctx, runID, "copy retention outcome", func(run *CopyRun) error {
		run.RetentionError = boundedNotificationError(retentionError)
		return nil
	})
}

func (store *Store) recordCopyRunNotification(ctx context.Context, runID string, attempted, sent bool, notificationError string) error {
	return store.updateTerminalCopyRunJSON(ctx, runID, "copy notification outcome", func(run *CopyRun) error {
		run.NotificationAttempted = attempted
		run.NotificationSent = sent
		run.NotificationError = boundedNotificationError(notificationError)
		return nil
	})
}

// updateTerminalCopyRunJSON merges post-run facts without mutating indexed
// status/timestamps or losing another concurrent post-run update.
func (store *Store) updateTerminalCopyRunJSON(ctx context.Context, runID, action string, mutate func(*CopyRun) error) error {
	if store == nil || store.db == nil {
		return fmt.Errorf("backup store is not open")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("copy run ID is required")
	}
	if mutate == nil {
		return fmt.Errorf("copy run update is required")
	}
	for attempt := 0; attempt < 8; attempt++ {
		var original []byte
		var status RunStatus
		if err := store.db.QueryRowContext(ctx, `SELECT run_json, status FROM copy_runs WHERE id = ?`, runID).Scan(&original, &status); err != nil {
			return fmt.Errorf("load copy run for %s: %w", action, err)
		}
		if status == RunRunning {
			return fmt.Errorf("cannot record %s while copy run %q is still running", action, runID)
		}
		var run CopyRun
		if err := json.Unmarshal(original, &run); err != nil {
			return fmt.Errorf("decode copy run for %s: %w", action, err)
		}
		if err := mutate(&run); err != nil {
			return err
		}
		updatedPayload, err := json.Marshal(run)
		if err != nil {
			return fmt.Errorf("encode copy run for %s: %w", action, err)
		}
		result, err := store.db.ExecContext(ctx, `UPDATE copy_runs SET run_json = ? WHERE id = ? AND status = ? AND run_json = ?`, updatedPayload, runID, status, original)
		if err != nil {
			return fmt.Errorf("record %s: %w", action, err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("confirm %s: %w", action, err)
		}
		if changed == 1 {
			return nil
		}
	}
	return fmt.Errorf("record %s: copy run changed repeatedly; retry", action)
}
