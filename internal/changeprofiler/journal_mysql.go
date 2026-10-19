package changeprofiler

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

type mysqlJournalTrigger struct {
	Name   string `json:"name"`
	Table  string `json:"table"`
	Event  string `json:"event"`
	Action string `json:"action"`
}

type mysqlJournalState struct {
	Schema       string                `json:"schema"`
	Events       string                `json:"events"`
	Gate         string                `json:"gate"`
	Triggers     []mysqlJournalTrigger `json:"triggers"`
	Lock         string                `json:"lock"`
	ConnectionID int64                 `json:"connection_id"`
	EventsMade   bool                  `json:"events_made"`
	GateMade     bool                  `json:"gate_made"`
	Owner        string                `json:"owner"`
	Definer      string                `json:"definer"`
	EventsHash   string                `json:"events_hash,omitempty"`
	GateHash     string                `json:"gate_hash,omitempty"`
}

type mysqlJournalGuard struct {
	mu     sync.Mutex
	conn   *sql.Conn
	tx     *sql.Tx
	source *sql.DB
	lock   string
}

var mysqlJournalGuards = struct {
	sync.Mutex
	entries map[string]*mysqlJournalGuard
}{entries: make(map[string]*mysqlJournalGuard)}

func mysqlJournalObject(schema, name string) string {
	return quoteIdentifier(config.MySQL, schema) + "." + quoteIdentifier(config.MySQL, name)
}

func mysqlJournalDecode(spec JournalSpec) (mysqlJournalState, error) {
	var state mysqlJournalState
	if err := json.Unmarshal(spec.State, &state); err != nil {
		return state, fmt.Errorf("read MySQL checkpoint journal: %w", err)
	}
	if state.Schema == "" || state.Events == "" || state.Gate == "" || state.Lock == "" {
		return state, fmt.Errorf("MySQL checkpoint journal metadata is incomplete")
	}
	if err := mysqlJournalValidateNames(spec, state); err != nil {
		return state, err
	}
	return state, nil
}

func mysqlJournalSave(spec JournalSpec, state mysqlJournalState) JournalSpec {
	spec.State, _ = json.Marshal(state)
	return spec
}

