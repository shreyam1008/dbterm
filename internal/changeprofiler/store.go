package changeprofiler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/internal/appdirs"
	"github.com/shreyam1008/dbterm/internal/config"
	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	path string
}

func DefaultPath() (string, error) {
	dir, err := appdirs.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "change-profiler", "change-profiler.db"), nil
}

func OpenDefaultStore() (*Store, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return OpenStore(path)
}

func OpenStore(path string) (*Store, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" {
		return nil, fmt.Errorf("change profiler store path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create change profiler state directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open change profiler store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, path: path}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		_ = db.Close()
		return nil, fmt.Errorf("protect change profiler store: %w", err)
	}
	if _, err := db.Exec(`UPDATE profiler_anchors SET status = 'failed', error = 'dbterm stopped before this operation completed'
		WHERE status IN ('capturing', 'comparing')`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("recover interrupted change profiler operations: %w", err)
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
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA cache_size=-32768`,
		`PRAGMA mmap_size=134217728`,
		`PRAGMA wal_autocheckpoint=2048`,
		`PRAGMA journal_size_limit=67108864`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA auto_vacuum=INCREMENTAL`,
		`CREATE TABLE IF NOT EXISTS profiler_anchors (
			id TEXT PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE, connection_key TEXT NOT NULL,
			connection_label TEXT NOT NULL, engine TEXT NOT NULL, target_label TEXT NOT NULL,
			status TEXT NOT NULL, consistency TEXT NOT NULL, started_at TEXT NOT NULL,
			last_scanned_at TEXT, finished_at TEXT, inserted_count INTEGER NOT NULL DEFAULT 0,
			updated_count INTEGER NOT NULL DEFAULT 0, deleted_count INTEGER NOT NULL DEFAULT 0,
			schema_count INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS profiler_anchor_name_idx ON profiler_anchors(name)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS profiler_active_target_idx ON profiler_anchors(connection_key)
			WHERE status IN ('capturing', 'active', 'comparing')`,
		`CREATE TABLE IF NOT EXISTS profiler_tables (
			anchor_id TEXT NOT NULL, table_name TEXT NOT NULL, columns_json BLOB NOT NULL,
			key_columns_json BLOB NOT NULL, key_kind TEXT NOT NULL, schema_hash BLOB NOT NULL,
			included INTEGER NOT NULL DEFAULT 1,
			baseline_present INTEGER NOT NULL DEFAULT 1,
			baseline_rows INTEGER NOT NULL DEFAULT 0, inserted_count INTEGER NOT NULL DEFAULT 0,
			updated_count INTEGER NOT NULL DEFAULT 0, deleted_count INTEGER NOT NULL DEFAULT 0,
			schema_changed INTEGER NOT NULL DEFAULT 0, risk TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(anchor_id, table_name), FOREIGN KEY(anchor_id) REFERENCES profiler_anchors(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS profiler_baseline_rows (
			anchor_id TEXT NOT NULL, table_name TEXT NOT NULL, key_hash BLOB NOT NULL, key_blob BLOB NOT NULL,
			row_hash BLOB NOT NULL, row_blob BLOB NOT NULL,
			PRIMARY KEY(anchor_id, table_name, key_hash, key_blob),
			FOREIGN KEY(anchor_id, table_name) REFERENCES profiler_tables(anchor_id, table_name) ON DELETE CASCADE
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS profiler_seen_rows (
			anchor_id TEXT NOT NULL, table_name TEXT NOT NULL, key_hash BLOB NOT NULL, key_blob BLOB NOT NULL,
			PRIMARY KEY(anchor_id, table_name, key_hash, key_blob)
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS profiler_diff_rows (
			anchor_id TEXT NOT NULL, table_name TEXT NOT NULL, key_hash BLOB NOT NULL, key_blob BLOB NOT NULL,
			kind TEXT NOT NULL, before_blob BLOB, after_blob BLOB, changed_columns_json BLOB NOT NULL,
			PRIMARY KEY(anchor_id, table_name, key_hash, key_blob, kind)
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS profiler_schema_events (
			anchor_id TEXT NOT NULL, table_name TEXT NOT NULL, kind TEXT NOT NULL, details TEXT NOT NULL,
			PRIMARY KEY(anchor_id, table_name, kind)
		) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS profiler_diff_kind_idx
			ON profiler_diff_rows(anchor_id,table_name,kind,key_hash,key_blob)`,
		`CREATE TABLE IF NOT EXISTS profiler_activity (
			anchor_id TEXT NOT NULL, occurred_at TEXT NOT NULL, connection_label TEXT NOT NULL,
			sql_text TEXT NOT NULL, rows_affected INTEGER NOT NULL,
			PRIMARY KEY(anchor_id, occurred_at, sql_text)
		) WITHOUT ROWID`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize change profiler store: %w", err)
		}
	}
	// Table metadata serves both the immutable baseline and the current report.
	// Preserve that distinction so a table created after capture stays "created"
	// on every comparison, rather than becoming part of the baseline.
	columns, err := s.db.QueryContext(ctx, `PRAGMA table_info(profiler_tables)`)
	if err != nil {
		return fmt.Errorf("inspect change profiler store schema: %w", err)
	}
	hasBaselinePresent := false
	for columns.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := columns.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = columns.Close()
			return err
		}
		hasBaselinePresent = hasBaselinePresent || name == "baseline_present"
	}
	if err := columns.Close(); err != nil {
		return err
	}
	if err := columns.Err(); err != nil {
		return err
	}
	if !hasBaselinePresent {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `ALTER TABLE profiler_tables ADD COLUMN baseline_present INTEGER NOT NULL DEFAULT 1`); err != nil {
			return fmt.Errorf("upgrade change profiler table metadata: %w", err)
		}
		// Old stores retain the first report's creation event, which safely
		// identifies report-only metadata that can be recovered during upgrade.
		if _, err := tx.ExecContext(ctx, `UPDATE profiler_tables SET baseline_present=0
			WHERE EXISTS (SELECT 1 FROM profiler_schema_events e
			WHERE e.anchor_id=profiler_tables.anchor_id AND e.table_name=profiler_tables.table_name AND e.kind='table_created')`); err != nil {
			return fmt.Errorf("recover created-table metadata: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit change profiler store upgrade: %w", err)
		}
	}
	return nil
}

