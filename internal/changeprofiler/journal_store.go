package changeprofiler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Store) saveJournal(ctx context.Context, id string, spec JournalSpec, pending bool, lastError string) error {
	payload, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO profiler_journal_sessions(anchor_id,spec_json,cleanup_pending,last_error)
		VALUES(?,?,?,?) ON CONFLICT(anchor_id) DO UPDATE SET spec_json=excluded.spec_json,cleanup_pending=excluded.cleanup_pending,last_error=excluded.last_error`, id, payload, pending, lastError)
	return err
}

func (s *Store) loadJournal(ctx context.Context, id string) (JournalSpec, bool, error) {
	var payload []byte
	var pending bool
	if err := s.db.QueryRowContext(ctx, `SELECT spec_json,cleanup_pending FROM profiler_journal_sessions WHERE anchor_id=?`, id).Scan(&payload, &pending); err != nil {
		return JournalSpec{}, false, err
	}
	var spec JournalSpec
	if err := json.Unmarshal(payload, &spec); err != nil {
		return spec, pending, fmt.Errorf("read saved source-journal metadata: %w", err)
	}
	if spec.ID != id || spec.Version != 1 {
		return spec, pending, fmt.Errorf("checkpoint journal identity or version does not match")
	}
	return spec, pending, nil
}

func (s *Store) startJournal(ctx context.Context, source *sql.DB, request StartRequest, progress ProgressFunc) (_ Anchor, resultErr error) {
	release, err := s.lockJournalOperation()
	if err != nil {
		return Anchor{}, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return Anchor{}, err
	}
	if !request.AllowSourceObjects {
		return Anchor{}, fmt.Errorf("incremental capture installs source journal objects; review and explicitly allow this before starting")
	}
	if !SupportsJournal(request.Engine) {
		return Anchor{}, fmt.Errorf("%s does not expose a supported incremental capture route; choose snapshot comparison explicitly", request.Engine)
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || strings.TrimSpace(request.ConnectionKey) == "" {
		return Anchor{}, fmt.Errorf("checkpoint name and connection identity are required")
	}
	var pending int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM profiler_journal_sessions j JOIN profiler_anchors a ON a.id=j.anchor_id
		WHERE a.connection_key=? AND j.cleanup_pending=1`, request.ConnectionKey).Scan(&pending); err != nil {
		return Anchor{}, err
	}
	if pending > 0 {
		return Anchor{}, fmt.Errorf("this connection already owns a checkpoint journal; stop or remove it before starting another")
	}
	plans := make([]TablePlan, 0, len(request.Tables))
	for _, reviewed := range request.Tables {
		if !reviewed.Included {
			continue
		}
		plan, err := inspectTable(ctx, source, request.Engine, reviewed.Name)
		if err != nil {
			return Anchor{}, err
		}
		if plan.KeyKind == KeyFullRow || len(plan.KeyColumns) == 0 {
			return Anchor{}, fmt.Errorf("table %q needs a stable row identity for incremental capture; choose a keyed scope or snapshot comparison", plan.Name)
		}
		plan.Included = true
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		return Anchor{}, fmt.Errorf("select at least one table")
	}
	id, err := newID()
	if err != nil {
		return Anchor{}, err
	}
	anchor := Anchor{ID: id, Name: request.Name, ConnectionKey: request.ConnectionKey, ConnectionLabel: request.ConnectionLabel,
		Engine: request.Engine, TargetLabel: request.TargetLabel, Status: StatusCapturing, Consistency: ConsistencyJournal,
		CaptureMode: CaptureJournal, CleanupPending: true, StartedAt: time.Now().UTC()}
	if err := s.insertAnchor(ctx, anchor, plans); err != nil {
		return Anchor{}, err
	}
	spec := JournalSpec{Version: 1, ID: id, Engine: request.Engine, Plans: plans}
	if err := s.saveJournal(ctx, id, spec, true, "installation has not completed"); err != nil {
		_, _ = s.db.ExecContext(context.Background(), `UPDATE profiler_anchors SET status='failed',error=? WHERE id=?`, err.Error(), id)
		return Anchor{}, err
	}
	// Adapters call this before their first source-side DDL, and again when
	// final object identities become available inside the install transaction.
	installCtx := context.WithValue(ctx, journalRecorderKey{}, func(planned JournalSpec) error {
		planned.Engine = request.Engine
		spec = planned
		return s.saveJournal(ctx, id, planned, true, "installation in progress")
	})
	if progress != nil {
		progress(Progress{Phase: "arming change journal", TableCount: len(plans)})
	}
	installed, installErr := installJournal(installCtx, source, id, request.Engine, plans)
	if installed.ID != "" && len(installed.State) > 0 {
		spec = installed
		spec.Engine = request.Engine
	}
	if installErr == nil {
		installErr = s.saveJournal(ctx, id, spec, true, "")
	}
	if installErr != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cleanupErr := error(nil)
		if len(spec.State) > 0 {
			cleanupErr = cleanupJournal(cleanupCtx, source, spec)
		}
		pending := cleanupErr != nil
		message := errors.Join(installErr, cleanupErr).Error()
		_ = s.saveJournal(cleanupCtx, id, spec, pending, message)
		_, _ = s.db.ExecContext(cleanupCtx, `UPDATE profiler_anchors SET status='failed',error=? WHERE id=?`, message, id)
		return Anchor{}, fmt.Errorf("could not arm checkpoint: %s", message)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE profiler_anchors SET status='active',error='' WHERE id=? AND status='capturing'`, id); err != nil {
		return Anchor{}, fmt.Errorf("journal is installed; reconnect and remove or stop checkpoint %q: %w", anchor.Name, err)
	}
	anchor.Status = StatusActive
	if progress != nil {
		progress(Progress{Phase: "recording committed changes", TableCount: len(plans), Percent: 100, Complete: true})
	}
	return anchor, nil
}

func (s *Store) scanJournal(ctx context.Context, source *sql.DB, anchor Anchor, finish bool, progress ProgressFunc) (_ Anchor, resultErr error) {
	release, err := s.lockJournalOperation()
	if err != nil {
		return Anchor{}, err
	}
	defer release()
	var reloadErr error
	anchor, reloadErr = s.GetAnchor(ctx, anchor.ID)
	if reloadErr != nil {
		return Anchor{}, reloadErr
	}
	if !finish {
		return Anchor{}, fmt.Errorf("this checkpoint records changes continuously; use Finish (F) to stop capture and build its final diff without a full-table scan")
	}
	if source == nil {
		return Anchor{}, fmt.Errorf("connect to the original database to stop this checkpoint")
	}
	spec, pending, err := s.loadJournal(ctx, anchor.ID)
	if err != nil {
		return Anchor{}, err
	}
	if anchor.Status == StatusComplete && !pending {
		return anchor, nil
	}
	if !pending {
		return Anchor{}, fmt.Errorf("this checkpoint no longer owns an active source journal")
	}
	defer func() {
		if resultErr != nil {
			// Preserve the last report and durable journal for retry or explicit
			// cleanup. A frozen journal must never restart recording on retry.
			recoveryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.saveJournal(recoveryCtx, anchor.ID, spec, true, resultErr.Error())
			_, _ = s.db.ExecContext(recoveryCtx, `UPDATE profiler_anchors SET error=? WHERE id=?`, resultErr.Error(), anchor.ID)
		}
	}()
	if !spec.Frozen {
		if progress != nil {
			progress(Progress{Phase: "stopping change capture", TableCount: len(spec.Plans)})
		}
		frozen, err := freezeJournal(ctx, source, spec)
		if err != nil {
			return Anchor{}, fmt.Errorf("cannot prove a complete checkpoint; journal retained for recovery: %w", err)
		}
		spec = frozen
		if err := s.saveJournal(ctx, anchor.ID, spec, true, ""); err != nil {
			return Anchor{}, err
		}
	}
	if anchor.Status != StatusComplete {
		if err := s.materializeJournalReport(ctx, source, anchor, spec, progress); err != nil {
			return Anchor{}, err
		}
	}
	// Source cleanup happens after the local report commits. A failed cleanup
	// is visible and retryable; it cannot discard the completed report.
	if err := cleanupJournal(ctx, source, spec); err != nil {
		return Anchor{}, fmt.Errorf("diff saved; source journal cleanup needs retry: %w", err)
	}
	if err := s.saveJournal(ctx, anchor.ID, spec, false, ""); err != nil {
		return Anchor{}, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE profiler_anchors SET error='' WHERE id=?`, anchor.ID)
	if err != nil {
		return Anchor{}, err
	}
	return s.GetAnchor(ctx, anchor.ID)
}

