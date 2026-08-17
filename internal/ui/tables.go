package ui

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
)

type tableListSnapshotItem struct {
	label      string
	identifier string
}

type tableListSnapshot struct {
	items         []tableListSnapshotItem
	tableCount    int
	selectedIndex int
	selectedTable string
}

// LoadTables fetches the list of tables from the connected database and applies it to the UI.
func (a *App) LoadTables() error {
	currentIndex := a.tables.GetCurrentItem()
	snapshot, err := loadTableListSnapshot(a.db, a.dbType, a.selectedTable, currentIndex)
	if err != nil {
		return err
	}
	a.applyTableListSnapshot(snapshot)

	// Load database objects (views, functions, triggers, etc.) asynchronously
	a.loadDatabaseObjects()

	return nil
}

func loadTableListSnapshot(db *sql.DB, dbType config.DBType, selectedTable string, currentIndex int) (*tableListSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return loadTableListSnapshotContext(ctx, db, dbType, selectedTable, currentIndex)
}

func loadTableListSnapshotContext(ctx context.Context, db *sql.DB, dbType config.DBType, selectedTable string, currentIndex int) (*tableListSnapshot, error) {
	if db == nil {
		return nil, fmt.Errorf("not connected to any database")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	query := database.ListTablesQuery(dbType)
	if query == "" {
		return nil, fmt.Errorf("unsupported database type: %s", dbType)
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("could not list tables: %w", err)
	}
	defer rows.Close()

	snapshot := &tableListSnapshot{selectedIndex: 0}
	foundSelection := false

	lastNamespace := ""
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, fmt.Errorf("could not read table name: %w", err)
		}

		namespace := namespaceForTable(dbType, tableName)
		if namespace != "" && namespace != lastNamespace {
			snapshot.items = append(snapshot.items, tableListSnapshotItem{
				label: fmt.Sprintf("[#6c7086]── %s %s (%s) ──[-]", iconTables, namespaceKindLabel(dbType), namespace),
			})
			lastNamespace = namespace
		}

		snapshot.items = append(snapshot.items, tableListSnapshotItem{label: tableName, identifier: tableName})
		itemIndex := len(snapshot.items) - 1
		if tableName == selectedTable {
			snapshot.selectedIndex = itemIndex
			foundSelection = true
		}
		snapshot.tableCount++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if !foundSelection && currentIndex >= 0 && currentIndex < len(snapshot.items) {
		snapshot.selectedIndex = currentIndex
	}

	if snapshot.tableCount == 0 {
		snapshot.selectedTable = ""
		snapshot.items = append(snapshot.items, tableListSnapshotItem{label: fmt.Sprintf("[gray]%s No tables found[-]", iconInfo)})
		return snapshot, nil
	}

	if !isSelectableTableListSnapshotItem(snapshot, snapshot.selectedIndex) {
		snapshot.selectedIndex = firstSelectableTableSnapshotIndex(snapshot)
	}
	if snapshot.selectedIndex >= 0 && snapshot.selectedIndex < len(snapshot.items) {
		if tableName := snapshot.items[snapshot.selectedIndex].identifier; tableName != "" {
			snapshot.selectedTable = tableName
		}
	}

	return snapshot, nil
}

