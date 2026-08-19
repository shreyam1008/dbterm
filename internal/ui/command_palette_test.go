package ui

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
	"github.com/shreyam1008/dbterm/internal/history"
)

func TestCommandPaletteSearchTargetsCollapsedColumns(t *testing.T) {
	entries := buildSidebarSearchIndex(
		[]string{"users", "orders"},
		sqlCompletionCatalog{relations: []sqlCompletionRelation{
			{name: "users", kind: sqlCompletionTable, columns: []string{"id", "email_address"}},
			{name: "orders", kind: sqlCompletionTable, columns: []string{"id", "total"}},
		}},
		nil,
	)
	matches := searchSidebarColumnPalette(entries, "email", 20)
	if len(matches) == 0 {
		t.Fatal("column palette search returned no matches")
	}
	item := matches[0].item
	if item.kind != commandPaletteColumn || item.objectName != "users" || item.columnName != "email_address" || item.title != "users.email_address" {
		t.Fatalf("column palette result = %#v", item)
	}
	if commandPaletteCategoryTag(item.kind) != "[#74c7ec]COLUMN[-]" {
		t.Fatalf("column category tag = %q", commandPaletteCategoryTag(item.kind))
	}
}

func TestUnifiedPaletteKeepsTableForSingleTermAndNarrowsQualifiedColumnIntent(t *testing.T) {
	entries := buildSidebarSearchIndex(
		[]string{"users"},
		sqlCompletionCatalog{relations: []sqlCompletionRelation{
			{name: "users", kind: sqlCompletionTable, columns: []string{"id", "email"}},
		}}, nil,
	)
	app := &App{sidebarSearchIndex: entries, sidebarSearchLookup: buildSidebarSearchLookup(entries)}
	base := []commandPaletteItem{{
		id: "table:users", kind: commandPaletteTable, title: "users", objectName: "users", sortOrder: 100,
	}}

	tableMatches := app.searchCommandPaletteItemsWithColumns(base, "users", 20)
	if len(tableMatches) == 0 || tableMatches[0].item.kind != commandPaletteTable {
		t.Fatalf("single-term table matches = %#v, want table first", tableMatches)
	}
	columnMatches := app.searchCommandPaletteItemsWithColumns(base, "users email", 20)
	if len(columnMatches) == 0 || columnMatches[0].item.kind != commandPaletteColumn || columnMatches[0].item.columnName != "email" {
		t.Fatalf("qualified column matches = %#v, want users.email", columnMatches)
	}
	fuzzyColumnMatches := app.searchCommandPaletteItemsWithColumns(base, "users eml", 20)
	if len(fuzzyColumnMatches) == 0 || fuzzyColumnMatches[0].item.kind != commandPaletteColumn || fuzzyColumnMatches[0].item.columnName != "email" {
		t.Fatalf("anchored fuzzy column matches = %#v, want users.email", fuzzyColumnMatches)
	}
}

func TestCommandPaletteColumnOpensActiveTableHeaderDirectly(t *testing.T) {
	app := testExpandableSidebarApp()
	application := tview.NewApplication()
	pages := tview.NewPages()
	results := newResultTable()
	results.SetCell(0, 0, tview.NewTableCell("ID").SetReference("id").SetSelectable(true))
	results.SetCell(0, 1, tview.NewTableCell("EMAIL").SetReference("email").SetSelectable(true))
	results.SetCell(1, 0, tview.NewTableCell("1"))
	results.SetCell(1, 1, tview.NewTableCell("a@example.com"))
	root := tview.NewFlex().AddItem(app.tables, 20, 0, false).AddItem(results, 0, 1, true)
	pages.AddPage("main", root, true, true)
	application.SetRoot(pages, true)
	app.app, app.pages, app.results = application, pages, results
	app.queryInput = tview.NewTextArea()
	app.db = &sql.DB{}
	app.activeTable, app.selectedTable, app.tableResultsActive = "users", "users", true
	app.statusBar = tview.NewTextView()

	app.openCommandPaletteColumn("users", "email")
	if row, column := results.GetSelection(); row != 0 || column != 1 {
		t.Fatalf("palette column result selection = (%d, %d), want header (0, 1)", row, column)
	}
	if application.GetFocus() != results {
		t.Fatalf("palette column focus = %T, want Results", application.GetFocus())
	}
	if selection := app.currentSidebarSelection(); selection.table != "users" || selection.column != "email" {
		t.Fatalf("palette did not reveal sidebar column: %#v", selection)
	}
}

