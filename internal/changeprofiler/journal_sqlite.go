package changeprofiler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/shreyam1008/dbterm/internal/config"
)

// sqliteJournalSpec is kept with the local checkpoint and in its durable source
// receipt. All source objects have a checkpoint-specific, unguessable name.
type sqliteJournalSpec struct {
	Name          string                `json:"name"`
	SchemaVersion int64                 `json:"schema_version"`
	Objects       []sqliteJournalObject `json:"objects"`
	Tables        []TablePlan           `json:"tables"`
	Frozen        bool                  `json:"frozen"`
	Encoding      string                `json:"encoding"`
}

type sqliteJournalObject struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	SQL     string `json:"sql"`
	Dropped bool   `json:"dropped,omitempty"`
}

type sqliteJournalChange struct {
	Sequence int64
	Table    string
	Before   *encodedRow
	After    *encodedRow
	Probe    bool
}

func installSQLiteJournal(ctx context.Context, source *sql.DB, id string, plans []TablePlan) (JournalSpec, error) {
	tx, err := source.BeginTx(ctx, nil)
	if err != nil {
		return JournalSpec{}, err
	}
	defer tx.Rollback()
	state, err := sqliteJournalInstall(ctx, tx, id, plans)
	if err != nil {
		payload, _ := json.Marshal(state)
		return JournalSpec{Version: 1, ID: id, Engine: config.SQLite, Plans: plans, State: payload}, err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return JournalSpec{}, err
	}
	if err := tx.Commit(); err != nil {
		return JournalSpec{Version: 1, ID: id, Engine: config.SQLite, Plans: plans, State: payload}, fmt.Errorf("commit SQLite journal installation: %w", err)
	}
	return JournalSpec{Version: 1, ID: id, Engine: config.SQLite, Plans: plans, State: payload}, nil
}

func freezeSQLiteJournal(ctx context.Context, source *sql.DB, spec JournalSpec) (JournalSpec, error) {
	var state sqliteJournalSpec
	if err := json.Unmarshal(spec.State, &state); err != nil {
		return spec, err
	}
	if err := sqliteJournalCheckReceipt(spec, state); err != nil {
		return spec, err
	}
	if spec.Frozen {
		return spec, nil
	}
	tx, err := source.BeginTx(ctx, nil)
	if err != nil {
		return spec, err
	}
	defer tx.Rollback()
	// Acquire the writer before any metadata reads. This constant false update
	// does not visit source or journal rows and closes races with other writers.
	if _, err := tx.ExecContext(ctx, `UPDATE `+quoteName(config.SQLite, state.Name)+` SET seq=seq WHERE 0`); err != nil {
		return spec, fmt.Errorf("reserve SQLite checkpoint stop boundary: %w", err)
	}
	// The source receipt survives a successful COMMIT whose acknowledgement or
	// local state save was lost. Recover that exact boundary instead of treating
	// our already-removed triggers as an unexplained capture gap.
	var owner, durable string
	if err := tx.QueryRowContext(ctx, `SELECT before_row,after_row FROM `+quoteName(config.SQLite, state.Name)+` WHERE seq=0 AND op='meta'`).Scan(&owner, &durable); err != nil {
		return spec, fmt.Errorf("read durable SQLite journal receipt: %w", err)
	}
	if owner != spec.ID {
		return spec, fmt.Errorf("SQLite journal ownership receipt does not match checkpoint")
	}
	var recorded sqliteJournalSpec
	if err := json.Unmarshal([]byte(durable), &recorded); err != nil {
		return spec, fmt.Errorf("invalid durable SQLite journal receipt: %w", err)
	}
	if err := sqliteJournalCheckReceipt(spec, recorded); err != nil {
		return spec, err
	}
	if recorded.Frozen {
		if recorded.Name != state.Name {
			return spec, fmt.Errorf("SQLite journal receipt object does not match checkpoint")
		}
		if err := tx.Commit(); err != nil {
			return spec, err
		}
		spec.State, spec.Frozen = json.RawMessage(durable), true
		return spec, nil
	}
	state = recorded
	if err := sqliteJournalValidate(ctx, tx, state); err != nil {
		return spec, err
	}
	if err := sqliteJournalResolveProbes(ctx, tx, state); err != nil {
		return spec, err
	}
	for index := range state.Objects {
		object := &state.Objects[index]
		if object.Type != "trigger" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER `+quoteName(config.SQLite, object.Name)); err != nil {
			return spec, err
		}
		object.Dropped = true
	}
	state.Frozen = true
	if err := tx.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&state.SchemaVersion); err != nil {
		return spec, err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return spec, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE `+quoteName(config.SQLite, state.Name)+` SET after_row=? WHERE seq=0 AND op='meta' AND before_row=?`, string(payload), spec.ID); err != nil {
		return spec, err
	}
	if err := tx.Commit(); err != nil {
		return spec, fmt.Errorf("commit SQLite checkpoint stop boundary: %w", err)
	}
	spec.State, spec.Frozen = payload, true
	return spec, nil
}

