package changeprofiler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/shreyam1008/dbterm/internal/config"
)

// Integration tests use an explicitly supplied disposable PostgreSQL database.
// Every test owns one schema and removes only that schema and its own journal.
func postgresJournalFixture(t *testing.T) (*sql.DB, string, TablePlan) {
	t.Helper()
	dsn := os.Getenv("DBTERM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set DBTERM_TEST_POSTGRES_DSN to run PostgreSQL journal integration tests")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(6)
	t.Cleanup(func() { _ = db.Close() })
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	schema := "dbterm_journal_test_" + strings.TrimPrefix(id, "anchor_")
	if _, err := db.Exec("CREATE SCHEMA " + quoteName(config.PostgreSQL, schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP SCHEMA " + quoteName(config.PostgreSQL, schema) + " CASCADE") })
	table := schema + ".rows"
	if _, err := db.Exec("CREATE TABLE " + quoteIdentifier(config.PostgreSQL, table) + ` (
		id bigint PRIMARY KEY, precise numeric(60,20), data bytea, doc json, note text,
		occurred timestamptz, enabled boolean, tags text[], nullable text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO "+quoteIdentifier(config.PostgreSQL, table)+` VALUES
		(1, 1234567890123456789012345678901234567890.12345678901234567890,
		decode('0001ff0080','hex'), '{ "n": 12345678901234567890, "x": [null, true] }', $1,
		'2026-01-02 03:04:05.123456+05:30', true, ARRAY['hello','comma,quote"','देवनागरी'], NULL)`, strings.Repeat("hello 👩🏽‍💻\n", 1000)); err != nil {
		t.Fatal(err)
	}
	plan, err := inspectPostgresTable(context.Background(), db, table)
	if err != nil {
		t.Fatal(err)
	}
	plan.Included = true
	return db, id, plan
}

func TestPostgresJournalCommittedWritesAndTypes(t *testing.T) {
	db, id, plan := postgresJournalFixture(t)
	ctx := context.Background()
	spec, err := installPostgresJournal(ctx, db, id, []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanupPostgresJournal(ctx, db, spec); err != nil {
			t.Error(err)
		}
	})
	var count int
	if err := db.QueryRow("SELECT count(*) FROM " + postgresJournalObject(id, "events")).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("Start copied %d source rows; expected an empty change journal", count)
	}
	if err := readPostgresJournal(ctx, db, spec, func(JournalEvent) error { return nil }); err == nil {
		t.Fatal("read accepted an active journal")
	}
	rolledBack, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rolledBack.Exec("INSERT INTO " + quoteIdentifier(config.PostgreSQL, plan.Name) + " (id,note) VALUES (99,'rolled back')"); err != nil {
		t.Fatal(err)
	}
	if err := rolledBack.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE " + quoteIdentifier(config.PostgreSQL, plan.Name) + " SET id=2,note='changed' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO " + quoteIdentifier(config.PostgreSQL, plan.Name) + " (id,note) VALUES (3,'temporary')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM " + quoteIdentifier(config.PostgreSQL, plan.Name) + " WHERE id=3"); err != nil {
		t.Fatal(err)
	}
	spec, err = freezePostgresJournal(ctx, db, spec)
	if err != nil {
		t.Fatal(err)
	}
	// A lost freeze acknowledgement is retried using the original local spec.
	stale := spec
	stale.Frozen = false
	if retry, err := freezePostgresJournal(ctx, db, stale); err != nil || !retry.Frozen {
		t.Fatalf("retry freeze: %+v, %v", retry, err)
	}
	if _, err := db.Exec("INSERT INTO " + quoteIdentifier(config.PostgreSQL, plan.Name) + " (id,note) VALUES (4,'after Stop')"); err != nil {
		t.Fatal(err)
	}
	var events []JournalEvent
	if err := readPostgresJournal(ctx, db, spec, func(event JournalEvent) error { events = append(events, event); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d committed events, want 3", len(events))
	}
	before, err := decodeRow(events[0].Before)
	if err != nil {
		t.Fatal(err)
	}
	after, err := decodeRow(events[0].After)
	if err != nil {
		t.Fatal(err)
	}
	if before["id"].Text != "1" || after["id"].Text != "2" {
		t.Fatalf("primary key change lost: %v -> %v", before["id"], after["id"])
	}
	if before["precise"].Text != "1234567890123456789012345678901234567890.12345678901234567890" {
		t.Fatalf("numeric precision lost: %v", before["precise"])
	}
	if before["data"].Text != "0x0001ff0080" {
		t.Fatalf("bytes lost: %v", before["data"])
	}
	if before["doc"].Text != `{ "n": 12345678901234567890, "x": [null, true] }` {
		t.Fatalf("JSON text lost: %v", before["doc"])
	}
	if !before["nullable"].Null || before["enabled"].Text != "true" {
		t.Fatalf("null/bool lost: %v/%v", before["nullable"], before["enabled"])
	}
	if !strings.Contains(before["note"].Text, "👩🏽‍💻") || len(before["note"].Text) < 1000 {
		t.Fatal("large Unicode text was truncated")
	}
	if err := cleanupPostgresJournal(ctx, db, spec); err != nil {
		t.Fatal(err)
	}
	if err := cleanupPostgresJournal(ctx, db, spec); err != nil {
		t.Fatalf("idempotent cleanup: %v", err)
	}
	if err := db.QueryRow("SELECT count(*) FROM " + quoteIdentifier(config.PostgreSQL, plan.Name)).Scan(&count); err != nil || count != 2 {
		t.Fatalf("cleanup changed application rows: count=%d err=%v", count, err)
	}
}

func TestPostgresJournalTruncateWaitsForRecordingToStop(t *testing.T) {
	db, id, plan := postgresJournalFixture(t)
	ctx := context.Background()
	spec, err := installPostgresJournal(ctx, db, id, []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanupPostgresJournal(ctx, db, spec); err != nil {
			t.Error(err)
		}
	})
	done := make(chan error, 1)
	truncateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	go func() {
		_, err := db.ExecContext(truncateCtx, "TRUNCATE "+quoteIdentifier(config.PostgreSQL, plan.Name))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("TRUNCATE bypassed the live DDL guard: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	spec, err = freezePostgresJournal(ctx, db, spec)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TRUNCATE remained blocked after Stop")
	}
	count := 0
	if err := readPostgresJournal(ctx, db, spec, func(event JournalEvent) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("TRUNCATE executed after Stop entered the frozen checkpoint: %d events", count)
	}
}

func TestPostgresJournalSchemaChangesWaitForStop(t *testing.T) {
	db, id, plan := postgresJournalFixture(t)
	ctx := context.Background()
	spec, err := installPostgresJournal(ctx, db, id, []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanupPostgresJournal(ctx, db, spec); err != nil {
			t.Error(err)
		}
	})
	postgresJournalAssertDDLBlocked(t, db, "ALTER TABLE "+quoteIdentifier(config.PostgreSQL, plan.Name)+" ADD COLUMN unexpected text")
	spec, err = freezePostgresJournal(ctx, db, spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE " + quoteIdentifier(config.PostgreSQL, plan.Name) + " ADD COLUMN unexpected text"); err != nil {
		t.Fatalf("DDL remained blocked after Stop: %v", err)
	}
}

func TestPostgresJournalStopWaitsForCommittedWriter(t *testing.T) {
	db, id, plan := postgresJournalFixture(t)
	ctx := context.Background()
	spec, err := installPostgresJournal(ctx, db, id, []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanupPostgresJournal(ctx, db, spec); err != nil {
			t.Error(err)
		}
	})
	writer, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.Exec("UPDATE " + quoteIdentifier(config.PostgreSQL, plan.Name) + " SET note='committed at Stop' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		var freezeErr error
		spec, freezeErr = freezePostgresJournal(ctx, db, spec)
		finished <- freezeErr
	}()
	select {
	case err := <-finished:
		t.Fatalf("Stop did not wait for open writer: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not finish after writer commit")
	}
	count := 0
	if err := readPostgresJournal(ctx, db, spec, func(event JournalEvent) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed writer missing: got %d events", count)
	}
}

func TestPostgresJournalWideRowsAndSQLNames(t *testing.T) {
	plan := TablePlan{Name: "public.wide"}
	for i := 0; i < 250; i++ {
		plan.Columns = append(plan.Columns, Column{Name: fmt.Sprintf("column %d '\\", i), Type: "text"})
	}
	expression := postgresJournalRow(plan, "NEW")
	if strings.Count(expression, "pg_catalog.jsonb_build_array(") != 4 {
		t.Fatal("wide row did not chunk PostgreSQL function arguments")
	}
	if !strings.Contains(expression, `NEW."column 0 '\"`) {
		t.Fatal("column name was not SQL identifier quoted")
	}
	var state postgresJournalState
	payload, _ := json.Marshal(postgresJournalState{Schema: "wrong", Tables: map[string]postgresJournalTableState{}})
	_, err := postgresJournalStateFor(JournalSpec{Version: 1, ID: "anchor_foo", Engine: config.PostgreSQL, State: payload})
	if err == nil {
		t.Fatalf("invalid state accepted: %+v", state)
	}
}

func TestPostgresJournalTriggerDisableBlockedThroughoutRecording(t *testing.T) {
	db, id, plan := postgresJournalFixture(t)
	ctx := context.Background()
	spec, err := installPostgresJournal(ctx, db, id, []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanupPostgresJournal(ctx, db, spec); err != nil {
			t.Error(err)
		}
	})
	var name string
	if err := db.QueryRow("SELECT r.tgname FROM " + postgresJournalObject(id, "tables") + " m JOIN pg_catalog.pg_trigger r ON r.oid=m.row_trigger").Scan(&name); err != nil {
		t.Fatal(err)
	}
	postgresJournalAssertDDLBlocked(t, db, "ALTER TABLE "+quoteIdentifier(config.PostgreSQL, plan.Name)+" DISABLE TRIGGER "+quoteName(config.PostgreSQL, name))
	if _, err := db.Exec("UPDATE " + quoteIdentifier(config.PostgreSQL, plan.Name) + " SET note='captured despite attempted disable' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	spec, err = freezePostgresJournal(ctx, db, spec)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := readPostgresJournal(ctx, db, spec, func(event JournalEvent) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("attempted trigger disable lost a write: %d events", count)
	}
}

func TestPostgresJournalPersistsIntentBeforeSourceChanges(t *testing.T) {
	db, id, plan := postgresJournalFixture(t)
	called := 0
	ctx := context.WithValue(context.Background(), journalRecorderKey{}, func(spec JournalSpec) error {
		called++
		var exists bool
		if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname=$1)", postgresJournalSchema(id)).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatal("source changes became visible before install intent was durable")
		}
		if _, err := postgresJournalStateFor(spec); err != nil {
			t.Fatal(err)
		}
		return fmt.Errorf("fixture durable-state failure")
	})
	spec, err := installPostgresJournal(ctx, db, id, []TablePlan{plan})
	if err == nil || called != 1 {
		t.Fatalf("expected durable-state error before source DDL: count=%d err=%v", called, err)
	}
	if err := cleanupPostgresJournal(context.Background(), db, spec); err != nil {
		t.Fatalf("cleanup of uninstalled journal: %v", err)
	}
}