func newID() (string, error) {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "anchor_" + hex.EncodeToString(bytes[:]), nil
}

func (s *Store) ListAnchors(ctx context.Context) ([]Anchor, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, connection_key, connection_label, engine, target_label,
		status, consistency, started_at, COALESCE(last_scanned_at,''), COALESCE(finished_at,''),
		inserted_count, updated_count, deleted_count, schema_count, error
		FROM profiler_anchors ORDER BY started_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var anchors []Anchor
	for rows.Next() {
		anchor, err := scanAnchor(rows)
		if err != nil {
			return nil, err
		}
		anchors = append(anchors, anchor)
	}
	return anchors, rows.Err()
}

func (s *Store) GetAnchor(ctx context.Context, id string) (Anchor, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, connection_key, connection_label, engine, target_label,
		status, consistency, started_at, COALESCE(last_scanned_at,''), COALESCE(finished_at,''),
		inserted_count, updated_count, deleted_count, schema_count, error
		FROM profiler_anchors WHERE id = ?`, id)
	return scanAnchor(row)
}

type rowScanner interface{ Scan(...any) error }

func scanAnchor(row rowScanner) (Anchor, error) {
	var anchor Anchor
	var started, scanned, finished string
	if err := row.Scan(&anchor.ID, &anchor.Name, &anchor.ConnectionKey, &anchor.ConnectionLabel, &anchor.Engine,
		&anchor.TargetLabel, &anchor.Status, &anchor.Consistency, &started, &scanned, &finished,
		&anchor.Inserted, &anchor.Updated, &anchor.Deleted, &anchor.SchemaChanges, &anchor.Error); err != nil {
		return Anchor{}, err
	}
	anchor.StartedAt = parseTime(started)
	anchor.LastScannedAt = parseTime(scanned)
	anchor.FinishedAt = parseTime(finished)
	return anchor, nil
}

func (s *Store) ActiveAnchor(ctx context.Context, connectionKey string) (Anchor, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, connection_key, connection_label, engine, target_label,
		status, consistency, started_at, COALESCE(last_scanned_at,''), COALESCE(finished_at,''),
		inserted_count, updated_count, deleted_count, schema_count, error
		FROM profiler_anchors WHERE connection_key = ? AND status IN ('capturing','active','comparing')`, connectionKey)
	return scanAnchor(row)
}