func BenchmarkCommandPaletteColumnSearchLargeSchema(b *testing.B) {
	entries := make([]sidebarSearchEntry, 0, 100000)
	order := 0
	for tableIndex := range 5000 {
		table := fmt.Sprintf("table_%04d", tableIndex)
		for columnIndex := range 20 {
			column := fmt.Sprintf("column_%02d", columnIndex)
			entries = append(entries, sidebarSearchEntry{
				table: table, column: column, tableFolded: table,
				columnFolded: column, order: order,
			})
			order++
		}
	}
	lookup := buildSidebarSearchLookup(entries)
	b.ResetTimer()
	for range b.N {
		_ = searchSidebarColumnPaletteWithLookup(entries, lookup, "table_4999 column_19", 80)
	}
}

func BenchmarkCommandPaletteAnchoredFuzzyLargeSchema(b *testing.B) {
	entries := make([]sidebarSearchEntry, 0, 100000)
	order := 0
	for tableIndex := range 5000 {
		table := fmt.Sprintf("table_%04d", tableIndex)
		for columnIndex := range 20 {
			column := fmt.Sprintf("column_%02d", columnIndex)
			entries = append(entries, sidebarSearchEntry{
				table: table, column: column, tableFolded: table,
				columnFolded: column, order: order,
			})
			order++
		}
	}
	lookup := buildSidebarSearchLookup(entries)
	b.ResetTimer()
	for range b.N {
		_ = searchSidebarColumnPaletteWithLookup(entries, lookup, "table_4999 colmn_19", 80)
	}
}

func BenchmarkCommandPaletteUnifiedLargeSchema(b *testing.B) {
	baseItems := make([]commandPaletteItem, 5000)
	entries := make([]sidebarSearchEntry, 0, 100000)
	order := 0
	for tableIndex := range 5000 {
		table := fmt.Sprintf("table_%04d", tableIndex)
		baseItems[tableIndex] = commandPaletteItem{
			id: "table:" + table, kind: commandPaletteTable, title: table,
			objectName: table, description: "Open table rows", keywords: "database relation records rows data",
			sortOrder: 100 + tableIndex,
		}
		for columnIndex := range 20 {
			column := fmt.Sprintf("column_%02d", columnIndex)
			entries = append(entries, sidebarSearchEntry{
				table: table, column: column, tableFolded: table,
				columnFolded: column, order: order,
			})
			order++
		}
	}
	app := &App{sidebarSearchIndex: entries, sidebarSearchLookup: buildSidebarSearchLookup(entries)}
	b.ResetTimer()
	for range b.N {
		_ = app.searchCommandPaletteItemsWithColumns(baseItems, "table_4999 column_19", 80)
	}
}

func TestFuzzySubsequenceMatchIsCaseInsensitiveAndScoresCompactMatchesFirst(t *testing.T) {
	positions, _, ok := fuzzySubsequenceMatch("Inspect Schema", "isch")
	if !ok {
		t.Fatal("expected Inspect Schema to fuzzy-match isch")
	}
	wantPositions := []int{0, 2, 5, 10}
	if len(positions) != len(wantPositions) {
		t.Fatalf("positions = %#v, want %#v", positions, wantPositions)
	}
	for index := range wantPositions {
		if positions[index] != wantPositions[index] {
			t.Fatalf("positions = %#v, want %#v", positions, wantPositions)
		}
	}

	_, compactScore, ok := fuzzySubsequenceMatch("users", "usr")
	if !ok {
		t.Fatal("expected users to match usr")
	}
	_, scatteredScore, ok := fuzzySubsequenceMatch("user_account_records", "usr")
	if !ok {
		t.Fatal("expected user_account_records to match usr")
	}
	if compactScore >= scatteredScore {
		t.Fatalf("compact score = %d, scattered score = %d; compact match should rank first", compactScore, scatteredScore)
	}
}