func TestPostgresJournalMillionRowsRecordsOnlyWrites(t *testing.T) {
	db, id, fixture := postgresJournalFixture(t)
	ctx := context.Background()
	schema, _ := splitQualified(fixture.Name, "public")
	name := schema + ".million"
	if _, err := db.Exec("CREATE TABLE " + quoteIdentifier(config.PostgreSQL, name) + " (id bigint PRIMARY KEY,note text)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO " + quoteIdentifier(config.PostgreSQL, name) + " SELECT n, md5(n::text) FROM generate_series(1,1000000) AS n"); err != nil {
		t.Fatal(err)
	}
	plan, err := inspectPostgresTable(ctx, db, name)
	if err != nil {
		t.Fatal(err)
	}
	plan.Included = true
	started := time.Now()
	spec, err := installPostgresJournal(ctx, db, id, []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	startDuration := time.Since(started)
	t.Cleanup(func() {
		if err := cleanupPostgresJournal(ctx, db, spec); err != nil {
			t.Error(err)
		}
	})
	var count int
	if err := db.QueryRow("SELECT count(*) FROM " + postgresJournalObject(id, "events")).Scan(&count); err != nil || count != 0 {
		t.Fatalf("Start copied rows: count=%d err=%v", count, err)
	}
	if _, err := db.Exec("UPDATE " + quoteIdentifier(config.PostgreSQL, name) + " SET note='one changed row' WHERE id=500000"); err != nil {
		t.Fatal(err)
	}
	started = time.Now()
	spec, err = freezePostgresJournal(ctx, db, spec)
	if err != nil {
		t.Fatal(err)
	}
	stopDuration := time.Since(started)
	eventCount := 0
	if err := readPostgresJournal(ctx, db, spec, func(event JournalEvent) error { eventCount++; return nil }); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("expected one changed row, got %d events for million-row table", eventCount)
	}
	t.Logf("1,000,000 PostgreSQL rows: Start %s, Stop %s; zero baseline rows, one journal event", startDuration, stopDuration)
}