func installMySQLJournal(ctx context.Context, source *sql.DB, id string, plans []TablePlan) (spec JournalSpec, resultErr error) {
	if source == nil || strings.TrimSpace(id) == "" || len(plans) == 0 {
		return spec, fmt.Errorf("MySQL checkpoint requires a connection, identity, and selected tables")
	}
	if maximum := source.Stats().MaxOpenConnections; maximum > 0 && maximum < 2 {
		return spec, fmt.Errorf("MySQL checkpoint needs at least two pooled connections for its metadata guard")
	}
	// Object names derive from a hash rather than the caller-controlled name.
	token := hex.EncodeToString(sha256Bytes([]byte(id)))[:24]
	state := mysqlJournalState{Events: "_dbterm_cp_" + token + "_events", Gate: "_dbterm_cp_" + token + "_gate", Lock: "dbterm_checkpoint_" + token, Owner: mysqlJournalOwner(id)}
	if err := source.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&state.Schema); err != nil {
		return spec, fmt.Errorf("identify MySQL checkpoint database: %w", err)
	}
	spec = JournalSpec{Version: 1, ID: id, Engine: config.MySQL, Plans: plans}
	defer func() {
		spec = mysqlJournalSave(spec, state)
		if resultErr != nil && (state.EventsMade || state.GateMade) {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if cleanupErr := cleanupMySQLJournal(cleanupCtx, source, mysqlJournalSave(spec, state)); cleanupErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove incomplete MySQL checkpoint objects: %w", cleanupErr))
			}
		}
	}()
	for _, plan := range plans {
		if err := validateMySQLJournalTable(ctx, source, state.Schema, plan); err != nil {
			return spec, err
		}
	}
	for tableIndex, plan := range plans {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			state.Triggers = append(state.Triggers, mysqlJournalTrigger{Name: fmt.Sprintf("_dbterm_cp_%s_%d_%s", token, tableIndex, strings.ToLower(event[:1])), Table: plan.Name, Event: event, Action: mysqlJournalTriggerAction(state, plan, event)})
		}
	}
	if err := source.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&state.Definer); err != nil {
		return spec, fmt.Errorf("identify MySQL checkpoint object owner: %w", err)
	}
	// Save every planned name before DDL so an interrupted installation remains
	// discoverable. First refuse existing objects, including a prior journal.
	var collisions int
	if err := source.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=? AND table_name IN (?,?)`, state.Schema, state.Events, state.Gate).Scan(&collisions); err != nil || collisions != 0 {
		state.Triggers = nil // No objects belong to this failed installation.
		return spec, fmt.Errorf("reserve MySQL journal names: %w", nonNilMySQLJournalError(err, "checkpoint objects already exist; recover or clean up the existing checkpoint"))
	}
	for _, trigger := range state.Triggers {
		if err := source.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema=? AND trigger_name=?`, state.Schema, trigger.Name).Scan(&collisions); err != nil || collisions != 0 {
			state.Triggers = nil
			return spec, fmt.Errorf("reserve MySQL journal trigger names: %w", nonNilMySQLJournalError(err, "a checkpoint trigger already exists"))
		}
	}
	state.EventsMade, state.GateMade = true, true
	spec = mysqlJournalSave(spec, state)
	if err := persistJournalInstall(ctx, spec); err != nil {
		return spec, err
	}
	events := mysqlJournalObject(state.Schema, state.Events)
	gate := mysqlJournalObject(state.Schema, state.Gate)
	if _, err := source.ExecContext(ctx, "CREATE TABLE "+events+" (seq BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, table_name VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL, before_data LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL, after_data LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin COMMENT='"+state.Owner+" events'"); err != nil {
		return spec, fmt.Errorf("create MySQL checkpoint change journal (CREATE permission required): %w", err)
	}
	var err error
	state.EventsHash, err = mysqlJournalDDLHash(ctx, source, state.Schema, state.Events)
	if err != nil {
		return spec, err
	}
	if err := persistJournalInstall(ctx, mysqlJournalSave(spec, state)); err != nil {
		return spec, err
	}
	if _, err := source.ExecContext(ctx, "CREATE TABLE "+gate+" (id TINYINT NOT NULL PRIMARY KEY, active BOOLEAN NOT NULL, phase VARCHAR(16) NOT NULL) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin COMMENT='"+state.Owner+" gate'"); err != nil {
		return spec, fmt.Errorf("create MySQL checkpoint gate: %w", err)
	}
	state.GateHash, err = mysqlJournalDDLHash(ctx, source, state.Schema, state.Gate)
	if err != nil {
		return spec, err
	}
	if err := persistJournalInstall(ctx, mysqlJournalSave(spec, state)); err != nil {
		return spec, err
	}
	if _, err := source.ExecContext(ctx, "INSERT INTO "+gate+" (id, active, phase) VALUES (1, FALSE, 'installing')"); err != nil {
		return spec, fmt.Errorf("initialize MySQL checkpoint gate: %w", err)
	}
	for _, trigger := range state.Triggers {
		query := "CREATE TRIGGER " + mysqlJournalObject(state.Schema, trigger.Name) + " AFTER " + trigger.Event + " ON " + mysqlJournalObject(state.Schema, trigger.Table) + " FOR EACH ROW " + trigger.Action
		if _, err := source.ExecContext(ctx, query); err != nil {
			return spec, fmt.Errorf("install MySQL checkpoint trigger for %s (TRIGGER permission required): %w", trigger.Table, err)
		}
	}
	// CREATE TRIGGER implicitly commits and needs exclusive metadata locks. Only
	// after all triggers exist may the retained read-only metadata guard begin.
	conn, err := source.Conn(ctx)
	if err != nil {
		return spec, fmt.Errorf("reserve MySQL checkpoint metadata guard: %w", err)
	}
	guard := &mysqlJournalGuard{conn: conn, source: source, lock: state.Lock}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	mysqlJournalGuards.Lock()
	mysqlJournalGuards.entries[id] = guard
	mysqlJournalGuards.Unlock()
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&state.ConnectionID); err != nil {
		return spec, err
	}
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", state.Lock).Scan(&acquired); err != nil || !acquired.Valid || acquired.Int64 != 1 {
		return spec, fmt.Errorf("acquire MySQL checkpoint metadata guard: %w", nonNilMySQLJournalError(err, "guard lock is already held"))
	}
	// The operation context ends when Start returns. The retained guard has its
	// own lifetime and is explicitly released by Stop or cleanup instead.
	guard.tx, err = conn.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
	if err != nil {
		return spec, fmt.Errorf("begin MySQL checkpoint metadata guard: %w", err)
	}
	for _, plan := range plans {
		rows, err := guard.tx.QueryContext(ctx, "SELECT 1 FROM "+mysqlJournalObject(state.Schema, plan.Name)+" LIMIT 0")
		if err != nil {
			return spec, fmt.Errorf("guard MySQL checkpoint table %s: %w", plan.Name, err)
		}
		if err := rows.Close(); err != nil {
			return spec, err
		}
		if err := validateMySQLJournalTable(ctx, source, state.Schema, plan); err != nil {
			return spec, err
		}
		fresh, err := inspectMySQLTable(ctx, source, plan.Name)
		if err != nil {
			return spec, fmt.Errorf("revalidate MySQL checkpoint table %s: %w", plan.Name, err)
		}
		_, expected, _ := tableSchemaPayload(plan)
		_, actual, _ := tableSchemaPayload(fresh)
		if !bytes.Equal(expected, actual) {
			return spec, fmt.Errorf("MySQL table %s changed while installing the checkpoint; review and start again", plan.Name)
		}
	}
	for _, trigger := range state.Triggers {
		if exists, err := mysqlJournalVerifyTrigger(ctx, source, state, trigger); err != nil || !exists {
			return spec, fmt.Errorf("verify MySQL checkpoint trigger %s: %w", trigger.Name, nonNilMySQLJournalError(err, "trigger disappeared during installation"))
		}
	}
	if err := mysqlJournalVerifyOwnership(ctx, source, state); err != nil {
		return spec, err
	}
	// Each trigger takes a transaction-duration shared lock on this row, even
	// while inactive. This exclusive update drains preceding writers and gives
	// all selected tables one commit boundary without scanning their data.
	if _, err := source.ExecContext(ctx, "UPDATE "+gate+" SET active=TRUE, phase='recording' WHERE id=1"); err != nil {
		return spec, fmt.Errorf("activate MySQL checkpoint journal: %w", err)
	}
	return mysqlJournalSave(spec, state), nil
}

