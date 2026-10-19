package changeprofiler

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

func mysqlJournalOwner(id string) string {
	return "dbterm checkpoint " + hex.EncodeToString(sha256Bytes([]byte("mysql journal owner:"+id)))
}

func mysqlJournalTriggerAction(state mysqlJournalState, plan TablePlan, event string) string {
	before, after := "NULL", "NULL"
	if event != "INSERT" {
		before = mysqlJournalRowExpr(plan, "OLD")
	}
	if event != "DELETE" {
		after = mysqlJournalRowExpr(plan, "NEW")
	}
	return "BEGIN DECLARE checkpoint_active BOOLEAN; SELECT active INTO checkpoint_active FROM " + mysqlJournalObject(state.Schema, state.Gate) + " WHERE id=1 LOCK IN SHARE MODE; IF checkpoint_active THEN INSERT INTO " + mysqlJournalObject(state.Schema, state.Events) + " (table_name,before_data,after_data) VALUES (" + mysqlJournalText(plan.Name) + "," + before + "," + after + "); END IF; END"
}

// A saved name alone is never deletion authority. Re-derive the entire private
// namespace and trigger definitions from the checkpoint identity and plans.
func mysqlJournalValidateNames(spec JournalSpec, state mysqlJournalState) error {
	token := hex.EncodeToString(sha256Bytes([]byte(spec.ID)))[:24]
	if spec.ID == "" || state.Events != "_dbterm_cp_"+token+"_events" || state.Gate != "_dbterm_cp_"+token+"_gate" || state.Lock != "dbterm_checkpoint_"+token || state.Owner != mysqlJournalOwner(spec.ID) {
		return fmt.Errorf("MySQL checkpoint object identity does not match its checkpoint; refusing source changes")
	}
	if !state.EventsMade && !state.GateMade {
		return nil // Validation failed before any source-object creation.
	}
	if state.Definer == "" {
		return fmt.Errorf("MySQL checkpoint source owner is missing; refusing source changes")
	}
	if len(state.Triggers) != len(spec.Plans)*3 {
		return fmt.Errorf("MySQL checkpoint trigger identities are incomplete; refusing source changes")
	}
	index := 0
	for tableIndex, plan := range spec.Plans {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			trigger := state.Triggers[index]
			name := fmt.Sprintf("_dbterm_cp_%s_%d_%s", token, tableIndex, strings.ToLower(event[:1]))
			if trigger.Name != name || trigger.Table != plan.Name || trigger.Event != event || trigger.Action != mysqlJournalTriggerAction(state, plan, event) {
				return fmt.Errorf("MySQL checkpoint trigger identity %s does not match its checkpoint; refusing source changes", trigger.Name)
			}
			index++
		}
	}
	return nil
}

var mysqlJournalCounter = regexp.MustCompile(` AUTO_INCREMENT=[0-9]+`)
var mysqlJournalIntegerWidth = regexp.MustCompile(`^(tinyint|bigint)\([0-9]+\)`)

func mysqlJournalDDLHash(ctx context.Context, source *sql.DB, schema, table string) (string, error) {
	var name, definition string
	if err := source.QueryRowContext(ctx, "SHOW CREATE TABLE "+mysqlJournalObject(schema, table)).Scan(&name, &definition); err != nil {
		return "", fmt.Errorf("inspect MySQL checkpoint table definition %s: %w", table, err)
	}
	// This is the only mutable SHOW CREATE attribute: committed and rolled-back
	// row writes advance the counter without changing the table's definition.
	definition = mysqlJournalCounter.ReplaceAllString(definition, "")
	return hex.EncodeToString(sha256Bytes([]byte(definition))), nil
}

type mysqlJournalExpectedColumn struct {
	name, kind, nullable, extra string
}