func (a *App) applyTableListSnapshot(snapshot *tableListSnapshot) {
	// Invalidate any object-discovery worker that was built against the prior
	// sidebar. A fresh discovery will capture a newer generation below.
	a.objectGeneration.Add(1)
	a.tables.Clear()
	a.databaseObjects = map[int]databaseObjectListItem{}
	a.sqlCompletionRoutines = nil
	a.tableIdentifiers = map[int]string{}
	a.tableSearch = ""
	a.databaseObjectCount = 0
	a.tableCount = 0
	a.tableOrder = nil
	a.tableSidebarItems = 0
	if snapshot == nil {
		a.reloadSQLCompletionCatalog()
		return
	}

	for _, item := range snapshot.items {
		if item.identifier != "" {
			a.tableOrder = append(a.tableOrder, item.identifier)
		}
	}
	items := a.orderedTableSidebarItems()
	if snapshot.tableCount == 0 {
		items = snapshot.items
	}
	for index, item := range items {
		a.tables.AddItem(item.label, "", 0, nil)
		if item.identifier != "" {
			a.tableIdentifiers[index] = item.identifier
		}
	}
	a.tableSidebarItems = len(items)

	a.tableCount = snapshot.tableCount
	a.tables.SetMainTextColor(peach)
	a.tables.SetSelectedBackgroundColor(blue)
	a.updateTableListTitle()
	if snapshot.tableCount == 0 {
		a.selectedTable = ""
	} else {
		a.selectedTable = snapshot.selectedTable
		a.selectTableListIdentifier(snapshot.selectedTable)
	}
	a.refreshTableSidebarState()

	a.tables.SetSelectedFunc(func(index int, _ string, _ string, _ rune) {
		if obj, ok := a.databaseObjects[index]; ok {
			a.clearResultNavigation()
			a.onDatabaseObjectSelected(obj.objType, obj.name)
			return
		}
		selectedTable, ok := a.tableIdentifiers[index]
		if !ok {
			return
		}
		previous := a.captureResultNavigationState()
		previousStack := append([]resultNavigationState(nil), a.resultNavStack...)
		a.selectTableWithRememberedFilter(selectedTable)
		a.clearResultNavigation()
		a.resetSort()
		a.resetPagination()
		a.loadCurrentTableAsync(tableLoadOptions{
			loadingText:  fmt.Sprintf("Loading %s...", selectedTable),
			cancelText:   "Press Esc to cancel opening this table.",
			canceledText: "Table loading canceled",
			errorText:    fmt.Sprintf("Could not load table %q", selectedTable),
			rollback: func() {
				a.restoreResultNavigationState(previous)
				a.resultNavStack = previousStack
				a.selectTableListIdentifier(previous.table)
			},
		})
	})
	a.reloadSQLCompletionCatalog()
}

func (a *App) orderedTableSidebarItems() []tableListSnapshotItem {
	if a == nil || len(a.tableOrder) == 0 {
		return nil
	}

	available := make(map[string]bool, len(a.tableOrder))
	for _, table := range a.tableOrder {
		available[table] = true
	}
	pinned := make([]string, 0)
	pinnedSet := make(map[string]bool)
	for _, table := range a.pinnedTablesForConnection() {
		if available[table] && !pinnedSet[table] {
			pinned = append(pinned, table)
			pinnedSet[table] = true
		}
	}

	items := make([]tableListSnapshotItem, 0, len(a.tableOrder)+len(pinned)+1)
	if len(pinned) > 0 {
		items = append(items, tableListSnapshotItem{
			label: fmt.Sprintf("[#6c7086]── %s Pinned (%d) ──[-]", iconPin, len(pinned)),
		})
		for _, table := range pinned {
			items = append(items, tableListSnapshotItem{label: table, identifier: table})
		}
	}

	lastNamespace := ""
	for _, table := range a.tableOrder {
		if pinnedSet[table] {
			continue
		}
		namespace := namespaceForTable(a.dbType, table)
		if namespace != "" && namespace != lastNamespace {
			items = append(items, tableListSnapshotItem{
				label: fmt.Sprintf("[#6c7086]── %s %s (%s) ──[-]", iconTables, namespaceKindLabel(a.dbType), namespace),
			})
			lastNamespace = namespace
		}
		items = append(items, tableListSnapshotItem{label: table, identifier: table})
	}
	return items
}

type preservedSidebarItem struct {
	mainText      string
	secondaryText string
	object        *databaseObjectListItem
}

