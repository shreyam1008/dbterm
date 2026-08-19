package ui

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

func TestDateDatabaseValuesRemainDateOnly(t *testing.T) {
	value := time.Date(2021, time.October, 23, 15, 4, 5, 123400000, time.FixedZone("test", 5*60*60+30*60))

	display, _ := formatCellValueForDatabaseType(value, "DATE")
	if display != "2021-10-23" {
		t.Fatalf("DATE display = %q, want date only", display)
	}

	reference := newResultCellReferenceForDatabaseType(value, display, "DATE")
	if reference.value != "2021-10-23" {
		t.Fatalf("DATE complete value = %q, want date only", reference.value)
	}
	if exported := resultExportReferenceText(reference); exported != "2021-10-23" {
		t.Fatalf("DATE export = %q, want date only", exported)
	}

	temporalCases := map[string]string{
		"TIME":        "15:04:05.1234",
		"TIMETZ":      "15:04:05.1234+05:30",
		"TIMESTAMP":   "2021-10-23 15:04:05.1234",
		"DATETIME":    "2021-10-23 15:04:05.1234",
		"TIMESTAMPTZ": "2021-10-23 15:04:05.1234+05:30",
	}
	for databaseType, want := range temporalCases {
		got, _ := formatCellValueForDatabaseType(value, databaseType)
		if got != want {
			t.Errorf("%s display = %q, want %q", databaseType, got, want)
		}
	}
}

func TestPopulateTableEscapesLiteralTViewTags(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	const literal = "[red]literal[-]"
	rows, err := db.Query(`SELECT '` + literal + `' AS "[value]"`)
	if err != nil {
		t.Fatalf("query literal: %v", err)
	}
	defer rows.Close()

	table := newResultTable()
	if _, err := populateTable(table, rows); err != nil {
		t.Fatalf("populate table: %v", err)
	}
	if got, want := table.GetCell(0, 0).Text, tview.Escape("[VALUE]"); got != want {
		t.Fatalf("header text = %q, want escaped %q", got, want)
	}
	if got, want := table.GetCell(1, 0).Text, tview.Escape(literal); got != want {
		t.Fatalf("cell text = %q, want escaped %q", got, want)
	}
	reference, ok := table.GetCell(1, 0).GetReference().(resultCellReference)
	if !ok || reference.value != literal {
		t.Fatalf("raw reference = %#v, want literal value", table.GetCell(1, 0).GetReference())
	}
}

func TestTextByteValuesUseRuneSafePreview(t *testing.T) {
	value := []byte(strings.Repeat("界", maxCellPreviewRunes+1))
	display, _ := formatCellValueForDatabaseType(value, "VARCHAR")
	if !utf8.ValidString(display) {
		t.Fatalf("VARCHAR preview is invalid UTF-8: %q", display)
	}
	if got := utf8.RuneCountInString(display); got != maxCellPreviewRunes {
		t.Fatalf("VARCHAR preview rune count = %d, want %d", got, maxCellPreviewRunes)
	}
	if !strings.HasSuffix(display, "...") {
		t.Fatalf("VARCHAR preview = %q, want ellipsis", display)
	}
	reference := newResultCellReferenceForDatabaseType(value, display, "VARCHAR")
	if !reference.truncated || reference.value != string(value) {
		t.Fatalf("VARCHAR reference lost full value: %#v", reference)
	}
}

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

func TestResultSelectionMovesBetweenDataAndColumnHeader(t *testing.T) {
	table := newResultTable()
	for col := 0; col < 3; col++ {
		table.SetCell(0, col, tview.NewTableCell("header").SetReference("header").SetSelectable(true))
		table.SetCell(1, col, tview.NewTableCell("value"))
		table.SetCell(2, col, tview.NewTableCell("value"))
	}
	app := &App{results: table}

	table.Select(1, 2)
	up := tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone)
	if !app.handleResultColumnInput(up) {
		t.Fatal("Up on the first data row should enter the header")
	}
	if row, col := table.GetSelection(); row != 0 || col != 2 {
		t.Fatalf("selection = (%d, %d), want header (0, 2)", row, col)
	}

	down := tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)
	if !app.handleResultColumnInput(down) {
		t.Fatal("Down on the header should return to data")
	}
	if row, col := table.GetSelection(); row != 1 || col != 2 {
		t.Fatalf("selection after Down = (%d, %d), want (1, 2)", row, col)
	}

	table.Select(2, 2)
	if app.handleResultColumnInput(up) {
		t.Fatal("Up below the first data row should reach the table navigation handler")
	}
	table.InputHandler()(up, func(tview.Primitive) {})
	if row, col := table.GetSelection(); row != 1 || col != 2 {
		t.Fatalf("selection after moving up = (%d, %d), want (1, 2)", row, col)
	}
}

