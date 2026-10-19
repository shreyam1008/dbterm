package changeprofiler

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func mysqlJournalFixture(t *testing.T, definition string) (*sql.DB, TablePlan) {
	t.Helper()
	dsn := os.Getenv("DBTERM_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set DBTERM_TEST_MYSQL_DSN to an expendable MySQL test database")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	name := fmt.Sprintf("journal_fixture_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE TABLE `" + name + "` (" + definition + ") ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS `" + name + "`") })
	plan, err := inspectMySQLTable(context.Background(), db, name)
	if err != nil {
		t.Fatal(err)
	}
	plan.Included = true
	return db, plan
}

func installMySQLFixture(t *testing.T, db *sql.DB, plan TablePlan) JournalSpec {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	spec, err := installMySQLJournal(ctx, db, fmt.Sprintf("mysql_fixture_%d", time.Now().UnixNano()), []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := cleanupMySQLJournal(ctx, db, spec); err != nil {
			t.Error(err)
		}
	})
	return spec
}

func TestMySQLJournalExactImagesAndTransactions(t *testing.T) {
	db, plan := mysqlJournalFixture(t, "id BIGINT UNSIGNED PRIMARY KEY, amount DECIMAL(30,12), payload LONGBLOB, note LONGTEXT, happened TIMESTAMP(6) NULL, ratio DOUBLE, nothing VARCHAR(10)")
	spec := installMySQLFixture(t, db, plan)
	blob := []byte{0, 255, 128, '\n', '\r', 1}
	note := strings.Repeat("नमस्ते 👩🏽‍💻\n", 300)
	if _, err := db.Exec("INSERT INTO `"+plan.Name+"` VALUES (18446744073709551615, 123456789012345678.123456789012, ?, ?, '2024-01-02 03:04:05.123456', 0.12345678901234567, NULL)", blob, note); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE `" + plan.Name + "` SET note='rolled back'"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	writer, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(context.Background(), "SET SESSION time_zone='+05:30'"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(context.Background(), "UPDATE `"+plan.Name+"` SET id=1, amount=-0.000000000001, note='', payload=?", []byte{}); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	if _, err := db.Exec("DELETE FROM `" + plan.Name + "` WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	spec, err = freezeMySQLJournal(context.Background(), db, spec)
	if err != nil {
		t.Fatal(err)
	}
	var events []JournalEvent
	if err := readMySQLJournal(context.Background(), db, spec, func(event JournalEvent) error { events = append(events, event); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want insert/update/delete with rollback excluded", len(events))
	}
	var first encodedRow
	if err := json.Unmarshal(events[0].After, &first); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"id": "18446744073709551615", "amount": "123456789012345678.123456789012", "payload": base64.StdEncoding.EncodeToString(blob), "note": note, "happened": "2024-01-02T03:04:05.123456Z", "ratio": "0.12345678901234566", "nothing": ""}
	for _, cell := range first.Cells {
		if got, exists := want[cell.Name]; exists && cell.Data != got {
			t.Errorf("%s = %q, want %q", cell.Name, cell.Data, got)
		}
		if cell.Name == "nothing" && cell.Kind != "null" {
			t.Errorf("NULL cell kind = %q", cell.Kind)
		}
		if cell.Name == "payload" && cell.Kind != "bytes" {
			t.Errorf("BLOB cell kind = %q", cell.Kind)
		}
	}
	if string(events[0].After) != string(events[1].Before) || string(events[1].After) != string(events[2].Before) {
		t.Fatal("before and after images do not form an exact change chain")
	}
	// A locally unacknowledged Stop can recover the durable source boundary.
	spec.Frozen = false
	if recovered, err := freezeMySQLJournal(context.Background(), db, spec); err != nil || !recovered.Frozen {
		t.Fatalf("recover committed stop boundary: frozen=%v err=%v", recovered.Frozen, err)
	}
}