func validateMySQLJournalTable(ctx context.Context, source *sql.DB, schema string, plan TablePlan) error {
	var engine, tableType string
	if err := source.QueryRowContext(ctx, `SELECT COALESCE(engine,''), table_type FROM information_schema.tables WHERE table_schema=? AND table_name=?`, schema, plan.Name).Scan(&engine, &tableType); err != nil {
		return fmt.Errorf("inspect MySQL checkpoint table %s: %w", plan.Name, err)
	}
	if !strings.EqualFold(engine, "InnoDB") || tableType != "BASE TABLE" {
		return fmt.Errorf("MySQL journal requires InnoDB base tables; %s uses %s", plan.Name, engine)
	}
	var cascades int
	if err := source.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.referential_constraints WHERE constraint_schema=? AND table_name=? AND (update_rule IN ('CASCADE','SET NULL') OR delete_rule IN ('CASCADE','SET NULL'))`, schema, plan.Name).Scan(&cascades); err != nil {
		return fmt.Errorf("inspect MySQL cascading foreign keys for %s: %w", plan.Name, err)
	}
	if cascades != 0 {
		return fmt.Errorf("MySQL table %s has cascading foreign keys: MySQL does not fire row triggers for cascading changes; use snapshot comparison", plan.Name)
	}
	return nil
}

func nonNilMySQLJournalError(err error, message string) error {
	if err != nil {
		return err
	}
	return errors.New(message)
}

func freezeMySQLJournal(ctx context.Context, source *sql.DB, spec JournalSpec) (JournalSpec, error) {
	state, err := mysqlJournalDecode(spec)
	if err != nil || spec.Frozen {
		return spec, err
	}
	if err := mysqlJournalVerifyOwnership(ctx, source, state); err != nil {
		return spec, err
	}
	var phase string
	if err := source.QueryRowContext(ctx, "SELECT phase FROM "+mysqlJournalObject(state.Schema, state.Gate)+" WHERE id=1").Scan(&phase); err != nil {
		return spec, fmt.Errorf("read MySQL checkpoint journal boundary: %w", err)
	}
	if phase == "frozen" {
		spec.Frozen = true
		return spec, nil
	}
	mysqlJournalGuards.Lock()
	guard := mysqlJournalGuards.entries[spec.ID]
	mysqlJournalGuards.Unlock()
	var holder sql.NullInt64
	if guard == nil {
		return spec, fmt.Errorf("MySQL checkpoint metadata guard was lost after disconnect or restart; completeness cannot be established; clean up this journal and start again")
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if err := source.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", state.Lock).Scan(&holder); err != nil {
		return spec, fmt.Errorf("check MySQL checkpoint guard: %w", err)
	}
	if !holder.Valid || holder.Int64 != state.ConnectionID {
		return spec, fmt.Errorf("MySQL checkpoint metadata guard connection was lost; completeness cannot be established; clean up this journal and start again")
	}
	if _, err := source.ExecContext(ctx, "UPDATE "+mysqlJournalObject(state.Schema, state.Gate)+" SET active=FALSE, phase='stopping' WHERE id=1"); err != nil {
		return spec, fmt.Errorf("freeze MySQL checkpoint journal: %w", err)
	}
	// A failed guard must never be reported as a complete journal, even if the
	// gate update succeeded. Successful commit releases the DDL metadata locks.
	if err := guard.tx.Commit(); err != nil {
		return spec, fmt.Errorf("MySQL checkpoint metadata guard ended unexpectedly; completeness cannot be established: %w", err)
	}
	guard.tx = nil
	// Only mark the boundary durable after a successful guard commit proves DDL
	// stayed excluded through the gate drain. A crash in this narrow gap fails
	// conservatively rather than claiming an unproven complete report.
	if _, err := source.ExecContext(ctx, "UPDATE "+mysqlJournalObject(state.Schema, state.Gate)+" SET phase='frozen' WHERE id=1 AND active=FALSE"); err != nil {
		mysqlJournalReleaseGuardLocked(ctx, guard, state.Lock)
		mysqlJournalGuards.Lock()
		delete(mysqlJournalGuards.entries, spec.ID)
		mysqlJournalGuards.Unlock()
		return spec, fmt.Errorf("save MySQL checkpoint stop boundary: %w", err)
	}
	mysqlJournalReleaseGuardLocked(ctx, guard, state.Lock)
	mysqlJournalGuards.Lock()
	delete(mysqlJournalGuards.entries, spec.ID)
	mysqlJournalGuards.Unlock()
	spec.Frozen = true
	return spec, nil
}

func mysqlJournalReleaseGuard(ctx context.Context, guard *mysqlJournalGuard, lock string) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	mysqlJournalReleaseGuardLocked(ctx, guard, lock)
}

func mysqlJournalReleaseGuardLocked(ctx context.Context, guard *mysqlJournalGuard, lock string) {
	if guard.tx != nil {
		_ = guard.tx.Rollback()
	}
	if _, err := guard.conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", lock); err != nil {
		// Returning a connection with a named lock to the pool would leak the
		// lock. Discard it if release failed; server disconnect releases locks.
		_ = guard.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	_ = guard.conn.Close()
}

// releaseMySQLJournalGuards releases reserved connections on an explicit source
// disconnect. Source objects remain available for recovery cleanup; subsequent
// Stop refuses an unproven complete interval after the metadata guard is lost.
func releaseMySQLJournalGuards(ctx context.Context, source *sql.DB) {
	var guards []*mysqlJournalGuard
	mysqlJournalGuards.Lock()
	for id, guard := range mysqlJournalGuards.entries {
		if source == nil || guard.source == source {
			guards = append(guards, guard)
			delete(mysqlJournalGuards.entries, id)
		}
	}
	mysqlJournalGuards.Unlock()
	for _, guard := range guards {
		mysqlJournalReleaseGuard(ctx, guard, guard.lock)
	}
}

func readMySQLJournal(ctx context.Context, source *sql.DB, spec JournalSpec, emit func(JournalEvent) error) error {
	if !spec.Frozen {
		return fmt.Errorf("freeze the MySQL checkpoint journal before reading its changes")
	}
	state, err := mysqlJournalDecode(spec)
	if err != nil {
		return err
	}
	if err := mysqlJournalVerifyOwnership(ctx, source, state); err != nil {
		return err
	}
	rows, err := source.QueryContext(ctx, "SELECT seq,table_name,before_data,after_data FROM "+mysqlJournalObject(state.Schema, state.Events)+" ORDER BY seq")
	if err != nil {
		return fmt.Errorf("read MySQL checkpoint journal: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var event JournalEvent
		if err := rows.Scan(&event.Sequence, &event.Table, &event.Before, &event.After); err != nil {
			return err
		}
		for _, image := range []*[]byte{&event.Before, &event.After} {
			if len(*image) == 0 {
				continue
			}
			var row encodedRow
			if err := json.Unmarshal(*image, &row); err != nil {
				return fmt.Errorf("decode MySQL checkpoint row %d: %w", event.Sequence, err)
			}
			*image, _, err = encodeRow(row)
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

func cleanupMySQLJournal(ctx context.Context, source *sql.DB, spec JournalSpec) error {
	state, err := mysqlJournalDecode(spec)
	if err != nil {
		return err
	}
	if !state.EventsMade && !state.GateMade {
		return nil
	}
	// Release our retained metadata locks before dropping trigger definitions.
	mysqlJournalGuards.Lock()
	guard := mysqlJournalGuards.entries[spec.ID]
	delete(mysqlJournalGuards.entries, spec.ID)
	mysqlJournalGuards.Unlock()
	if guard != nil {
		mysqlJournalReleaseGuard(ctx, guard, state.Lock)
	}
	if err := mysqlJournalVerifyOwnership(ctx, source, state); err != nil {
		return err
	}
	// Drop triggers first: removing a live gate or event table would make source
	// writes fail. On any trigger cleanup error, leave both shared objects intact.
	for _, trigger := range state.Triggers {
		if _, err := mysqlJournalVerifyTrigger(ctx, source, state, trigger); err != nil {
			return err
		}
		if _, err := source.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+mysqlJournalObject(state.Schema, trigger.Name)); err != nil {
			return fmt.Errorf("remove MySQL checkpoint trigger %s: %w", trigger.Name, err)
		}
	}
	if state.GateMade {
		if _, err := mysqlJournalVerifyTable(ctx, source, state, state.Gate, "gate", state.GateHash); err != nil {
			return err
		}
		if _, err := source.ExecContext(ctx, "DROP TABLE IF EXISTS "+mysqlJournalObject(state.Schema, state.Gate)); err != nil {
			return fmt.Errorf("remove MySQL checkpoint gate: %w", err)
		}
	}
	if state.EventsMade {
		if _, err := mysqlJournalVerifyTable(ctx, source, state, state.Events, "events", state.EventsHash); err != nil {
			return err
		}
		if _, err := source.ExecContext(ctx, "DROP TABLE IF EXISTS "+mysqlJournalObject(state.Schema, state.Events)); err != nil {
			return fmt.Errorf("remove MySQL checkpoint journal: %w", err)
		}
	}
	return nil
}

// mysqlJournalText uses a character-set-qualified hexadecimal literal so names
// and type labels do not depend on the connection's NO_BACKSLASH_ESCAPES mode.
func mysqlJournalText(value string) string {
	if value == "" {
		return "CONVERT(X'' USING utf8mb4)"
	}
	return "CONVERT(X'" + hex.EncodeToString([]byte(value)) + "' USING utf8mb4)"
}

// mysqlJournalRowExpr encodes the source row directly into the same lossless
// cell envelope used by the local report. Numbers are kept as text rather than
// passing through a JSON/Go float64, and binary data is explicitly base64.
func mysqlJournalRowExpr(plan TablePlan, image string) string {
	cells := make([]string, 0, len(plan.Columns))
	for _, column := range plan.Columns {
		value := image + "." + quoteIdentifier(config.MySQL, column.Name)
		baseType := strings.ToLower(strings.TrimSpace(column.Type))
		if index := strings.IndexAny(baseType, "( "); index >= 0 {
			baseType = baseType[:index]
		}
		kind := "text"
		data := "CAST(" + value + " AS CHAR CHARACTER SET utf8mb4)"
		switch baseType {
		case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "bit", "geometry", "point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon", "geometrycollection", "vector":
			kind = "bytes"
			data = "REPLACE(TO_BASE64(CAST(" + value + " AS BINARY)), CHAR(10), '')"
		case "float", "double", "real":
			// MySQL's JSON numeric serializer preserves the floating point value;
			// an ordinary CAST to CHAR can round a FLOAT to fewer significant digits.
			data = "CAST(JSON_EXTRACT(JSON_ARRAY(" + value + "), '$[0]') AS CHAR CHARACTER SET utf8mb4)"
		case "timestamp":
			// The same TIMESTAMP can be written by clients in different time zones.
			// Epoch arithmetic produces a canonical UTC image without time zone
			// tables, and keeps zero dates distinct from the Unix epoch.
			data = "CASE WHEN UNIX_TIMESTAMP(" + value + ") = 0 THEN CAST(" + value + " AS CHAR CHARACTER SET utf8mb4) ELSE DATE_FORMAT(TIMESTAMPADD(MICROSECOND, ROUND(UNIX_TIMESTAMP(" + value + ") * 1000000), CAST('1970-01-01 00:00:00' AS DATETIME(6))), '%Y-%m-%dT%H:%i:%s.%fZ') END"
		}
		cells = append(cells, fmt.Sprintf("JSON_OBJECT('n', %s, 'k', IF(%s IS NULL, 'null', '%s'), 't', %s, 'v', IFNULL(%s, ''))",
			mysqlJournalText(column.Name), value, kind, mysqlJournalText(column.Type), data))
	}
	return "JSON_OBJECT('c', JSON_ARRAY(" + strings.Join(cells, ", ") + "))"
}