func readSQLiteJournal(ctx context.Context, source *sql.DB, spec JournalSpec, emit func(JournalEvent) error) error {
	var state sqliteJournalSpec
	if err := json.Unmarshal(spec.State, &state); err != nil {
		return err
	}
	if err := sqliteJournalCheckReceipt(spec, state); err != nil {
		return err
	}
	if !spec.Frozen || !state.Frozen {
		return fmt.Errorf("stop the SQLite journal before reading its final diff")
	}
	return sqliteJournalRead(ctx, source, state, func(change sqliteJournalChange) error {
		event := JournalEvent{Sequence: change.Sequence, Table: change.Table, Probe: change.Probe}
		if change.Before != nil {
			payload, _, err := encodeRow(*change.Before)
			if err != nil {
				return err
			}
			event.Before = payload
		}
		if change.After != nil {
			payload, _, err := encodeRow(*change.After)
			if err != nil {
				return err
			}
			event.After = payload
		}
		return emit(event)
	})
}

func cleanupSQLiteJournal(ctx context.Context, source *sql.DB, spec JournalSpec) error {
	var state sqliteJournalSpec
	if err := json.Unmarshal(spec.State, &state); err != nil {
		return err
	}
	if err := sqliteJournalCheckReceipt(spec, state); err != nil {
		return err
	}
	tx, err := source.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var journalSQL string
	err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, state.Name).Scan(&journalSQL)
	if err == sql.ErrNoRows {
		// DROP TABLE on the journal does not remove triggers attached to the
		// application tables. They would keep rejecting writes to their missing
		// destination. The saved local receipt still proves their exact names
		// and SQL; remove those verified objects even without the source owner
		// row. Any replacement definition makes cleanup fail and roll back.
		if err := sqliteJournalDrop(ctx, tx, state); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if len(state.Objects) == 0 || state.Objects[0].Type != "table" || state.Objects[0].Name != state.Name || state.Objects[0].SQL != journalSQL {
		return fmt.Errorf("refuse cleanup: SQLite journal table ownership definition changed")
	}
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT before_row FROM `+quoteName(config.SQLite, state.Name)+` WHERE seq=0 AND op='meta'`).Scan(&owner); err != nil {
		return fmt.Errorf("refuse cleanup without SQLite journal ownership receipt: %w", err)
	}
	if owner != spec.ID {
		return fmt.Errorf("refuse cleanup: SQLite journal belongs to another checkpoint")
	}
	if err := sqliteJournalDrop(ctx, tx, state); err != nil {
		return err
	}
	return tx.Commit()
}

type sqliteJournalExecutor interface {
	Queryer
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// Treat the durable source receipt as data. Even a modified receipt must never
// direct cleanup or freeze to source objects outside this checkpoint's exact
// generated namespace and selected table set.
func sqliteJournalCheckReceipt(spec JournalSpec, state sqliteJournalSpec) error {
	hash := sha256.Sum256([]byte(spec.ID))
	expected := "__dbterm_cp_" + hex.EncodeToString(hash[:12])
	if state.Name != expected {
		return fmt.Errorf("SQLite journal object does not belong to this checkpoint")
	}
	if len(state.Tables) != len(spec.Plans) {
		return fmt.Errorf("SQLite journal receipt selected-table set changed")
	}
	allowed := map[string]string{expected: "table"}
	for index, plan := range spec.Plans {
		original, _ := json.Marshal(plan)
		actual, _ := json.Marshal(state.Tables[index])
		if string(original) != string(actual) {
			return fmt.Errorf("SQLite journal receipt table plan changed for %s", plan.Name)
		}
		for _, suffix := range []string{"insert", "update", "delete", "probe_insert", "probe_update"} {
			allowed[fmt.Sprintf("%s_%d_%s", expected, index, suffix)] = "trigger"
		}
	}
	for _, object := range state.Objects {
		if object.Type == "source" {
			found := false
			for _, plan := range spec.Plans {
				if object.Name == plan.Name {
					found = true
					break
				}
			}
			if found {
				continue
			}
		}
		kind, owned := allowed[object.Name]
		if !owned || kind != object.Type {
			return fmt.Errorf("SQLite journal receipt contains an unowned source object %s", object.Name)
		}
	}
	return nil
}

// sqliteJournalInstall must run inside the caller's source write transaction.
// BEGIN IMMEDIATE is preferable for local SQLite: it reserves the writer before
// reading metadata, giving all installed triggers one atomic start boundary.
// Installing these ordinary (not TEMP) triggers deliberately observes writers
// on every connection, and their writes roll back with the source transaction.
func sqliteJournalInstall(ctx context.Context, source sqliteJournalExecutor, id string, plans []TablePlan) (sqliteJournalSpec, error) {
	hash := sha256.Sum256([]byte(id))
	spec := sqliteJournalSpec{Name: "__dbterm_cp_" + hex.EncodeToString(hash[:12]), Tables: plans}
	if len(plans) == 0 {
		return spec, fmt.Errorf("select at least one table for the change journal")
	}
	journal := quoteName(config.SQLite, spec.Name)
	if err := source.QueryRowContext(ctx, `PRAGMA encoding`).Scan(&spec.Encoding); err != nil {
		return spec, fmt.Errorf("read SQLite text encoding: %w", err)
	}
	create := `CREATE TABLE ` + journal + ` (seq INTEGER PRIMARY KEY,table_name TEXT NOT NULL,op TEXT NOT NULL,before_row TEXT,after_row TEXT)`
	spec.Objects = append(spec.Objects, sqliteJournalObject{Type: "table", Name: spec.Name, SQL: create})
	for index, plan := range plans {
		if plan.KeyKind == KeyFullRow || len(plan.KeyColumns) == 0 {
			return spec, fmt.Errorf("%s has no stable indexed identity; choose snapshot comparison for this table", plan.Name)
		}
		var tableSQL string
		if err := source.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, plan.Name).Scan(&tableSQL); err != nil {
			return spec, fmt.Errorf("inspect journal source %s: %w", plan.Name, err)
		}
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(tableSQL)), "CREATE VIRTUAL TABLE") {
			return spec, fmt.Errorf("%s is a virtual table and cannot be covered by SQLite row triggers", plan.Name)
		}
		if strings.HasPrefix(plan.Name, "__dbterm_cp_") || strings.HasPrefix(plan.Name, "sqlite_") {
			return spec, fmt.Errorf("%s is an internal table and cannot be monitored", plan.Name)
		}
		uniqueKeys, err := sqliteJournalUniqueKeys(ctx, source, plan)
		if err != nil {
			return spec, err
		}
		spec.Objects = append(spec.Objects, sqliteJournalObject{Type: "source", Name: plan.Name, SQL: tableSQL})
		oldRow, err := sqliteJournalRowExpression(plan, "OLD")
		if err != nil {
			return spec, err
		}
		newRow, err := sqliteJournalRowExpression(plan, "NEW")
		if err != nil {
			return spec, err
		}
		for _, event := range []struct{ suffix, action, before, after string }{
			{"insert", "INSERT", "NULL", newRow},
			{"update", "UPDATE", oldRow, newRow},
			{"delete", "DELETE", oldRow, "NULL"},
		} {
			name := fmt.Sprintf("%s_%d_%s", spec.Name, index, event.suffix)
			statement := `CREATE TRIGGER ` + quoteName(config.SQLite, name) + ` AFTER ` + event.action + ` ON ` + quoteName(config.SQLite, plan.Name) +
				` BEGIN INSERT INTO ` + journal + `(table_name,op,before_row,after_row) VALUES (` + sqliteJournalLiteral(plan.Name) + `,` + sqliteJournalLiteral(event.suffix) + `,` + event.before + `,` + event.after + `); END`
			spec.Objects = append(spec.Objects, sqliteJournalObject{Type: "trigger", Name: name, SQL: statement})
		}
		// SQLite's REPLACE conflict handler can remove a conflicting row without
		// firing DELETE triggers (recursive_triggers is connection-local and OFF
		// by default). Record indexed conflict candidates before each insert or
		// update, then resolve their final images at the stop boundary. Harmless
		// IGNORE/UPSERT candidates cancel naturally in the resulting net diff.
		probeRow, err := sqliteJournalRowExpression(plan, "candidate")
		if err != nil {
			return spec, err
		}
		for _, action := range []string{"INSERT", "UPDATE"} {
			name := fmt.Sprintf("%s_%d_probe_%s", spec.Name, index, strings.ToLower(action))
			var body strings.Builder
			for _, key := range uniqueKeys {
				conditions := make([]string, len(key))
				for columnIndex, column := range key {
					quoted := quoteName(config.SQLite, column.Name)
					left := `candidate.` + quoted
					if column.Collation != "" {
						left += ` COLLATE ` + quoteName(config.SQLite, column.Collation)
					}
					conditions[columnIndex] = left + ` = NEW.` + quoted
				}
				if action == "UPDATE" {
					// The ordinary UPDATE event already carries this row's exact
					// before image. Probe only a different row that a conflict
					// handler could evict, keeping normal updates to one event.
					self := make([]string, len(plan.KeyColumns))
					for keyIndex, name := range plan.KeyColumns {
						if plan.KeyKind == KeyRowID && name == "__dbterm_rowid" {
							name = sqliteRowIDAlias(plan.Columns)
						}
						quoted := quoteName(config.SQLite, name)
						self[keyIndex] = `candidate.` + quoted + ` IS OLD.` + quoted
					}
					conditions = append(conditions, `NOT (`+strings.Join(self, ` AND `)+`)`)
				}
				body.WriteString(`INSERT INTO ` + journal + `(table_name,op,before_row,after_row) SELECT ` + sqliteJournalLiteral(plan.Name) + `,'probe',` + probeRow + `,NULL FROM ` + quoteName(config.SQLite, plan.Name) + ` AS candidate WHERE ` + strings.Join(conditions, ` AND `) + `; `)
			}
			statement := `CREATE TRIGGER ` + quoteName(config.SQLite, name) + ` BEFORE ` + action + ` ON ` + quoteName(config.SQLite, plan.Name) + ` BEGIN ` + body.String() + ` END`
			spec.Objects = append(spec.Objects, sqliteJournalObject{Type: "trigger", Name: name, SQL: statement})
		}
	}
	planned, err := json.Marshal(spec)
	if err != nil {
		return spec, err
	}
	if err := persistJournalInstall(ctx, JournalSpec{Version: 1, ID: id, Engine: config.SQLite, Plans: plans, State: planned}); err != nil {
		return spec, fmt.Errorf("save SQLite journal installation receipt: %w", err)
	}
	for _, object := range spec.Objects {
		if object.Type == "source" {
			continue
		}
		if _, err := source.ExecContext(ctx, object.SQL); err != nil {
			return spec, fmt.Errorf("install checkpoint %s %s: %w", object.Type, object.Name, err)
		}
	}
	// Store SQLite's canonical SQL, including its normalization of CREATE syntax.
	for index := range spec.Objects {
		object := &spec.Objects[index]
		if object.Type == "source" {
			continue
		}
		if err := source.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type=? AND name=?`, object.Type, object.Name).Scan(&object.SQL); err != nil {
			return spec, fmt.Errorf("verify installed journal object %s: %w", object.Name, err)
		}
	}
	if err := source.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&spec.SchemaVersion); err != nil {
		return spec, fmt.Errorf("read journal schema version: %w", err)
	}
	payload, err := json.Marshal(spec)
	if err != nil {
		return spec, err
	}
	if _, err := source.ExecContext(ctx, `INSERT INTO `+journal+`(seq,table_name,op,before_row,after_row) VALUES(0,'','meta',?,?)`, id, string(payload)); err != nil {
		return spec, err
	}
	return spec, nil
}