func TestPostgresJournalAppWriterCannotReadPrivateImages(t *testing.T) {
	db, id, plan := postgresJournalFixture(t)
	ctx := context.Background()
	var canCreateRole bool
	if err := db.QueryRow("SELECT rolcreaterole OR rolsuper FROM pg_catalog.pg_roles WHERE rolname=current_user").Scan(&canCreateRole); err != nil {
		t.Fatal(err)
	}
	if !canCreateRole {
		t.Skip("test fixture role cannot create a disposable application role")
	}
	role := "dbterm_app_" + strings.TrimPrefix(id, "anchor_")
	quotedRole := quoteName(config.PostgreSQL, role)
	if _, err := db.Exec("CREATE ROLE " + quotedRole); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DROP OWNED BY " + quotedRole); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec("DROP ROLE " + quotedRole); err != nil {
			t.Error(err)
		}
	})
	schema, _ := splitQualified(plan.Name, "public")
	if _, err := db.Exec("GRANT USAGE ON SCHEMA " + quoteName(config.PostgreSQL, schema) + " TO " + quotedRole); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("GRANT SELECT, UPDATE ON " + quoteIdentifier(config.PostgreSQL, plan.Name) + " TO " + quotedRole); err != nil {
		t.Fatal(err)
	}
	spec, err := installPostgresJournal(ctx, db, id, []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanupPostgresJournal(ctx, db, spec); err != nil {
			t.Error(err)
		}
	})
	app, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if _, err := app.ExecContext(ctx, "SET ROLE "+quotedRole); err != nil {
		t.Fatal(err)
	}
	defer app.ExecContext(ctx, "RESET ROLE")
	if _, err := app.ExecContext(ctx, "UPDATE "+quoteIdentifier(config.PostgreSQL, plan.Name)+" SET note='application write' WHERE id=1"); err != nil {
		t.Fatalf("private trigger broke application write: %v", err)
	}
	if _, err := app.ExecContext(ctx, "SELECT * FROM "+postgresJournalObject(id, "events")); err == nil {
		t.Fatal("application role could read private checkpoint row images")
	}
	spec, err = freezePostgresJournal(ctx, db, spec)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := readPostgresJournal(ctx, db, spec, func(event JournalEvent) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("application role write was not journaled: %d", count)
	}
}

