package ui

import (
	"context"
	"database/sql"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

func TestResolvedResultLimitGuardsWideTables(t *testing.T) {
	t.Run("requested limit respected when below guard", func(t *testing.T) {
		if got := resolvedResultLimit(100, 4); got != 100 {
			t.Fatalf("resolvedResultLimit(100, 4) = %d, want 100", got)
		}
	})

	t.Run("adaptive mode caps wide tables", func(t *testing.T) {
		got := resolvedResultLimit(adaptiveTablePreviewLimit, 80)
		if got <= 0 || got >= maxResultRows {
			t.Fatalf("resolvedResultLimit(auto, 80) = %d, expected a guarded value between 1 and %d", got, maxResultRows)
		}
	})

	t.Run("column count always yields at least one row", func(t *testing.T) {
		if got := resolvedResultLimit(adaptiveTablePreviewLimit, 5000); got < 1 || got > 2 {
			t.Fatalf("resolvedResultLimit(auto, 5000) = %d, want a minimal guarded value", got)
		}
	})
}

func TestResultSizeShortcutsSeparateColumnZoomAndRows(t *testing.T) {
	tests := []struct {
		name  string
		event *tcell.EventKey
		want  resultSizeShortcut
	}{
		{name: "plain plus selected column", event: tcell.NewEventKey(tcell.KeyRune, '+', tcell.ModShift), want: resultSizeSelectedIncrease},
		{name: "plain minus selected column", event: tcell.NewEventKey(tcell.KeyRune, '-', tcell.ModNone), want: resultSizeSelectedDecrease},
		{name: "shifted minus selected column", event: tcell.NewEventKey(tcell.KeyRune, '_', tcell.ModShift), want: resultSizeSelectedDecrease},
		{name: "ctrl equal all columns", event: tcell.NewEventKey(tcell.KeyRune, '=', tcell.ModCtrl), want: resultSizeAllIncrease},
		{name: "ctrl plus all columns", event: tcell.NewEventKey(tcell.KeyRune, '+', tcell.ModCtrl|tcell.ModShift), want: resultSizeAllIncrease},
		{name: "ctrl minus all columns", event: tcell.NewEventKey(tcell.KeyRune, '-', tcell.ModCtrl), want: resultSizeAllDecrease},
		{name: "ctrl underscore fallback", event: tcell.NewEventKey(tcell.KeyCtrlUnderscore, 0, tcell.ModNone), want: resultSizeAllDecrease},
		{name: "ctrl zero reset", event: tcell.NewEventKey(tcell.KeyRune, '0', tcell.ModCtrl), want: resultSizeAllReset},
		{name: "alt plus more rows", event: tcell.NewEventKey(tcell.KeyRune, '+', tcell.ModAlt|tcell.ModShift), want: resultSizeRowsIncrease},
		{name: "alt minus fewer rows", event: tcell.NewEventKey(tcell.KeyRune, '-', tcell.ModAlt), want: resultSizeRowsDecrease},
		{name: "alt zero toggles rows", event: tcell.NewEventKey(tcell.KeyRune, '0', tcell.ModAlt), want: resultSizeRowsToggle},
		{name: "greater fallback all columns", event: tcell.NewEventKey(tcell.KeyRune, '>', tcell.ModShift), want: resultSizeAllIncrease},
		{name: "less fallback all columns", event: tcell.NewEventKey(tcell.KeyRune, '<', tcell.ModShift), want: resultSizeAllDecrease},
		{name: "plain zero reset", event: tcell.NewEventKey(tcell.KeyRune, '0', tcell.ModNone), want: resultSizeAllReset},
		{name: "mixed modifiers ignored", event: tcell.NewEventKey(tcell.KeyRune, '+', tcell.ModCtrl|tcell.ModAlt), want: resultSizeNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resultSizeShortcutFor(tt.event); got != tt.want {
				t.Fatalf("resultSizeShortcutFor() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPreparedTableResultRequestsHaveUniqueGenerations(t *testing.T) {
	app := &App{
		db:            &sql.DB{},
		dbType:        config.SQLite,
		selectedTable: "users",
		results:       newResultTable(),
		resultLimit:   100,
		sortColumn:    -1,
	}

	first, err := app.prepareTableResultRequest()
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	second, err := app.prepareTableResultRequest()
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	if first.generation == second.generation {
		t.Fatalf("request generations both = %d, want unique values", first.generation)
	}
	if !app.tableResultRequestIsCurrent(second) {
		t.Fatal("newest request should own the current result generation")
	}
	if app.tableResultRequestIsCurrent(first) {
		t.Fatal("older request should be stale")
	}
}

func TestApplyTableResultSnapshotRejectsStaleRequest(t *testing.T) {
	app := &App{
		db:            &sql.DB{},
		dbType:        config.SQLite,
		dbName:        "test",
		selectedTable: "users",
		results:       newResultTable(),
		statusBar:     tview.NewTextView(),
		resultLimit:   100,
		totalRowCount: -1,
		sortColumn:    -1,
	}
	app.results.SetCell(0, 0, tview.NewTableCell("ID").SetReference("id"))
	app.results.SetCell(1, 0, tview.NewTableCell("old").SetReference(resultCellReference{value: "old"}))

	staleRequest, err := app.prepareTableResultRequest()
	if err != nil {
		t.Fatalf("stale request: %v", err)
	}
	currentRequest, err := app.prepareTableResultRequest()
	if err != nil {
		t.Fatalf("current request: %v", err)
	}

	currentTable := newResultTable()
	currentTable.SetCell(0, 0, tview.NewTableCell("ID").SetReference("id"))
	currentTable.SetCell(1, 0, tview.NewTableCell("new").SetReference(resultCellReference{value: "complete-new-value"}))
	if !app.applyTableResultSnapshot(&tableResultSnapshot{
		request:     currentRequest,
		results:     currentTable,
		columnNames: []string{"id"},
		rowCount:    1,
	}) {
		t.Fatal("current snapshot was not applied")
	}

	staleTable := newResultTable()
	staleTable.SetCell(0, 0, tview.NewTableCell("ID").SetReference("id"))
	staleTable.SetCell(1, 0, tview.NewTableCell("stale"))
	if app.applyTableResultSnapshot(&tableResultSnapshot{
		request:     staleRequest,
		results:     staleTable,
		columnNames: []string{"id"},
		rowCount:    1,
	}) {
		t.Fatal("stale snapshot should have been rejected")
	}

	cell := app.results.GetCell(1, 0)
	if cell.Text != "new" {
		t.Fatalf("visible value = %q, want newest snapshot value", cell.Text)
	}
	ref, ok := cell.GetReference().(resultCellReference)
	if !ok || ref.value != "complete-new-value" {
		t.Fatalf("visible cell reference = %#v, want full newest value", cell.GetReference())
	}
}

func TestPrepareTableResultRequestRequiresResultsView(t *testing.T) {
	app := &App{db: &sql.DB{}, selectedTable: "users"}
	if _, err := app.prepareTableResultRequest(); err == nil {
		t.Fatal("prepareTableResultRequest() expected an unavailable results view error")
	}
}

func TestFetchTableResultSnapshotAppliesExactFilterOffscreen(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`CREATE TABLE items (id INTEGER, code TEXT);
		INSERT INTO items VALUES (1, 'alpha'), (2, 'beta'), (3, 'alpha');`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}

	app := &App{
		db:            db,
		dbType:        config.SQLite,
		selectedTable: "items",
		resultFilter:  &resultValueFilter{table: "items", column: "code", value: "beta"},
		results:       newResultTable(),
		resultLimit:   100,
		sortColumn:    -1,
	}
	request, err := app.prepareTableResultRequest()
	if err != nil {
		t.Fatalf("prepare filtered request: %v", err)
	}
	snapshot, err := fetchTableResultSnapshot(context.Background(), request)
	if err != nil {
		t.Fatalf("fetch filtered snapshot: %v", err)
	}
	if snapshot.rowCount != 1 {
		t.Fatalf("filtered row count = %d, want 1", snapshot.rowCount)
	}
	if got := snapshot.results.GetCell(1, 1).Text; got != "beta" {
		t.Fatalf("filtered code = %q, want beta", got)
	}
	if app.results.GetRowCount() != 0 {
		t.Fatal("background fetch mutated the live results table before apply")
	}
}

func TestQuoteIdentifierQualifiedNames(t *testing.T) {
	if got := quoteIdentifier(config.PostgreSQL, "public.users"); got != `"public"."users"` {
		t.Fatalf("postgres qualified quote = %q", got)
	}

	if got := quoteIdentifier(config.MySQL, "app.users"); got != "`app`.`users`" {
		t.Fatalf("mysql qualified quote = %q", got)
	}
}

func TestResultRowSelectionPreservesCellValueReference(t *testing.T) {
	app := &App{results: newResultTable()}
	app.results.SetCell(0, 0, tview.NewTableCell("ID").SetReference("id"))
	app.results.SetCell(1, 0, tview.NewTableCell("42").SetReference(resultCellReference{value: "42"}))

	if !app.setResultRowSelected(1, true) {
		t.Fatal("expected row selection state to change")
	}
	ref, ok := app.results.GetCell(1, 0).GetReference().(resultCellReference)
	if !ok {
		t.Fatal("cell reference type was not preserved")
	}
	if ref.value != "42" || !ref.rowSelected {
		t.Fatalf("cell reference = %#v, want value 42 and selected", ref)
	}
}