func mysqlJournalVerifyTable(ctx context.Context, source *sql.DB, state mysqlJournalState, name, role, ddlHash string) (bool, error) {
	var tableType, engine, comment, collation, options string
	err := source.QueryRowContext(ctx, `SELECT table_type, COALESCE(engine,''), table_comment, table_collation, create_options FROM information_schema.tables WHERE table_schema=? AND table_name=?`, state.Schema, name).Scan(&tableType, &engine, &comment, &collation, &options)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	foreign := func(detail string) (bool, error) {
		return false, fmt.Errorf("MySQL checkpoint object %s is not an unchanged owned %s table (%s); refusing source cleanup", name, role, detail)
	}
	if tableType != "BASE TABLE" || !strings.EqualFold(engine, "InnoDB") || comment != state.Owner+" "+role || collation != "utf8mb4_bin" || options != "" {
		return foreign("owner marker or table properties differ")
	}
	var expected []mysqlJournalExpectedColumn
	if role == "events" {
		expected = []mysqlJournalExpectedColumn{{"seq", "bigint unsigned", "NO", "auto_increment"}, {"table_name", "varchar(255)", "NO", ""}, {"before_data", "longtext", "YES", ""}, {"after_data", "longtext", "YES", ""}}
	} else {
		expected = []mysqlJournalExpectedColumn{{"id", "tinyint", "NO", ""}, {"active", "tinyint", "NO", ""}, {"phase", "varchar(16)", "NO", ""}}
	}
	rows, err := source.QueryContext(ctx, `SELECT column_name,column_type,is_nullable,column_default,extra,generation_expression,character_set_name,collation_name FROM information_schema.columns WHERE table_schema=? AND table_name=? ORDER BY ordinal_position`, state.Schema, name)
	if err != nil {
		return false, err
	}
	index := 0
	columnsMatch := true
	for rows.Next() {
		var column, kind, nullable, extra, generated string
		var defaultValue sql.NullString
		var characterSet, columnCollation sql.NullString
		if err := rows.Scan(&column, &kind, &nullable, &defaultValue, &extra, &generated, &characterSet, &columnCollation); err != nil {
			_ = rows.Close()
			return false, err
		}
		kind = mysqlJournalIntegerWidth.ReplaceAllString(strings.ToLower(kind), "$1")
		if index >= len(expected) || expected[index] != (mysqlJournalExpectedColumn{column, kind, nullable, strings.ToLower(extra)}) || defaultValue.Valid || generated != "" {
			columnsMatch = false
		}
		if strings.HasPrefix(kind, "varchar") || kind == "longtext" {
			if characterSet.String != "utf8mb4" || columnCollation.String != "utf8mb4_bin" {
				columnsMatch = false
			}
		} else if characterSet.Valid || columnCollation.Valid {
			columnsMatch = false
		}
		index++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	_ = rows.Close()
	if !columnsMatch || index != len(expected) {
		return foreign("column definitions differ")
	}
	var indexes, primaryColumns, attachedTriggers int
	if err := source.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(index_name='PRIMARY' AND non_unique=0 AND seq_in_index=1 AND column_name=? AND sub_part IS NULL),0) FROM information_schema.statistics WHERE table_schema=? AND table_name=?`, expected[0].name, state.Schema, name).Scan(&indexes, &primaryColumns); err != nil {
		return false, err
	}
	if indexes != 1 || primaryColumns != 1 {
		return foreign("index definitions differ")
	}
	if err := source.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.triggers WHERE event_object_schema=? AND event_object_table=?`, state.Schema, name).Scan(&attachedTriggers); err != nil {
		return false, err
	}
	if attachedTriggers != 0 {
		return foreign("unexpected triggers are attached")
	}
	if ddlHash != "" {
		actual, err := mysqlJournalDDLHash(ctx, source, state.Schema, name)
		if err != nil {
			return false, err
		}
		if actual != ddlHash {
			return foreign("saved CREATE TABLE fingerprint differs")
		}
	}
	return true, nil
}

func mysqlJournalVerifyTrigger(ctx context.Context, source *sql.DB, state mysqlJournalState, trigger mysqlJournalTrigger) (bool, error) {
	var schema, table, event, timing, action, definer string
	err := source.QueryRowContext(ctx, `SELECT event_object_schema,event_object_table,event_manipulation,action_timing,action_statement,definer FROM information_schema.triggers WHERE trigger_schema=? AND trigger_name=?`, state.Schema, trigger.Name).Scan(&schema, &table, &event, &timing, &action, &definer)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if schema != state.Schema || table != trigger.Table || event != trigger.Event || timing != "AFTER" || action != trigger.Action || definer != state.Definer {
		return false, fmt.Errorf("MySQL checkpoint trigger %s has a different owner or definition; refusing source cleanup", trigger.Name)
	}
	return true, nil
}

// Validate every surviving object before dropping even one. Missing objects are
// expected after interrupted cleanup; replacement objects must remain intact.
func mysqlJournalVerifyOwnership(ctx context.Context, source *sql.DB, state mysqlJournalState) error {
	var currentSchema string
	if err := source.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&currentSchema); err != nil {
		return err
	}
	if currentSchema != state.Schema {
		return fmt.Errorf("MySQL checkpoint schema differs from the connected database; refusing source cleanup")
	}
	if state.EventsMade {
		if _, err := mysqlJournalVerifyTable(ctx, source, state, state.Events, "events", state.EventsHash); err != nil {
			return err
		}
	}
	if state.GateMade {
		if _, err := mysqlJournalVerifyTable(ctx, source, state, state.Gate, "gate", state.GateHash); err != nil {
			return err
		}
	}
	for _, trigger := range state.Triggers {
		if _, err := mysqlJournalVerifyTrigger(ctx, source, state, trigger); err != nil {
			return err
		}
	}
	return nil
}
