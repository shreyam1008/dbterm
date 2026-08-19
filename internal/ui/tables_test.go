package ui

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

func testExpandableSidebarApp() *App {
	tables := tview.NewList().ShowSecondaryText(false)
	app := &App{
		tables: tables, tableOrder: []string{"users", "orders"}, tableCount: 2,
		tableIdentifiers: map[int]string{}, tableColumnItems: map[int]sidebarColumnRef{},
		databaseObjects: map[int]databaseObjectListItem{}, sortColumn: -1,
		sqlCompletionCatalog: sqlCompletionCatalog{relations: []sqlCompletionRelation{
			{name: "users", kind: sqlCompletionTable, columns: []string{"id", "email", "account_id"}},
			{name: "orders", kind: sqlCompletionTable, columns: []string{"id", "total"}},
		}},
	}
	app.sidebarSearchIndex = buildSidebarSearchIndex(app.tableOrder, app.sqlCompletionCatalog, nil)
	app.rebuildTableSidebar(sidebarSelection{table: "users"})
	return app
}

func TestSidebarRightLeftUsesAccordionTableExpansion(t *testing.T) {
	app := testExpandableSidebarApp()
	if got := app.tables.GetItemCount(); got != 2 {
		t.Fatalf("collapsed sidebar items = %d, want 2", got)
	}

	if event := app.handleTableListInput(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone)); event != nil {
		t.Fatal("Right expansion was not consumed")
	}
	if app.expandedSidebarTable != "users" || len(app.tableColumnItems) != 3 {
		t.Fatalf("expanded users state = %q, columns=%#v", app.expandedSidebarTable, app.tableColumnItems)
	}
	app.handleTableListInput(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if column, ok := app.tableColumnItems[app.tables.GetCurrentItem()]; !ok || column.table != "users" || column.column != "id" {
		t.Fatalf("second Right did not enter the first child: %#v", column)
	}

	app.selectTableListIdentifier("orders")
	app.handleTableListInput(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if app.expandedSidebarTable != "orders" || len(app.tableColumnItems) != 2 {
		t.Fatalf("accordion did not replace users with orders: expanded=%q columns=%#v", app.expandedSidebarTable, app.tableColumnItems)
	}
	for _, column := range app.tableColumnItems {
		if column.table != "orders" {
			t.Fatalf("stale expanded column remained: %#v", column)
		}
	}

	for index, column := range app.tableColumnItems {
		if column.column == "total" {
			app.tables.SetCurrentItem(index)
			break
		}
	}
	app.handleTableListInput(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	if app.expandedSidebarTable != "orders" {
		t.Fatalf("Left on child unexpectedly collapsed table: %q", app.expandedSidebarTable)
	}
	if table, ok := app.selectedSidebarTable(); !ok || table != "orders" {
		t.Fatalf("Left did not return to parent table: %q, %v", table, ok)
	}
	app.handleTableListInput(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	if app.expandedSidebarTable != "" {
		t.Fatalf("Left on parent did not collapse table: %q", app.expandedSidebarTable)
	}
}

func TestSidebarSearchFindsColumnInsideCollapsedTable(t *testing.T) {
	app := testExpandableSidebarApp()
	for _, r := range "EMAIL" {
		app.handleTableListInput(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	if app.expandedSidebarTable != "users" {
		t.Fatalf("column search expanded %q, want users", app.expandedSidebarTable)
	}
	index := app.tables.GetCurrentItem()
	column, ok := app.tableColumnItems[index]
	if !ok || column.table != "users" || column.column != "email" {
		t.Fatalf("column search selection = %#v, want users.email", column)
	}
	label, _ := app.tables.GetItemText(index)
	if !strings.Contains(label, "[black:#f9e2af:b]email[-:-:-]") {
		t.Fatalf("column search match is not highlighted: %q", label)
	}
}

func TestSidebarMetadataRefreshPreservesColumnSelectionAndSearchIndex(t *testing.T) {
	app := testExpandableSidebarApp()
	app.toggleSidebarTable("users")
	for index, column := range app.tableColumnItems {
		if column.column == "email" {
			app.tables.SetCurrentItem(index)
			break
		}
	}
	app.ensureSidebarSearchIndex()
	indexBacking := &app.sidebarSearchIndex[0]
	app.applySidebarColumnMetadata("users", []sidebarColumnMeta{
		{name: "id", dataType: "integer", primaryKey: true},
		{name: "email", dataType: "text", notNull: true},
		{name: "account_id", dataType: "integer", foreignKey: true, target: "accounts.id"},
	})

	selected := app.currentSidebarSelection()
	if selected.table != "users" || selected.column != "email" {
		t.Fatalf("metadata refresh selection = %#v, want users.email", selected)
	}
	if &app.sidebarSearchIndex[0] != indexBacking {
		t.Fatal("metadata-only refresh rebuilt the full sidebar search index")
	}
	label, _ := app.tables.GetItemText(app.tables.GetCurrentItem())
	if !strings.Contains(label, "email") || !strings.Contains(label, "NN") || !strings.Contains(label, "text") {
		t.Fatalf("refreshed metadata label = %q", label)
	}
}

func TestSidebarChevronClickTogglesExpansionWithoutOpeningRows(t *testing.T) {
	app := testExpandableSidebarApp()
	app.tables.SetBorder(true).SetRect(4, 3, 40, 10)
	innerX, innerY, _, _ := app.tables.GetInnerRect()
	event := tcell.NewEventMouse(innerX+4, innerY, tcell.Button1, tcell.ModNone)
	action, returned := app.handleTableListMouse(tview.MouseLeftClick, event)
	if action != tview.MouseConsumed || returned != nil {
		t.Fatalf("chevron click = (%v, %#v), want consumed", action, returned)
	}
	if app.expandedSidebarTable != "users" || len(app.tableColumnItems) != 3 {
		t.Fatalf("chevron did not expand users: expanded=%q columns=%d", app.expandedSidebarTable, len(app.tableColumnItems))
	}
}

func TestSidebarNavigationAndMouseSkipDecorativeRows(t *testing.T) {
	list := tview.NewList().ShowSecondaryText(false)
	list.AddItem("users", "", 0, nil)
	list.AddItem("[#6c7086]── Schema (audit) ──[-]", "", 0, nil)
	list.AddItem("audit.events", "", 0, nil)
	app := &App{
		tables: list, tableIdentifiers: map[int]string{0: "users", 2: "audit.events"},
		tableColumnItems: map[int]sidebarColumnRef{}, databaseObjects: map[int]databaseObjectListItem{},
	}
	list.SetCurrentItem(0)
	if event := app.handleTableListInput(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)); event != nil {
		t.Fatal("Down navigation was not consumed")
	}
	if got := list.GetCurrentItem(); got != 2 {
		t.Fatalf("Down selected decorative row %d, want actionable row 2", got)
	}
	app.handleTableListInput(tcell.NewEventKey(tcell.KeyHome, 0, tcell.ModNone))
	if got := list.GetCurrentItem(); got != 0 {
		t.Fatalf("Home selection = %d, want first actionable row", got)
	}
	app.handleTableListInput(tcell.NewEventKey(tcell.KeyEnd, 0, tcell.ModNone))
	if got := list.GetCurrentItem(); got != 2 {
		t.Fatalf("End selection = %d, want last actionable row", got)
	}

	list.SetBorder(true).SetRect(2, 2, 40, 8)
	innerX, innerY, _, _ := list.GetInnerRect()
	list.SetCurrentItem(0)
	action, returned := app.handleTableListMouse(
		tview.MouseLeftClick,
		tcell.NewEventMouse(innerX+12, innerY+1, tcell.Button1, tcell.ModNone),
	)
	if action != tview.MouseConsumed || returned != nil || list.GetCurrentItem() != 0 {
		t.Fatalf("decorative click = (%v, %#v), selection=%d; want consumed without selection change", action, returned, list.GetCurrentItem())
	}
}

func TestLoadSQLiteSidebarColumnMetadataMarksKeysAndNullability(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys = ON;
CREATE TABLE accounts (id INTEGER PRIMARY KEY);
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES accounts(id),
  email TEXT
);`); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	columns, err := loadSidebarColumnMetadata(context.Background(), db, config.SQLite, "users", "")
	if err != nil {
		t.Fatalf("load sidebar metadata: %v", err)
	}
	byName := make(map[string]sidebarColumnMeta, len(columns))
	for _, column := range columns {
		byName[column.name] = column
	}
	if !byName["id"].primaryKey || byName["id"].dataType != "INTEGER" {
		t.Fatalf("id metadata = %#v", byName["id"])
	}
	if !byName["account_id"].foreignKey || !byName["account_id"].notNull || byName["account_id"].target != "accounts.id" {
		t.Fatalf("account_id metadata = %#v", byName["account_id"])
	}
	app := &App{sidebarColumnMetadata: map[string][]sidebarColumnMeta{"users": columns}}
	label := app.sidebarColumnLabel(sidebarColumnRef{table: "users", column: "account_id"}, false, "")
	for _, want := range []string{"account_id", "FK", "NN", "integer"} {
		if !strings.Contains(label, want) {
			t.Fatalf("metadata label = %q, want %q", label, want)
		}
	}
}

func BenchmarkSidebarSearchLargeSchema(b *testing.B) {
	tables := make([]string, 5000)
	catalog := sqlCompletionCatalog{relations: make([]sqlCompletionRelation, len(tables))}
	for tableIndex := range tables {
		table := fmt.Sprintf("table_%04d", tableIndex)
		tables[tableIndex] = table
		columns := make([]string, 20)
		for columnIndex := range columns {
			columns[columnIndex] = fmt.Sprintf("column_%02d", columnIndex)
		}
		catalog.relations[tableIndex] = sqlCompletionRelation{name: table, kind: sqlCompletionTable, columns: columns}
	}
	index := buildSidebarSearchIndex(tables, catalog, nil)
	b.ResetTimer()
	for range b.N {
		_, _ = bestSidebarSearchMatch(index, "table_4999.column_19")
	}
}

func BenchmarkSidebarApplySearchLargeSchema(b *testing.B) {
	tables := make([]string, 5000)
	catalog := sqlCompletionCatalog{relations: make([]sqlCompletionRelation, len(tables))}
	for tableIndex := range tables {
		table := fmt.Sprintf("table_%04d", tableIndex)
		tables[tableIndex] = table
		columns := make([]string, 20)
		for columnIndex := range columns {
			columns[columnIndex] = fmt.Sprintf("column_%02d", columnIndex)
		}
		catalog.relations[tableIndex] = sqlCompletionRelation{name: table, kind: sqlCompletionTable, columns: columns}
	}
	app := &App{
		tables: tview.NewList().ShowSecondaryText(false), tableOrder: tables, tableCount: len(tables),
		tableIdentifiers: map[int]string{}, tableColumnItems: map[int]sidebarColumnRef{},
		databaseObjects: map[int]databaseObjectListItem{}, sortColumn: -1, sqlCompletionCatalog: catalog,
	}
	app.sidebarSearchIndex = buildSidebarSearchIndex(tables, catalog, nil)
	app.rebuildTableSidebar(sidebarSelection{})
	app.tableSearch = "table_4999.column_19"
	app.applyTableSearch()
	b.ResetTimer()
	for iteration := range b.N {
		if iteration%2 == 0 {
			app.tableSearch = "table_4999.column_18"
		} else {
			app.tableSearch = "table_4999.column_19"
		}
		app.applyTableSearch()
	}
}

func BenchmarkSidebarMetadataRefreshLargeSchema(b *testing.B) {
	tables := make([]string, 5000)
	catalog := sqlCompletionCatalog{relations: make([]sqlCompletionRelation, len(tables))}
	for tableIndex := range tables {
		table := fmt.Sprintf("table_%04d", tableIndex)
		tables[tableIndex] = table
		columns := make([]string, 20)
		for columnIndex := range columns {
			columns[columnIndex] = fmt.Sprintf("column_%02d", columnIndex)
		}
		catalog.relations[tableIndex] = sqlCompletionRelation{name: table, kind: sqlCompletionTable, columns: columns}
	}
	app := &App{
		tables: tview.NewList().ShowSecondaryText(false), tableOrder: tables, tableCount: len(tables),
		tableIdentifiers: map[int]string{}, tableColumnItems: map[int]sidebarColumnRef{},
		databaseObjects: map[int]databaseObjectListItem{}, sortColumn: -1, sqlCompletionCatalog: catalog,
		expandedSidebarTable: "table_4999",
	}
	app.sidebarSearchIndex = buildSidebarSearchIndex(tables, catalog, nil)
	app.sidebarSearchLookup = buildSidebarSearchLookup(app.sidebarSearchIndex)
	app.rebuildTableSidebar(sidebarSelection{table: "table_4999", column: "column_19"})
	metadata := make([]sidebarColumnMeta, 20)
	for index := range metadata {
		metadata[index] = sidebarColumnMeta{name: fmt.Sprintf("column_%02d", index), dataType: "text", notNull: index%2 == 0}
	}
	b.ResetTimer()
	for range b.N {
		app.applySidebarColumnMetadata("table_4999", metadata)
	}
}

func BenchmarkSidebarRebuildLargeSchema(b *testing.B) {
	tables := make([]string, 5000)
	catalog := sqlCompletionCatalog{relations: make([]sqlCompletionRelation, len(tables))}
	for tableIndex := range tables {
		table := fmt.Sprintf("table_%04d", tableIndex)
		tables[tableIndex] = table
		columns := make([]string, 20)
		for columnIndex := range columns {
			columns[columnIndex] = fmt.Sprintf("column_%02d", columnIndex)
		}
		catalog.relations[tableIndex] = sqlCompletionRelation{name: table, kind: sqlCompletionTable, columns: columns}
	}
	app := &App{
		tables: tview.NewList().ShowSecondaryText(false), tableOrder: tables, tableCount: len(tables),
		tableIdentifiers: map[int]string{}, tableColumnItems: map[int]sidebarColumnRef{},
		databaseObjects: map[int]databaseObjectListItem{}, sortColumn: -1, sqlCompletionCatalog: catalog,
	}
	app.rebuildTableSidebar(sidebarSelection{table: "table_4999"})
	b.ResetTimer()
	for iteration := range b.N {
		if iteration%2 == 0 {
			app.expandedSidebarTable = "table_4999"
		} else {
			app.expandedSidebarTable = ""
		}
		app.rebuildTableSidebar(sidebarSelection{table: "table_4999"})
	}
}

func TestIsSelectableTableLabelIgnoresDecorativeRows(t *testing.T) {
	cases := []struct {
		name  string
		label string
		want  bool
	}{
		{name: "plain table", label: "public.users", want: true},
		{name: "active table", label: "[#a6e3a1]▶[-][#cba6f7]/[-] public.users", want: true},
		{name: "visited table", label: "[#6c7086]•[-]  public.orders", want: true},
		{name: "filtered unvisited table", label: "[#cba6f7]/[-] public.logs", want: true},
		{name: "pinned table", label: "[#f9e2af]" + iconPin + "[-] public.events", want: true},
		{name: "collapsed table", label: "    [#6c7086]▸[-] public.events", want: true},
		{name: "expanded table", label: "    [#a6adc8]▾[-] public.events", want: true},
		{name: "expanded column", label: "      [#6c7086]├─[-] account_id [#cba6f7]FK[-]", want: true},
		{name: "section header", label: "[#6c7086]── Views (2) ──[-]", want: false},
		{name: "indented styled object", label: "  [#a6adc8]◈[-] reporting_view", want: false},
		{name: "empty decorative", label: "   [gray]No tables found[-]", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSelectableTableLabel(tc.label); got != tc.want {
				t.Fatalf("isSelectableTableLabel(%q) = %v, want %v", tc.label, got, tc.want)
			}
		})
	}
}

func TestTableSearchMatchRangeIsCaseInsensitive(t *testing.T) {
	start, end, matched := tableSearchMatchRange("audit.UserProfiles", "user")
	if !matched {
		t.Fatal("expected a match")
	}
	if got := string([]rune("audit.UserProfiles")[start:end]); got != "User" {
		t.Fatalf("matched text = %q, want %q", got, "User")
	}
}

func TestTableTypeAheadRanksPrefixAndClearsOnEnter(t *testing.T) {
	list := tview.NewList().ShowSecondaryText(false)
	list.AddItem("[#6c7086]── Schema (public) ──[-]", "", 0, nil)
	list.AddItem("audit_users", "", 0, nil)
	list.AddItem("user_profiles", "", 0, nil)

	app := &App{
		tables: list,
		tableIdentifiers: map[int]string{
			1: "audit_users",
			2: "user_profiles",
		},
		tableCount: 2,
	}

	for _, r := range "USER" {
		if got := app.handleTableListInput(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone)); got != nil {
			t.Fatalf("type-ahead event %q was not consumed", r)
		}
	}

	if got := list.GetCurrentItem(); got != 2 {
		t.Fatalf("selected index = %d, want stronger prefix match at index 2", got)
	}
	label, _ := list.GetItemText(2)
	if !strings.Contains(label, "[black:#f9e2af:b]user[-:-:-]") {
		t.Fatalf("selected match is not highlighted: %q", label)
	}
	if !strings.Contains(list.GetTitle(), "USER") {
		t.Fatalf("title does not show active search: %q", list.GetTitle())
	}

	enter := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	if got := app.handleTableListInput(enter); got != enter {
		t.Fatal("Enter should continue to the list after clearing a successful search")
	}
	if app.tableSearch != "" {
		t.Fatalf("search was not cleared: %q", app.tableSearch)
	}
	label, _ = list.GetItemText(2)
	if strings.Contains(label, "[black:#f9e2af:b]") || !strings.Contains(label, "▸[-] user_profiles") {
		t.Fatalf("highlight was not cleared: %q", label)
	}
}

func TestPinnedTablesMoveToTopWithoutDuplication(t *testing.T) {
	activeConnection := &config.ConnectionConfig{Type: config.SQLite, FilePath: "/tmp/pins.db"}
	app := &App{
		activeConn: activeConnection,
		settings:   config.DefaultSettings(),
		dbType:     config.SQLite,
		tableOrder: []string{"users", "orders", "events"},
	}
	connection, ok := app.activeConnectionKey()
	if !ok {
		t.Fatal("active connection key is unavailable")
	}
	app.settings.PinnedTables[connection] = []string{"orders", "users", "missing"}

	items := app.orderedTableSidebarItems()
	if len(items) != 4 {
		t.Fatalf("sidebar item count = %d, want pin header + 3 tables", len(items))
	}
	if !strings.Contains(items[0].label, "Pinned (2)") {
		t.Fatalf("first item is not the pinned section: %#v", items[0])
	}
	want := []string{"orders", "users", "events"}
	got := make([]string, 0, len(want))
	for _, item := range items {
		if item.identifier != "" {
			got = append(got, item.identifier)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("table order = %#v, want %#v", got, want)
	}
}

func TestTablePinShortcutTogglesAndPersistsPerConnection(t *testing.T) {
	t.Setenv("DBTERM_CONFIG_DIR", t.TempDir())
	t.Setenv("DBTERM_STATE_DIR", t.TempDir())
	list := tview.NewList().ShowSecondaryText(false)
	list.AddItem("users", "", 0, nil)
	list.AddItem("orders", "", 0, nil)
	app := &App{
		tables:            list,
		tableIdentifiers:  map[int]string{0: "users", 1: "orders"},
		tableOrder:        []string{"users", "orders"},
		tableSidebarItems: 2,
		tableCount:        2,
		dbType:            config.SQLite,
		activeConn:        &config.ConnectionConfig{Type: config.SQLite, FilePath: "/tmp/pins.db"},
		settings:          config.DefaultSettings(),
	}

	space := tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone)
	if got := app.handleTableListInput(space); got != nil {
		t.Fatal("Space pin shortcut was not consumed")
	}
	connection, _ := app.activeConnectionKey()
	if got := app.settings.PinnedTables[connection]; len(got) != 1 || got[0] != "users" {
		t.Fatalf("pins after Space = %#v, want [users]", got)
	}
	if selected, ok := app.selectedSidebarTable(); !ok || selected != "users" {
		t.Fatalf("selection after pin = %q, %v; want users", selected, ok)
	}
	label, _ := list.GetItemText(list.GetCurrentItem())
	if !strings.Contains(label, iconPin) {
		t.Fatalf("pinned row has no pin icon: %q", label)
	}

	if got := app.handleTableListInput(space); got != nil {
		t.Fatal("Space unpin shortcut was not consumed")
	}
	if got := app.settings.PinnedTables[connection]; len(got) != 0 {
		t.Fatalf("pins after second P = %#v, want none", got)
	}
	reloaded, err := config.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	if got := reloaded.PinnedTables[connection]; len(got) != 0 {
		t.Fatalf("persisted pins after unpin = %#v, want none", got)
	}
}

func TestPinShortcutDoesNotStealPrintableTableSearch(t *testing.T) {
	list := tview.NewList().ShowSecondaryText(false)
	list.AddItem("people", "", 0, nil)
	app := &App{
		tables:           list,
		tableIdentifiers: map[int]string{0: "people"},
	}
	app.handleTableListInput(tcell.NewEventKey(tcell.KeyRune, 'p', tcell.ModNone))
	if app.tableSearch != "p" {
		t.Fatalf("search beginning with p = %q, want p", app.tableSearch)
	}
	app.tableSearch = "peo"
	app.handleTableListInput(tcell.NewEventKey(tcell.KeyRune, 'p', tcell.ModNone))
	if app.tableSearch != "peop" {
		t.Fatalf("active search = %q, want peop", app.tableSearch)
	}
	app.handleTableListInput(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone))
	if app.tableSearch != "peop " {
		t.Fatalf("space inside active search = %q, want %q", app.tableSearch, "peop ")
	}
}

func TestTableNameCopyKeyUsesCopyLetterWithoutStealingLowercaseSearch(t *testing.T) {
	for _, test := range []struct {
		name  string
		event *tcell.EventKey
		want  bool
	}{
		{name: "shift c", event: tcell.NewEventKey(tcell.KeyRune, 'C', tcell.ModShift), want: true},
		{name: "uppercase rune fallback", event: tcell.NewEventKey(tcell.KeyRune, 'C', tcell.ModNone), want: true},
		{name: "lowercase search", event: tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModNone), want: false},
		{name: "alt c remains global", event: tcell.NewEventKey(tcell.KeyRune, 'C', tcell.ModAlt), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isTableNameCopyKey(test.event); got != test.want {
				t.Fatalf("isTableNameCopyKey() = %v, want %v", got, test.want)
			}
		})
	}

	list := tview.NewList().ShowSecondaryText(false)
	list.AddItem("customers", "", 0, nil)
	app := &App{tables: list, tableIdentifiers: map[int]string{0: "customers"}}
	if got := app.handleTableListInput(tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModNone)); got != nil {
		t.Fatal("lowercase c search event was not consumed")
	}
	if app.tableSearch != "c" {
		t.Fatalf("lowercase c search = %q, want c", app.tableSearch)
	}
	app.handleTableListInput(tcell.NewEventKey(tcell.KeyRune, 'C', tcell.ModShift))
	if app.tableSearch != "cC" {
		t.Fatalf("uppercase C during active search = %q, want cC", app.tableSearch)
	}
}

func TestTableContextHintsPrioritizePinAndSearch(t *testing.T) {
	tables := tview.NewList()
	app := &App{tables: tables, focusedPanel: tables}

	for _, test := range []struct {
		width int
		want  []string
	}{
		{width: 72, want: []string{"Space", "Pin", "Enter", "Open", "Shift+C", "Copy", "Esc", "Back"}},
		{width: 112, want: []string{"Space", "Type", "Find", "Shift+C", "Copy", "Ctrl+P"}},
		{width: 160, want: []string{"Space", "Shift+C/Right-click", "Copy", "Alt+M", "Schema", "Ctrl+P"}},
	} {
		hint := app.statusActionText(test.width)
		for _, want := range test.want {
			if !strings.Contains(hint, want) {
				t.Fatalf("statusActionText(%d) = %q, want %q", test.width, hint, want)
			}
		}
	}

	app.tableCount = 3
	app.updateTableListTitle()
	if title := tables.GetTitle(); !strings.Contains(title, "Space") || !strings.Contains(title, iconPin) {
		t.Fatalf("table title does not expose pin control: %q", title)
	}
}

func TestTableListIndexAtPointAccountsForBorderAndScroll(t *testing.T) {
	list := tview.NewList().ShowSecondaryText(false)
	for _, name := range []string{"users", "orders", "events", "logs"} {
		list.AddItem(name, "", 0, nil)
	}
	list.SetBorder(true)
	list.SetRect(10, 5, 20, 5)
	list.SetOffset(1, 0)

	innerX, innerY, _, _ := list.GetInnerRect()
	if got := tableListIndexAtPoint(list, innerX, innerY); got != 1 {
		t.Fatalf("first visible row index = %d, want 1", got)
	}
	if got := tableListIndexAtPoint(list, innerX+2, innerY+1); got != 2 {
		t.Fatalf("second visible row index = %d, want 2", got)
	}
	if got := tableListIndexAtPoint(list, innerX-1, innerY); got != -1 {
		t.Fatalf("border point index = %d, want -1", got)
	}
}

func TestPinReorderPreservesDatabaseObjectRows(t *testing.T) {
	list := tview.NewList().ShowSecondaryText(false)
	list.AddItem("users", "", 0, nil)
	list.AddItem("orders", "", 0, nil)
	list.AddItem("[#6c7086]── ◈ Views (1) ──[-]", "", 0, nil)
	list.AddItem("  [#a6adc8]◈[-] active_users", "", 0, nil)
	app := &App{
		tables:            list,
		tableIdentifiers:  map[int]string{0: "users", 1: "orders"},
		databaseObjects:   map[int]databaseObjectListItem{3: {name: "active_users"}},
		tableOrder:        []string{"users", "orders"},
		tableSidebarItems: 2,
		dbType:            config.SQLite,
		activeConn:        &config.ConnectionConfig{Type: config.SQLite, FilePath: "/tmp/pins.db"},
		settings:          config.DefaultSettings(),
	}
	connection, _ := app.activeConnectionKey()
	app.settings.PinnedTables[connection] = []string{"users"}

	app.rebuildTableSidebarForPins("orders")
	if got := list.GetItemCount(); got != 5 {
		t.Fatalf("item count after reorder = %d, want 5", got)
	}
	object, ok := app.databaseObjects[4]
	if !ok || object.name != "active_users" {
		t.Fatalf("database object mapping after reorder = %#v, %v", object, ok)
	}
	if selected, ok := app.selectedSidebarTable(); !ok || selected != "orders" {
		t.Fatalf("selected table after reorder = %q, %v; want orders", selected, ok)
	}
}

func TestTableSidebarShowsActiveVisitedAndFilteredStates(t *testing.T) {
	list := tview.NewList().ShowSecondaryText(false)
	for _, table := range []string{"users", "orders", "logs"} {
		list.AddItem(table, "", 0, nil)
	}
	app := &App{
		tables:             list,
		tableIdentifiers:   map[int]string{0: "users", 1: "orders", 2: "logs"},
		tableCount:         3,
		selectedTable:      "users",
		activeTable:        "users",
		tableResultsActive: true,
		visitedTables:      map[string]bool{"users": true, "orders": true},
		resultFilter: newResultValueFilter("users", []resultFilterPredicate{
			{column: "status", operator: resultFilterEqual, value: "active"},
		}),
		resultFilters: map[string]*resultValueFilter{
			"users": newResultValueFilter("users", []resultFilterPredicate{
				{column: "status", operator: resultFilterEqual, value: "active"},
			}),
		},
	}

	app.refreshTableSidebarState()
	users, _ := list.GetItemText(0)
	orders, _ := list.GetItemText(1)
	logs, _ := list.GetItemText(2)
	if !strings.Contains(users, "▶") || !strings.Contains(users, "/") {
		t.Fatalf("active filtered table markers missing: %q", users)
	}
	if !strings.Contains(orders, "•") || strings.Contains(orders, "/") {
		t.Fatalf("visited table marker is wrong: %q", orders)
	}
	if strings.ContainsAny(logs, "▶•/") {
		t.Fatalf("untouched table has a state marker: %q", logs)
	}
	if title := list.GetTitle(); !strings.Contains(title, "Space") || !strings.Contains(title, iconPin) || !strings.Contains(title, "▶ • /") {
		t.Fatalf("sidebar marker legend missing: %q", list.GetTitle())
	}

	app.tableSearch = "SER"
	app.applyTableSearch()
	users, _ = list.GetItemText(0)
	if !strings.Contains(users, "▶") || !strings.Contains(users, "/") || !strings.Contains(users, "[black:#f9e2af:b]ser[-:-:-]") {
		t.Fatalf("search did not preserve sidebar state markers: %q", users)
	}
}

func TestClearTableSessionStateDropsConnectionScopedMarkers(t *testing.T) {
	list := tview.NewList().ShowSecondaryText(false)
	list.AddItem("users", "", 0, nil)
	app := &App{
		tables:             list,
		tableIdentifiers:   map[int]string{0: "users"},
		tableCount:         1,
		selectedTable:      "users",
		tableSearch:        "user",
		activeTable:        "users",
		tableResultsActive: true,
		visitedTables:      map[string]bool{"users": true},
		resultPositions: map[string]resultSelectionState{
			"users": {row: 3, col: 1, offsetRow: 2},
		},
		resultFilters: map[string]*resultValueFilter{
			"users": newResultValueFilter("users", []resultFilterPredicate{
				{column: "id", operator: resultFilterEqual, value: "42"},
			}),
		},
	}
	app.resultFilter = cloneResultValueFilter(app.resultFilters["users"])

	app.clearTableSessionState()
	if app.activeTable != "" || app.selectedTable != "" || app.tableSearch != "" || app.tableResultsActive || app.visitedTables != nil || app.resultPositions != nil || app.resultFilter != nil || app.resultFilters != nil {
		t.Fatalf("connection-scoped table state was not cleared: %#v", app)
	}
	label, _ := list.GetItemText(0)
	if strings.ContainsAny(label, "▶•/") {
		t.Fatalf("sidebar retained a connection marker after cleanup: %q", label)
	}
}

func TestTableTypeAheadEnterDoesNotOpenWhenNothingMatches(t *testing.T) {
	list := tview.NewList().ShowSecondaryText(false)
	list.AddItem("users", "", 0, nil)
	app := &App{
		tables:           list,
		tableIdentifiers: map[int]string{0: "users"},
		tableCount:       1,
	}

	for _, r := range "missing" {
		app.handleTableListInput(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	if got := app.handleTableListInput(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); got != nil {
		t.Fatal("Enter should be consumed when the search has no match")
	}
	if app.tableSearch != "" {
		t.Fatalf("search was not cleared: %q", app.tableSearch)
	}
}