func (a *App) rebuildTableSidebarForPins(selectedTable string) {
	if a == nil || a.tables == nil {
		return
	}
	preserved := make([]preservedSidebarItem, 0, max(0, a.tables.GetItemCount()-a.tableSidebarItems))
	for index := a.tableSidebarItems; index < a.tables.GetItemCount(); index++ {
		mainText, secondaryText := a.tables.GetItemText(index)
		item := preservedSidebarItem{mainText: mainText, secondaryText: secondaryText}
		if object, ok := a.databaseObjects[index]; ok {
			objectCopy := object
			item.object = &objectCopy
		}
		preserved = append(preserved, item)
	}

	a.tables.Clear()
	a.tableIdentifiers = map[int]string{}
	a.databaseObjects = map[int]databaseObjectListItem{}
	items := a.orderedTableSidebarItems()
	for index, item := range items {
		a.tables.AddItem(item.label, "", 0, nil)
		if item.identifier != "" {
			a.tableIdentifiers[index] = item.identifier
		}
	}
	a.tableSidebarItems = len(items)
	for _, item := range preserved {
		index := a.tables.GetItemCount()
		a.tables.AddItem(item.mainText, item.secondaryText, 0, nil)
		if item.object != nil {
			a.databaseObjects[index] = *item.object
		}
	}
	a.selectTableListIdentifier(selectedTable)
	a.refreshTableSidebarState()
}

func (a *App) pinnedTablesForConnection() []string {
	if a == nil || a.settings == nil || a.settings.PinnedTables == nil {
		return nil
	}
	connection, ok := a.activeConnectionKey()
	if !ok {
		return nil
	}
	return a.settings.PinnedTables[connection]
}

func (a *App) tableIsPinned(table string) bool {
	for _, pinned := range a.pinnedTablesForConnection() {
		if pinned == table {
			return true
		}
	}
	return false
}

func (a *App) selectedSidebarTable() (string, bool) {
	if a == nil || a.tables == nil {
		return "", false
	}
	table, ok := a.tableIdentifiers[a.tables.GetCurrentItem()]
	return table, ok && table != ""
}

func (a *App) toggleSelectedTablePin() {
	table, ok := a.selectedSidebarTable()
	if !ok {
		a.flashStatus("[yellow]Select a table to pin or unpin[-]", a.currentResultRowCount(), 1600*time.Millisecond)
		return
	}
	connection, ok := a.activeConnectionKey()
	if !ok || a.settings == nil {
		a.flashStatus("[yellow]Table pins require an active database connection[-]", a.currentResultRowCount(), 1800*time.Millisecond)
		return
	}
	if a.settings.PinnedTables == nil {
		a.settings.PinnedTables = make(map[string][]string)
	}
	previous := append([]string(nil), a.settings.PinnedTables[connection]...)
	pins := append([]string(nil), previous...)
	pinned := true
	for index, candidate := range pins {
		if candidate == table {
			pins = append(pins[:index], pins[index+1:]...)
			pinned = false
			break
		}
	}
	if pinned {
		pins = append(pins, table)
	}
	if len(pins) == 0 {
		delete(a.settings.PinnedTables, connection)
	} else {
		a.settings.PinnedTables[connection] = pins
	}
	if err := config.SaveSettings(a.settings); err != nil {
		if len(previous) == 0 {
			delete(a.settings.PinnedTables, connection)
		} else {
			a.settings.PinnedTables[connection] = previous
		}
		a.flashStatus(fmt.Sprintf("[yellow]Could not save table pin: %s[-]", tview.Escape(err.Error())), a.currentResultRowCount(), 2400*time.Millisecond)
		return
	}

	a.rebuildTableSidebarForPins(table)
	verb := "Unpinned"
	if pinned {
		verb = "Pinned"
	}
	a.flashStatus(fmt.Sprintf("[green]%s %s[-]", verb, tview.Escape(table)), a.currentResultRowCount(), 1600*time.Millisecond)
}

func namespaceForTable(dbType config.DBType, tableName string) string {
	parts := strings.SplitN(tableName, ".", 2)
	if len(parts) == 2 {
		return parts[0]
	}
	if dbType == config.PostgreSQL {
		return "public"
	}
	return ""
}

func namespaceKindLabel(dbType config.DBType) string {
	switch dbType {
	case config.PostgreSQL:
		return "Schema"
	case config.MySQL:
		return "Database"
	default:
		return "Group"
	}
}