func TestResultColumnTypeAheadHighlightsAndSelectsMatch(t *testing.T) {
	table := newResultTable()
	columns := []string{"id", "email_address", "created_at"}
	for col, column := range columns {
		table.SetCell(0, col, tview.NewTableCell(strings.ToUpper(column)).SetReference(column).SetSelectable(true))
		table.SetCell(1, col, tview.NewTableCell("value"))
	}
	app := &App{results: table, sortColumn: -1}
	table.Select(0, 0)

	for _, r := range "EMAIL" {
		if !app.handleResultColumnInput(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone)) {
			t.Fatalf("column search rune %q was not consumed", r)
		}
	}
	if row, col := table.GetSelection(); row != 0 || col != 1 {
		t.Fatalf("column search selection = (%d, %d), want header (0, 1)", row, col)
	}
	if got := table.GetCell(0, 1).Text; !strings.Contains(got, "[black:#f9e2af:b]EMAIL[-:-:-]") {
		t.Fatalf("matching header is not highlighted: %q", got)
	}

	if !app.handleResultColumnInput(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)) {
		t.Fatal("Down did not leave column search")
	}
	if app.resultColumnSearch != "" {
		t.Fatalf("column search was not cleared: %q", app.resultColumnSearch)
	}
	if row, col := table.GetSelection(); row != 1 || col != 1 {
		t.Fatalf("data selection = (%d, %d), want (1, 1)", row, col)
	}
}

func TestResultHeaderStatusExplainsColumnSearchControls(t *testing.T) {
	table := newResultTable()
	table.SetCell(0, 0, tview.NewTableCell("EMAIL").SetReference("email").SetSelectable(true))
	table.SetCell(1, 0, tview.NewTableCell("a@example.com"))
	table.Select(0, 0)
	app := &App{results: table, focusedPanel: table, resultColumnSearch: "em"}

	hint := app.statusActionText(140)
	for _, want := range []string{"find:", "Type", "Find column", "↓/Enter", "Shift+C", "Tab", "Tables"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("header status = %q, want %q", hint, want)
		}
	}
}

