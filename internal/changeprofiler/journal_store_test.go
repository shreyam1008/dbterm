package changeprofiler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func journalStoreFixture(t *testing.T) (*Store, *sql.DB, StartRequest) {
	t.Helper()
	source := openTestDatabase(t)
	source.SetMaxOpenConns(1)
	execTestSQL(t, source,
		`PRAGMA busy_timeout=1`,
		`CREATE TABLE items(id INTEGER PRIMARY KEY, value TEXT, note BLOB)`,
		`INSERT INTO items VALUES(1,'before',X'0001FF'),(2,'delete',NULL),(3,'move',NULL),(4,'unchanged',NULL)`,
	)
	plans, err := Preflight(context.Background(), source, config.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	for index := range plans {
		plans[index].Included = true
	}
	return openTestStore(t), source, StartRequest{Name: "incremental fixture", ConnectionKey: "journal-source", Engine: config.SQLite,
		Mode: CaptureJournal, AllowSourceObjects: true, Tables: plans}
}

func journalStoreOtherWriter(t *testing.T, source *sql.DB) *sql.DB {
	t.Helper()
	var sequence int
	var schema, path string
	if err := source.QueryRow(`PRAGMA database_list`).Scan(&sequence, &schema, &path); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	other.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = other.Close() })
	return other
}