func postgresJournalAssertDDLBlocked(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SET LOCAL lock_timeout='100ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(statement); err == nil || !strings.Contains(err.Error(), "lock timeout") {
		t.Fatalf("DDL was not blocked by the checkpoint: %v", err)
	}
}

func TestPostgresJournalLostGuardRejectsDisableUpdateReenableGap(t *testing.T) {
	db, id, plan := postgresJournalFixture(t)
	ctx := context.Background()
	spec, err := installPostgresJournal(ctx, db, id, []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanupPostgresJournal(ctx, db, spec); err != nil {
			t.Error(err)
		}
	})
	var name string
	if err := db.QueryRow("SELECT r.tgname FROM " + postgresJournalObject(id, "tables") + " m JOIN pg_catalog.pg_trigger r ON r.oid=m.row_trigger").Scan(&name); err != nil {
		t.Fatal(err)
	}
	releasePostgresJournalGuards(ctx, db)
	for _, statement := range []string{
		"ALTER TABLE " + quoteIdentifier(config.PostgreSQL, plan.Name) + " DISABLE TRIGGER " + quoteName(config.PostgreSQL, name),
		"UPDATE " + quoteIdentifier(config.PostgreSQL, plan.Name) + " SET note='unlogged write' WHERE id=1",
		"ALTER TABLE " + quoteIdentifier(config.PostgreSQL, plan.Name) + " ENABLE ALWAYS TRIGGER " + quoteName(config.PostgreSQL, name),
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := freezePostgresJournal(ctx, db, spec); err == nil || !strings.Contains(err.Error(), "lost its live DDL guard") {
		t.Fatalf("unlogged disable/update/re-enable gap was accepted as complete: %v", err)
	}
}

