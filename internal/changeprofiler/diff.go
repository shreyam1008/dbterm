package changeprofiler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (s *Store) Scan(ctx context.Context, source *sql.DB, anchorID string, finish bool, progress ProgressFunc) (_ Anchor, returnErr error) {
	anchor, err := s.GetAnchor(ctx, anchorID)
	if err != nil {
		return Anchor{}, err
	}
	if anchor.Status != StatusActive {
		return Anchor{}, fmt.Errorf("anchor %q is %s, not active", anchor.Name, anchor.Status)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE profiler_anchors SET status = 'comparing', error = '' WHERE id = ? AND status = 'active'`, anchorID); err != nil {
		return Anchor{}, err
	}
	restoreActive := func(scanErr error) {
		_, _ = s.db.ExecContext(context.Background(), `UPDATE profiler_anchors SET status = 'active', error = ? WHERE id = ? AND status = 'comparing'`, scanErr.Error(), anchorID)
	}
	// Registered before the source/local transaction defers, so rollback and
	// connection release always happen before status recovery uses the store.
	defer func() {
		if returnErr != nil {
			restoreActive(returnErr)
		}
	}()

	queryer, closeSnapshot, err := beginSnapshot(ctx, source, anchor.Engine)
	if err != nil {
		return Anchor{}, err
	}
	defer closeSnapshot()
	currentPlans, err := inspectAllTablesWithProgress(ctx, queryer, anchor.Engine, progress)
	if err != nil {
		return Anchor{}, err
	}
	baselinePlans, err := s.loadPlans(ctx, anchorID)
	if err != nil {
		return Anchor{}, err
	}
	currentByName := make(map[string]TablePlan, len(currentPlans))
	for _, plan := range currentPlans {
		currentByName[plan.Name] = plan
	}
	excluded, err := s.excludedTables(ctx, anchorID)
	if err != nil {
		return Anchor{}, err
	}
	for _, table := range excluded {
		delete(currentByName, table)
	}
	baselineNames := make(map[string]bool, len(baselinePlans))
	workPlans := append([]TablePlan(nil), baselinePlans...)
	for _, plan := range baselinePlans {
		baselineNames[plan.Name] = true
	}
	var addedPlans []TablePlan
	for name, plan := range currentByName {
		if !baselineNames[name] {
			addedPlans = append(addedPlans, plan)
		}
	}
	sort.Slice(addedPlans, func(i, j int) bool { return addedPlans[i].Name < addedPlans[j].Name })
	workPlans = append(workPlans, addedPlans...)
	meter := newProgressMeter(workPlans, len(addedPlans) == 0)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Anchor{}, err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`DELETE FROM profiler_seen_rows WHERE anchor_id = ?`,
		`DELETE FROM profiler_diff_rows WHERE anchor_id = ?`,
		`DELETE FROM profiler_schema_events WHERE anchor_id = ?`,
		`UPDATE profiler_tables SET inserted_count=0, updated_count=0, deleted_count=0, schema_changed=0 WHERE anchor_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, anchorID); err != nil {
			return Anchor{}, err
		}
	}

	for _, baseline := range baselinePlans {
		tableProgress := ProgressFunc(nil)
		if progress != nil {
			progress(meter.decorate(Progress{Phase: "comparing", Table: baseline.Name}, false))
			tableProgress = func(event Progress) { progress(meter.decorate(event, event.Complete)) }
		}
		current, exists := currentByName[baseline.Name]
		if !exists {
			if err := recordSchemaEvent(ctx, tx, anchorID, baseline.Name, "table_dropped", "Table was removed after the anchor"); err != nil {
				return Anchor{}, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO profiler_diff_rows
				(anchor_id,table_name,key_hash,key_blob,kind,before_blob,after_blob,changed_columns_json)
				SELECT anchor_id,table_name,key_hash,key_blob,'deleted',row_blob,NULL,'[]'
				FROM profiler_baseline_rows WHERE anchor_id=? AND table_name=?`, anchorID, baseline.Name); err != nil {
				return Anchor{}, err
			}
			if progress != nil {
				progress(meter.decorate(Progress{Phase: "comparing", Table: baseline.Name, Rows: baseline.EstimatedRows, Complete: true}, true))
			}
			continue
		}
		delete(currentByName, baseline.Name)
		_, currentHash, _ := tableSchemaPayload(current)
		_, baselineHash, _ := tableSchemaPayload(baseline)
		schemaChanged := !equalBytes(currentHash, baselineHash)
		if schemaChanged {
			if err := recordSchemaEvent(ctx, tx, anchorID, baseline.Name, "table_structure_changed", schemaChangeDescription(baseline, current)); err != nil {
				return Anchor{}, err
			}
		}
		canPair := sameKey(baseline, current) && columnsContain(current.Columns, baseline.KeyColumns)
		if !canPair {
			if _, err := tx.ExecContext(ctx, `INSERT INTO profiler_diff_rows
				(anchor_id,table_name,key_hash,key_blob,kind,before_blob,after_blob,changed_columns_json)
				SELECT anchor_id,table_name,key_hash,key_blob,'deleted',row_blob,NULL,'[]'
				FROM profiler_baseline_rows WHERE anchor_id=? AND table_name=?`, anchorID, baseline.Name); err != nil {
				return Anchor{}, err
			}
			if err := scanCurrentTable(ctx, queryer, tx, anchor, current, false, tableProgress); err != nil {
				return Anchor{}, err
			}
			continue
		}
		// Preserve the baseline identity while selecting current columns. This
		// pairs rows across ordinary column additions/removals without guessing.
		current.KeyColumns, current.KeyKind = baseline.KeyColumns, baseline.KeyKind
		if err := scanCurrentTable(ctx, queryer, tx, anchor, current, true, tableProgress); err != nil {
			return Anchor{}, fmt.Errorf("compare %s: %w", baseline.Name, err)
		}
	}

	for _, current := range addedPlans {
		columns, _ := json.Marshal(current.Columns)
		keys, _ := json.Marshal(current.KeyColumns)
		_, schemaHash, _ := tableSchemaPayload(current)
		if _, err := tx.ExecContext(ctx, `INSERT INTO profiler_tables
			(anchor_id,table_name,columns_json,key_columns_json,key_kind,schema_hash,included,schema_changed)
			VALUES(?,?,?,?,?,?,1,1)`, anchorID, current.Name, columns, keys, current.KeyKind, schemaHash); err != nil {
			return Anchor{}, err
		}
		if err := recordSchemaEvent(ctx, tx, anchorID, current.Name, "table_created", "Table was created after the anchor"); err != nil {
			return Anchor{}, err
		}
		tableProgress := ProgressFunc(nil)
		if progress != nil {
			progress(meter.decorate(Progress{Phase: "comparing", Table: current.Name}, false))
			tableProgress = func(event Progress) { progress(meter.decorate(event, event.Complete)) }
		}
		if err := scanCurrentTable(ctx, queryer, tx, anchor, current, false, tableProgress); err != nil {
			return Anchor{}, err
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE profiler_tables SET
		inserted_count=(SELECT COUNT(*) FROM profiler_diff_rows d WHERE d.anchor_id=profiler_tables.anchor_id AND d.table_name=profiler_tables.table_name AND d.kind='inserted'),
		updated_count=(SELECT COUNT(*) FROM profiler_diff_rows d WHERE d.anchor_id=profiler_tables.anchor_id AND d.table_name=profiler_tables.table_name AND d.kind='updated'),
		deleted_count=(SELECT COUNT(*) FROM profiler_diff_rows d WHERE d.anchor_id=profiler_tables.anchor_id AND d.table_name=profiler_tables.table_name AND d.kind='deleted'),
		schema_changed=CASE WHEN EXISTS(SELECT 1 FROM profiler_schema_events e WHERE e.anchor_id=profiler_tables.anchor_id AND e.table_name=profiler_tables.table_name) THEN 1 ELSE 0 END
		WHERE anchor_id=?`, anchorID); err != nil {
		return Anchor{}, err
	}
	now := time.Now().UTC()
	status := StatusActive
	finishedAt := ""
	if finish {
		status = StatusComplete
		finishedAt = formatTime(now)
		if _, err := tx.ExecContext(ctx, `DELETE FROM profiler_baseline_rows WHERE anchor_id=?`, anchorID); err != nil {
			return Anchor{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM profiler_seen_rows WHERE anchor_id=?`, anchorID); err != nil {
		return Anchor{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE profiler_anchors SET status=?, last_scanned_at=?, finished_at=NULLIF(?,''),
		inserted_count=(SELECT COALESCE(SUM(inserted_count),0) FROM profiler_tables WHERE anchor_id=?),
		updated_count=(SELECT COALESCE(SUM(updated_count),0) FROM profiler_tables WHERE anchor_id=?),
		deleted_count=(SELECT COALESCE(SUM(deleted_count),0) FROM profiler_tables WHERE anchor_id=?),
		schema_count=(SELECT COUNT(*) FROM profiler_schema_events WHERE anchor_id=?), error=''
		WHERE id=?`, status, formatTime(now), finishedAt, anchorID, anchorID, anchorID, anchorID, anchorID); err != nil {
		return Anchor{}, err
	}
	if err := tx.Commit(); err != nil {
		return Anchor{}, err
	}
	if finish {
		_, _ = s.db.ExecContext(context.Background(), `PRAGMA incremental_vacuum(2000)`)
	}
	return s.GetAnchor(ctx, anchorID)
}

func (s *Store) loadPlans(ctx context.Context, anchorID string) ([]TablePlan, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT table_name, columns_json, key_columns_json, key_kind, risk, baseline_rows
		FROM profiler_tables WHERE anchor_id=? AND included=1 ORDER BY table_name`, anchorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plans []TablePlan
	for rows.Next() {
		var plan TablePlan
		var columns, keys []byte
		var risk string
		if err := rows.Scan(&plan.Name, &columns, &keys, &plan.KeyKind, &risk, &plan.EstimatedRows); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(columns, &plan.Columns); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(keys, &plan.KeyColumns); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

func (s *Store) excludedTables(ctx context.Context, anchorID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT table_name FROM profiler_tables WHERE anchor_id=? AND included=0`, anchorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		result = append(result, table)
	}
	return result, rows.Err()
}

func scanCurrentTable(ctx context.Context, source Queryer, tx *sql.Tx, anchor Anchor, plan TablePlan, pairBaseline bool, progress ProgressFunc) error {
	rows, names, types, err := queryTableRows(ctx, source, anchor.Engine, plan)
	if err != nil {
		return err
	}
	defer rows.Close()
	lookup, err := tx.PrepareContext(ctx, `SELECT row_hash,row_blob FROM profiler_baseline_rows
		WHERE anchor_id=? AND table_name=? AND key_hash=? AND key_blob=?`)
	if err != nil {
		return err
	}
	defer lookup.Close()
	seen, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO profiler_seen_rows(anchor_id,table_name,key_hash,key_blob) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer seen.Close()
	diff, err := tx.PrepareContext(ctx, `INSERT INTO profiler_diff_rows
		(anchor_id,table_name,key_hash,key_blob,kind,before_blob,after_blob,changed_columns_json) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer diff.Close()
	values := make([]any, len(names))
	pointers := make([]any, len(names))
	for index := range values {
		pointers[index] = &values[index]
	}
	occurrences := map[[sha256.Size]byte]int{}
	var count, bytesRead int64
	lastProgress := time.Time{}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		rowBlob, rowHash, err := encodeScannedRow(names, types, values)
		if err != nil {
			return err
		}
		occurrence := 0
		if plan.KeyKind == KeyFullRow {
			var key [sha256.Size]byte
			copy(key[:], rowHash)
			occurrence = occurrences[key]
			occurrences[key]++
		}
		keyBlob, keyHash, err := encodeKey(rowBlob, plan.KeyColumns, plan.KeyKind, occurrence)
		if err != nil {
			return err
		}
		var beforeHash, beforeBlob []byte
		lookupErr := sql.ErrNoRows
		if pairBaseline {
			lookupErr = lookup.QueryRowContext(ctx, anchor.ID, plan.Name, keyHash, keyBlob).Scan(&beforeHash, &beforeBlob)
		}
		switch {
		case lookupErr == nil:
			if _, err := seen.ExecContext(ctx, anchor.ID, plan.Name, keyHash, keyBlob); err != nil {
				return err
			}
			if !equalBytes(beforeHash, rowHash) {
				beforeRow, err := unpackRow(beforeBlob)
				if err != nil {
					return err
				}
				changed, err := changedColumns(beforeRow, rowBlob)
				if err != nil {
					return err
				}
				afterBlob, err := packRow(rowBlob)
				if err != nil {
					return err
				}
				changedJSON, _ := json.Marshal(changed)
				if _, err := diff.ExecContext(ctx, anchor.ID, plan.Name, keyHash, keyBlob, DiffUpdated, beforeBlob, afterBlob, changedJSON); err != nil {
					return err
				}
			}
		case lookupErr == sql.ErrNoRows:
			afterBlob, err := packRow(rowBlob)
			if err != nil {
				return err
			}
			if _, err := diff.ExecContext(ctx, anchor.ID, plan.Name, keyHash, keyBlob, DiffInserted, nil, afterBlob, `[]`); err != nil {
				return err
			}
		default:
			return lookupErr
		}
		count++
		bytesRead += int64(len(rowBlob))
		if progress != nil && (count == 1 || time.Since(lastProgress) >= 200*time.Millisecond) {
			progress(Progress{Phase: "comparing", Table: plan.Name, Rows: count, Bytes: bytesRead})
			lastProgress = time.Now()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if pairBaseline {
		_, err = tx.ExecContext(ctx, `INSERT INTO profiler_diff_rows
			(anchor_id,table_name,key_hash,key_blob,kind,before_blob,after_blob,changed_columns_json)
			SELECT b.anchor_id,b.table_name,b.key_hash,b.key_blob,'deleted',b.row_blob,NULL,'[]'
			FROM profiler_baseline_rows b WHERE b.anchor_id=? AND b.table_name=?
			AND NOT EXISTS(SELECT 1 FROM profiler_seen_rows s WHERE s.anchor_id=b.anchor_id AND s.table_name=b.table_name AND s.key_hash=b.key_hash AND s.key_blob=b.key_blob)`, anchor.ID, plan.Name)
		if err != nil {
			return err
		}
	}
	if progress != nil {
		progress(Progress{Phase: "comparing", Table: plan.Name, Rows: count, Bytes: bytesRead, Complete: true})
	}
	return nil
}

func recordSchemaEvent(ctx context.Context, tx *sql.Tx, anchorID, table, kind, details string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO profiler_schema_events(anchor_id,table_name,kind,details) VALUES(?,?,?,?)`, anchorID, table, kind, details)
	return err
}

func sameKey(left, right TablePlan) bool {
	if left.KeyKind != right.KeyKind || len(left.KeyColumns) != len(right.KeyColumns) {
		return false
	}
	for index := range left.KeyColumns {
		if left.KeyColumns[index] != right.KeyColumns[index] {
			return false
		}
	}
	return true
}

func columnsContain(columns []Column, names []string) bool {
	available := make(map[string]bool, len(columns)+1)
	for _, column := range columns {
		available[column.Name] = true
	}
	available["__dbterm_rowid"] = true
	for _, name := range names {
		if !available[name] {
			return false
		}
	}
	return true
}

func schemaChangeDescription(before, after TablePlan) string {
	oldNames := make(map[string]Column, len(before.Columns))
	for _, column := range before.Columns {
		oldNames[column.Name] = column
	}
	var changes []string
	seen := map[string]bool{}
	for _, column := range after.Columns {
		seen[column.Name] = true
		old, ok := oldNames[column.Name]
		if !ok {
			changes = append(changes, "added column "+column.Name)
		} else if old.Type != column.Type || old.Nullable != column.Nullable || old.Default != column.Default {
			changes = append(changes, "changed column "+column.Name)
		}
	}
	for _, column := range before.Columns {
		if !seen[column.Name] {
			changes = append(changes, "removed column "+column.Name)
		}
	}
	if !sameKey(before, after) {
		changes = append(changes, "changed row identity")
	}
	if len(changes) == 0 {
		return "Table structure changed"
	}
	return strings.Join(changes, "; ")
}