func firstSelectableTableSnapshotIndex(snapshot *tableListSnapshot) int {
	for i := range snapshot.items {
		if isSelectableTableListSnapshotItem(snapshot, i) {
			return i
		}
	}
	return 0
}

func isSelectableTableListSnapshotItem(snapshot *tableListSnapshot, index int) bool {
	if snapshot == nil || index < 0 || index >= len(snapshot.items) {
		return false
	}
	return snapshot.items[index].identifier != ""
}

func firstSelectableTableIndex(list interface {
	GetItemCount() int
	GetItemText(index int) (string, string)
}) int {
	count := list.GetItemCount()
	for i := 0; i < count; i++ {
		if isSelectableTableListItem(list, i) {
			return i
		}
	}
	return 0
}

func isSelectableTableListItem(list interface {
	GetItemText(index int) (string, string)
}, index int) bool {
	label, _ := list.GetItemText(index)
	return isSelectableTableLabel(label)
}

func isSelectableTableLabel(label string) bool {
	trimmed := strings.TrimSpace(label)
	if strings.HasPrefix(trimmed, "[#a6e3a1]▶[-]") ||
		strings.HasPrefix(trimmed, "[#6c7086]•[-]") ||
		strings.HasPrefix(trimmed, "[#cba6f7]/[-]") ||
		strings.HasPrefix(trimmed, "[#f9e2af]"+iconPin+"[-]") ||
		strings.HasPrefix(trimmed, "[green]+[-]") ||
		strings.HasPrefix(trimmed, "[yellow]~[-]") ||
		strings.HasPrefix(trimmed, "[red]-[-]") ||
		strings.HasPrefix(trimmed, "[#cba6f7]Δ[-]") {
		return true
	}
	return !strings.HasPrefix(trimmed, "[")
}

func (a *App) handleTableListInput(event *tcell.EventKey) *tcell.EventKey {
	if event == nil {
		return nil
	}

	switch event.Key() {
	case tcell.KeyEnter:
		if !a.hasActiveTableSearch() {
			return event
		}
		matched := a.firstTableSearchMatch(a.tableSearch) >= 0
		a.clearTableSearch()
		if matched {
			return event
		}
		return nil
	case tcell.KeyEscape:
		if a.hasActiveTableSearch() {
			a.clearTableSearch()
			return nil
		}
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if a.hasActiveTableSearch() {
			runes := []rune(a.tableSearch)
			a.tableSearch = string(runes[:len(runes)-1])
			a.applyTableSearch()
			return nil
		}
	case tcell.KeyRune:
		if event.Modifiers()&(tcell.ModCtrl|tcell.ModAlt|tcell.ModMeta) == 0 && !a.hasActiveTableSearch() && event.Rune() == ' ' {
			a.toggleSelectedTablePin()
			return nil
		}
		if event.Modifiers()&(tcell.ModCtrl|tcell.ModAlt|tcell.ModMeta) == 0 && unicode.IsPrint(event.Rune()) {
			a.tableSearch += string(event.Rune())
			a.applyTableSearch()
			return nil
		}
	}

	return event
}

func (a *App) hasActiveTableSearch() bool {
	return a != nil && a.tableSearch != ""
}

func (a *App) clearTableSearch() {
	if a == nil || a.tableSearch == "" {
		return
	}
	a.tableSearch = ""
	a.applyTableSearch()
}

func (a *App) applyTableSearch() {
	if a == nil || a.tables == nil {
		return
	}

	firstMatch := -1
	for index := 0; index < a.tables.GetItemCount(); index++ {
		identifier, ok := a.tableIdentifiers[index]
		if !ok {
			continue
		}

		identifierLabel := tview.Escape(identifier)
		if a.tableSearch != "" {
			if highlighted, matched := highlightTableSearchMatch(identifier, a.tableSearch); matched {
				identifierLabel = highlighted
				if firstMatch < 0 {
					firstMatch = index
				}
			}
		}
		a.tables.SetItemText(index, a.tableSidebarLabel(identifier, identifierLabel), "")
	}

	if firstMatch >= 0 {
		a.tables.SetCurrentItem(firstMatch)
	}
	a.updateTableListTitle()
}