// sqliteJournalValidate intentionally fails closed after any source DDL. Merely
// finding the expected trigger text is insufficient: a removed trigger can be
// recreated with identical text after writes have escaped its coverage.
func sqliteJournalValidate(ctx context.Context, source Queryer, spec sqliteJournalSpec) error {
	var version int64
	if err := source.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&version); err != nil {
		return fmt.Errorf("verify checkpoint coverage: %w", err)
	}
	if version != spec.SchemaVersion {
		return fmt.Errorf("checkpoint coverage cannot be guaranteed: database schema changed during recording (version %d to %d); use snapshot comparison for DDL changes", spec.SchemaVersion, version)
	}
	for _, object := range spec.Objects {
		if object.Dropped {
			continue
		}
		kind := object.Type
		if kind == "source" {
			kind = "table"
		}
		var actual string
		if err := source.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type=? AND name=?`, kind, object.Name).Scan(&actual); err != nil {
			return fmt.Errorf("checkpoint coverage lost: %s %s is missing or unreadable: %w", kind, object.Name, err)
		}
		if actual != object.SQL {
			return fmt.Errorf("checkpoint coverage lost: %s %s changed during recording", kind, object.Name)
		}
	}
	return nil
}

// sqliteJournalRead streams only recorded changes. Existing untouched source
// rows are never visited at either boundary.
func sqliteJournalRead(ctx context.Context, source Queryer, spec sqliteJournalSpec, visit func(sqliteJournalChange) error) error {
	plans := make(map[string]TablePlan, len(spec.Tables))
	for _, plan := range spec.Tables {
		plans[plan.Name] = plan
	}
	rows, err := source.QueryContext(ctx, `SELECT seq,table_name,op,before_row,after_row FROM `+quoteName(config.SQLite, spec.Name)+` WHERE seq>0 ORDER BY seq`)
	if err != nil {
		return fmt.Errorf("read SQLite change journal: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var change sqliteJournalChange
		var op string
		var before, after sql.NullString
		if err := rows.Scan(&change.Sequence, &change.Table, &op, &before, &after); err != nil {
			return err
		}
		plan, ok := plans[change.Table]
		if !ok {
			return fmt.Errorf("unexpected table %q in checkpoint journal", change.Table)
		}
		if op != "insert" && op != "update" && op != "delete" && op != "probe" {
			return fmt.Errorf("unexpected checkpoint journal operation %q", op)
		}
		change.Probe = op == "probe"
		if before.Valid {
			row, err := sqliteJournalDecodeRowEncoding(plan, before.String, spec.Encoding)
			if err != nil {
				return fmt.Errorf("decode %s before image at event %d: %w", plan.Name, change.Sequence, err)
			}
			change.Before = &row
		}
		if after.Valid {
			row, err := sqliteJournalDecodeRowEncoding(plan, after.String, spec.Encoding)
			if err != nil {
				return fmt.Errorf("decode %s after image at event %d: %w", plan.Name, change.Sequence, err)
			}
			change.After = &row
		}
		if err := visit(change); err != nil {
			return err
		}
	}
	return rows.Err()
}

// sqliteJournalDrop removes only objects named in this checkpoint's receipt,
// after checking their exact definitions. A user replacement is never dropped.
// Source tables are recorded for validation and are always skipped here.
func sqliteJournalDrop(ctx context.Context, source sqliteJournalExecutor, spec sqliteJournalSpec) error {
	for index := len(spec.Objects) - 1; index >= 0; index-- {
		object := spec.Objects[index]
		if object.Type == "source" {
			continue
		}
		if object.Type != "table" && object.Type != "trigger" {
			return fmt.Errorf("refuse to remove unknown checkpoint object type %q", object.Type)
		}
		var actual string
		err := source.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type=? AND name=?`, object.Type, object.Name).Scan(&actual)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		if object.Dropped {
			return fmt.Errorf("refuse to remove recreated %s %s; inspect it manually", object.Type, object.Name)
		}
		if actual != object.SQL {
			return fmt.Errorf("refuse to remove changed %s %s; inspect it manually", object.Type, object.Name)
		}
		if _, err := source.ExecContext(ctx, `DROP `+strings.ToUpper(object.Type)+` `+quoteName(config.SQLite, object.Name)); err != nil {
			return fmt.Errorf("remove checkpoint %s %s: %w", object.Type, object.Name, err)
		}
	}
	return nil
}

func sqliteJournalLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

// Every conflict probe is an indexed lookup. Expression/partial UNIQUE indexes
// cannot be reconstructed safely from PRAGMA index_info, so fail explicitly
// instead of quietly missing a REPLACE eviction through one of those indexes.
type sqliteJournalKeyCell struct{ Name, Collation string }

func sqliteJournalUniqueKeys(ctx context.Context, source Queryer, plan TablePlan) ([][]sqliteJournalKeyCell, error) {
	keys := [][]sqliteJournalKeyCell{}
	if plan.KeyKind == KeyRowID {
		keys = append(keys, []sqliteJournalKeyCell{{Name: sqliteRowIDAlias(plan.Columns)}})
	} else {
		key := make([]sqliteJournalKeyCell, len(plan.KeyColumns))
		for index, name := range plan.KeyColumns {
			key[index] = sqliteJournalKeyCell{Name: name}
		}
		keys = append(keys, key)
	}
	rows, err := source.QueryContext(ctx, `PRAGMA index_list(`+quoteName(config.SQLite, plan.Name)+`)`)
	if err != nil {
		return nil, err
	}
	var indexes []string
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return nil, err
		}
		if unique == 0 {
			continue
		}
		if partial != 0 {
			rows.Close()
			return nil, fmt.Errorf("%s has partial UNIQUE index %s; journal mode cannot guarantee REPLACE coverage, use snapshot comparison", plan.Name, name)
		}
		indexes = append(indexes, name)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	initialIdentity, _ := json.Marshal(keys[0])
	seen := map[string]bool{string(initialIdentity): true}
	for _, index := range indexes {
		rows, err := source.QueryContext(ctx, `PRAGMA index_xinfo(`+quoteName(config.SQLite, index)+`)`)
		if err != nil {
			return nil, err
		}
		var columns []sqliteJournalKeyCell
		for rows.Next() {
			var seq, cid, desc, key int
			var name, collation sql.NullString
			if err := rows.Scan(&seq, &cid, &name, &desc, &collation, &key); err != nil {
				rows.Close()
				return nil, err
			}
			if key == 0 {
				continue
			}
			if cid < 0 || !name.Valid {
				rows.Close()
				return nil, fmt.Errorf("%s has expression UNIQUE index %s; journal mode cannot guarantee REPLACE coverage, use snapshot comparison", plan.Name, index)
			}
			// Match the index's collation explicitly: it can differ from the
			// column's declared collation, e.g. UNIQUE(value COLLATE NOCASE).
			if collation.Valid && !strings.EqualFold(collation.String, "BINARY") && !strings.EqualFold(collation.String, "NOCASE") && !strings.EqualFold(collation.String, "RTRIM") {
				rows.Close()
				return nil, fmt.Errorf("%s has UNIQUE index %s with collation %s; use snapshot comparison", plan.Name, index, collation.String)
			}
			columns = append(columns, sqliteJournalKeyCell{Name: name.String, Collation: collation.String})
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		identityJSON, _ := json.Marshal(columns)
		identity := string(identityJSON)
		if len(columns) != 0 && !seen[identity] {
			keys = append(keys, columns)
			seen[identity] = true
		}
	}
	return keys, nil
}