func TestResultHeaderPositionRestoresPerTable(t *testing.T) {
	table := newResultTable()
	for col := 0; col < 3; col++ {
		table.SetCell(0, col, tview.NewTableCell("HEADER").SetReference(fmt.Sprintf("column_%d", col)).SetSelectable(true))
		table.SetCell(1, col, tview.NewTableCell("value"))
	}
	app := &App{results: table}
	app.restoreResultSelection(resultSelectionState{row: 0, col: 2, offsetCol: 1}, 1)

	if row, col := table.GetSelection(); row != 0 || col != 2 {
		t.Fatalf("restored header selection = (%d, %d), want (0, 2)", row, col)
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

func TestPreparedTableResultRequestRestoresDestinationTablePosition(t *testing.T) {
	remembered := resultSelectionState{
		row:             7,
		col:             3,
		offsetRow:       5,
		offsetCol:       2,
		hasDataRow:      true,
		selectedRowText: []string{"value:remembered"},
	}
	app := &App{
		db:                 &sql.DB{},
		dbType:             config.SQLite,
		selectedTable:      "orders",
		activeTable:        "users",
		tableResultsActive: true,
		results:            newResultTable(),
		resultLimit:        100,
		sortColumn:         -1,
		resultPositions:    map[string]resultSelectionState{"orders": remembered},
	}
	app.results.SetCell(0, 0, tview.NewTableCell("id").SetSelectable(false))
	app.results.SetCell(1, 0, tview.NewTableCell("source"))
	app.results.Select(1, 0)

	request, err := app.prepareTableResultRequest()
	if err != nil {
		t.Fatalf("prepare request: %v", err)
	}
	if got := request.selection; got.row != remembered.row || got.col != remembered.col || got.offsetRow != remembered.offsetRow || got.offsetCol != remembered.offsetCol || !got.hasDataRow || strings.Join(got.selectedRowText, ",") != strings.Join(remembered.selectedRowText, ",") {
		t.Fatalf("request selection = %#v, want remembered %#v", got, remembered)
	}

	// The request owns a copy so later cache mutations cannot alter in-flight work.
	app.resultPositions["orders"].selectedRowText[0] = "value:changed"
	if got := request.selection.selectedRowText[0]; got != "value:remembered" {
		t.Fatalf("request selection signature mutated to %q", got)
	}
}

func TestPreparedTableResultRequestStartsUnvisitedTableAtTopLeft(t *testing.T) {
	app := &App{
		db:                 &sql.DB{},
		dbType:             config.SQLite,
		selectedTable:      "orders",
		activeTable:        "users",
		tableResultsActive: true,
		results:            newResultTable(),
		resultLimit:        100,
		sortColumn:         -1,
	}
	app.results.SetCell(0, 0, tview.NewTableCell("id").SetSelectable(false))
	app.results.SetCell(1, 0, tview.NewTableCell("source"))
	app.results.Select(1, 0)
	app.results.SetOffset(4, 2)

	request, err := app.prepareTableResultRequest()
	if err != nil {
		t.Fatalf("prepare request: %v", err)
	}
	if got := request.selection; got.row != 1 || got.col != 0 || got.offsetRow != 0 || got.offsetCol != 0 || got.hasDataRow {
		t.Fatalf("new table selection = %#v, want top-left default", got)
	}
}

func TestResetCurrentResultPositionReturnsToTopLeftAndForgetsCache(t *testing.T) {
	results := newResultTable()
	for col := 0; col < 3; col++ {
		results.SetCell(0, col, tview.NewTableCell("header").SetSelectable(false))
		for row := 1; row <= 4; row++ {
			results.SetCell(row, col, tview.NewTableCell("value"))
		}
	}
	results.Select(4, 2)
	results.SetOffset(3, 1)
	app := &App{
		activeTable:        "users",
		selectedTable:      "users",
		tableResultsActive: true,
		results:            results,
		resultPositions: map[string]resultSelectionState{
			"users": {row: 4, col: 2, offsetRow: 3, offsetCol: 1},
		},
	}

	app.resetCurrentResultPosition()
	if row, col := results.GetSelection(); row != 1 || col != 0 {
		t.Fatalf("reset selection = (%d, %d), want (1, 0)", row, col)
	}
	if row, col := results.GetOffset(); row != 0 || col != 0 {
		t.Fatalf("reset offset = (%d, %d), want (0, 0)", row, col)
	}
	if _, remembered := app.resultPositions["users"]; remembered {
		t.Fatal("reset position remained in the per-table cache")
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

func TestManualQueryResultOwnershipRejectsNewerTableIntent(t *testing.T) {
	db := &sql.DB{}
	app := &App{db: db}
	queryGeneration := app.advanceResultGeneration()
	if !app.manualQueryResultIsCurrent(db, queryGeneration) {
		t.Fatal("manual query should initially own the result view")
	}
	app.advanceResultGeneration()
	if app.manualQueryResultIsCurrent(db, queryGeneration) {
		t.Fatal("manual query remained current after a newer result intent")
	}
	app.db = &sql.DB{}
	if app.manualQueryResultIsCurrent(db, app.currentResultGeneration()) {
		t.Fatal("manual query remained current after the connection changed")
	}
}

func TestApplyColumnWidthsUsesUniformFixedDefault(t *testing.T) {
	table := newResultTable()
	table.SetCell(0, 0, tview.NewTableCell("ID").SetReference("id").SetExpansion(1))
	table.SetCell(0, 1, tview.NewTableCell("VERY_LONG_PROFILE_COLUMN").SetReference("profile").SetExpansion(1))
	table.SetCell(1, 0, tview.NewTableCell("1").SetExpansion(1))
	table.SetCell(1, 1, tview.NewTableCell("a value").SetExpansion(1))

	app := &App{results: table, lastScreenW: 120, lastScreenH: 40}
	app.applyColumnWidths()

	for col := 0; col < table.GetColumnCount(); col++ {
		for row := 0; row < table.GetRowCount(); row++ {
			cell := table.GetCell(row, col)
			if cell.MaxWidth != defaultColBase || cell.Expansion != 0 {
				t.Fatalf("cell (%d,%d) width/expansion = %d/%d, want %d/0", row, col, cell.MaxWidth, cell.Expansion, defaultColBase)
			}
		}
		if got := tview.TaggedStringWidth(table.GetCell(0, col).Text); got != defaultColBase {
			t.Fatalf("header %d rendered width = %d, want %d", col, got, defaultColBase)
		}
	}
	app.sortColumn = 0
	app.sortAsc = true
	app.setSortHeaderIndicator()
	if got := tview.TaggedStringWidth(table.GetCell(0, 0).Text); got != defaultColBase {
		t.Fatalf("sorted header width = %d, want %d", got, defaultColBase)
	}
}

func TestLocalSortUsesCompleteValuesInsteadOfRoundedPreview(t *testing.T) {
	table := newResultTable()
	table.SetCell(0, 0, tview.NewTableCell("AMOUNT").SetReference("amount"))
	table.SetCell(1, 0, tview.NewTableCell("1.235").SetReference(newResultCellReference(1.2349, "1.235")))
	table.SetCell(2, 0, tview.NewTableCell("1.235").SetReference(newResultCellReference(1.2341, "1.235")))

	app := &App{results: table, sortColumn: 0, sortAsc: true}
	app.applySort()

	first, ok := table.GetCell(1, 0).GetReference().(resultCellReference)
	if !ok || first.rawValue != 1.2341 {
		t.Fatalf("first sorted value = %#v, want complete value 1.2341", table.GetCell(1, 0).GetReference())
	}
}

func TestRowSignatureUsesCompleteValuesInsteadOfTruncatedPreview(t *testing.T) {
	table := newResultTable()
	table.SetCell(0, 0, tview.NewTableCell("VALUE").SetReference("value"))
	table.SetCell(1, 0, tview.NewTableCell("same preview...").SetReference(newResultCellReference("first complete value", "same preview...")))
	table.SetCell(2, 0, tview.NewTableCell("same preview...").SetReference(newResultCellReference("second complete value", "same preview...")))

	signature := tableRowSignature(table, 2, 1)
	if got := findMatchingRow(table, signature, 2, 1); got != 2 {
		t.Fatalf("matching row = %d, want the row with the same complete value", got)
	}
}

func TestColumnWidthsPersistPerConnectionAndTable(t *testing.T) {
	t.Setenv("DBTERM_CONFIG_DIR", t.TempDir())
	t.Setenv("DBTERM_STATE_DIR", t.TempDir())
	connection := &config.ConnectionConfig{Type: config.SQLite, FilePath: filepath.Join(t.TempDir(), "test.db")}
	settings := config.DefaultSettings()
	app := &App{
		activeConn:         connection,
		settings:           settings,
		selectedTable:      "users",
		tableResultsActive: true,
		colWidthOverrides:  map[string]int{"id": 18, "profile": 42},
	}

	if err := app.persistColumnWidths(); err != nil {
		t.Fatalf("persistColumnWidths() error = %v", err)
	}
	app.selectedTable = "orders"
	app.colWidthOverrides = map[string]int{"total": 22}
	if err := app.persistColumnWidths(); err != nil {
		t.Fatalf("persist orders widths: %v", err)
	}
	reloaded, err := config.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	restarted := &App{activeConn: connection, settings: reloaded}
	restarted.restoreColumnWidths("users")
	if got := restarted.colWidthOverrides["profile"]; got != 42 {
		t.Fatalf("restored users.profile width = %d, want 42", got)
	}

	restarted.restoreColumnWidths("orders")
	if got := restarted.colWidthOverrides["total"]; got != 22 {
		t.Fatalf("restored orders.total width = %d, want 22", got)
	}
	if _, inherited := restarted.colWidthOverrides["profile"]; inherited {
		t.Fatalf("orders inherited users widths: %#v", restarted.colWidthOverrides)
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

func TestFetchTableResultSnapshotAppliesComposableFiltersAndNull(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`CREATE TABLE events (id INTEGER, name TEXT, score INTEGER, deleted_at TEXT);
		INSERT INTO events VALUES
			(1, 'alpha_100%', 12, NULL),
			(2, 'alphaX100Y', 12, NULL),
			(3, 'alpha_100%', 8, NULL),
			(4, 'alpha_100%', 15, '2026-01-01');`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}

	app := &App{
		db:            db,
		dbType:        config.SQLite,
		selectedTable: "events",
		resultFilter: newResultValueFilter("events", []resultFilterPredicate{
			{column: "name", operator: resultFilterContains, value: "_100%"},
			{column: "score", operator: resultFilterGreaterEqual, value: 10},
			{column: "deleted_at", operator: resultFilterIsNull},
		}),
		results:     newResultTable(),
		resultLimit: 100,
		sortColumn:  -1,
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
		t.Fatalf("composite filtered row count = %d, want 1", snapshot.rowCount)
	}
	if got := snapshot.results.GetCell(1, 0).Text; got != "1" {
		t.Fatalf("composite filtered id = %q, want 1", got)
	}
	deletedReference, ok := snapshot.results.GetCell(1, 3).GetReference().(resultCellReference)
	if !ok || !deletedReference.isNull {
		t.Fatalf("filtered NULL cell reference = %#v", snapshot.results.GetCell(1, 3).GetReference())
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