func TestSearchCommandPaletteItemsSearchesAllFieldsAndHighlightsTitle(t *testing.T) {
	items := []commandPaletteItem{
		{
			kind:        commandPaletteAction,
			title:       "Inspect Schema",
			description: "Show columns and foreign keys.",
			keywords:    "metadata indexes",
			sortOrder:   0,
		},
		{
			kind:        commandPaletteTable,
			title:       "customer_orders",
			objectName:  "customer_orders",
			description: "Open table rows.",
			keywords:    "records data",
			sortOrder:   1,
		},
	}

	titleMatches := searchCommandPaletteItems(items, "isch")
	if len(titleMatches) != 1 || titleMatches[0].item.title != "Inspect Schema" {
		t.Fatalf("title matches = %#v, want Inspect Schema", titleMatches)
	}
	if len(titleMatches[0].titlePositions) != 4 {
		t.Fatalf("title highlight positions = %#v, want four matched characters", titleMatches[0].titlePositions)
	}

	descriptionMatches := searchCommandPaletteItems(items, "foreign")
	if len(descriptionMatches) != 1 || descriptionMatches[0].item.title != "Inspect Schema" {
		t.Fatalf("description matches = %#v, want Inspect Schema", descriptionMatches)
	}

	keywordMatches := searchCommandPaletteItems(items, "idx")
	if len(keywordMatches) != 1 || keywordMatches[0].item.title != "Inspect Schema" {
		t.Fatalf("keyword matches = %#v, want Inspect Schema", keywordMatches)
	}

	objectMatches := searchCommandPaletteItems(items, "cord")
	if len(objectMatches) != 1 || objectMatches[0].item.title != "customer_orders" {
		t.Fatalf("object-name matches = %#v, want customer_orders", objectMatches)
	}

	multiFieldMatches := searchCommandPaletteItems(items, "schema fk")
	if len(multiFieldMatches) != 1 || multiFieldMatches[0].item.title != "Inspect Schema" {
		t.Fatalf("multi-field matches = %#v, want Inspect Schema", multiFieldMatches)
	}
}

func TestHighlightCommandPaletteTitleEscapesTextAndMarksMatchedRunes(t *testing.T) {
	got := highlightCommandPaletteTitle("user[role]", []int{0, 2})
	if strings.Count(got, "[black:#f9e2af:b]") != 2 {
		t.Fatalf("highlighted title = %q, want two highlighted groups", got)
	}
	if strings.Contains(got, "[role]") {
		t.Fatalf("highlighted title = %q, raw tview tag-like content was not escaped", got)
	}
}

