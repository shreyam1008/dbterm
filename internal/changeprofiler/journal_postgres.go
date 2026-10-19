package changeprofiler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

type postgresJournalState struct {
	Schema   string                               `json:"schema"`
	Tables   map[string]postgresJournalTableState `json:"tables"`
	GuardPID int                                  `json:"guard_pid,omitempty"`
}

type postgresJournalGuard struct {
	mu     sync.Mutex
	source *sql.DB
	conn   *sql.Conn
	tx     *sql.Tx
	cancel context.CancelFunc
	closed bool
}

var postgresJournalGuards = struct {
	sync.Mutex
	entries map[string]*postgresJournalGuard
}{entries: map[string]*postgresJournalGuard{}}

func postgresJournalGuardFor(source *sql.DB, id string) *postgresJournalGuard {
	postgresJournalGuards.Lock()
	defer postgresJournalGuards.Unlock()
	guard := postgresJournalGuards.entries[id]
	if guard != nil && guard.source == source {
		return guard
	}
	return nil
}

func closePostgresJournalGuard(id string, guard *postgresJournalGuard) {
	if guard.closed {
		return
	}
	guard.closed = true
	guard.cancel()
	_ = guard.tx.Rollback()
	_ = guard.conn.Close()
	postgresJournalGuards.Lock()
	if postgresJournalGuards.entries[id] == guard {
		delete(postgresJournalGuards.entries, id)
	}
	postgresJournalGuards.Unlock()
}

// releasePostgresJournalGuards must run before a source pool closes. A lost
// guard never resumes: its journal stays owned for explicit cleanup, and Stop
// refuses a complete report because table/trigger continuity is no longer known.
func releasePostgresJournalGuards(_ context.Context, source *sql.DB) {
	postgresJournalGuards.Lock()
	guards := map[string]*postgresJournalGuard{}
	for id, guard := range postgresJournalGuards.entries {
		if guard.source == source {
			guards[id] = guard
		}
	}
	postgresJournalGuards.Unlock()
	for id, guard := range guards {
		guard.mu.Lock()
		closePostgresJournalGuard(id, guard)
		guard.mu.Unlock()
	}
}

type postgresJournalTableState struct {
	OID                int64  `json:"oid"`
	RowTriggerOID      int64  `json:"row_trigger_oid"`
	TruncateTriggerOID int64  `json:"truncate_trigger_oid"`
	SchemaHash         string `json:"schema_hash"`
}

// PostgreSQL checkpoints use a private, transactional source journal. This is
// deliberately distinct from logical decoding: it needs neither a replication
// slot that can retain unlimited WAL nor changes to wal_level/replica identity.
// Installing the journal is an explicit source-database write operation.
func postgresJournalSchema(id string) string {
	hash := sha256.Sum256([]byte(id))
	return "dbterm_cp_" + hex.EncodeToString(hash[:12])
}

func postgresJournalObject(id, object string) string {
	return quoteName(config.PostgreSQL, postgresJournalSchema(id)) + "." + quoteName(config.PostgreSQL, object)
}

func postgresLiteral(value string) string {
	// PostgreSQL's standard_conforming_strings is not assumed for literals.
	return "E'" + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), "'", "''") + "'"
}

// postgresJournalRow returns the existing lossless encodedRow format using
// source-side text output. In particular JSON values never pass through a
// float64, arbitrary bytes use base64, and SQL NULL remains distinct from text.
// The source function fixes the GUCs that affect textual output.
func postgresJournalRow(plan TablePlan, record string) string {
	cells := make([]string, 0, len(plan.Columns))
	for _, column := range plan.Columns {
		value := record + "." + quoteName(config.PostgreSQL, column.Name)
		kind, data := "text", value+"::text"
		if strings.EqualFold(column.Type, "bytea") {
			kind = "bytes"
			data = "pg_catalog.replace(pg_catalog.encode(" + value + ", 'base64'), E'\\n', '')"
		}
		cells = append(cells, "pg_catalog.jsonb_build_object('n', "+postgresLiteral(column.Name)+
			", 'k', CASE WHEN "+value+" IS NULL THEN 'null' ELSE '"+kind+"' END, 't', "+postgresLiteral(column.Type)+
			", 'v', COALESCE("+data+", ''))")
	}
	// PostgreSQL functions accept at most 100 arguments. Wide tables must not
	// make either Start or their first write fail with too_many_arguments.
	arrays := make([]string, 0, (len(cells)+63)/64)
	for begin := 0; begin < len(cells); begin += 64 {
		end := min(begin+64, len(cells))
		arrays = append(arrays, "pg_catalog.jsonb_build_array("+strings.Join(cells[begin:end], ", ")+")")
	}
	if len(arrays) == 0 {
		arrays = append(arrays, "'[]'::jsonb")
	}
	return "pg_catalog.jsonb_build_object('c', " + strings.Join(arrays, " || ") + ")"
}

