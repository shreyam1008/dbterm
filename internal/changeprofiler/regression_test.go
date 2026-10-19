package changeprofiler

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestRepeatedScansCompareCreatedTablesAgainstOriginalBaseline(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE original(id INTEGER PRIMARY KEY)`)
	store := openTestStore(t)
	plans, err := Preflight(ctx, source, config.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := store.Start(ctx, source, StartRequest{Name: "repeat", ConnectionKey: "repeat", Engine: config.SQLite, Tables: plans}, nil)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `CREATE TABLE later(id INTEGER PRIMARY KEY, value TEXT)`, `INSERT INTO later VALUES(1,'first')`)
	for scan := 0; scan < 3; scan++ {
		anchor, err = store.Scan(ctx, source, anchor.ID, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if anchor.Inserted != 1 || anchor.SchemaChanges != 1 || anchor.Deleted != 0 {
			t.Fatalf("scan %d lost original baseline: %#v", scan, anchor)
		}
		var event string
		if err := store.db.QueryRow(`SELECT kind FROM profiler_schema_events WHERE anchor_id=? AND table_name='later'`, anchor.ID).Scan(&event); err != nil || event != "table_created" {
			t.Fatalf("scan %d event = %q, err = %v", scan, event, err)
		}
	}
	execTestSQL(t, source, `DROP TABLE later`)
	anchor, err = store.Scan(ctx, source, anchor.ID, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.Inserted != 0 || anchor.Deleted != 0 || anchor.SchemaChanges != 0 {
		t.Fatalf("table created and removed after the baseline should leave no diff: %#v", anchor)
	}
	// Reusing the same name is another creation, not a conflict with stale report metadata.
	execTestSQL(t, source, `CREATE TABLE later(id INTEGER PRIMARY KEY, note BLOB)`, `INSERT INTO later VALUES(2,X'00FF')`)
	anchor, err = store.Scan(ctx, source, anchor.ID, true, nil)
	if err != nil || anchor.SchemaChanges != 1 || anchor.Inserted != 1 {
		t.Fatalf("recreated table: %#v, err = %v", anchor, err)
	}
}

func TestDeleteAnchorRemovesSavedValuesActivityAndSchema(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE items(id INTEGER PRIMARY KEY, value TEXT)`, `INSERT INTO items VALUES(1,'private before value')`)
	store := openTestStore(t)
	plans, err := Preflight(ctx, source, config.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := store.Start(ctx, source, StartRequest{Name: "delete", ConnectionKey: "delete", Engine: config.SQLite, Tables: plans}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordActivity(ctx, "delete", "test", "UPDATE private data", 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `UPDATE items SET value='private after value'`, `ALTER TABLE items ADD COLUMN extra TEXT`)
	if _, err := store.Scan(ctx, source, anchor.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAnchor(ctx, anchor.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"profiler_anchors", "profiler_tables", "profiler_baseline_rows", "profiler_seen_rows", "profiler_diff_rows", "profiler_schema_events", "profiler_activity"} {
		var count int
		key := "anchor_id"
		if table == "profiler_anchors" {
			key = "id"
		}
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+key+`=?`, anchor.ID).Scan(&count); err != nil || count != 0 {
			t.Errorf("%s retains %d rows, err = %v", table, count, err)
		}
	}
}

func TestDiffPagesReachEverySavedChangeOnce(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE items(id INTEGER PRIMARY KEY, value TEXT)`)
	store := openTestStore(t)
	plans, err := Preflight(ctx, source, config.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := store.Start(ctx, source, StartRequest{Name: "pages", ConnectionKey: "pages", Engine: config.SQLite, Tables: plans}, nil)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<1234) INSERT INTO items SELECT i, 'value-' || i FROM n`)
	anchor, err = store.Scan(ctx, source, anchor.ID, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for offset := 0; offset < 1400; offset += 137 {
		page, err := store.ListDiffRowsPage(ctx, anchor.ID, "items", offset, 137)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > 137 {
			t.Fatalf("unbounded page: %d rows", len(page))
		}
		for _, row := range page {
			if seen[row.Key] {
				t.Fatalf("row repeated across pages: %s", row.Key)
			}
			seen[row.Key] = true
		}
	}
	if len(seen) != 1234 || anchor.Inserted != 1234 {
		t.Fatalf("pages returned %d of %d rows", len(seen), anchor.Inserted)
	}
	// Fetch the actual last key in the saved ordering. This proves highlighting
	// can retrieve visible changes beyond an arbitrary first-page cap.
	lastPage, err := store.ListDiffRowsPage(ctx, anchor.ID, "items", 1233, 1)
	if err != nil || len(lastPage) != 1 {
		t.Fatalf("last page = %#v, err = %v", lastPage, err)
	}
	var lastID int64
	if err := source.QueryRow(`SELECT id FROM items WHERE CAST(id AS TEXT)=?`, lastPage[0].After["id"].Text).Scan(&lastID); err != nil {
		t.Fatal(err)
	}
	visible, err := store.ListDiffRowsForValues(ctx, anchor.ID, "items", []string{"id"}, []string{"INTEGER"}, [][]any{{lastID}})
	if err != nil || len(visible) != 1 || visible[0].Key != lastPage[0].Key {
		t.Fatalf("late visible change = %#v, err = %v", visible, err)
	}
	legacy, err := store.ListDiffRowsForValues(ctx, anchor.ID, "items", []string{"id"}, nil, [][]any{{lastID}})
	if err != nil || len(legacy) != 1 || legacy[0].Key != lastPage[0].Key {
		t.Fatalf("legacy result without driver types = %#v, err = %v", legacy, err)
	}
	// Ensure SQLite can serve a report page without sorting every saved diff.
	rows, err := store.db.Query(`EXPLAIN QUERY PLAN SELECT kind, key_blob, before_blob, after_blob, changed_columns_json FROM profiler_diff_rows WHERE anchor_id=? AND table_name=? ORDER BY kind,key_hash,key_blob LIMIT ? OFFSET ?`, anchor.ID, "items", 100, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToUpper(detail), "TEMP B-TREE") {
			t.Fatalf("report page sorts the full diff: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestVisibleCompositeKeysUseBaselineOrderAndExactValues(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE items(first BLOB NOT NULL, second TEXT NOT NULL, value TEXT, PRIMARY KEY(second,first))`, `INSERT INTO items VALUES(X'00FF','தமிழ்','before')`)
	store := openTestStore(t)
	plans, err := Preflight(ctx, source, config.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := store.Start(ctx, source, StartRequest{Name: "composite", ConnectionKey: "composite", Engine: config.SQLite, Tables: plans}, nil)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `UPDATE items SET value='after'`)
	if _, err := store.Scan(ctx, source, anchor.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	// Display order, primary-key order, and physical column order all differ.
	rows, err := store.ListDiffRowsForValues(ctx, anchor.ID, "items", []string{"value", "second", "first"}, []string{"TEXT", "TEXT", "BLOB"}, [][]any{{"after", "தமிழ்", []byte{0, 255}}})
	if err != nil || len(rows) != 1 || rows[0].Kind != DiffUpdated || rows[0].Before["value"].Text != "before" {
		t.Fatalf("composite visible change = %#v, err = %v", rows, err)
	}
	rows, err = store.ListDiffRowsForValues(ctx, anchor.ID, "items", []string{"value"}, []string{"TEXT"}, [][]any{{"after"}})
	if err != nil || len(rows) != 0 {
		t.Fatalf("projection without a stable key was paired: %#v, err = %v", rows, err)
	}
}

func TestSQLiteUnusualKeysAndIdentifiersPreserveExactRows(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source,
		`CREATE TABLE nullable_primary(key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO nullable_primary VALUES(NULL,'first'),(NULL,'second')`,
		`CREATE TABLE descending_primary(key INTEGER PRIMARY KEY DESC, value TEXT)`,
		`INSERT INTO descending_primary VALUES(NULL,'first'),(NULL,'second')`,
		`CREATE TABLE shadowed(rowid TEXT, value TEXT)`,
		`INSERT INTO shadowed VALUES('duplicate','first'),('duplicate','second')`,
		`CREATE TABLE "table.with.dot" ("id.key" INTEGER PRIMARY KEY, "value.with.dot" TEXT, blob BLOB)`,
		`INSERT INTO "table.with.dot" VALUES(1,'தமிழ் 日本語 👩🏽‍💻' || char(10) || 'line two',X'00FF80')`,
	)
	plans, err := Preflight(ctx, source, config.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	for index := range plans {
		plan := &plans[index]
		if plan.Name != "table.with.dot" && plan.KeyKind != KeyRowID {
			t.Fatalf("%s selected an unsafe key: %#v", plan.Name, plan)
		}
		plan.Included = true
	}
	store := openTestStore(t)
	anchor, err := store.Start(ctx, source, StartRequest{Name: "unusual", ConnectionKey: "unusual", Engine: config.SQLite, Tables: plans}, nil)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source,
		`UPDATE nullable_primary SET value='updated' WHERE rowid=1`,
		`UPDATE descending_primary SET value='updated' WHERE rowid=1`,
		`UPDATE shadowed SET value='updated' WHERE _rowid_=1`,
		`UPDATE "table.with.dot" SET "value.with.dot"='changed' WHERE "id.key"=1`,
	)
	anchor, err = store.Scan(ctx, source, anchor.ID, false, nil)
	if err != nil || anchor.Updated != 4 || anchor.Inserted != 0 || anchor.Deleted != 0 {
		t.Fatalf("unusual row identities: %#v, err = %v", anchor, err)
	}
	rows, err := store.ListDiffRows(ctx, anchor.ID, "table.with.dot", 10)
	if err != nil || len(rows) != 1 || rows[0].Before["blob"].Text != "0x00ff80" || !strings.Contains(rows[0].Before["value.with.dot"].Text, "👩🏽‍💻\nline two") {
		t.Fatalf("exact unusual values lost: %#v, err = %v", rows, err)
	}
}

func TestSharedIdentityEncodingMatchesExistingAnchors(t *testing.T) {
	names := []string{"id", "text", "blob", "empty", "null", "float", "bool", "time"}
	types := []string{"INTEGER", "TEXT", "BLOB", "TEXT", "TEXT", "REAL", "BOOLEAN", "TIMESTAMP"}
	values := []any{int64(42), "தமிழ் 日本語 👩🏽‍💻\n\t" + strings.Repeat("wide row", 1000), []byte{0, 255, 128}, "", nil, -0.125, true, time.Unix(0, 123456).UTC()}
	for _, kind := range []KeyKind{KeyPrimary, KeyUnique, KeyFullRow} {
		for duplicate := 0; duplicate < 3; duplicate++ {
			rowBlob, rowHash, keyBlob, keyHash, err := encodeScannedIdentity(names, types, values, []string{"id", "text"}, kind, duplicateOccurrences(kind, duplicate, names, types, values))
			if err != nil {
				t.Fatal(err)
			}
			oldRow, oldHash, err := encodeScannedRow(names, types, values)
			if err != nil {
				t.Fatal(err)
			}
			occurrence := 0
			if kind == KeyFullRow {
				occurrence = duplicate
			}
			oldKey, oldKeyHash, err := encodeKey(oldRow, []string{"id", "text"}, kind, occurrence)
			if err != nil || !bytes.Equal(oldRow, rowBlob) || !bytes.Equal(oldHash, rowHash) || !bytes.Equal(oldKey, keyBlob) || !bytes.Equal(oldKeyHash, keyHash) {
				t.Fatalf("%s encoding changed existing identity, err = %v", kind, err)
			}
		}
	}
}

func TestExistingProfilerStoreMigratesBaselineMetadata(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE original(id INTEGER PRIMARY KEY)`)
	plans, err := Preflight(ctx, source, config.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := store.Start(ctx, source, StartRequest{Name: "migration", ConnectionKey: "migration", Engine: config.SQLite, Tables: plans}, nil)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `CREATE TABLE later(id INTEGER PRIMARY KEY)`, `INSERT INTO later VALUES(1)`)
	if _, err := store.Scan(ctx, source, anchor.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	// SQLite supports dropping the new column; this reproduces a store schema
	// created by the previous dbterm version without a separate fixture binary.
	if _, err := store.db.Exec(`ALTER TABLE profiler_tables DROP COLUMN baseline_present`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var columns int
	if err := store.db.QueryRow(`SELECT count(*) FROM pragma_table_info('profiler_tables') WHERE name='baseline_present'`).Scan(&columns); err != nil || columns != 1 {
		t.Fatalf("store upgrade missing baseline flag: %d, err = %v", columns, err)
	}
	anchor, err = store.Scan(ctx, source, anchor.ID, false, nil)
	if err != nil || anchor.SchemaChanges != 1 || anchor.Inserted != 1 {
		t.Fatalf("migrated anchor lost its original baseline: %#v, err = %v", anchor, err)
	}
}

func TestLegacyNullableSQLitePrimaryKeyRequiresFreshAnchorAndPreservesReport(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE items(code TEXT PRIMARY KEY, value TEXT)`, `INSERT INTO items VALUES('a','before'),('b','untouched')`)
	store := openTestStore(t)
	plans, err := Preflight(ctx, source, config.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	legacy := plans[0]
	legacy.KeyKind, legacy.KeyColumns, legacy.Included = KeyPrimary, []string{"code"}, true
	for index := range legacy.Columns {
		if legacy.Columns[index].PrimaryPos > 0 {
			legacy.Columns[index].Nullable = false
		}
	}
	anchor := Anchor{ID: "legacy-anchor", Name: "old capture", ConnectionKey: "legacy", Engine: config.SQLite, Status: StatusActive, Consistency: ConsistencySnapshot, StartedAt: time.Now()}
	if err := store.insertAnchor(ctx, anchor, []TablePlan{legacy}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.captureBaselineTable(ctx, source, anchor, legacy, nil); err != nil {
		t.Fatal(err)
	}
	// Retain a previous successful report, as an upgraded user's store would.
	if _, err := store.db.Exec(`INSERT INTO profiler_diff_rows(anchor_id,table_name,key_hash,key_blob,kind,before_blob,after_blob,changed_columns_json)
		SELECT anchor_id,table_name,key_hash,key_blob,'updated',row_blob,row_blob,'[]' FROM profiler_baseline_rows WHERE anchor_id=? LIMIT 1`, anchor.ID); err != nil {
		t.Fatal(err)
	}
	before, err := store.ListDiffRows(ctx, anchor.ID, "items", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scan(ctx, source, anchor.ID, false, nil); err == nil || !strings.Contains(err.Error(), "Create a new anchor") {
		t.Fatalf("legacy identity was silently replaced: %v", err)
	}
	after, err := store.ListDiffRows(ctx, anchor.ID, "items", 10)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if len(before) != 1 || !bytes.Equal(beforeJSON, afterJSON) {
		t.Fatalf("legacy report changed: before=%s after=%s", beforeJSON, afterJSON)
	}
	restored, err := store.GetAnchor(ctx, anchor.ID)
	if err != nil || restored.Status != StatusFailed {
		t.Fatalf("legacy anchor status was lost: %#v, err = %v", restored, err)
	}
	var baselineRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM profiler_baseline_rows WHERE anchor_id=?`, anchor.ID).Scan(&baselineRows); err != nil || baselineRows != 2 {
		t.Fatalf("legacy baseline changed: rows=%d, err=%v", baselineRows, err)
	}
	replacement, err := store.Start(ctx, source, StartRequest{Name: "new capture", ConnectionKey: anchor.ConnectionKey, Engine: config.SQLite, Tables: plans}, nil)
	if err != nil || replacement.Status != StatusActive {
		t.Fatalf("legacy anchor blocks replacement capture: %#v, err = %v", replacement, err)
	}
	retained, err := store.ListDiffRows(ctx, anchor.ID, "items", 10)
	if err != nil {
		t.Fatal(err)
	}
	retainedJSON, _ := json.Marshal(retained)
	if !bytes.Equal(beforeJSON, retainedJSON) {
		t.Fatal("replacement capture changed the legacy anchor's saved report")
	}
}

// Duplicate counts are supplied separately so this compatibility check uses
// the legacy encoding as its oracle rather than duplicating its implementation.
func duplicateOccurrences(kind KeyKind, occurrence int, names, types []string, values []any) map[[32]byte]int {
	counts := make(map[[32]byte]int)
	if kind == KeyFullRow {
		_, hash, _ := encodeScannedRow(names, types, values)
		var key [32]byte
		copy(key[:], hash)
		counts[key] = occurrence
	}
	return counts
}