func TestPostgresJournalStartContextDoesNotOwnGuardLifetime(t *testing.T) {
	db, id, plan := postgresJournalFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	spec, err := installPostgresJournal(ctx, db, id, []TablePlan{plan})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	t.Cleanup(func() {
		if err := cleanupPostgresJournal(context.Background(), db, spec); err != nil {
			t.Error(err)
		}
	})
	postgresJournalAssertDDLBlocked(t, db, "ALTER TABLE "+quoteIdentifier(config.PostgreSQL, plan.Name)+" DISABLE TRIGGER ALL")
	if _, err := db.Exec("UPDATE " + quoteIdentifier(config.PostgreSQL, plan.Name) + " SET note='after Start context ended' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := freezePostgresJournal(context.Background(), db, spec); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresJournalPreStartRepeatableReadWriterMustRetry(t *testing.T) {
	db, id, plan := postgresJournalFixture(t)
	ctx := context.Background()
	writer, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	var note string
	if err := writer.QueryRow("SELECT note FROM " + quoteIdentifier(config.PostgreSQL, plan.Name) + " WHERE id=1").Scan(&note); err != nil {
		t.Fatal(err)
	}
	spec, err := installPostgresJournal(ctx, db, id, []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanupPostgresJournal(ctx, db, spec); err != nil {
			t.Error(err)
		}
	})
	if _, err := writer.Exec("UPDATE " + quoteIdentifier(config.PostgreSQL, plan.Name) + " SET note='old snapshot writer' WHERE id=1"); err == nil || !strings.Contains(err.Error(), "predates checkpoint") {
		t.Fatalf("pre-Start repeatable-read writer could skip the gate: %v", err)
	}
	if err := writer.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := freezePostgresJournal(ctx, db, spec); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresJournalWriterTimeoutRetainsGuardForRetry(t *testing.T) {
	db, id, plan := postgresJournalFixture(t)
	ctx := context.Background()
	spec, err := installPostgresJournal(ctx, db, id, []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanupPostgresJournal(ctx, db, spec); err != nil {
			t.Error(err)
		}
	})
	writer, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.Exec("UPDATE " + quoteIdentifier(config.PostgreSQL, plan.Name) + " SET note='long transaction' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := freezePostgresJournal(ctx, db, spec); err == nil || !strings.Contains(err.Error(), "lock timeout") {
		t.Fatalf("Stop did not time out on the unfinished writer: %v", err)
	}
	postgresJournalAssertDDLBlocked(t, db, "ALTER TABLE "+quoteIdentifier(config.PostgreSQL, plan.Name)+" DISABLE TRIGGER ALL")
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	spec, err = freezePostgresJournal(ctx, db, spec)
	if err != nil {
		t.Fatalf("Stop could not retry after releasing the writer: %v", err)
	}
	count := 0
	if err := readPostgresJournal(ctx, db, spec, func(event JournalEvent) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("long transaction missing after retry: %d events", count)
	}
}

func TestPostgresJournalKilledGuardCannotProduceCompleteReport(t *testing.T) {
	db, id, plan := postgresJournalFixture(t)
	ctx := context.Background()
	spec, err := installPostgresJournal(ctx, db, id, []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanupPostgresJournal(ctx, db, spec); err != nil {
			t.Error(err)
		}
	})
	var state postgresJournalState
	if err := json.Unmarshal(spec.State, &state); err != nil {
		t.Fatal(err)
	}
	var terminated bool
	if err := db.QueryRow("SELECT pg_catalog.pg_terminate_backend($1)", state.GuardPID).Scan(&terminated); err != nil {
		t.Skipf("test role cannot terminate its own guard backend: %v", err)
	}
	if !terminated {
		t.Fatal("fixture guard was not terminated")
	}
	if _, err := freezePostgresJournal(ctx, db, spec); err == nil {
		t.Fatal("terminated DDL guard produced an apparently complete checkpoint")
	}
}

