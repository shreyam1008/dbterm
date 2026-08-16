package changeprofiler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func (s *Store) Start(ctx context.Context, source *sql.DB, request StartRequest, progress ProgressFunc) (Anchor, error) {
	if s == nil || s.db == nil {
		return Anchor{}, fmt.Errorf("change profiler store is not open")
	}
	if source == nil {
		return Anchor{}, fmt.Errorf("source database is required")
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || strings.TrimSpace(request.ConnectionKey) == "" {
		return Anchor{}, fmt.Errorf("anchor name and connection identity are required")
	}
	selected := make([]TablePlan, 0, len(request.Tables))
	for _, plan := range request.Tables {
		if plan.Included {
			selected = append(selected, plan)
		}
	}
	if len(selected) == 0 {
		return Anchor{}, fmt.Errorf("select at least one table")
	}
	id, err := newID()
	if err != nil {
		return Anchor{}, err
	}
	consistency := ConsistencySnapshot
	if request.Engine == config.CloudflareD1 {
		consistency = ConsistencyBestEffort
	}
	now := time.Now().UTC()
	anchor := Anchor{ID: id, Name: request.Name, ConnectionKey: request.ConnectionKey,
		ConnectionLabel: request.ConnectionLabel, Engine: request.Engine, TargetLabel: request.TargetLabel,
		Status: StatusCapturing, Consistency: consistency, StartedAt: now}
	queryer, closeSnapshot, err := beginSnapshot(ctx, source, request.Engine)
	if err != nil {
		return Anchor{}, err
	}
	defer closeSnapshot()
	// Re-read table metadata inside the source snapshot. The review screen is
	// advisory; the baseline must use the exact structure captured at Start.
	fresh := make([]TablePlan, 0, len(selected))
	for index, reviewed := range selected {
		if progress != nil {
			progress(Progress{Phase: "validating", Table: reviewed.Name, TableIndex: index + 1, TableCount: len(selected), Percent: index * 100 / len(selected)})
		}
		plan, inspectErr := inspectTable(ctx, queryer, request.Engine, reviewed.Name)
		if inspectErr != nil {
			return Anchor{}, fmt.Errorf("revalidate table %s: %w", reviewed.Name, inspectErr)
		}
		plan.Included = true
		fresh = append(fresh, plan)
		if progress != nil {
			progress(Progress{Phase: "validating", Table: reviewed.Name, TableIndex: index + 1, TableCount: len(selected), Percent: (index + 1) * 100 / len(selected)})
		}
	}
	selected = fresh
	meter := newProgressMeter(selected, false)
	allPlans := append([]TablePlan(nil), request.Tables...)
	freshByName := make(map[string]TablePlan, len(selected))
	for _, plan := range selected {
		freshByName[plan.Name] = plan
	}
	for index := range allPlans {
		if freshPlan, ok := freshByName[allPlans[index].Name]; ok {
			allPlans[index] = freshPlan
		}
	}
	if err := s.insertAnchor(ctx, anchor, allPlans); err != nil {
		return Anchor{}, err
	}

	for _, plan := range selected {
		if err := ctx.Err(); err != nil {
			_ = s.DeleteAnchor(context.Background(), id)
			return Anchor{}, err
		}
		if progress != nil {
			progress(meter.decorate(Progress{Phase: "capturing", Table: plan.Name}, false))
		}
		tableProgress := ProgressFunc(nil)
		if progress != nil {
			tableProgress = func(event Progress) { progress(meter.decorate(event, false)) }
		}
		count, err := s.captureBaselineTable(ctx, queryer, anchor, plan, tableProgress)
		if err != nil {
			_ = s.DeleteAnchor(context.Background(), id)
			return Anchor{}, fmt.Errorf("capture %s: %w", plan.Name, err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE profiler_tables SET baseline_rows = ? WHERE anchor_id = ? AND table_name = ?`, count, id, plan.Name); err != nil {
			_ = s.DeleteAnchor(context.Background(), id)
			return Anchor{}, err
		}
		if progress != nil {
			progress(meter.decorate(Progress{Phase: "capturing", Table: plan.Name, Rows: count}, true))
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE profiler_anchors SET status = 'active' WHERE id = ? AND status = 'capturing'`, id); err != nil {
		_ = s.DeleteAnchor(context.Background(), id)
		return Anchor{}, err
	}
	anchor.Status = StatusActive
	return anchor, nil
}

func (s *Store) insertAnchor(ctx context.Context, anchor Anchor, plans []TablePlan) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO profiler_anchors
		(id,name,connection_key,connection_label,engine,target_label,status,consistency,started_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, anchor.ID, anchor.Name, anchor.ConnectionKey, anchor.ConnectionLabel,
		anchor.Engine, anchor.TargetLabel, anchor.Status, anchor.Consistency, formatTime(anchor.StartedAt))
	if err != nil {
		return fmt.Errorf("create named anchor: %w", err)
	}
	for _, plan := range plans {
		columns, _ := json.Marshal(plan.Columns)
		keys, _ := json.Marshal(plan.KeyColumns)
		_, schemaHash, err := tableSchemaPayload(plan)
		if err != nil {
			return err
		}
		risks := make([]string, len(plan.Risks))
		for index := range plan.Risks {
			risks[index] = string(plan.Risks[index])
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO profiler_tables
			(anchor_id,table_name,columns_json,key_columns_json,key_kind,schema_hash,included,risk)
			VALUES(?,?,?,?,?,?,?,?)`, anchor.ID, plan.Name, columns, keys, plan.KeyKind, schemaHash, plan.Included, strings.Join(risks, "; ")); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func beginSnapshot(ctx context.Context, db *sql.DB, engine config.DBType) (Queryer, func(), error) {
	if engine == config.CloudflareD1 {
		return db, func() {}, nil
	}
	options := &sql.TxOptions{}
	switch engine {
	case config.PostgreSQL, config.MySQL:
		options.Isolation = sql.LevelRepeatableRead
		options.ReadOnly = true
	case config.SQLite, config.Turso:
		options = nil
	}
	tx, err := db.BeginTx(ctx, options)
	if err != nil {
		return nil, func() {}, fmt.Errorf("start consistent change profiler snapshot: %w", err)
	}
	return tx, func() { _ = tx.Rollback() }, nil
}

func (s *Store) captureBaselineTable(ctx context.Context, source Queryer, anchor Anchor, plan TablePlan, progress ProgressFunc) (int64, error) {
	rows, names, types, err := queryTableRows(ctx, source, anchor.Engine, plan)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, `INSERT INTO profiler_baseline_rows
		(anchor_id,table_name,key_hash,key_blob,row_hash,row_blob) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer statement.Close()
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
			return 0, err
		}
		if err := rows.Scan(pointers...); err != nil {
			return 0, err
		}
		rowBlob, rowHash, err := encodeScannedRow(names, types, values)
		if err != nil {
			return 0, err
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
			return 0, err
		}
		storedRow, err := packRow(rowBlob)
		if err != nil {
			return 0, err
		}
		if _, err := statement.ExecContext(ctx, anchor.ID, plan.Name, keyHash, keyBlob, rowHash, storedRow); err != nil {
			return 0, err
		}
		count++
		bytesRead += int64(len(rowBlob))
		if progress != nil && (count == 1 || time.Since(lastProgress) >= 200*time.Millisecond) {
			progress(Progress{Phase: "capturing", Table: plan.Name, Rows: count, Bytes: bytesRead})
			lastProgress = time.Now()
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func queryTableRows(ctx context.Context, source Queryer, engine config.DBType, plan TablePlan) (*sql.Rows, []string, []string, error) {
	selectColumns := make([]string, 0, len(plan.Columns)+1)
	if plan.KeyKind == KeyRowID {
		selectColumns = append(selectColumns, `rowid AS "__dbterm_rowid"`)
	}
	for _, column := range plan.Columns {
		selectColumns = append(selectColumns, quoteIdentifier(engine, column.Name))
	}
	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(selectColumns, ", "), quoteIdentifier(engine, plan.Name))
	if plan.KeyKind != KeyFullRow && len(plan.KeyColumns) > 0 {
		order := make([]string, len(plan.KeyColumns))
		for index, column := range plan.KeyColumns {
			if column == "__dbterm_rowid" {
				order[index] = "rowid"
			} else {
				order[index] = quoteIdentifier(engine, column)
			}
		}
		query += " ORDER BY " + strings.Join(order, ", ")
	}
	rows, err := source.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, nil, err
	}
	names, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		return nil, nil, nil, err
	}
	columnTypes, _ := rows.ColumnTypes()
	types := make([]string, len(names))
	for index := range columnTypes {
		if index < len(types) && columnTypes[index] != nil {
			types[index] = columnTypes[index].DatabaseTypeName()
		}
	}
	return rows, names, types, nil
}

func equalBytes(left, right []byte) bool { return bytes.Equal(left, right) }

var errBaselineNotFound = errors.New("baseline row not found")