func TestMySQLJournalStopWaitsForCommit(t *testing.T) {
	db, plan := mysqlJournalFixture(t, "id INT PRIMARY KEY, value TEXT")
	spec := installMySQLFixture(t, db, plan)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO `" + plan.Name + "` VALUES (1,'uncommitted')"); err != nil {
		t.Fatal(err)
	}
	type result struct {
		spec JournalSpec
		err  error
	}
	finished := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { frozen, err := freezeMySQLJournal(ctx, db, spec); finished <- result{frozen, err} }()
	select {
	case got := <-finished:
		t.Fatalf("Stop returned before an active transaction committed: %v", got.err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := <-finished
	if got.err != nil {
		t.Fatal(got.err)
	}
	var count int
	if err := readMySQLJournal(ctx, db, got.spec, func(event JournalEvent) error { count++; return nil }); err != nil || count != 1 {
		t.Fatalf("read committed event: count=%d err=%v", count, err)
	}
}

func TestMySQLJournalPreventsUncapturedDDL(t *testing.T) {
	db, plan := mysqlJournalFixture(t, "id INT PRIMARY KEY, value TEXT")
	spec := installMySQLFixture(t, db, plan)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE `"+plan.Name+"`"); err == nil {
		t.Fatal("TRUNCATE succeeded while the checkpoint metadata guard should prevent unlogged DDL")
	}
	if _, err := freezeMySQLJournal(context.Background(), db, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("TRUNCATE TABLE `" + plan.Name + "`"); err != nil {
		t.Fatalf("metadata guard remained after Stop: %v", err)
	}
}

func TestMySQLJournalRejectsLostMetadataGuard(t *testing.T) {
	db, plan := mysqlJournalFixture(t, "id INT PRIMARY KEY")
	spec := installMySQLFixture(t, db, plan)
	state, err := mysqlJournalDecode(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf("KILL CONNECTION %d", state.ConnectionID)); err != nil {
		t.Fatal(err)
	}
	if _, err := freezeMySQLJournal(context.Background(), db, spec); err == nil || !strings.Contains(err.Error(), "completeness cannot be established") {
		t.Fatalf("lost guard must refuse a complete report: %v", err)
	}
}

func TestMySQLJournalDisconnectReleasesMetadataLocks(t *testing.T) {
	db, plan := mysqlJournalFixture(t, "id INT PRIMARY KEY")
	spec := installMySQLFixture(t, db, plan)
	releaseMySQLJournalGuards(context.Background(), db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE `"+plan.Name+"`"); err != nil {
		t.Fatalf("disconnect left a server metadata lock held: %v", err)
	}
	if _, err := freezeMySQLJournal(ctx, db, spec); err == nil || !strings.Contains(err.Error(), "completeness cannot be established") {
		t.Fatalf("a disconnected guard must refuse a complete report: %v", err)
	}
}

func TestMySQLJournalRejectsUnobservableCascade(t *testing.T) {
	db, parent := mysqlJournalFixture(t, "id INT PRIMARY KEY")
	childName := fmt.Sprintf("journal_child_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE TABLE `" + childName + "` (id INT PRIMARY KEY, parent_id INT, FOREIGN KEY(parent_id) REFERENCES `" + parent.Name + "`(id) ON DELETE CASCADE) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS `" + childName + "`") })
	child, err := inspectMySQLTable(context.Background(), db, childName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installMySQLJournal(context.Background(), db, fmt.Sprintf("cascade_%d", time.Now().UnixNano()), []TablePlan{child}); err == nil || !strings.Contains(err.Error(), "cascading foreign keys") {
		t.Fatalf("must reject uncaptured cascading changes: %v", err)
	}
}