func TestJournalStoreRequiresSourceObjectConsent(t *testing.T) {
	store, source, request := journalStoreFixture(t)
	request.AllowSourceObjects = false
	if _, err := store.Start(context.Background(), source, request, nil); err == nil || !strings.Contains(err.Error(), "explicitly allow") {
		t.Fatalf("incremental capture did not require consent: %v", err)
	}
	var objects int
	if err := source.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name GLOB '__dbterm_cp_*'`).Scan(&objects); err != nil {
		t.Fatal(err)
	}
	if objects != 0 {
		t.Fatalf("refused start left %d source objects", objects)
	}
	anchors, err := store.ListAnchors(context.Background())
	if err != nil || len(anchors) != 0 {
		t.Fatalf("refused start created anchors: %+v, %v", anchors, err)
	}
}

func TestJournalStoreAllWritersNetDiffWithoutBaseline(t *testing.T) {
	store, source, request := journalStoreFixture(t)
	ctx := context.Background()
	anchor, err := store.Start(ctx, source, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.CaptureMode != CaptureJournal || !anchor.CleanupPending || anchor.Status != StatusActive {
		t.Fatalf("unexpected armed checkpoint: %+v", anchor)
	}
	var baseline int
	if err := store.db.QueryRow(`SELECT count(*) FROM profiler_baseline_rows WHERE anchor_id=?`, anchor.ID).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	if baseline != 0 {
		t.Fatalf("incremental Start copied %d baseline rows", baseline)
	}
	other := journalStoreOtherWriter(t, source)
	execTestSQL(t, other,
		`UPDATE items SET value='intermediate' WHERE id=1`,
		`UPDATE items SET value='after',note=X'00FF80' WHERE id=1`,
		`DELETE FROM items WHERE id=2`,
		`UPDATE items SET id=30 WHERE id=3`,
		`UPDATE items SET value='temporary' WHERE id=4`,
		`UPDATE items SET value='unchanged' WHERE id=4`,
		`INSERT INTO items VALUES(50,'temporary',NULL)`,
		`DELETE FROM items WHERE id=50`,
		`INSERT INTO items VALUES(5,'new',NULL)`,
	)
	rolledBack, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rolledBack.Exec(`INSERT INTO items VALUES(99,'rolled back',NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := rolledBack.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scan(ctx, source, anchor.ID, false, nil); err == nil {
		t.Fatal("live comparison silently scanned an incremental checkpoint")
	}
	finished, err := store.Scan(ctx, source, anchor.ID, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != StatusComplete || finished.CleanupPending || finished.Inserted != 2 || finished.Updated != 1 || finished.Deleted != 2 {
		t.Fatalf("incorrect final net changes: %+v", finished)
	}
	rows, err := store.ListDiffRows(ctx, anchor.ID, "items", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("expected 5 net differences, got %+v", rows)
	}
	updated := false
	for _, row := range rows {
		if row.Kind == DiffUpdated {
			updated = true
			if row.Before["value"].Text != "before" || row.After["value"].Text != "after" || row.After["note"].Text != "0x00ff80" {
				t.Fatalf("before/after images corrupted: %+v", row)
			}
		}
	}
	if !updated {
		t.Fatal("update missing")
	}
	if _, err := store.Scan(ctx, source, anchor.ID, true, nil); err != nil {
		t.Fatalf("finished checkpoint could not be reopened: %v", err)
	}
}

func TestJournalStoreDeleteRequiresExplicitSourceCleanup(t *testing.T) {
	store, source, request := journalStoreFixture(t)
	ctx := context.Background()
	anchor, err := store.Start(ctx, source, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAnchor(ctx, anchor.ID); err == nil {
		t.Fatal("deleting a recording checkpoint orphaned its source objects")
	}
	if err := store.RemoveJournal(ctx, source, anchor.ID); err != nil {
		t.Fatal(err)
	}
	removed, err := store.GetAnchor(ctx, anchor.ID)
	if err != nil || removed.CleanupPending || removed.Status != StatusFailed {
		t.Fatalf("explicit discard was not recorded: %+v, %v", removed, err)
	}
	if err := store.DeleteAnchor(ctx, anchor.ID); err != nil {
		t.Fatalf("deleting a cleaned checkpoint: %v", err)
	}
}

func TestJournalStoreCanceledReductionRetainsFrozenJournalForRetry(t *testing.T) {
	store, source, request := journalStoreFixture(t)
	ctx := context.Background()
	anchor, err := store.Start(ctx, source, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `UPDATE items SET value='captured at Stop' WHERE id=1`)
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	if _, err := store.Scan(canceled, source, anchor.ID, true, func(progress Progress) {
		if progress.Phase == "reducing committed changes" {
			cancel()
		}
	}); err == nil {
		t.Fatal("reduction unexpectedly succeeded after cancellation")
	}
	spec, pending, err := store.loadJournal(ctx, anchor.ID)
	if err != nil || !spec.Frozen || !pending {
		t.Fatalf("frozen journal was not retained: %+v, pending=%v, err=%v", spec, pending, err)
	}
	// Writes after the committed Stop boundary must never enter a retried report.
	execTestSQL(t, source, `UPDATE items SET value='after Stop' WHERE id=1`)
	finished, err := store.Scan(ctx, source, anchor.ID, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if finished.CleanupPending || finished.Updated != 1 {
		t.Fatalf("retry failed: %+v", finished)
	}
	rows, err := store.ListDiffRows(ctx, anchor.ID, "items", 10)
	if err != nil || len(rows) != 1 || rows[0].After["value"].Text != "captured at Stop" {
		t.Fatalf("retry moved the Stop boundary: %+v, %v", rows, err)
	}
}

func TestJournalStoreCleanupFailurePreservesCompletedDiffAndRetries(t *testing.T) {
	store, source, request := journalStoreFixture(t)
	ctx := context.Background()
	anchor, err := store.Start(ctx, source, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `UPDATE items SET value='after' WHERE id=1`)
	locker := journalStoreOtherWriter(t, source)
	locked := false
	_, err = store.Scan(ctx, source, anchor.ID, true, func(progress Progress) {
		if progress.Phase == "checkpoint diff saved" {
			if _, lockErr := locker.Exec(`BEGIN EXCLUSIVE`); lockErr != nil {
				t.Fatal(lockErr)
			}
			locked = true
		}
	})
	if locked {
		if _, unlockErr := locker.Exec(`ROLLBACK`); unlockErr != nil {
			t.Fatal(unlockErr)
		}
	}
	if err == nil {
		t.Fatal("expected source cleanup to report its write lock failure")
	}
	completed, err := store.GetAnchor(ctx, anchor.ID)
	if err != nil || completed.Status != StatusComplete || !completed.CleanupPending || completed.Updated != 1 {
		t.Fatalf("cleanup failure discarded saved report: %+v, %v", completed, err)
	}
	if err := store.DeleteAnchor(ctx, anchor.ID); err == nil {
		t.Fatal("cleanup-pending report deletion was allowed")
	}
	finished, err := store.Scan(ctx, source, anchor.ID, true, nil)
	if err != nil || finished.CleanupPending || finished.Updated != 1 {
		t.Fatalf("cleanup retry did not preserve report: %+v, %v", finished, err)
	}
}

func TestJournalStoreCoverageFailurePreservesExistingReport(t *testing.T) {
	store, source, request := journalStoreFixture(t)
	ctx := context.Background()
	anchor, err := store.Start(ctx, source, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	// An existing durable report is a fixture for the failure guarantee. A failed
	// stop must never clear it before source coverage has been established.
	before := encodedRow{Cells: []encodedCell{normalizeCell("id", "INTEGER", int64(1)), normalizeCell("value", "TEXT", "before"), normalizeCell("note", "BLOB", nil)}}
	after := before
	after.Cells = append([]encodedCell(nil), before.Cells...)
	after.Cells[1] = normalizeCell("value", "TEXT", "kept report")
	beforeBlob, _, _ := encodeRow(before)
	afterBlob, _, _ := encodeRow(after)
	key, _, _ := encodeRowKey(before, []string{"id"}, KeyPrimary, 0)
	hash := sha256.Sum256(key)
	if _, err := store.db.Exec(`INSERT INTO profiler_diff_rows(anchor_id,table_name,key_hash,key_blob,kind,before_blob,after_blob,changed_columns_json) VALUES(?,?,?,?,?,?,?,?)`, anchor.ID, "items", hash[:], key, DiffUpdated, beforeBlob, afterBlob, `["value"]`); err != nil {
		t.Fatal(err)
	}
	spec, _, err := store.loadJournal(ctx, anchor.ID)
	if err != nil {
		t.Fatal(err)
	}
	var state sqliteJournalSpec
	if err := json.Unmarshal(spec.State, &state); err != nil {
		t.Fatal(err)
	}
	for _, object := range state.Objects {
		if object.Type == "trigger" {
			execTestSQL(t, source, "DROP TRIGGER "+quoteName(config.SQLite, object.Name))
			break
		}
	}
	if _, err := store.Scan(ctx, source, anchor.ID, true, nil); err == nil {
		t.Fatal("missing trigger was accepted as complete coverage")
	}
	rows, err := store.ListDiffRows(ctx, anchor.ID, "items", 10)
	if err != nil || len(rows) != 1 || rows[0].After["value"].Text != "kept report" {
		t.Fatalf("coverage failure discarded report: %+v, %v", rows, err)
	}
	retained, err := store.GetAnchor(ctx, anchor.ID)
	if err != nil || !retained.CleanupPending || retained.Error == "" {
		t.Fatalf("coverage failure did not retain recovery metadata: %+v, %v", retained, err)
	}
}

func TestJournalStoreConcurrentFinishCannotReviveCleanedJournal(t *testing.T) {
	store, source, request := journalStoreFixture(t)
	ctx := context.Background()
	anchor, err := store.Start(ctx, source, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, source, `UPDATE items SET value='after' WHERE id=1`)
	entered := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := store.Scan(ctx, source, anchor.ID, true, func(progress Progress) {
			if progress.Phase == "stopping change capture" {
				close(entered)
				<-release
			}
		})
		first <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first Finish did not reach its Stop boundary")
	}
	second := make(chan error, 1)
	go func() { _, err := store.Scan(ctx, source, anchor.ID, true, nil); second <- err }()
	// A concurrent Finish may reject an in-flight operation or wait for it.
	// Both policies must preserve the successful operation's completed state.
	secondFinished := false
	select {
	case <-second:
		secondFinished = true
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-first:
		if err != nil {
			t.Errorf("first Finish: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first Finish remained blocked")
	}
	if !secondFinished {
		select {
		case <-second:
		case <-time.After(5 * time.Second):
			t.Fatal("second Finish remained blocked")
		}
	}
	finished, err := store.GetAnchor(ctx, anchor.ID)
	if err != nil || finished.Status != StatusComplete || finished.CleanupPending || finished.Updated != 1 {
		t.Fatalf("concurrent Finish revived a cleaned journal: %+v, %v", finished, err)
	}
}

func TestJournalStoreSeparateHandlesCannotRaceFinish(t *testing.T) {
	store, source, request := journalStoreFixture(t)
	ctx := context.Background()
	anchor, err := store.Start(ctx, source, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := OpenStore(store.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	execTestSQL(t, source, `UPDATE items SET value='after' WHERE id=1`)
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := store.Scan(ctx, source, anchor.ID, true, func(progress Progress) {
			if progress.Phase == "stopping change capture" {
				close(entered)
				<-release
			}
		})
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first catalog handle did not reach Stop")
	}
	_, secondErr := other.Scan(ctx, source, anchor.ID, true, nil)
	close(release)
	if secondErr == nil || !strings.Contains(secondErr.Error(), "another checkpoint operation") {
		t.Errorf("second catalog handle did not report an in-flight operation: %v", secondErr)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first catalog handle remained blocked")
	}
	result, err := other.GetAnchor(ctx, anchor.ID)
	if err != nil || result.Status != StatusComplete || result.CleanupPending || result.Updated != 1 {
		t.Fatalf("second catalog handle corrupted Finish metadata: %+v, %v", result, err)
	}
}

func TestJournalStoreOpenCannotRecoverAnotherHandlesLiveOperation(t *testing.T) {
	store, source, request := journalStoreFixture(t)
	ctx := context.Background()
	anchor, err := store.Start(ctx, source, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	release, err := store.lockJournalOperation()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// A process arming a checkpoint owns this lock while its local row still
	// says capturing. Another opener must not call that live work interrupted.
	if _, err := store.db.Exec(`UPDATE profiler_anchors SET status='capturing' WHERE id=?`, anchor.ID); err != nil {
		t.Fatal(err)
	}
	other, err := OpenStore(store.path)
	if other != nil {
		_ = other.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "another checkpoint operation") {
		t.Fatalf("OpenStore raced a live checkpoint operation: %v", err)
	}
	unchanged, err := store.GetAnchor(ctx, anchor.ID)
	if err != nil || unchanged.Status != StatusCapturing {
		t.Fatalf("OpenStore misclassified live operation as interrupted: %+v, %v", unchanged, err)
	}
}