func sqliteJournalResolveProbes(ctx context.Context, source sqliteJournalExecutor, spec sqliteJournalSpec) error {
	byName := make(map[string]TablePlan, len(spec.Tables))
	for _, plan := range spec.Tables {
		byName[plan.Name] = plan
	}
	var lastSequence int64
	for {
		rows, err := source.QueryContext(ctx, `SELECT seq,table_name,before_row FROM `+quoteName(config.SQLite, spec.Name)+` WHERE op='probe' AND seq>? ORDER BY seq LIMIT 64`, lastSequence)
		if err != nil {
			return err
		}
		type pendingProbe struct {
			sequence      int64
			table, before string
		}
		var probes []pendingProbe
		bytesRead := 0
		for rows.Next() {
			var probe pendingProbe
			if err := rows.Scan(&probe.sequence, &probe.table, &probe.before); err != nil {
				rows.Close()
				return err
			}
			probes = append(probes, probe)
			bytesRead += len(probe.before)
			if bytesRead >= 4<<20 {
				break
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(probes) == 0 {
			return nil
		}
		for _, probe := range probes {
			if err := ctx.Err(); err != nil {
				return err
			}
			plan, ok := byName[probe.table]
			if !ok {
				return fmt.Errorf("unexpected probe table %q", probe.table)
			}
			before, err := sqliteJournalDecodeRowEncoding(plan, probe.before, spec.Encoding)
			if err != nil {
				return err
			}
			conditions := make([]string, len(plan.KeyColumns))
			arguments := make([]any, len(plan.KeyColumns))
			for index, key := range plan.KeyColumns {
				actual := key
				if plan.KeyKind == KeyRowID && key == "__dbterm_rowid" {
					actual = sqliteRowIDAlias(plan.Columns)
				}
				conditions[index] = `candidate.` + quoteName(config.SQLite, actual) + ` IS ?`
				found := false
				for _, cell := range before.Cells {
					if cell.Name != key {
						continue
					}
					arguments[index], err = sqliteJournalCellArgument(cell)
					if err != nil {
						return err
					}
					found = true
					break
				}
				if !found {
					return fmt.Errorf("missing probe identity column %s", key)
				}
			}
			expression, err := sqliteJournalRowExpression(plan, "candidate")
			if err != nil {
				return err
			}
			var final any
			var payload string
			err = source.QueryRowContext(ctx, `SELECT `+expression+` FROM `+quoteName(config.SQLite, plan.Name)+` AS candidate WHERE `+strings.Join(conditions, ` AND `)+` LIMIT 1`, arguments...).Scan(&payload)
			if err == nil {
				final = payload
			} else if err != sql.ErrNoRows {
				return fmt.Errorf("resolve final probe image for %s: %w", plan.Name, err)
			}
			if _, err := source.ExecContext(ctx, `UPDATE `+quoteName(config.SQLite, spec.Name)+` SET after_row=? WHERE seq=?`, final, probe.sequence); err != nil {
				return err
			}
			lastSequence = probe.sequence
		}
	}
}

func sqliteJournalCellArgument(cell encodedCell) (any, error) {
	switch cell.Kind {
	case "null":
		return nil, nil
	case "int":
		return strconv.ParseInt(cell.Data, 10, 64)
	case "float":
		bits, err := strconv.ParseUint(cell.Data, 16, 64)
		if err != nil {
			return nil, err
		}
		return math.Float64frombits(bits), nil
	case "text":
		return cell.Data, nil
	case "bytes":
		return base64.StdEncoding.DecodeString(cell.Data)
	default:
		return nil, fmt.Errorf("unsupported SQLite probe identity storage class %s", cell.Kind)
	}
}

func sqliteJournalColumns(plan TablePlan) ([]string, []string, error) {
	names := make([]string, 0, len(plan.Columns)+1)
	types := make([]string, 0, len(plan.Columns)+1)
	if plan.KeyKind == KeyRowID {
		if sqliteRowIDAlias(plan.Columns) == "" || hasColumn(plan.Columns, "__dbterm_rowid") {
			return nil, nil, fmt.Errorf("%s has no accessible unshadowed rowid", plan.Name)
		}
		names = append(names, "__dbterm_rowid")
		types = append(types, "INTEGER")
	}
	for _, column := range plan.Columns {
		names = append(names, column.Name)
		types = append(types, column.Type)
	}
	return names, types, nil
}

func sqliteJournalRowExpression(plan TablePlan, image string) (string, error) {
	names, _, err := sqliteJournalColumns(plan)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(names))
	for index, name := range names {
		if name == "__dbterm_rowid" && plan.KeyKind == KeyRowID {
			name = sqliteRowIDAlias(plan.Columns)
		}
		value := image + `.` + quoteName(config.SQLite, name)
		// Hex is necessary for text as well as blobs: SQLite quote() truncates
		// text at embedded NUL, and JSON text encoders may normalize bytes.
		parts[index] = `(CASE typeof(` + value + `) WHEN 'null' THEN 'n;' WHEN 'integer' THEN 'i'||CAST(` + value + ` AS TEXT)||';' WHEN 'real' THEN 'r'||printf('%!.26g',` + value + `)||';' WHEN 'text' THEN 't'||hex(CAST(` + value + ` AS BLOB))||';' WHEN 'blob' THEN 'b'||hex(` + value + `)||';' END)`
	}
	return sqliteJournalConcat(parts), nil
}

// Balanced concatenation keeps trigger expression depth logarithmic even for
// wide tables, unlike a left-associated chain of thousands of cell values.
func sqliteJournalConcat(parts []string) string {
	if len(parts) == 0 {
		return `''`
	}
	if len(parts) == 1 {
		return parts[0]
	}
	middle := len(parts) / 2
	return `(` + sqliteJournalConcat(parts[:middle]) + `||` + sqliteJournalConcat(parts[middle:]) + `)`
}

func sqliteJournalDecodeRow(plan TablePlan, payload string) (encodedRow, error) {
	return sqliteJournalDecodeRowEncoding(plan, payload, "UTF-8")
}

func sqliteJournalDecodeRowEncoding(plan TablePlan, payload, encoding string) (encodedRow, error) {
	names, types, err := sqliteJournalColumns(plan)
	if err != nil {
		return encodedRow{}, err
	}
	parts := strings.Split(payload, ";")
	if len(parts) != len(names)+1 || parts[len(parts)-1] != "" {
		return encodedRow{}, fmt.Errorf("journal image has %d cells; expected %d", len(parts)-1, len(names))
	}
	row := encodedRow{Cells: make([]encodedCell, len(names))}
	for index, token := range parts[:len(parts)-1] {
		if token == "" {
			return encodedRow{}, fmt.Errorf("empty journal cell %d", index)
		}
		var value any
		switch token[0] {
		case 'n':
			if len(token) != 1 {
				return encodedRow{}, fmt.Errorf("invalid null journal cell %d", index)
			}
		case 'i':
			value, err = strconv.ParseInt(token[1:], 10, 64)
		case 'r':
			value, err = strconv.ParseFloat(token[1:], 64)
		case 't', 'b':
			var decoded []byte
			decoded, err = hex.DecodeString(token[1:])
			if token[0] == 't' {
				if err == nil {
					value, err = sqliteJournalDecodeText(decoded, encoding)
				}
			} else {
				value = decoded
			}
		default:
			err = fmt.Errorf("unknown journal storage class %q", token[:1])
		}
		if err != nil {
			return encodedRow{}, fmt.Errorf("decode journal cell %s: %w", names[index], err)
		}
		row.Cells[index] = normalizeCell(names[index], types[index], value)
		// The actual SQLite storage class determines binary/text identity even
		// if a column has TEXT affinity and currently contains a BLOB value.
		if token[0] == 'b' {
			row.Cells[index] = encodedCell{Name: names[index], Type: types[index], Kind: "bytes", Data: base64.StdEncoding.EncodeToString(value.([]byte))}
		}
	}
	return row, nil
}

func sqliteJournalDecodeText(value []byte, encoding string) (string, error) {
	if encoding == "" || strings.EqualFold(encoding, "UTF-8") {
		if !utf8.Valid(value) {
			return "", fmt.Errorf("SQLite TEXT contains invalid UTF-8 bytes; use BLOB storage or snapshot inspection")
		}
		return string(value), nil
	}
	var order binary.ByteOrder
	switch strings.ToUpper(encoding) {
	case "UTF-16LE":
		order = binary.LittleEndian
	case "UTF-16BE":
		order = binary.BigEndian
	default:
		return "", fmt.Errorf("unsupported SQLite text encoding %s", encoding)
	}
	if len(value)%2 != 0 {
		return "", fmt.Errorf("SQLite TEXT contains incomplete UTF-16 data")
	}
	units := make([]uint16, len(value)/2)
	for index := range units {
		units[index] = order.Uint16(value[index*2:])
	}
	for index := 0; index < len(units); index++ {
		unit := units[index]
		if unit >= 0xd800 && unit <= 0xdbff {
			if index+1 >= len(units) || units[index+1] < 0xdc00 || units[index+1] > 0xdfff {
				return "", fmt.Errorf("SQLite TEXT contains unpaired UTF-16 surrogate")
			}
			index++
		} else if unit >= 0xdc00 && unit <= 0xdfff {
			return "", fmt.Errorf("SQLite TEXT contains unpaired UTF-16 surrogate")
		}
	}
	return string(utf16.Decode(units)), nil
}
