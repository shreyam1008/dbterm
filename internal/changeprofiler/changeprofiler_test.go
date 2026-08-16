package changeprofiler

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shreyam1008/dbterm/internal/config"
	_ "modernc.org/sqlite"
)

func TestAnchorCapturesAndComparesDataAndStructure(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, note BLOB)`,
		`INSERT INTO users(id,name,note) VALUES (1,'Ada',X'0102'),(2,'Grace',NULL)`,
	)
	store := openTestStore(t)
	plans, err := Preflight(ctx, source, config.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].KeyKind != KeyPrimary {
		t.Fatalf("plans = %#v", plans)
	}
	plans[0].Included = true
	anchor, err := store.Start(ctx, source, StartRequest{
		Name: "registration test", ConnectionKey: "test-db", ConnectionLabel: "local test",
		TargetLabel: "test.sqlite", Engine: config.SQLite, Tables: plans,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.Status != StatusActive || anchor.Consistency != ConsistencySnapshot {
		t.Fatalf("anchor = %#v", anchor)
	}

	execTestSQL(t, source,
		`UPDATE users SET name='Ada Lovelace' WHERE id=1`,
		`DELETE FROM users WHERE id=2`,
		`INSERT INTO users(id,name,note) VALUES (3,'Linus',X'FF')`,
		`ALTER TABLE users ADD COLUMN active INTEGER DEFAULT 1`,
		`CREATE TABLE audit_log (id INTEGER PRIMARY KEY, event TEXT)`,
		`INSERT INTO audit_log VALUES (1,'created')`,
	)
	lastScan := Progress{}
	anchor, err = store.Scan(ctx, source, anchor.ID, false, func(progress Progress) {
		if progress.Phase == "comparing" {
			lastScan = progress
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if lastScan.Percent != 100 {
		t.Fatalf("final scan progress = %#v", lastScan)
	}
	if anchor.Status != StatusActive || anchor.Inserted != 2 || anchor.Updated != 1 || anchor.Deleted != 1 || anchor.SchemaChanges != 2 {
		t.Fatalf("scanned anchor = %#v", anchor)
	}
	summaries, err := store.TableSummaries(ctx, anchor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries = %#v", summaries)
	}
	rows, err := store.ListDiffRows(ctx, anchor.ID, "users", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("user diffs = %#v", rows)
	}
	var updated *DiffRow
	for index := range rows {
		if rows[index].Kind == DiffUpdated {
			updated = &rows[index]
		}
	}
	if updated == nil || updated.Before["name"].Text != "Ada" || updated.After["name"].Text != "Ada Lovelace" {
		t.Fatalf("updated row = %#v", updated)
	}
	if !contains(updated.ChangedColumns, "name") || !contains(updated.ChangedColumns, "active") {
		t.Fatalf("changed columns = %v", updated.ChangedColumns)
	}

	anchor, err = store.Scan(ctx, source, anchor.ID, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.Status != StatusComplete || anchor.FinishedAt.IsZero() {
		t.Fatalf("finished anchor = %#v", anchor)
	}
	var baselineCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM profiler_baseline_rows WHERE anchor_id=?`, anchor.ID).Scan(&baselineCount); err != nil {
		t.Fatal(err)
	}
	if baselineCount != 0 {
		t.Fatalf("completed anchor retained %d baseline rows", baselineCount)
	}
}