func postgresJournalPlans(plans []TablePlan) []TablePlan {
	selected := make([]TablePlan, 0, len(plans))
	for _, plan := range plans {
		if plan.Included {
			selected = append(selected, plan)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	return selected
}

func lockPostgresJournalTables(ctx context.Context, tx *sql.Tx, plans []TablePlan) error {
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
		return err
	}
	for _, plan := range postgresJournalPlans(plans) {
		// The mode conflicts with row writers. Taking every selected lock before
		// installation/removal provides one committed boundary across tables.
		if _, err := tx.ExecContext(ctx, "LOCK TABLE "+quoteIdentifier(config.PostgreSQL, plan.Name)+" IN SHARE ROW EXCLUSIVE MODE"); err != nil {
			return fmt.Errorf("lock %s for checkpoint boundary: %w", plan.Name, err)
		}
	}
	return nil
}

// installPostgresJournal does not SELECT application rows. Its work and initial
// storage depend on the selected schema size rather than the database row count.
func installPostgresJournal(ctx context.Context, source *sql.DB, id string, plans []TablePlan) (spec JournalSpec, resultErr error) {
	if source == nil || strings.TrimSpace(id) == "" {
		return spec, fmt.Errorf("PostgreSQL checkpoint requires a connection and identity")
	}
	if maximum := source.Stats().MaxOpenConnections; maximum > 0 && maximum < 2 {
		return spec, fmt.Errorf("PostgreSQL incremental capture needs at least two pooled connections for its live DDL guard")
	}
	selected := postgresJournalPlans(plans)
	if len(selected) == 0 {
		return spec, fmt.Errorf("select at least one table for the checkpoint journal")
	}
	for index, plan := range selected {
		if index > 0 && plan.Name == selected[index-1].Name {
			return spec, fmt.Errorf("duplicate checkpoint table %s", plan.Name)
		}
	}
	tx, err := source.BeginTx(ctx, nil)
	if err != nil {
		return spec, err
	}
	defer tx.Rollback()
	if err := lockPostgresJournalTables(ctx, tx, selected); err != nil {
		return spec, err
	}

	// Fail the entire transaction before leaving any source objects behind if
	// the selected table cannot provide complete row coverage.
	for _, plan := range selected {
		if plan.KeyKind == KeyFullRow || len(plan.KeyColumns) == 0 {
			return spec, fmt.Errorf("PostgreSQL journal requires a stable key on %s", plan.Name)
		}
		var kind string
		var temporary, inherited, rls, readable bool
		schema, name := splitQualified(plan.Name, "public")
		err := tx.QueryRowContext(ctx, `SELECT c.relkind::text, c.relpersistence = 't',
			EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhrelid=c.oid OR i.inhparent=c.oid),
			c.relrowsecurity, pg_catalog.has_table_privilege(c.oid, 'SELECT')
			FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=$1 AND c.relname=$2`, schema, name).Scan(&kind, &temporary, &inherited, &rls, &readable)
		if err != nil {
			return spec, fmt.Errorf("inspect journal coverage for %s: %w", plan.Name, err)
		}
		if kind != "r" || temporary || inherited || rls {
			return spec, fmt.Errorf("PostgreSQL journal requires an ordinary persistent table without inheritance, partitions, or row security: %s", plan.Name)
		}
		if !readable {
			return spec, fmt.Errorf("PostgreSQL journal requires SELECT on %s to capture rows removed by TRUNCATE", plan.Name)
		}
		fresh, err := inspectPostgresTable(ctx, tx, plan.Name)
		if err != nil {
			return spec, err
		}
		_, actual, err := tableSchemaPayload(fresh)
		if err != nil {
			return spec, err
		}
		_, expected, err := tableSchemaPayload(plan)
		if err != nil || !bytes.Equal(actual, expected) {
			return spec, fmt.Errorf("table %s changed while preparing the journal; review its tables again", plan.Name)
		}
	}
	state := postgresJournalState{Schema: postgresJournalSchema(id), Tables: map[string]postgresJournalTableState{}}
	for _, plan := range selected {
		_, hash, err := tableSchemaPayload(plan)
		if err != nil {
			return spec, err
		}
		state.Tables[plan.Name] = postgresJournalTableState{SchemaHash: hex.EncodeToString(hash)}
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return spec, err
	}
	spec = JournalSpec{Version: 1, ID: id, Engine: config.PostgreSQL, Plans: selected, State: stateJSON}
	if err := persistJournalInstall(ctx, spec); err != nil {
		return spec, err
	}

	schema := quoteName(config.PostgreSQL, postgresJournalSchema(id))
	log := postgresJournalObject(id, "events")
	meta := postgresJournalObject(id, "tables")
	statements := []string{
		"CREATE SCHEMA " + schema,
		"REVOKE ALL ON SCHEMA " + schema + " FROM PUBLIC",
		"CREATE TABLE " + postgresJournalObject(id, "session") + " (anchor_id text PRIMARY KEY, active boolean NOT NULL DEFAULT false, frozen boolean NOT NULL DEFAULT false)",
		"INSERT INTO " + postgresJournalObject(id, "session") + " (anchor_id) VALUES (" + postgresLiteral(id) + ")",
		"CREATE TABLE " + log + " (sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, table_name text NOT NULL, before_row jsonb, after_row jsonb)",
		"CREATE TABLE " + meta + " (table_name text PRIMARY KEY, table_oid oid NOT NULL, row_trigger oid NOT NULL, truncate_trigger oid NOT NULL)",
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return spec, fmt.Errorf("create private PostgreSQL checkpoint journal (requires CREATE on the database and ownership of selected tables): %w", err)
		}
	}
	for index, plan := range selected {
		function := postgresJournalObject(id, "capture_"+strconv.Itoa(index))
		truncateFunction := postgresJournalObject(id, "truncate_"+strconv.Itoa(index))
		trigger := "dbterm_cp_" + postgresJournalSchema(id)[len("dbterm_cp_"):] + "_" + strconv.Itoa(index)
		truncateTrigger := trigger + "_truncate"
		table := quoteIdentifier(config.PostgreSQL, plan.Name)
		functionSettings := " LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog SET TimeZone = 'UTC' SET DateStyle = 'ISO, YMD' SET IntervalStyle = 'iso_8601' SET extra_float_digits = 3 SET lc_monetary = 'C' SET bytea_output = 'hex' "
		gate := "DECLARE checkpoint_active boolean; BEGIN SELECT active INTO checkpoint_active FROM " + postgresJournalObject(id, "session") + " WHERE anchor_id=" + postgresLiteral(id) + " FOR SHARE; IF NOT FOUND THEN RAISE EXCEPTION 'Transaction predates checkpoint installation; retry the transaction' USING ERRCODE='40001'; END IF; IF checkpoint_active IS DISTINCT FROM TRUE THEN RETURN NULL; END IF; "
		body := gate + "IF TG_OP = 'INSERT' THEN INSERT INTO " + log + " (table_name, after_row) VALUES (" + postgresLiteral(plan.Name) + ", " + postgresJournalRow(plan, "NEW") + "); " +
			"ELSIF TG_OP = 'DELETE' THEN INSERT INTO " + log + " (table_name, before_row) VALUES (" + postgresLiteral(plan.Name) + ", " + postgresJournalRow(plan, "OLD") + "); " +
			"ELSE INSERT INTO " + log + " (table_name, before_row, after_row) VALUES (" + postgresLiteral(plan.Name) + ", " + postgresJournalRow(plan, "OLD") + ", " + postgresJournalRow(plan, "NEW") + "); END IF; RETURN NULL; END;"
		truncateBody := gate + "INSERT INTO " + log + " (table_name, before_row) SELECT " + postgresLiteral(plan.Name) + ", " + postgresJournalRow(plan, "source_row") + " FROM " + table + " AS source_row; RETURN NULL; END;"
		statements := []string{
			"CREATE FUNCTION " + function + "() RETURNS trigger" + functionSettings + "AS " + postgresLiteral(body),
			"CREATE FUNCTION " + truncateFunction + "() RETURNS trigger" + functionSettings + "AS " + postgresLiteral(truncateBody),
			"REVOKE ALL ON FUNCTION " + function + "() FROM PUBLIC",
			"REVOKE ALL ON FUNCTION " + truncateFunction + "() FROM PUBLIC",
			"CREATE TRIGGER " + quoteName(config.PostgreSQL, trigger) + " AFTER INSERT OR UPDATE OR DELETE ON " + table + " FOR EACH ROW EXECUTE FUNCTION " + function + "()",
			"CREATE TRIGGER " + quoteName(config.PostgreSQL, truncateTrigger) + " BEFORE TRUNCATE ON " + table + " FOR EACH STATEMENT EXECUTE FUNCTION " + truncateFunction + "()",
			"ALTER TABLE " + table + " ENABLE ALWAYS TRIGGER " + quoteName(config.PostgreSQL, trigger),
			"ALTER TABLE " + table + " ENABLE ALWAYS TRIGGER " + quoteName(config.PostgreSQL, truncateTrigger),
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return spec, fmt.Errorf("install transactional checkpoint triggers on %s: %w", plan.Name, err)
			}
		}
		schemaName, tableName := splitQualified(plan.Name, "public")
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+meta+` SELECT $1, c.oid, r.oid, t.oid
			FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
			JOIN pg_catalog.pg_trigger r ON r.tgrelid=c.oid AND r.tgname=$4
			JOIN pg_catalog.pg_trigger t ON t.tgrelid=c.oid AND t.tgname=$5
			WHERE n.nspname=$2 AND c.relname=$3`, plan.Name, schemaName, tableName, trigger, truncateTrigger); err != nil {
			return spec, err
		}
		var tableState postgresJournalTableState
		if err := tx.QueryRowContext(ctx, "SELECT table_oid::bigint, row_trigger::bigint, truncate_trigger::bigint FROM "+meta+" WHERE table_name=$1", plan.Name).Scan(&tableState.OID, &tableState.RowTriggerOID, &tableState.TruncateTriggerOID); err != nil {
			return spec, err
		}
		_, schemaHash, err := tableSchemaPayload(plan)
		if err != nil {
			return spec, err
		}
		tableState.SchemaHash = hex.EncodeToString(schemaHash)
		state.Tables[plan.Name] = tableState
	}
	stateJSON, err = json.Marshal(state)
	if err != nil {
		return spec, err
	}
	spec.State = stateJSON
	if err := persistJournalInstall(ctx, spec); err != nil {
		return spec, err
	}
	if err := tx.Commit(); err != nil {
		return spec, err
	}
	// Installing triggers and acquiring the retained guard cannot be one
	// transaction: other writers must be able to see the committed triggers.
	// Keep their gate inactive during this handoff, then validate under the
	// guard and arm one shared gate boundary after all tables are protected.
	conn, err := source.Conn(ctx)
	if err != nil {
		return spec, err
	}
	guardCtx, cancelGuard := context.WithCancel(context.Background())
	guardTx, err := conn.BeginTx(guardCtx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		cancelGuard()
		_ = conn.Close()
		return spec, err
	}
	guard := &postgresJournalGuard{source: source, conn: conn, tx: guardTx, cancel: cancelGuard}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	postgresJournalGuards.Lock()
	postgresJournalGuards.entries[id] = guard
	postgresJournalGuards.Unlock()
	defer func() {
		if resultErr != nil {
			closePostgresJournalGuard(id, guard)
		}
	}()
	if _, err := guardTx.ExecContext(ctx, `SET LOCAL lock_timeout='5s'`); err != nil {
		return spec, err
	}
	for _, plan := range selected {
		if _, err := guardTx.ExecContext(ctx, "LOCK TABLE "+quoteIdentifier(config.PostgreSQL, plan.Name)+" IN SHARE UPDATE EXCLUSIVE MODE"); err != nil {
			return spec, fmt.Errorf("protect PostgreSQL checkpoint from DDL: %w", err)
		}
	}
	if err := validatePostgresJournalCoverage(ctx, guardTx, spec, state); err != nil {
		return spec, err
	}
	if err := guardTx.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&state.GuardPID); err != nil {
		return spec, err
	}
	spec.State, err = json.Marshal(state)
	if err != nil {
		return spec, err
	}
	if err := persistJournalInstall(ctx, spec); err != nil {
		return spec, err
	}
	// Every trigger takes a transaction-duration shared gate lock even while
	// inactive. This update drains all pre-Start writers across selected tables.
	if _, err := source.ExecContext(ctx, "UPDATE "+postgresJournalObject(id, "session")+" SET active=true WHERE anchor_id=$1 AND NOT frozen", id); err != nil {
		return spec, fmt.Errorf("arm PostgreSQL checkpoint boundary: %w", err)
	}
	return spec, nil
}