func TestPostgresJournalMoneyAndByteaOutputIgnoreWriterFormatting(t *testing.T) {
	db, id, fixture := postgresJournalFixture(t)
	ctx := context.Background()
	schema, _ := splitQualified(fixture.Name, "public")
	domain := quoteName(config.PostgreSQL, schema) + `.binary_value`
	if _, err := db.Exec("CREATE DOMAIN " + domain + " AS bytea"); err != nil {
		t.Fatal(err)
	}
	table := schema + ".formatting"
	quoted := quoteIdentifier(config.PostgreSQL, table)
	if _, err := db.Exec("CREATE TABLE " + quoted + " (id bigint PRIMARY KEY, amount money, binary_domain " + domain + ", binary_array bytea[])"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO " + quoted + ` VALUES(1,1234.56::numeric::money,decode('005cff807f','hex'),ARRAY[decode('005cff807f','hex')])`); err != nil {
		t.Fatal(err)
	}
	plan, err := inspectPostgresTable(ctx, db, table)
	if err != nil {
		t.Fatal(err)
	}
	plan.Included = true
	spec, err := installPostgresJournal(ctx, db, id, []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanupPostgresJournal(ctx, db, spec); err != nil {
			t.Error(err)
		}
	})
	writer, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, `SET lc_monetary='C'`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `SET bytea_output='hex'`); err != nil {
		t.Fatal(err)
	}
	var initialMoney, initialBinary string
	if err := writer.QueryRowContext(ctx, "SELECT amount::text,binary_array::text FROM "+quoted+" WHERE id=1").Scan(&initialMoney, &initialBinary); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, "UPDATE "+quoted+" SET id=id WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	localeChanged := false
	for _, locale := range []string{"de_DE.UTF-8", "de_DE.utf8", "fr_FR.UTF-8", "en_IN.UTF-8", "en_US.utf8", "C.utf8"} {
		if _, err := writer.ExecContext(ctx, "SET lc_monetary="+postgresLiteral(locale)); err != nil {
			continue
		}
		var current string
		if err := writer.QueryRowContext(ctx, "SELECT amount::text FROM "+quoted+" WHERE id=1").Scan(&current); err != nil {
			t.Fatal(err)
		}
		if current != initialMoney {
			localeChanged = true
			t.Logf("writer monetary output differs: %q -> %q", initialMoney, current)
			break
		}
	}
	if !localeChanged {
		t.Log("fixture has no locale with different monetary formatting; deterministic function GUCs and bytea variation are still checked")
	}
	if _, err := writer.ExecContext(ctx, `SET bytea_output='escape'`); err != nil {
		t.Fatal(err)
	}
	var changedBinary string
	if err := writer.QueryRowContext(ctx, "SELECT binary_array::text FROM "+quoted+" WHERE id=1").Scan(&changedBinary); err != nil {
		t.Fatal(err)
	}
	if changedBinary == initialBinary {
		t.Fatal("fixture did not exercise different bytea array formatting")
	}
	if _, err := writer.ExecContext(ctx, "UPDATE "+quoted+" SET id=id WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	var fixed int
	if err := db.QueryRow(`SELECT count(*) FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND p.proname='capture_0' AND 'lc_monetary=C'=ANY(p.proconfig) AND 'bytea_output=hex'=ANY(p.proconfig)`, postgresJournalSchema(id)).Scan(&fixed); err != nil || fixed != 1 {
		t.Fatalf("capture function does not fix formatting GUCs: count=%d err=%v", fixed, err)
	}
	spec, err = freezePostgresJournal(ctx, db, spec)
	if err != nil {
		t.Fatal(err)
	}
	var events []JournalEvent
	if err := readPostgresJournal(ctx, db, spec, func(event JournalEvent) error { events = append(events, event); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected two no-op writes, got %d", len(events))
	}
	for _, event := range events {
		if !bytes.Equal(events[0].Before, event.Before) || !bytes.Equal(events[0].Before, event.After) {
			t.Fatal("writer formatting changed unchanged row images; journal continuity would fail")
		}
	}
	before, err := decodeRow(events[0].Before)
	if err != nil {
		t.Fatal(err)
	}
	if before["amount"].Text != initialMoney || before["binary_array"].Text != initialBinary || before["binary_domain"].Text != "0x005cff807f" {
		t.Fatalf("canonical money/binary images were lost: %+v", before)
	}
}