func TestCanceledScanKeepsLastSuccessfulDiff(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE items(id INTEGER PRIMARY KEY, value TEXT)`, `INSERT INTO items VALUES(1,'before')`)
	store := openTestStore(t)
	plans, _ := Preflight(ctx, source, config.SQLite)
	plans[0].Included = true
	anchor, err := store.Start(ctx, source, StartRequest{Name: "cancel", ConnectionKey: "cancel-db", Engine: config.SQLite, Tables: plans}, nil)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `UPDATE items SET value='after' WHERE id=1`)
	if _, err := store.Scan(ctx, source, anchor.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	if _, err := store.Scan(canceled, source, anchor.ID, false, func(Progress) { cancel() }); err == nil {
		t.Fatal("expected canceled scan error")
	}
	rows, err := store.ListDiffRows(ctx, anchor.ID, "items", 10)
	if err != nil || len(rows) != 1 || rows[0].Kind != DiffUpdated {
		t.Fatalf("last successful diff lost: rows=%#v err=%v", rows, err)
	}
	recovered, err := store.GetAnchor(ctx, anchor.ID)
	if err != nil || recovered.Status != StatusActive {
		t.Fatalf("anchor did not return active: %#v err=%v", recovered, err)
	}
}

func TestAnchorRenameAndDelete(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE one(id INTEGER PRIMARY KEY)`)
	store := openTestStore(t)
	plans, _ := Preflight(ctx, source, config.SQLite)
	plans[0].Included = true
	anchor, err := store.Start(ctx, source, StartRequest{Name: "first", ConnectionKey: "rename-db", Engine: config.SQLite, Tables: plans}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RenameAnchor(ctx, anchor.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetAnchor(ctx, anchor.ID); got.Name != "renamed" {
		t.Fatalf("name = %q", got.Name)
	}
	if err := store.DeleteAnchor(ctx, anchor.ID); err != nil {
		t.Fatal(err)
	}
	if anchors, _ := store.ListAnchors(ctx); len(anchors) != 0 {
		t.Fatalf("anchors after delete = %#v", anchors)
	}
}

func TestExcludedTableIsNotReportedAsNew(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source,
		`CREATE TABLE tracked(id INTEGER PRIMARY KEY, value TEXT)`,
		`CREATE TABLE excluded(id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO tracked VALUES(1,'same')`,
		`INSERT INTO excluded VALUES(1,'before')`,
	)
	store := openTestStore(t)
	plans, err := Preflight(ctx, source, config.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	for index := range plans {
		plans[index].Included = plans[index].Name == "tracked"
	}
	anchor, err := store.Start(ctx, source, StartRequest{Name: "scope", ConnectionKey: "scope-db", Engine: config.SQLite, Tables: plans}, nil)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `UPDATE excluded SET value='after' WHERE id=1`)
	anchor, err = store.Scan(ctx, source, anchor.ID, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.Inserted != 0 || anchor.Updated != 0 || anchor.Deleted != 0 || anchor.SchemaChanges != 0 {
		t.Fatalf("excluded table leaked into report: %#v", anchor)
	}
	if summaries, _ := store.TableSummaries(ctx, anchor.ID); len(summaries) != 0 {
		t.Fatalf("excluded table summaries = %#v", summaries)
	}
}

func TestSQLiteStableKeySelectionRejectsPartialAndNullableUniqueIndexes(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source,
		`CREATE TABLE stable(email TEXT NOT NULL UNIQUE, name TEXT)`,
		`CREATE TABLE partial_only(code TEXT NOT NULL, active INTEGER NOT NULL)`,
		`CREATE UNIQUE INDEX partial_code ON partial_only(code) WHERE active = 1`,
		`CREATE TABLE nullable_only(code TEXT UNIQUE, name TEXT)`,
	)
	plans, err := Preflight(ctx, source, config.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]TablePlan, len(plans))
	for _, plan := range plans {
		byName[plan.Name] = plan
	}
	if got := byName["stable"]; got.KeyKind != KeyUnique || len(got.KeyColumns) != 1 || got.KeyColumns[0] != "email" {
		t.Fatalf("stable key = %#v", got)
	}
	for _, table := range []string{"partial_only", "nullable_only"} {
		if got := byName[table]; got.KeyKind != KeyRowID {
			t.Fatalf("%s unsafe unique index selected: %#v", table, got)
		}
	}
}

func TestCompactRowEnvelopeCompressesAndReadsLegacyRows(t *testing.T) {
	raw := bytes.Repeat([]byte(`{"n":"repetitive profiler value"}`), 200)
	packed, err := packRow(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(packed) >= len(raw)/2 || packed[0] != rowEncodingZstd {
		t.Fatalf("packed length = %d for %d raw bytes", len(packed), len(raw))
	}
	unpacked, err := unpackRow(packed)
	if err != nil || !bytes.Equal(unpacked, raw) {
		t.Fatalf("compact round trip failed: len=%d err=%v", len(unpacked), err)
	}
	legacy := []byte(`{"c":[]}`)
	unpacked, err = unpackRow(legacy)
	if err != nil || !bytes.Equal(unpacked, legacy) {
		t.Fatalf("legacy row failed: %q err=%v", unpacked, err)
	}
}

func TestProgressMeterWeightsKnownRowsAndCompletes(t *testing.T) {
	meter := newProgressMeter([]TablePlan{{Name: "small", EstimatedRows: 100}, {Name: "large", EstimatedRows: 300}}, false)
	progress := meter.decorate(Progress{Phase: "capturing", Table: "large", Rows: 150}, false)
	if progress.Percent != 63 || progress.TableIndex != 2 || progress.TableCount != 2 || !progress.Approximate {
		t.Fatalf("mid progress = %#v", progress)
	}
	progress = meter.decorate(Progress{Phase: "capturing", Table: "large", Rows: 310}, true)
	if progress.Percent != 100 {
		t.Fatalf("complete progress = %#v", progress)
	}
}

func TestLargeBaselineStreamsCompressedRowsAndReachesHundredPercent(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE bulk(id INTEGER PRIMARY KEY, payload TEXT NOT NULL)`)
	tx, err := source.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO bulk(id,payload) VALUES(?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("compact-profiler-value-", 60)
	for index := 1; index <= 2000; index++ {
		if _, err := statement.Exec(index, payload); err != nil {
			t.Fatal(err)
		}
	}
	_ = statement.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	plans, err := Preflight(ctx, source, config.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	if plans[0].EstimatedRows != 2000 {
		t.Fatalf("fast SQLite row estimate = %d, want 2000", plans[0].EstimatedRows)
	}
	plans[0].Included = true
	last := Progress{}
	anchor, err := store.Start(ctx, source, StartRequest{Name: "large", ConnectionKey: "large-db", Engine: config.SQLite, Tables: plans}, func(progress Progress) {
		if progress.Phase == "capturing" {
			last = progress
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if last.Percent != 100 || last.Rows != 2000 {
		t.Fatalf("final progress = %#v", last)
	}
	var storedBytes int64
	if err := store.db.QueryRow(`SELECT COALESCE(SUM(length(row_blob)),0) FROM profiler_baseline_rows WHERE anchor_id=?`, anchor.ID).Scan(&storedBytes); err != nil {
		t.Fatal(err)
	}
	rawPayloadBytes := int64(len(payload) * 2000)
	if storedBytes >= rawPayloadBytes/3 {
		t.Fatalf("stored baseline = %d bytes, raw payload alone = %d", storedBytes, rawPayloadBytes)
	}
}

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "profiler.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func execTestSQL(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
