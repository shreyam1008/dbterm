package changeprofiler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestSQLiteJournalCapturesExternalCommittedWritesAndReplacementConflicts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "external.sqlite")
	source, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	external, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	execTestSQL(t, source,
		`CREATE TABLE records(id INTEGER PRIMARY KEY, unique_value TEXT UNIQUE, payload BLOB, mixed TEXT, number REAL)`,
		`INSERT INTO records VALUES(1,'one',X'00FF','before',1.25),(2,'two',NULL,NULL,2.5)`,
	)
	plan, err := inspectSQLiteTable(ctx, source, config.SQLite, "records")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := installSQLiteJournal(ctx, source, "external-writer", []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSQLiteJournal(ctx, source, spec)
	// The writer has its own connection and the default recursive_triggers OFF.
	// Replacing through a secondary UNIQUE key must preserve the evicted row.
	execTestSQL(t, external,
		`PRAGMA recursive_triggers=OFF`,
		`INSERT OR REPLACE INTO records VALUES(3,'one',X'010080FF',CAST(X'610062' AS TEXT),1.2345678901234567)`,
		`UPDATE records SET id=4,payload=X'' WHERE id=2`,
	)
	tx, err := external.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO records VALUES(90,'rollback',NULL,NULL,0)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	spec, err = freezeSQLiteJournal(ctx, source, spec)
	if err != nil {
		t.Fatal(err)
	}
	// A write after Stop must not alter either the stored event images or the
	// final point images used to resolve possible REPLACE evictions.
	execTestSQL(t, external, `INSERT INTO records VALUES(1,'after-stop',NULL,'later',0)`, `UPDATE records SET mixed='later' WHERE id=3`)
	var events []JournalEvent
	if err := readSQLiteJournal(ctx, source, spec, func(event JournalEvent) error { events = append(events, event); return nil }); err != nil {
		t.Fatal(err)
	}
	var lostReplacement, movedKey, exactText, blob, real bool
	for _, event := range events {
		var before, after encodedRow
		if len(event.Before) > 0 {
			if err := json.Unmarshal(event.Before, &before); err != nil {
				t.Fatal(err)
			}
		}
		if len(event.After) > 0 {
			if err := json.Unmarshal(event.After, &after); err != nil {
				t.Fatal(err)
			}
		}
		cells := append(append([]encodedCell(nil), before.Cells...), after.Cells...)
		for _, cell := range cells {
			if cell.Name == "id" && cell.Data == "90" {
				t.Fatal("rolled-back write was recorded")
			}
		}
		if event.Probe && sqliteJournalTestCell(before, "id").Data == "1" && event.After == nil {
			lostReplacement = true
		}
		if !event.Probe && sqliteJournalTestCell(before, "id").Data == "2" && sqliteJournalTestCell(after, "id").Data == "4" {
			movedKey = true
		}
		if !event.Probe && sqliteJournalTestCell(after, "id").Data == "3" {
			exactText = sqliteJournalTestCell(after, "mixed").Data == "a\x00b"
			blob = sqliteJournalTestCell(after, "payload").Data == "AQCA/w=="
			real = sqliteJournalTestCell(after, "number").Data == "3ff3c0ca428c59fb"
		}
	}
	if !lostReplacement || !movedKey || !exactText || !blob || !real {
		t.Fatalf("coverage replacement=%v move=%v text=%v blob=%v float=%v (%d events)", lostReplacement, movedKey, exactText, blob, real, len(events))
	}
}