func TestBuildCommandPaletteItemsIncludesObjectsRecentQueriesAndEffectiveShortcut(t *testing.T) {
	historyManager, err := history.NewManagerAt(t.TempDir()+"/history.json", 20)
	if err != nil {
		t.Fatalf("NewManagerAt() error = %v", err)
	}
	settings := config.DefaultSettings()
	settings.Keymap[config.ActionFocusTables] = []string{"ctrl+g", "alt+t"}
	activeConnection := &config.ConnectionConfig{
		Name:     "test-db",
		Type:     config.SQLite,
		FilePath: "/tmp/test.db",
	}
	app := &App{
		settings:   settings,
		historyMgr: historyManager,
		activeConn: activeConnection,
		tableIdentifiers: map[int]string{
			0: "users",
			1: "orders",
		},
		databaseObjects: map[int]databaseObjectListItem{
			2: {objType: database.ObjViews, name: "active_users"},
			3: {objType: database.ObjFunctions, name: "normalize_email"},
			4: {objType: database.ObjStoredProcedures, name: "rebuild_totals"},
			5: {objType: database.ObjTriggers, name: "audit_orders"},
			6: {objType: database.ObjExtensions, name: "uuid-ossp"},
		},
	}
	connectionKey, ok := app.activeConnectionKey()
	if !ok {
		t.Fatal("activeConnectionKey() did not resolve the test connection")
	}
	if err := historyManager.Append(connectionKey, "SELECT * FROM users WHERE active = true"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	items := app.buildCommandPaletteItems()
	wantedKinds := map[commandPaletteItemKind]bool{
		commandPaletteAction:    false,
		commandPaletteTable:     false,
		commandPaletteView:      false,
		commandPaletteFunction:  false,
		commandPaletteProcedure: false,
		commandPaletteTrigger:   false,
		commandPaletteQuery:     false,
	}
	focusTablesShortcut := ""
	pinShortcut := ""
	copyTableShortcut := ""
	findColumnShortcut := ""
	copyColumnShortcut := ""
	smartSQLShortcut := ""
	containsExtension := false
	for _, item := range items {
		if _, wanted := wantedKinds[item.kind]; wanted {
			wantedKinds[item.kind] = true
		}
		if item.action == actionFocusTables {
			focusTablesShortcut = item.shortcut
		}
		if item.action == paletteActionToggleTablePin {
			pinShortcut = item.shortcut
		}
		if item.action == paletteActionCopyTableName {
			copyTableShortcut = item.shortcut
		}
		if item.action == paletteActionFindResultColumn {
			findColumnShortcut = item.shortcut
		}
		if item.action == paletteActionCopyColumnName {
			copyColumnShortcut = item.shortcut
		}
		if item.action == paletteActionSQLSuggestions {
			smartSQLShortcut = item.shortcut
		}
		if item.objectName == "uuid-ossp" {
			containsExtension = true
		}
	}
	for kind, found := range wantedKinds {
		if !found {
			t.Errorf("palette did not include kind %q", kind)
		}
	}
	if focusTablesShortcut != "Ctrl+G / Alt+T" {
		t.Fatalf("effective shortcut = %q, want %q", focusTablesShortcut, "Ctrl+G / Alt+T")
	}
	if pinShortcut != "Space (Tables)" {
		t.Fatalf("pin action shortcut = %q, want Space (Tables)", pinShortcut)
	}
	if copyTableShortcut != "Shift+C / Right-click (Tables)" {
		t.Fatalf("copy-table action shortcut = %q, want Shift+C / Right-click (Tables)", copyTableShortcut)
	}
	if findColumnShortcut != "↑ from first data row" {
		t.Fatalf("find-column action shortcut = %q, want header navigation hint", findColumnShortcut)
	}
	if copyColumnShortcut != "Shift+C (Headers)" {
		t.Fatalf("copy-column action shortcut = %q, want Shift+C (Headers)", copyColumnShortcut)
	}
	if smartSQLShortcut != "Ctrl+Space" {
		t.Fatalf("smart SQL shortcut = %q, want Ctrl+Space", smartSQLShortcut)
	}
	pinMatches := searchCommandPaletteItems(items, "favorite sidebar")
	if len(pinMatches) == 0 || pinMatches[0].item.action != paletteActionToggleTablePin {
		t.Fatalf("pin action search = %#v, want pin toggle first", pinMatches)
	}
	copyTableMatches := searchCommandPaletteItems(items, "copy table name")
	if len(copyTableMatches) == 0 || copyTableMatches[0].item.action != paletteActionCopyTableName {
		t.Fatalf("copy-table action search = %#v, want table-name copy first", copyTableMatches)
	}
	findColumnMatches := searchCommandPaletteItems(items, "find result column")
	if len(findColumnMatches) == 0 || findColumnMatches[0].item.action != paletteActionFindResultColumn {
		t.Fatalf("find-column action search = %#v, want column finder first", findColumnMatches)
	}
	copyColumnMatches := searchCommandPaletteItems(items, "copy column name")
	if len(copyColumnMatches) == 0 || copyColumnMatches[0].item.action != paletteActionCopyColumnName {
		t.Fatalf("copy-column action search = %#v, want column-name copy first", copyColumnMatches)
	}
	smartSQLMatches := searchCommandPaletteItems(items, "ready sql template")
	if len(smartSQLMatches) == 0 || smartSQLMatches[0].item.action != paletteActionSQLSuggestions {
		t.Fatalf("smart SQL action search = %#v, want suggestions first", smartSQLMatches)
	}
	if containsExtension {
		t.Fatal("palette unexpectedly included an unsupported extension object")
	}

	recent := searchCommandPaletteItems(items, "active true")
	if len(recent) == 0 || recent[0].item.kind != commandPaletteQuery {
		t.Fatalf("recent-query search = %#v, want a recent query first", recent)
	}

	relationship := searchCommandPaletteItems(items, "relationship composite")
	foundFollow := false
	for _, match := range relationship {
		if match.item.action == paletteActionExploreRelationships {
			foundFollow = true
			if !strings.Contains(match.item.description, "every component") || !strings.Contains(match.item.description, "child rows") || match.item.shortcut != "F" {
				t.Fatalf("follow action details = %#v", match.item)
			}
		}
	}
	if !foundFollow {
		t.Fatal("palette search did not include related-row navigation action")
	}
}

func TestCommandPaletteDefaultShortcutResolvesCtrlP(t *testing.T) {
	resolver, err := newActionKeymap(config.DefaultSettings())
	if err != nil {
		t.Fatalf("newActionKeymap() error = %v", err)
	}
	event := tcell.NewEventKey(tcell.KeyCtrlP, 0, tcell.ModCtrl)
	action, ok := resolver.Resolve(event)
	if !ok || action != actionCommandPalette {
		t.Fatalf("Resolve(Ctrl+P) = (%q, %v), want (%q, true)", action, ok, actionCommandPalette)
	}
}

func TestBuildCommandPaletteItemsIncludesSavedConnectionBackupShortcut(t *testing.T) {
	app := &App{store: &config.Store{Connections: []config.ConnectionConfig{{
		ID: "conn-1", Name: "production", Type: config.PostgreSQL,
	}}}}
	items := app.buildCommandPaletteItems()
	for _, item := range items {
		if item.kind == commandPaletteBackupJob && item.objectName == "conn-1" {
			if item.shortcut != "Dashboard Ctrl+B" || !strings.Contains(item.title, "production") {
				t.Fatalf("backup palette item = %#v", item)
			}
			return
		}
	}
	t.Fatal("saved connection backup palette item was not added")
}