func (s *Store) materializeJournalReport(ctx context.Context, source *sql.DB, anchor Anchor, spec JournalSpec, progress ProgressFunc) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM profiler_diff_rows WHERE anchor_id=?`, anchor.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM profiler_schema_events WHERE anchor_id=?`, anchor.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE profiler_tables SET inserted_count=0,updated_count=0,deleted_count=0,schema_changed=0 WHERE anchor_id=?`, anchor.ID); err != nil {
		return err
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO profiler_diff_rows(anchor_id,table_name,key_hash,key_blob,kind,before_blob,after_blob,changed_columns_json) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer statement.Close()
	var inserted, updated, deleted, events int64
	read := func(emit func(JournalEvent) error) error {
		return readJournal(ctx, source, spec, func(event JournalEvent) error {
			events++
			if progress != nil && (events == 1 || events%500 == 0) {
				progress(Progress{Phase: "reducing committed changes", Table: event.Table, Rows: events})
			}
			return emit(event)
		})
	}
	err = reduceJournal(ctx, spec.Plans, read, func(diff JournalDiff) error {
		var before, after []byte
		var err error
		if len(diff.Before) > 0 {
			before, err = packRow(diff.Before)
			if err != nil {
				return err
			}
		}
		if len(diff.After) > 0 {
			after, err = packRow(diff.After)
			if err != nil {
				return err
			}
		}
		changed, err := json.Marshal(diff.ChangedColumns)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(diff.Key)
		if _, err := statement.ExecContext(ctx, anchor.ID, diff.Table, hash[:], diff.Key, diff.Kind, before, after, changed); err != nil {
			return err
		}
		switch diff.Kind {
		case DiffInserted:
			inserted++
		case DiffUpdated:
			updated++
		case DiffDeleted:
			deleted++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reduce checkpoint journal: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE profiler_tables SET
		inserted_count=(SELECT COUNT(*) FROM profiler_diff_rows d WHERE d.anchor_id=profiler_tables.anchor_id AND d.table_name=profiler_tables.table_name AND d.kind='inserted'),
		updated_count=(SELECT COUNT(*) FROM profiler_diff_rows d WHERE d.anchor_id=profiler_tables.anchor_id AND d.table_name=profiler_tables.table_name AND d.kind='updated'),
		deleted_count=(SELECT COUNT(*) FROM profiler_diff_rows d WHERE d.anchor_id=profiler_tables.anchor_id AND d.table_name=profiler_tables.table_name AND d.kind='deleted') WHERE anchor_id=?`, anchor.ID); err != nil {
		return err
	}
	now := formatTime(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `UPDATE profiler_anchors SET status='complete',last_scanned_at=?,finished_at=?,inserted_count=?,updated_count=?,deleted_count=?,schema_count=0,error='' WHERE id=?`, now, now, inserted, updated, deleted, anchor.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if progress != nil {
		progress(Progress{Phase: "checkpoint diff saved", Rows: events, Percent: 100, Complete: true})
	}
	return nil
}

// RemoveJournal explicitly discards source-side recording objects without
// deleting the local report. It also recovers abandoned/failed installations.
func (s *Store) RemoveJournal(ctx context.Context, source *sql.DB, id string) error {
	release, err := s.lockJournalOperation()
	if err != nil {
		return err
	}
	defer release()
	if source == nil {
		return fmt.Errorf("connect to the checkpoint's original database before removing its journal")
	}
	spec, pending, err := s.loadJournal(ctx, id)
	if err != nil || !pending {
		return err
	}
	if err := cleanupJournal(ctx, source, spec); err != nil {
		_ = s.saveJournal(ctx, id, spec, true, err.Error())
		return err
	}
	if err := s.saveJournal(ctx, id, spec, false, ""); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE profiler_anchors SET status=CASE WHEN status='complete' THEN status ELSE 'failed' END,error='Source journal removed; capture stopped without a new report' WHERE id=?`, id)
	return err
}