func postgresJournalStateFor(spec JournalSpec) (postgresJournalState, error) {
	var state postgresJournalState
	if spec.Engine != config.PostgreSQL || spec.ID == "" || spec.Version != 1 {
		return state, fmt.Errorf("invalid PostgreSQL checkpoint journal identity")
	}
	if err := json.Unmarshal(spec.State, &state); err != nil || state.Schema != postgresJournalSchema(spec.ID) || len(state.Tables) != len(postgresJournalPlans(spec.Plans)) {
		return state, fmt.Errorf("invalid PostgreSQL checkpoint journal state")
	}
	return state, nil
}

func validatePostgresJournalCoverage(ctx context.Context, tx *sql.Tx, spec JournalSpec, state postgresJournalState) error {
	var covered int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+postgresJournalObject(spec.ID, "tables")+` m
		JOIN pg_catalog.pg_class c ON c.oid=m.table_oid
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_catalog.pg_trigger r ON r.oid=m.row_trigger AND r.tgrelid=c.oid AND r.tgenabled='A'
		JOIN pg_catalog.pg_trigger t ON t.oid=m.truncate_trigger AND t.tgrelid=c.oid AND t.tgenabled='A'
		WHERE n.nspname || '.' || c.relname = m.table_name AND c.relkind='r' AND NOT c.relrowsecurity
		AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhrelid=c.oid OR i.inhparent=c.oid)`).Scan(&covered); err != nil {
		return fmt.Errorf("verify PostgreSQL journal coverage: %w", err)
	}
	if covered != len(postgresJournalPlans(spec.Plans)) {
		return fmt.Errorf("checkpoint coverage changed: a tracked table or capture trigger was removed, disabled, renamed, or changed; the journal cannot report a complete diff")
	}
	for _, plan := range postgresJournalPlans(spec.Plans) {
		expected, ok := state.Tables[plan.Name]
		if !ok {
			return fmt.Errorf("checkpoint has no table identity for %s", plan.Name)
		}
		var oid, rowOID, truncateOID int64
		if err := tx.QueryRowContext(ctx, "SELECT table_oid::bigint, row_trigger::bigint, truncate_trigger::bigint FROM "+postgresJournalObject(spec.ID, "tables")+" WHERE table_name=$1", plan.Name).Scan(&oid, &rowOID, &truncateOID); err != nil {
			return err
		}
		if oid != expected.OID || rowOID != expected.RowTriggerOID || truncateOID != expected.TruncateTriggerOID {
			return fmt.Errorf("checkpoint coverage changed: source table or trigger identity no longer matches %s", plan.Name)
		}
		fresh, err := inspectPostgresTable(ctx, tx, plan.Name)
		if err != nil {
			return err
		}
		_, hash, err := tableSchemaPayload(fresh)
		if err != nil {
			return err
		}
		if hex.EncodeToString(hash) != expected.SchemaHash {
			return fmt.Errorf("schema changed for %s during journal capture; a complete diff cannot be guaranteed", plan.Name)
		}
	}
	return nil
}