// refreshTableSidebarState redraws table-only rows without disturbing schema
// headers or discovered database objects. The markers work without color:
// ▶ is the table currently shown, • means opened this connection, / means that
// table has a remembered filter, and 📌 means pinned to the top.
func (a *App) refreshTableSidebarState() {
	if a == nil || a.tables == nil {
		return
	}
	a.applyTableSearch()
}

func (a *App) tableSidebarLabel(identifier, identifierLabel string) string {
	stateMarker := " "
	if identifier != "" && identifier == a.activeTable && a.tableResultsActive {
		stateMarker = "[#a6e3a1]▶[-]"
	} else if a.visitedTables != nil && a.visitedTables[identifier] {
		stateMarker = "[#6c7086]•[-]"
	}

	filterMarker := " "
	if a.tableHasRememberedFilter(identifier) {
		filterMarker = "[#cba6f7]/[-]"
	}
	pinMarker := "  "
	if a.tableIsPinned(identifier) {
		pinMarker = "[#f9e2af]" + iconPin + "[-]"
	}
	if summary, ok := a.profilerTableChanges[identifier]; ok {
		return fmt.Sprintf("%s%s%s%s %s", stateMarker, filterMarker, pinMarker, profilerTableMarker(summary), identifierLabel)
	}
	return fmt.Sprintf("%s%s%s %s", stateMarker, filterMarker, pinMarker, identifierLabel)
}

func (a *App) tableHasRememberedFilter(identifier string) bool {
	if a == nil || identifier == "" {
		return false
	}
	if filter := a.resultFilters[identifier]; filter != nil && len(filter.orderedPredicates()) > 0 {
		return true
	}
	return identifier == a.selectedTable && a.activeResultFilter(identifier) != nil
}

func (a *App) firstTableSearchMatch(query string) int {
	if a == nil || query == "" {
		return -1
	}
	for index := 0; a.tables != nil && index < a.tables.GetItemCount(); index++ {
		if identifier, ok := a.tableIdentifiers[index]; ok {
			if _, _, matched := tableSearchMatchRange(identifier, query); matched {
				return index
			}
		}
	}
	return -1
}

func highlightTableSearchMatch(identifier, query string) (string, bool) {
	start, end, matched := tableSearchMatchRange(identifier, query)
	if !matched {
		return identifier, false
	}

	runes := []rune(identifier)
	return tview.Escape(string(runes[:start])) +
		"[black:#f9e2af:b]" + tview.Escape(string(runes[start:end])) + "[-:-:-]" +
		tview.Escape(string(runes[end:])), true
}

func tableSearchMatchRange(identifier, query string) (int, int, bool) {
	identifierRunes := []rune(identifier)
	queryRunes := []rune(query)
	if len(queryRunes) == 0 || len(queryRunes) > len(identifierRunes) {
		return 0, 0, false
	}

	for start := 0; start <= len(identifierRunes)-len(queryRunes); start++ {
		matched := true
		for offset := range queryRunes {
			if unicode.ToLower(identifierRunes[start+offset]) != unicode.ToLower(queryRunes[offset]) {
				matched = false
				break
			}
		}
		if matched {
			return start, start + len(queryRunes), true
		}
	}
	return 0, 0, false
}

func (a *App) updateTableListTitle() {
	if a == nil || a.tables == nil {
		return
	}

	count := fmt.Sprintf("Tables (%d)", a.tableCount)
	if a.databaseObjectCount > 0 {
		count += fmt.Sprintf(" + %d obj", a.databaseObjectCount)
	}
	search := ""
	if a.tableSearch != "" {
		search = fmt.Sprintf(" [#a6adc8]find:[-] [#f9e2af]%s[-]", tview.Escape(a.tableSearch))
	}
	legend := fmt.Sprintf(" [yellow]Space[-] pin %s  [#6c7086]▶ • /[-]", iconPin)
	a.tables.SetTitle(fmt.Sprintf(" %s %s%s%s [yellow](Alt+T)[-] ", iconTables, count, search, legend))
}