func (s *Store) RenameAnchor(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("anchor name is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE profiler_anchors SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("rename anchor: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteAnchor(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM profiler_anchors WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete anchor: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return sql.ErrNoRows
	}
	// These report tables predate foreign keys. Delete them explicitly as part
	// of the same transaction so removing an anchor also removes its sensitive
	// before/after values, SQL activity, and temporary comparison data.
	for _, table := range []string{"profiler_diff_rows", "profiler_schema_events", "profiler_activity", "profiler_seen_rows"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE anchor_id = ?`, id); err != nil {
			return fmt.Errorf("remove anchor data: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `PRAGMA incremental_vacuum(1000)`)
	return nil
}

func (s *Store) TableSummaries(ctx context.Context, anchorID string) ([]TableSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT table_name, inserted_count, updated_count, deleted_count,
		schema_changed, key_kind, key_columns_json, risk FROM profiler_tables WHERE anchor_id = ?
		AND (inserted_count > 0 OR updated_count > 0 OR deleted_count > 0 OR schema_changed = 1)
		ORDER BY table_name`, anchorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var summaries []TableSummary
	for rows.Next() {
		var summary TableSummary
		var keys []byte
		if err := rows.Scan(&summary.Name, &summary.Inserted, &summary.Updated, &summary.Deleted,
			&summary.SchemaChanged, &summary.KeyKind, &keys, &summary.Risk); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(keys, &summary.KeyColumns); err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

func (s *Store) ListDiffRows(ctx context.Context, anchorID, table string, limit int) ([]DiffRow, error) {
	return s.ListDiffRowsPage(ctx, anchorID, table, 0, limit)
}

// ListDiffRowsPage loads a bounded page of the saved report. Offset is a row
// offset in the stable (kind, key hash, key) ordering; it is never a source DB
// query. Existing callers can continue to use ListDiffRows for the first page.
func (s *Store) ListDiffRowsPage(ctx context.Context, anchorID, table string, offset, limit int) ([]DiffRow, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind, key_blob, before_blob, after_blob, changed_columns_json
		FROM profiler_diff_rows WHERE anchor_id = ? AND table_name = ? ORDER BY kind, key_hash, key_blob LIMIT ? OFFSET ?`, anchorID, table, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DiffRow
	for rows.Next() {
		row, err := scanDiffRow(rows, table)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func scanDiffRow(scanner rowScanner, table string) (DiffRow, error) {
	var row DiffRow
	var key, before, after, changed []byte
	if err := scanner.Scan(&row.Kind, &key, &before, &after, &changed); err != nil {
		return DiffRow{}, err
	}
	row.Table, row.Key = table, string(key)
	var err error
	row.Before, err = decodeRow(before)
	if err != nil {
		return DiffRow{}, err
	}
	row.After, err = decodeRow(after)
	if err != nil {
		return DiffRow{}, err
	}
	if err := json.Unmarshal(changed, &row.ChangedColumns); err != nil {
		return DiffRow{}, err
	}
	return row, nil
}

// ListDiffRowsForValues finds saved changes for the currently displayed source
// rows. It reads only their stable keys, so highlighting a late result page does
// not load or truncate a potentially much larger report. Values and database
// types must be the unformatted driver values, in columnNames order.
func (s *Store) ListDiffRowsForValues(ctx context.Context, anchorID, table string, columnNames, databaseTypes []string, values [][]any) ([]DiffRow, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > 1000 {
		return nil, fmt.Errorf("change lookup supports at most 1000 displayed rows")
	}
	var columnsJSON, keysJSON []byte
	var kind KeyKind
	var engine config.DBType
	if err := s.db.QueryRowContext(ctx, `SELECT t.columns_json,t.key_columns_json,t.key_kind,a.engine
		FROM profiler_tables t JOIN profiler_anchors a ON a.id=t.anchor_id WHERE t.anchor_id=? AND t.table_name=?`, anchorID, table).Scan(&columnsJSON, &keysJSON, &kind, &engine); err != nil {
		return nil, err
	}
	if kind == KeyFullRow || kind == KeyRowID {
		return nil, nil
	}
	var columns []Column
	var keys []string
	if err := json.Unmarshal(columnsJSON, &columns); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(keysJSON, &keys); err != nil {
		return nil, err
	}
	inputIndexes := make(map[string]int, len(columnNames))
	for index, name := range columnNames {
		inputIndexes[name] = index
	}
	wanted := make(map[string]bool, len(keys))
	for _, key := range keys {
		if _, ok := inputIndexes[key]; !ok {
			return nil, nil // A projection without the key cannot be paired safely.
		}
		wanted[key] = true
	}
	// Identity encoding follows the baseline's column order, even if the visible
	// projection or a composite primary key uses a different order.
	var keyNames, keyTypes []string
	var keyIndexes []int
	for _, column := range columns {
		if !wanted[column.Name] {
			continue
		}
		index := inputIndexes[column.Name]
		keyNames = append(keyNames, column.Name)
		keyIndexes = append(keyIndexes, index)
		databaseType := ""
		if index < len(databaseTypes) {
			databaseType = databaseTypes[index]
		}
		if databaseType == "" && engine == config.SQLite {
			// Legacy result references did not retain driver type metadata.
			// SQLite exposes declared names; other engines use driver-specific
			// type names, so guessing for those would not pair keys safely.
			databaseType = strings.ToUpper(column.Type)
		}
		keyTypes = append(keyTypes, databaseType)
	}
	lookup, err := s.db.PrepareContext(ctx, `SELECT kind,key_blob,before_blob,after_blob,changed_columns_json
		FROM profiler_diff_rows WHERE anchor_id=? AND table_name=? AND key_hash=? AND key_blob=? AND kind IN ('inserted','updated')`)
	if err != nil {
		return nil, err
	}
	defer lookup.Close()
	keyValues := make([]any, len(keyIndexes))
	seen := make(map[string]bool, len(values))
	var result []DiffRow
	for _, rowValues := range values {
		if len(rowValues) != len(columnNames) {
			return nil, fmt.Errorf("displayed row has %d values for %d columns", len(rowValues), len(columnNames))
		}
		for index, inputIndex := range keyIndexes {
			keyValues[index] = rowValues[inputIndex]
		}
		keyBlob, keyHash, err := encodeRowKey(normalizedRow(keyNames, keyTypes, keyValues), keys, kind, 0)
		if err != nil {
			return nil, err
		}
		if seen[string(keyBlob)] {
			continue
		}
		seen[string(keyBlob)] = true
		row, err := scanDiffRow(lookup.QueryRowContext(ctx, anchorID, table, keyHash, keyBlob), table)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, nil
}

func (s *Store) RecordActivity(ctx context.Context, connectionKey, connectionLabel, sqlText string, rowsAffected int64, occurredAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO profiler_activity(anchor_id, occurred_at, connection_label, sql_text, rows_affected)
		SELECT id, ?, ?, ?, ? FROM profiler_anchors WHERE connection_key = ? AND status = 'active'`,
		formatTime(occurredAt), connectionLabel, sqlText, rowsAffected, connectionKey)
	return err
}

func (s *Store) ListActivity(ctx context.Context, anchorID string, limit int) ([]Activity, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT occurred_at, connection_label, sql_text, rows_affected
		FROM profiler_activity WHERE anchor_id=? ORDER BY occurred_at DESC LIMIT ?`, anchorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Activity
	for rows.Next() {
		var item Activity
		var occurred string
		if err := rows.Scan(&occurred, &item.ConnectionLabel, &item.SQL, &item.RowsAffected); err != nil {
			return nil, err
		}
		item.OccurredAt = parseTime(occurred)
		result = append(result, item)
	}
	return result, rows.Err()
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