func TestMySQLJournalMillionRowStartRecordsOnlyChanges(t *testing.T) {
	db, plan := mysqlJournalFixture(t, "id INT PRIMARY KEY, value VARCHAR(64)")
	_, err := db.Exec("INSERT INTO `" + plan.Name + "` WITH digits AS (SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) SELECT a.n+10*b.n+100*c.n+1000*d.n+10000*e.n+100000*f.n, 'unchanged' FROM digits a CROSS JOIN digits b CROSS JOIN digits c CROSS JOIN digits d CROSS JOIN digits e CROSS JOIN digits f")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	spec := installMySQLFixture(t, db, plan)
	t.Logf("Start on 1,000,000 existing rows: %s", time.Since(started))
	if _, err := db.Exec("UPDATE `" + plan.Name + "` SET value='changed' WHERE id IN (1,500000,999999)"); err != nil {
		t.Fatal(err)
	}
	stopped := time.Now()
	spec, err = freezeMySQLJournal(context.Background(), db, spec)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := readMySQLJournal(context.Background(), db, spec, func(event JournalEvent) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	t.Logf("Stop and read 3 changes: %s", time.Since(stopped))
	if count != 3 {
		t.Fatalf("read %d journal events, want only the 3 changed rows", count)
	}
}

func TestMySQLJournalUnusualTypesAndReplace(t *testing.T) {
	db, plan := mysqlJournalFixture(t, "id INT PRIMARY KEY, fixed BINARY(4), flags BIT(9), choice ENUM('α','β'), tags SET('red','blue'), document JSON, place POINT, plain_date DATE, duration TIME(6), exact_time DATETIME(6), small FLOAT")
	spec := installMySQLFixture(t, db, plan)
	writer, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	// Legacy deployments permit zero dates. Restrict this compatibility mode to
	// the expendable fixture writer rather than changing server configuration.
	if _, err := writer.ExecContext(context.Background(), "SET SESSION sql_mode='NO_ENGINE_SUBSTITUTION'"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(context.Background(), "INSERT INTO `"+plan.Name+"` VALUES(1, X'00FF', b'111111111', 'α', 'blue,red', JSON_OBJECT('big',18446744073709551615,'nothing',NULL,'str','🙂'), ST_GeomFromText('POINT(12.25 -3.5)',4326), '0000-00-00', '-237:45:12.123456', '2024-01-02 03:04:05.654321', 1.2345678)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("REPLACE INTO `" + plan.Name + "` (id, document) VALUES(1, JSON_OBJECT('replacement',true))"); err != nil {
		t.Fatal(err)
	}
	spec, err = freezeMySQLJournal(context.Background(), db, spec)
	if err != nil {
		t.Fatal(err)
	}
	var events []JournalEvent
	if err := readMySQLJournal(context.Background(), db, spec, func(event JournalEvent) error { events = append(events, event); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || len(events[1].After) != 0 || len(events[2].Before) != 0 {
		t.Fatalf("REPLACE must expose committed delete and insert images: %#v", events)
	}
	if string(events[0].After) != string(events[1].Before) {
		t.Fatal("unusual type images changed during REPLACE deletion")
	}
	var original encodedRow
	if err := json.Unmarshal(events[0].After, &original); err != nil {
		t.Fatal(err)
	}
	for _, cell := range original.Cells {
		switch cell.Name {
		case "fixed":
			if cell.Kind != "bytes" || cell.Data != base64.StdEncoding.EncodeToString([]byte{0, 255, 0, 0}) {
				t.Errorf("fixed BINARY image: %#v", cell)
			}
		case "flags":
			if cell.Kind != "bytes" || cell.Data != base64.StdEncoding.EncodeToString([]byte{1, 255}) {
				t.Errorf("BIT image: %#v", cell)
			}
		case "plain_date":
			if cell.Data != "0000-00-00" {
				t.Errorf("zero date image: %#v", cell)
			}
		case "duration":
			if cell.Data != "-237:45:12.123456" {
				t.Errorf("duration image: %#v", cell)
			}
		case "place":
			if cell.Kind != "bytes" {
				t.Errorf("geometry image must include binary SRID and WKB: %#v", cell)
			}
		}
	}
}

func TestMySQLJournalCleanupRejectsTamperedObjectNames(t *testing.T) {
	db, plan := mysqlJournalFixture(t, "id INT PRIMARY KEY, value TEXT")
	spec := installMySQLFixture(t, db, plan)
	if _, err := db.Exec("INSERT INTO `" + plan.Name + "` VALUES(42,'keep this user data')"); err != nil {
		t.Fatal(err)
	}
	state, err := mysqlJournalDecode(spec)
	if err != nil {
		t.Fatal(err)
	}
	state.Events = plan.Name
	tampered := mysqlJournalSave(spec, state)
	if err := cleanupMySQLJournal(context.Background(), db, tampered); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("tampered state must refuse cleanup: %v", err)
	}
	var value string
	if err := db.QueryRow("SELECT value FROM `" + plan.Name + "` WHERE id=42").Scan(&value); err != nil || value != "keep this user data" {
		t.Fatalf("cleanup changed ordinary table data: value=%q err=%v", value, err)
	}
}

func TestMySQLJournalCleanupPreservesReplacementTable(t *testing.T) {
	db, plan := mysqlJournalFixture(t, "id INT PRIMARY KEY")
	spec := installMySQLFixture(t, db, plan)
	state, err := mysqlJournalDecode(spec)
	if err != nil {
		t.Fatal(err)
	}
	releaseMySQLJournalGuards(context.Background(), db)
	events := mysqlJournalObject(state.Schema, state.Events)
	if _, err := db.Exec("DROP TABLE " + events); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE " + events + " (id INT PRIMARY KEY, value TEXT) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	// The fixture explicitly removes its replacement only after preservation is
	// asserted; checkpoint cleanup must leave this foreign object untouched.
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + events) })
	if _, err := db.Exec("INSERT INTO " + events + " VALUES(77,'foreign replacement')"); err != nil {
		t.Fatal(err)
	}
	if err := cleanupMySQLJournal(context.Background(), db, spec); err == nil || !strings.Contains(err.Error(), "owned") {
		t.Fatalf("replacement table must refuse cleanup: %v", err)
	}
	var value string
	if err := db.QueryRow("SELECT value FROM " + events + " WHERE id=77").Scan(&value); err != nil || value != "foreign replacement" {
		t.Fatalf("foreign replacement was changed: value=%q err=%v", value, err)
	}
	var triggers int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema=? AND event_object_table=?`, state.Schema, plan.Name).Scan(&triggers); err != nil || triggers != 3 {
		t.Fatalf("cleanup changed other objects before validating all ownership: triggers=%d err=%v", triggers, err)
	}
}

func TestMySQLJournalCleanupPreservesReplacementTrigger(t *testing.T) {
	db, plan := mysqlJournalFixture(t, "id INT PRIMARY KEY")
	spec := installMySQLFixture(t, db, plan)
	state, err := mysqlJournalDecode(spec)
	if err != nil {
		t.Fatal(err)
	}
	releaseMySQLJournalGuards(context.Background(), db)
	trigger := mysqlJournalObject(state.Schema, state.Triggers[0].Name)
	if _, err := db.Exec("DROP TRIGGER " + trigger); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER " + trigger + " AFTER INSERT ON " + mysqlJournalObject(state.Schema, plan.Name) + " FOR EACH ROW SET @foreign_trigger_marker=1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS " + trigger) })
	if err := cleanupMySQLJournal(context.Background(), db, spec); err == nil || !strings.Contains(err.Error(), "different owner or definition") {
		t.Fatalf("replacement trigger must refuse cleanup: %v", err)
	}
	var action string
	if err := db.QueryRow(`SELECT action_statement FROM information_schema.triggers WHERE trigger_schema=? AND trigger_name=?`, state.Schema, state.Triggers[0].Name).Scan(&action); err != nil || !strings.Contains(action, "foreign_trigger_marker") {
		t.Fatalf("foreign replacement trigger was changed: action=%q err=%v", action, err)
	}
}

func TestMySQLJournalCleanupRecoversPreHashInstallation(t *testing.T) {
	db, plan := mysqlJournalFixture(t, "id INT PRIMARY KEY")
	spec := installMySQLFixture(t, db, plan)
	state, err := mysqlJournalDecode(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after CREATE but before persisting SHOW CREATE hashes.
	state.EventsHash, state.GateHash = "", ""
	if err := cleanupMySQLJournal(context.Background(), db, mysqlJournalSave(spec, state)); err != nil {
		t.Fatalf("fixed owner definitions should recover the pre-hash crash window: %v", err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=? AND table_name IN (?,?)`, state.Schema, state.Events, state.Gate).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("owned objects remain after recovery cleanup: count=%d err=%v", remaining, err)
	}
}

func TestMySQLJournalCleanupPreservesChangedOwnedDefinition(t *testing.T) {
	db, plan := mysqlJournalFixture(t, "id INT PRIMARY KEY")
	spec := installMySQLFixture(t, db, plan)
	state, err := mysqlJournalDecode(spec)
	if err != nil {
		t.Fatal(err)
	}
	releaseMySQLJournalGuards(context.Background(), db)
	events := mysqlJournalObject(state.Schema, state.Events)
	if _, err := db.Exec("ALTER TABLE " + events + " ADD COLUMN extra_user_data TEXT"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("ALTER TABLE " + events + " DROP COLUMN extra_user_data") })
	if err := cleanupMySQLJournal(context.Background(), db, spec); err == nil || !strings.Contains(err.Error(), "definitions differ") {
		t.Fatalf("changed definition with original owner marker must be preserved: %v", err)
	}
	var present int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=? AND table_name=? AND column_name='extra_user_data'`, state.Schema, state.Events).Scan(&present); err != nil || present != 1 {
		t.Fatalf("cleanup removed an altered table: present=%d err=%v", present, err)
	}
}
