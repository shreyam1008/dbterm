package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

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

func TestTableTypeAheadSelectsFirstMatchAndClearsOnEnter(t *testing.T) {
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

	if got := list.GetCurrentItem(); got != 1 {
		t.Fatalf("selected index = %d, want first matching index 1", got)
	}
	label, _ := list.GetItemText(1)
	if !strings.Contains(label, "[black:#f9e2af:b]user[-:-:-]") {
		t.Fatalf("matching letters are not highlighted: %q", label)
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
	label, _ = list.GetItemText(1)
	if label != "     audit_users" {
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