// freezePostgresJournal commits the Stop gate inside the retained guard
// transaction. Releasing the DDL locks and freezing the log is one atomic source
// COMMIT, so there is no unguarded interval in which a trigger can be disabled.
func freezePostgresJournal(ctx context.Context, source *sql.DB, spec JournalSpec) (_ JournalSpec, resultErr error) {
	state, err := postgresJournalStateFor(spec)
	if err != nil {
		return spec, err
	}
	if err := ctx.Err(); err != nil {
		return spec, err
	}
	var anchorID string
	var frozen bool
	if err := source.QueryRowContext(ctx, "SELECT anchor_id,frozen FROM "+postgresJournalObject(spec.ID, "session")).Scan(&anchorID, &frozen); err != nil || anchorID != spec.ID {
		return spec, fmt.Errorf("checkpoint journal is missing or has the wrong identity; complete change coverage cannot be guaranteed")
	}
	if frozen {
		spec.Frozen = true
		return spec, dropPostgresJournalCaptureTriggers(ctx, source, spec)
	}
	guard := postgresJournalGuardFor(source, spec.ID)
	if guard == nil {
		return spec, fmt.Errorf("PostgreSQL checkpoint lost its live DDL guard; changes may be incomplete, so discard its source journal and start a new checkpoint")
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.closed {
		return spec, fmt.Errorf("PostgreSQL checkpoint DDL guard is closed; a complete diff cannot be guaranteed")
	}
	// Once a boundary begins, finish that short source operation even if the UI
	// cancels. lib/pq poisons connections on context cancellation, which would
	// otherwise silently release the guard midway. Server lock_timeout bounds
	// writer draining; the outer deadline covers a failed network connection.
	boundaryCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	savepoint := false
	committed := false
	defer func() {
		if resultErr == nil || committed {
			return
		}
		if !savepoint || boundaryCtx.Err() != nil {
			closePostgresJournalGuard(spec.ID, guard)
			return
		}
		recoveryCtx, recoverCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer recoverCancel()
		if _, err := guard.tx.ExecContext(recoveryCtx, "ROLLBACK TO SAVEPOINT dbterm_checkpoint_stop"); err != nil {
			closePostgresJournalGuard(spec.ID, guard)
			return
		}
		if _, err := guard.tx.ExecContext(recoveryCtx, "RELEASE SAVEPOINT dbterm_checkpoint_stop"); err != nil {
			closePostgresJournalGuard(spec.ID, guard)
		}
	}()
	if _, err := guard.tx.ExecContext(boundaryCtx, "SAVEPOINT dbterm_checkpoint_stop"); err != nil {
		return spec, fmt.Errorf("PostgreSQL checkpoint lost its DDL guard: %w", err)
	}
	savepoint = true
	var active bool
	if err := guard.tx.QueryRowContext(boundaryCtx, "SELECT anchor_id,active,frozen FROM "+postgresJournalObject(spec.ID, "session")+" FOR UPDATE").Scan(&anchorID, &active, &frozen); err != nil {
		return spec, fmt.Errorf("drain PostgreSQL writers at Stop: %w", err)
	}
	if anchorID != spec.ID || !active || frozen {
		return spec, fmt.Errorf("PostgreSQL checkpoint gate is no longer recording; complete coverage cannot be guaranteed")
	}
	if err := validatePostgresJournalCoverage(boundaryCtx, guard.tx, spec, state); err != nil {
		return spec, err
	}
	if _, err := guard.tx.ExecContext(boundaryCtx, "UPDATE "+postgresJournalObject(spec.ID, "session")+" SET active=false,frozen=true WHERE anchor_id=$1", spec.ID); err != nil {
		return spec, err
	}
	if err := guard.tx.Commit(); err != nil {
		closePostgresJournalGuard(spec.ID, guard)
		return spec, fmt.Errorf("commit PostgreSQL checkpoint Stop boundary (retry its durable receipt): %w", err)
	}
	committed = true
	closePostgresJournalGuard(spec.ID, guard)
	spec.Frozen = true
	return spec, dropPostgresJournalCaptureTriggers(ctx, source, spec)
}

// Frozen journals no longer rely on table metadata continuity. Their gate has
// already committed off, so removing attached triggers can safely be retried if
// a long-running writer or concurrent DDL temporarily blocks this cleanup.
func dropPostgresJournalCaptureTriggers(ctx context.Context, source *sql.DB, spec JournalSpec) error {
	tx, err := source.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout='5s'`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT n.nspname,c.relname,r.tgname FROM "+postgresJournalObject(spec.ID, "tables")+" m JOIN pg_catalog.pg_trigger r ON r.oid=m.row_trigger OR r.oid=m.truncate_trigger JOIN pg_catalog.pg_class c ON c.oid=r.tgrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace ORDER BY c.oid,r.oid")
	if err != nil {
		return err
	}
	type captureTrigger struct{ schema, table, name string }
	var triggers []captureTrigger
	for rows.Next() {
		var trigger captureTrigger
		if err := rows.Scan(&trigger.schema, &trigger.table, &trigger.name); err != nil {
			_ = rows.Close()
			return err
		}
		triggers = append(triggers, trigger)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, trigger := range triggers {
		table := quoteName(config.PostgreSQL, trigger.schema) + "." + quoteName(config.PostgreSQL, trigger.table)
		if _, err := tx.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+quoteName(config.PostgreSQL, trigger.name)+" ON "+table); err != nil {
			return fmt.Errorf("checkpoint frozen; retry removing its capture triggers: %w", err)
		}
	}
	return tx.Commit()
}

func readPostgresJournal(ctx context.Context, source *sql.DB, spec JournalSpec, emit func(JournalEvent) error) error {
	if _, err := postgresJournalStateFor(spec); err != nil {
		return err
	}
	var anchorID string
	var frozen bool
	if err := source.QueryRowContext(ctx, "SELECT anchor_id, frozen FROM "+postgresJournalObject(spec.ID, "session")).Scan(&anchorID, &frozen); err != nil || anchorID != spec.ID || !frozen {
		return fmt.Errorf("PostgreSQL checkpoint must be frozen before reading its final journal")
	}
	rows, err := source.QueryContext(ctx, "SELECT sequence, table_name, before_row, after_row FROM "+postgresJournalObject(spec.ID, "events")+" ORDER BY sequence")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var event JournalEvent
		if err := rows.Scan(&event.Sequence, &event.Table, &event.Before, &event.After); err != nil {
			return err
		}
		// jsonb object order is intentionally immaterial. Canonicalize through
		// encodedRow so equality, key construction, and compression use exactly
		// the same representation as every other checkpoint adapter.
		for _, payload := range []*[]byte{&event.Before, &event.After} {
			if len(*payload) == 0 {
				continue
			}
			var row encodedRow
			if err := json.Unmarshal(*payload, &row); err != nil {
				return fmt.Errorf("decode PostgreSQL journal row: %w", err)
			}
			*payload, _, err = encodeRow(row)
			if err != nil {
				return err
			}
		}
		if err := emit(event); err != nil {
			return err
		}
	}
	return rows.Err()
}

// cleanupPostgresJournal removes only the checkpoint-owned schema. PostgreSQL
// follows the dependency on its private functions to remove their table
// triggers. The application tables and their other triggers remain intact.
func cleanupPostgresJournal(ctx context.Context, source *sql.DB, spec JournalSpec) error {
	if _, err := postgresJournalStateFor(spec); err != nil {
		return err
	}
	if guard := postgresJournalGuardFor(source, spec.ID); guard != nil {
		guard.mu.Lock()
		closePostgresJournalGuard(spec.ID, guard)
		guard.mu.Unlock()
	}
	tx, err := source.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname=$1)", postgresJournalSchema(spec.ID)).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	var anchorID string
	if err := tx.QueryRowContext(ctx, "SELECT anchor_id FROM "+postgresJournalObject(spec.ID, "session")+" FOR UPDATE").Scan(&anchorID); err != nil || anchorID != spec.ID {
		return fmt.Errorf("refusing cleanup: checkpoint journal identity could not be verified")
	}
	if _, err := tx.ExecContext(ctx, "DROP SCHEMA "+quoteName(config.PostgreSQL, postgresJournalSchema(spec.ID))+" CASCADE"); err != nil {
		return err
	}
	return tx.Commit()
}