func TestSQLiteJournalFailsClosedAfterTriggerDropAndRecreate(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE items(id INTEGER PRIMARY KEY, value TEXT)`)
	plan, err := inspectSQLiteTable(ctx, source, config.SQLite, "items")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := installSQLiteJournal(ctx, source, "continuity", []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSQLiteJournal(ctx, source, spec)
	var state sqliteJournalSpec
	if err := json.Unmarshal(spec.State, &state); err != nil {
		t.Fatal(err)
	}
	for _, object := range state.Objects {
		if object.Type != "trigger" {
			continue
		}
		execTestSQL(t, source, `DROP TRIGGER `+quoteName(config.SQLite, object.Name), `INSERT INTO items VALUES(1,'gap')`, object.SQL)
		break
	}
	if _, err := freezeSQLiteJournal(ctx, source, spec); err == nil || !strings.Contains(err.Error(), "coverage") {
		t.Fatalf("lost trigger coverage should fail visibly: %v", err)
	}
}

func TestSQLiteJournalRejectsUnsafeUniqueIndexesAndLeavesNoObjects(t *testing.T) {
	for _, indexSQL := range []string{`CREATE UNIQUE INDEX special ON items(lower(value))`, `CREATE UNIQUE INDEX special ON items(value) WHERE id>0`} {
		t.Run(indexSQL, func(t *testing.T) {
			ctx := context.Background()
			source := openTestDatabase(t)
			execTestSQL(t, source, `CREATE TABLE items(id INTEGER PRIMARY KEY,value TEXT)`, indexSQL)
			plan, err := inspectSQLiteTable(ctx, source, config.SQLite, "items")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := installSQLiteJournal(ctx, source, "unsafe", []TablePlan{plan}); err == nil {
				t.Fatal("unsafe unique index was silently accepted")
			}
			var count int
			if err := source.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name LIKE '__dbterm_cp_%'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed installation left %d objects: %v", count, err)
			}
		})
	}
}

func TestSQLiteJournalPreservesStorageClassesAndCleansOnlyOwnedObjects(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE "table.with.dot" ("rowid" TEXT,"_rowid_" TEXT,payload TEXT,number REAL)`, `CREATE TABLE unrelated(value TEXT)`)
	plan, err := inspectSQLiteTable(ctx, source, config.SQLite, "table.with.dot")
	if err != nil {
		t.Fatal(err)
	}
	if plan.KeyKind != KeyRowID {
		t.Fatalf("unexpected identity: %s", plan.KeyKind)
	}
	spec, err := installSQLiteJournal(ctx, source, "types", []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `INSERT INTO "table.with.dot"(rowid,_rowid_,payload,number) VALUES('shadow','shadow',X'00FF',1e300)`)
	spec, err = freezeSQLiteJournal(ctx, source, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := readSQLiteJournal(ctx, source, spec, func(event JournalEvent) error {
		if event.Probe {
			return nil
		}
		var row encodedRow
		if err := json.Unmarshal(event.After, &row); err != nil {
			return err
		}
		if got := sqliteJournalTestCell(row, "payload"); got.Kind != "bytes" || got.Data != "AP8=" {
			t.Fatalf("BLOB in TEXT affinity column changed: %#v", got)
		}
		if got := sqliteJournalTestCell(row, "__dbterm_rowid"); got.Kind != "int" || got.Data != "1" {
			t.Fatalf("shadowed rowid identity changed: %#v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := cleanupSQLiteJournal(ctx, source, spec); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := source.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('table.with.dot','unrelated')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("cleanup removed source objects: %d %v", count, err)
	}
}

func TestSQLiteJournalDecodeRejectsCorruptValues(t *testing.T) {
	plan := TablePlan{Columns: []Column{{Name: "value", Type: "TEXT"}}, KeyKind: KeyPrimary, KeyColumns: []string{"value"}}
	for _, payload := range []string{"", "nextra;", "bzz;", "i9999999999999999999999999999999999;", "rnotfloat;", "x;", "t00;extra;"} {
		if _, err := sqliteJournalDecodeRow(plan, payload); err == nil {
			t.Fatalf("accepted invalid image %q", payload)
		}
	}
	row, err := sqliteJournalDecodeRow(plan, "r4.9406564584124654417656879e-324;")
	if err != nil || row.Cells[0].Data != "1" {
		t.Fatalf("smallest real lost: %#v %v", row, err)
	}
}

func TestSQLiteJournalStopRetryKeepsOriginalBoundary(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE items(id INTEGER PRIMARY KEY,value TEXT)`, `INSERT INTO items VALUES(1,'before')`)
	plan, err := inspectSQLiteTable(ctx, source, config.SQLite, "items")
	if err != nil {
		t.Fatal(err)
	}
	original, err := installSQLiteJournal(ctx, source, "retry-boundary", []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `UPDATE items SET value='at-stop'`)
	frozen, err := freezeSQLiteJournal(ctx, source, original)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSQLiteJournal(ctx, source, frozen)
	execTestSQL(t, source, `UPDATE items SET value='after-stop'`)
	// Simulate a lost COMMIT acknowledgement or local-state-save failure.
	recovered, err := freezeSQLiteJournal(ctx, source, original)
	if err != nil || !recovered.Frozen {
		t.Fatalf("failed to recover committed source stop: %#v %v", recovered, err)
	}
	if string(recovered.State) != string(frozen.State) {
		t.Fatal("retry changed the original stop receipt")
	}
	if err := readSQLiteJournal(ctx, source, recovered, func(event JournalEvent) error {
		if strings.Contains(string(event.After), "after-stop") {
			t.Fatal("a post-stop write leaked into recovered checkpoint")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := cleanupSQLiteJournal(ctx, source, recovered); err != nil {
		t.Fatal(err)
	}
	if err := cleanupSQLiteJournal(ctx, source, recovered); err != nil {
		t.Fatalf("cleanup is not idempotent: %v", err)
	}
}

func TestSQLiteJournalPreservesUnicodeInUTF16Databases(t *testing.T) {
	for _, encoding := range []string{"UTF-16le", "UTF-16be"} {
		t.Run(encoding, func(t *testing.T) {
			ctx := context.Background()
			source := openTestDatabase(t)
			execTestSQL(t, source, `PRAGMA encoding=`+sqliteJournalLiteral(encoding), `CREATE TABLE items(id INTEGER PRIMARY KEY,value TEXT)`)
			plan, err := inspectSQLiteTable(ctx, source, config.SQLite, "items")
			if err != nil {
				t.Fatal(err)
			}
			spec, err := installSQLiteJournal(ctx, source, encoding, []TablePlan{plan})
			if err != nil {
				t.Fatal(err)
			}
			value := "தமிழ் 日本語 👩🏽‍💻\x00after"
			if _, err := source.Exec(`INSERT INTO items VALUES(1,?)`, value); err != nil {
				t.Fatal(err)
			}
			spec, err = freezeSQLiteJournal(ctx, source, spec)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanupSQLiteJournal(ctx, source, spec)
			if err := readSQLiteJournal(ctx, source, spec, func(event JournalEvent) error {
				var row encodedRow
				if err := json.Unmarshal(event.After, &row); err != nil {
					return err
				}
				if got := sqliteJournalTestCell(row, "value").Data; got != value {
					t.Fatalf("text encoding corrupted %q into %q", value, got)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSQLiteJournalNetDiffMatchesIndependentSnapshotsAcrossConflictWrites(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE items(id INTEGER PRIMARY KEY,code TEXT UNIQUE,payload TEXT)`, `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<40) INSERT INTO items SELECT i,'code-'||i,'initial-'||i FROM n`)
	plan, err := inspectSQLiteTable(ctx, source, config.SQLite, "items")
	if err != nil {
		t.Fatal(err)
	}
	// Full reads are an independent oracle in this test only. Production Start
	// installs triggers and Stop reads their dirty history plus indexed probes.
	snapshot := func() map[string]string {
		expression, err := sqliteJournalRowExpression(plan, "candidate")
		if err != nil {
			t.Fatal(err)
		}
		rows, err := source.Query(`SELECT ` + expression + ` FROM items AS candidate`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := map[string]string{}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			row, err := sqliteJournalDecodeRow(plan, raw)
			if err != nil {
				t.Fatal(err)
			}
			payload, _, err := encodeRow(row)
			if err != nil {
				t.Fatal(err)
			}
			canonical, key, err := canonicalJournalRow(plan, payload)
			if err != nil {
				t.Fatal(err)
			}
			result[string(key)] = string(canonical)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := snapshot()
	spec, err := installSQLiteJournal(ctx, source, "differential", []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewSource(1806))
	for index := 0; index < 240; index++ {
		id, other := random.Intn(60)+1, random.Intn(60)+1
		code, payload := fmt.Sprintf("code-%d", random.Intn(50)+1), fmt.Sprintf("change-%d\x00end", index)
		var statement string
		var arguments []any
		switch random.Intn(6) {
		case 0:
			statement = `INSERT OR REPLACE INTO items VALUES(?,?,?)`
			arguments = []any{id, code, payload}
		case 1:
			statement = `INSERT OR IGNORE INTO items VALUES(?,?,?)`
			arguments = []any{id, code, payload}
		case 2:
			statement = `UPDATE OR REPLACE items SET id=?,code=?,payload=? WHERE id=?`
			arguments = []any{other, code, payload, id}
		case 3:
			statement = `DELETE FROM items WHERE id=?`
			arguments = []any{id}
		case 4:
			statement = `UPDATE OR IGNORE items SET payload=? WHERE id=?`
			arguments = []any{payload, id}
		case 5:
			statement = `INSERT INTO items VALUES(?,?,?) ON CONFLICT DO NOTHING`
			arguments = []any{id, code, payload}
		}
		if index%9 == 0 {
			tx, err := source.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(statement, arguments...); err != nil {
				t.Fatal(err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		} else if _, err := source.Exec(statement, arguments...); err != nil {
			t.Fatal(err)
		}
	}
	after := snapshot()
	spec, err = freezeSQLiteJournal(ctx, source, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSQLiteJournal(ctx, source, spec)
	expected := map[string]bool{}
	for key, value := range before {
		if after[key] != value {
			expected[key] = true
		}
	}
	for key, value := range after {
		if before[key] != value {
			expected[key] = true
		}
	}
	seen := map[string]bool{}
	err = reduceJournal(ctx, []TablePlan{plan}, func(emit func(JournalEvent) error) error { return readSQLiteJournal(ctx, source, spec, emit) }, func(diff JournalDiff) error {
		key := string(diff.Key)
		if !expected[key] || seen[key] {
			t.Fatalf("unexpected or duplicate net diff for %s", key)
		}
		if string(diff.Before) != before[key] || string(diff.After) != after[key] {
			t.Fatalf("net diff mismatches independent boundaries for %s", key)
		}
		seen[key] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(expected) {
		t.Fatalf("journal returned %d of %d independent net changes", len(seen), len(expected))
	}
}

func TestSQLiteJournalReplacementUsesUniqueIndexCollation(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE items(id INTEGER PRIMARY KEY,code TEXT,payload TEXT)`, `CREATE UNIQUE INDEX folded ON items(code COLLATE NOCASE)`, `INSERT INTO items VALUES(1,'lowercase','before')`)
	plan, err := inspectSQLiteTable(ctx, source, config.SQLite, "items")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := installSQLiteJournal(ctx, source, "collated-conflict", []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `PRAGMA recursive_triggers=OFF`, `INSERT OR REPLACE INTO items VALUES(2,'LOWERCASE','after')`)
	spec, err = freezeSQLiteJournal(ctx, source, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSQLiteJournal(ctx, source, spec)
	counts := map[DiffKind]int{}
	err = reduceJournal(ctx, []TablePlan{plan}, func(emit func(JournalEvent) error) error { return readSQLiteJournal(ctx, source, spec, emit) }, func(diff JournalDiff) error { counts[diff.Kind]++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if counts[DiffInserted] != 1 || counts[DiffDeleted] != 1 || counts[DiffUpdated] != 0 {
		t.Fatalf("missed collation-based replacement: %#v", counts)
	}
}

func TestSQLiteJournalIncludesStoredAndVirtualGeneratedColumns(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE items(id INTEGER PRIMARY KEY,value TEXT,folded TEXT GENERATED ALWAYS AS(lower(value)) VIRTUAL,byte_count INTEGER GENERATED ALWAYS AS(length(CAST(value AS BLOB))) STORED)`, `INSERT INTO items(id,value) VALUES(1,'BEFORE')`)
	plan, err := inspectSQLiteTable(ctx, source, config.SQLite, "items")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Columns) != 4 {
		t.Fatalf("generated columns omitted from checkpoint metadata: %#v", plan.Columns)
	}
	spec, err := installSQLiteJournal(ctx, source, "generated", []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `UPDATE items SET value='AFTER-LONGER' WHERE id=1`)
	spec, err = freezeSQLiteJournal(ctx, source, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSQLiteJournal(ctx, source, spec)
	count := 0
	if err := readSQLiteJournal(ctx, source, spec, func(event JournalEvent) error {
		if event.Probe {
			return nil
		}
		var before, after encodedRow
		if err := json.Unmarshal(event.Before, &before); err != nil {
			return err
		}
		if err := json.Unmarshal(event.After, &after); err != nil {
			return err
		}
		if sqliteJournalTestCell(before, "folded").Data != "before" || sqliteJournalTestCell(after, "folded").Data != "after-longer" {
			t.Fatal("virtual generated before/after values were lost")
		}
		if sqliteJournalTestCell(before, "byte_count").Data != "6" || sqliteJournalTestCell(after, "byte_count").Data != "12" {
			t.Fatal("stored generated before/after values were lost")
		}
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one generated-column update, got %d", count)
	}
}

func TestSQLiteJournalRejectsReceiptThatNamesUnrelatedSourceObjects(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE items(id INTEGER PRIMARY KEY)`, `CREATE TABLE unrelated(value TEXT)`)
	plan, err := inspectSQLiteTable(ctx, source, config.SQLite, "items")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := installSQLiteJournal(ctx, source, "owner-check", []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSQLiteJournal(ctx, source, spec)
	var state sqliteJournalSpec
	if err := json.Unmarshal(spec.State, &state); err != nil {
		t.Fatal(err)
	}
	var definition string
	if err := source.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='unrelated'`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	state.Objects = append(state.Objects, sqliteJournalObject{Type: "table", Name: "unrelated", SQL: definition})
	changed := spec
	changed.State, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanupSQLiteJournal(ctx, source, changed); err == nil {
		t.Fatal("cleanup accepted an unrelated table in a modified receipt")
	}
	if _, err := freezeSQLiteJournal(ctx, source, changed); err == nil {
		t.Fatal("stop accepted an unrelated table in a modified receipt")
	}
	var count int
	if err := source.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='unrelated'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unrelated source object was removed: %d %v", count, err)
	}
}

func TestSQLiteJournalCleanupRemovesOwnedTriggersWhenJournalTableIsMissing(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE items(id INTEGER PRIMARY KEY,value TEXT)`)
	plan, err := inspectSQLiteTable(ctx, source, config.SQLite, "items")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := installSQLiteJournal(ctx, source, "missing-journal", []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	var state sqliteJournalSpec
	if err := json.Unmarshal(spec.State, &state); err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `DROP TABLE `+quoteName(config.SQLite, state.Name))
	if _, err := source.Exec(`INSERT INTO items VALUES(1,'blocked')`); err == nil {
		t.Fatal("fixture should leave owned triggers targeting a missing journal")
	}
	if err := cleanupSQLiteJournal(ctx, source, spec); err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `INSERT INTO items VALUES(1,'writes restored')`)
	for _, object := range state.Objects {
		if object.Type != "trigger" {
			continue
		}
		var count int
		if err := source.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name=?`, object.Name).Scan(&count); err != nil || count != 0 {
			t.Fatalf("orphan trigger %s remains: %d %v", object.Name, count, err)
		}
	}
	if err := cleanupSQLiteJournal(ctx, source, spec); err != nil {
		t.Fatalf("missing-journal cleanup is not idempotent: %v", err)
	}
}

func TestSQLiteJournalMissingTableCleanupPreservesReplacedTriggerCode(t *testing.T) {
	ctx := context.Background()
	source := openTestDatabase(t)
	execTestSQL(t, source, `CREATE TABLE items(id INTEGER PRIMARY KEY,value TEXT)`, `CREATE TABLE user_audit(value TEXT)`)
	plan, err := inspectSQLiteTable(ctx, source, config.SQLite, "items")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := installSQLiteJournal(ctx, source, "replacement-code", []TablePlan{plan})
	if err != nil {
		t.Fatal(err)
	}
	var state sqliteJournalSpec
	if err := json.Unmarshal(spec.State, &state); err != nil {
		t.Fatal(err)
	}
	var replaced sqliteJournalObject
	for _, object := range state.Objects {
		if object.Type == "trigger" {
			replaced = object
			break
		}
	}
	execTestSQL(t, source, `DROP TABLE `+quoteName(config.SQLite, state.Name), `DROP TRIGGER `+quoteName(config.SQLite, replaced.Name), `CREATE TRIGGER `+quoteName(config.SQLite, replaced.Name)+` AFTER INSERT ON items BEGIN INSERT INTO user_audit VALUES('user code'); END`)
	var beforeSQL string
	if err := source.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?`, replaced.Name).Scan(&beforeSQL); err != nil {
		t.Fatal(err)
	}
	if err := cleanupSQLiteJournal(ctx, source, spec); err == nil || !strings.Contains(err.Error(), "refuse") {
		t.Fatalf("cleanup should retain recovery state for replaced code: %v", err)
	}
	var afterSQL string
	if err := source.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?`, replaced.Name).Scan(&afterSQL); err != nil || afterSQL != beforeSQL {
		t.Fatalf("cleanup changed replacement trigger code: %q %v", afterSQL, err)
	}
	// Restore the exact owned definition solely to clean up this fixture.
	execTestSQL(t, source, `DROP TRIGGER `+quoteName(config.SQLite, replaced.Name), replaced.SQL)
	if err := cleanupSQLiteJournal(ctx, source, spec); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteJournalStartWorkDependsOnWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("large existing-table fixtures are omitted in short mode")
	}
	for _, size := range []int{1_000, 100_000, 1_000_000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx := context.Background()
			source := openTestDatabase(t)
			execTestSQL(t, source, `CREATE TABLE records(id INTEGER PRIMARY KEY,code TEXT UNIQUE,value TEXT)`)
			if _, err := source.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
				INSERT INTO records SELECT i,'code-'||i,'unchanged-'||i FROM n`, size); err != nil {
				t.Fatal(err)
			}
			plan, err := inspectSQLiteTable(ctx, source, config.SQLite, "records")
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			spec, err := installSQLiteJournal(ctx, source, "scale-invariant", []TablePlan{plan})
			if err != nil {
				t.Fatal(err)
			}
			startElapsed := time.Since(started)
			defer func() { _ = cleanupSQLiteJournal(ctx, source, spec) }()
			var state sqliteJournalSpec
			if err := json.Unmarshal(spec.State, &state); err != nil {
				t.Fatal(err)
			}
			var initialEvents int
			if err := source.QueryRow(`SELECT COUNT(*) FROM ` + quoteName(config.SQLite, state.Name) + ` WHERE seq>0`).Scan(&initialEvents); err != nil {
				t.Fatal(err)
			}
			if initialEvents != 0 {
				t.Fatalf("Start copied %d existing source rows into the change journal", initialEvents)
			}
			if _, err := source.Exec(`UPDATE records SET value='changed' WHERE id=?`, size/2); err != nil {
				t.Fatal(err)
			}
			stopped := time.Now()
			spec, err = freezeSQLiteJournal(ctx, source, spec)
			if err != nil {
				t.Fatal(err)
			}
			stopElapsed := time.Since(stopped)
			events, diffs := 0, 0
			err = reduceJournal(ctx, []TablePlan{plan}, func(emit func(JournalEvent) error) error {
				return readSQLiteJournal(ctx, source, spec, func(event JournalEvent) error {
					events++
					if event.Probe || len(event.Before) == 0 || len(event.After) == 0 {
						t.Fatal("an ordinary update should produce one complete event without conflict probes")
					}
					return emit(event)
				})
			}, func(diff JournalDiff) error {
				diffs++
				if diff.Kind != DiffUpdated {
					t.Fatalf("expected one updated identity, got %s", diff.Kind)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if events != 1 || diffs != 1 {
				t.Fatalf("%d existing rows and one write produced %d events and %d net diffs", size, events, diffs)
			}
			if err := cleanupSQLiteJournal(ctx, source, spec); err != nil {
				t.Fatal(err)
			}
			// Timings are observations, never environment-dependent pass/fail
			// thresholds. The invariant is zero baseline rows and one dirty row.
			t.Logf("existing rows=%d Start=%s Stop=%s journal events=%d net diffs=%d", size, startElapsed, stopElapsed, events, diffs)
		})
	}
}

func sqliteJournalTestCell(row encodedRow, name string) encodedCell {
	for _, cell := range row.Cells {
		if cell.Name == name {
			return cell
		}
	}
	return encodedCell{}
}
