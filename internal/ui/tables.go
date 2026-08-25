package ui

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
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
	parent     string
	column     string
	lastColumn bool
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
	a.tableColumnItems = map[int]sidebarColumnRef{}
	a.tableSearch = ""
	a.expandedSidebarTable = ""
	a.sidebarColumnMetadata = nil
	a.sidebarMetadataLoads = nil
	a.sidebarSearchIndex = nil
	a.sidebarSearchLookup = sidebarSearchLookup{}
	a.sidebarRenderedSearch = sidebarSelection{}
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
		a.addTableSidebarItem(index, item)
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
		if column, ok := a.tableColumnItems[index]; ok {
			a.openSidebarColumn(column)
			return
		}
		if obj, ok := a.databaseObjects[index]; ok {
			a.clearResultNavigation()
			a.onDatabaseObjectSelected(obj.objType, obj.name)
			return
		}
		selectedTable, ok := a.tableIdentifiers[index]
		if !ok {
			return
		}
		a.openSidebarTable(selectedTable, nil)
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
			items = a.appendExpandedSidebarColumns(items, table)
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
		items = a.appendExpandedSidebarColumns(items, table)
	}
	return items
}

type preservedSidebarItem struct {
	mainText      string
	secondaryText string
	object        *databaseObjectListItem
}

func (a *App) rebuildTableSidebarForPins(selectedTable string) {
	a.rebuildTableSidebar(sidebarSelection{table: selectedTable})
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
	index := a.tables.GetCurrentItem()
	if column, ok := a.tableColumnItems[index]; ok {
		return column.table, column.table != ""
	}
	table, ok := a.tableIdentifiers[index]
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
		strings.HasPrefix(trimmed, "[#6c7086]▸[-]") ||
		strings.HasPrefix(trimmed, "[#a6adc8]▾[-]") ||
		strings.HasPrefix(trimmed, "[#6c7086]├─[-]") ||
		strings.HasPrefix(trimmed, "[#6c7086]└─[-]") ||
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
	if !a.hasActiveTableSearch() && isTableNameCopyKey(event) {
		if column, ok := a.tableColumnItems[a.tables.GetCurrentItem()]; ok {
			a.copyColumnName(column.column)
		} else {
			a.copySelectedTableName()
		}
		return nil
	}

	switch event.Key() {
	case tcell.KeyUp:
		if a.hasActiveTableSearch() {
			a.moveTableSearchSelection(-1)
			return nil
		}
		a.moveSidebarSelection(-1)
		return nil
	case tcell.KeyDown:
		if a.hasActiveTableSearch() {
			a.moveTableSearchSelection(1)
			return nil
		}
		a.moveSidebarSelection(1)
		return nil
	case tcell.KeyHome:
		a.selectSidebarBoundary(1)
		return nil
	case tcell.KeyEnd:
		a.selectSidebarBoundary(-1)
		return nil
	case tcell.KeyRight:
		index := a.tables.GetCurrentItem()
		if table, ok := a.tableIdentifiers[index]; ok {
			if a.expandedSidebarTable == table {
				a.selectFirstSidebarColumn(table)
			} else {
				a.toggleSidebarTable(table)
			}
			return nil
		}
	case tcell.KeyLeft:
		index := a.tables.GetCurrentItem()
		if column, ok := a.tableColumnItems[index]; ok {
			a.focusSidebarColumnParent(column)
			return nil
		}
		if table, ok := a.tableIdentifiers[index]; ok && a.expandedSidebarTable == table {
			a.toggleSidebarTable(table)
			return nil
		}
	case tcell.KeyEnter:
		if !a.hasActiveTableSearch() {
			return event
		}
		matched := a.sidebarIndexMatchesSearch(a.tables.GetCurrentItem(), a.tableSearch)
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
			if _, columnSelected := a.tableColumnItems[a.tables.GetCurrentItem()]; !columnSelected {
				a.toggleSelectedTablePin()
			}
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

type tableSearchMatch struct {
	index int
	score int
}

// tableSearchMatches returns only openable database objects. Columns remain
// available through schema expansion and the command palette, but sidebar
// type-ahead intentionally stays at the table/view/object level.
func (a *App) tableSearchMatches(query string) []tableSearchMatch {
	if a == nil || a.tables == nil {
		return nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}

	matches := make([]tableSearchMatch, 0)
	for index := 0; index < a.tables.GetItemCount(); index++ {
		name, ok := a.sidebarSearchableName(index)
		if !ok {
			continue
		}
		folded := strings.ToLower(name)
		score := 0
		switch {
		case strings.Contains(folded, query):
			score = sidebarTextMatchScore(folded, query)
		default:
			fuzzyScore, matched := foldedSubsequenceScore(folded, query)
			if !matched {
				continue
			}
			score = 80 + fuzzyScore
		}
		matches = append(matches, tableSearchMatch{index: index, score: score})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			return matches[i].index < matches[j].index
		}
		return matches[i].score < matches[j].score
	})
	return matches
}

func (a *App) sidebarSearchableName(index int) (string, bool) {
	if table, ok := a.tableIdentifiers[index]; ok && strings.TrimSpace(table) != "" {
		return table, true
	}
	if object, ok := a.databaseObjects[index]; ok && strings.TrimSpace(object.name) != "" {
		return object.name, true
	}
	return "", false
}

func (a *App) sidebarIndexMatchesSearch(index int, query string) bool {
	for _, match := range a.tableSearchMatches(query) {
		if match.index == index {
			return true
		}
	}
	return false
}

func (a *App) moveTableSearchSelection(direction int) bool {
	if direction == 0 {
		return false
	}
	matches := a.tableSearchMatches(a.tableSearch)
	if len(matches) == 0 {
		return false
	}
	current := a.tables.GetCurrentItem()
	position := -1
	for index, match := range matches {
		if match.index == current {
			position = index
			break
		}
	}
	if position < 0 {
		position = 0
	} else {
		position = (position + direction + len(matches)) % len(matches)
	}
	a.renderTableSidebarSearchIndex(matches[position].index, true)
	return true
}

func isTableNameCopyKey(event *tcell.EventKey) bool {
	return event != nil &&
		event.Key() == tcell.KeyRune &&
		event.Rune() == 'C' &&
		event.Modifiers()&(tcell.ModCtrl|tcell.ModAlt|tcell.ModMeta) == 0
}

func (a *App) handleTableListMouse(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
	if a == nil || a.tables == nil || event == nil {
		return action, event
	}

	x, y := event.Position()
	index := tableListIndexAtPoint(a.tables, x, y)
	if index < 0 {
		return action, event
	}
	if action == tview.MouseLeftClick {
		innerX, _, _, _ := a.tables.GetInnerRect()
		if table, ok := a.tableIdentifiers[index]; ok && table != "" && x < innerX+8 {
			a.tables.SetCurrentItem(index)
			if a.app != nil {
				a.setFocusWithColor(a.tables)
			}
			a.toggleSidebarTable(table)
			return tview.MouseConsumed, nil
		}
		if !a.sidebarIndexSelectable(index) {
			return tview.MouseConsumed, nil
		}
		return action, event
	}
	if action != tview.MouseRightClick {
		return action, event
	}

	if column, ok := a.tableColumnItems[index]; ok {
		a.tables.SetCurrentItem(index)
		if a.app != nil {
			a.setFocusWithColor(a.tables)
		}
		a.copyColumnName(column.column)
		return tview.MouseConsumed, nil
	}
	table, ok := a.tableIdentifiers[index]
	if !ok || table == "" {
		return tview.MouseConsumed, nil
	}
	a.tables.SetCurrentItem(index)
	if a.app != nil {
		a.setFocusWithColor(a.tables)
	}
	a.copyTableName(table)
	return tview.MouseConsumed, nil
}

func (a *App) sidebarIndexSelectable(index int) bool {
	if a == nil || a.tables == nil || index < 0 || index >= a.tables.GetItemCount() {
		return false
	}
	if _, ok := a.tableIdentifiers[index]; ok {
		return true
	}
	if _, ok := a.tableColumnItems[index]; ok {
		return true
	}
	_, ok := a.databaseObjects[index]
	return ok
}

func (a *App) moveSidebarSelection(direction int) bool {
	if a == nil || a.tables == nil || direction == 0 {
		return false
	}
	for index := a.tables.GetCurrentItem() + direction; index >= 0 && index < a.tables.GetItemCount(); index += direction {
		if a.sidebarIndexSelectable(index) {
			a.tables.SetCurrentItem(index)
			return true
		}
	}
	return false
}

func (a *App) selectSidebarBoundary(direction int) bool {
	if a == nil || a.tables == nil || direction == 0 {
		return false
	}
	start, end := 0, a.tables.GetItemCount()
	if direction < 0 {
		start, end = a.tables.GetItemCount()-1, -1
	}
	for index := start; index != end; index += direction {
		if a.sidebarIndexSelectable(index) {
			a.tables.SetCurrentItem(index)
			return true
		}
	}
	return false
}

func tableListIndexAtPoint(list *tview.List, x, y int) int {
	if list == nil {
		return -1
	}
	innerX, innerY, width, height := list.GetInnerRect()
	if x < innerX || x >= innerX+width || y < innerY || y >= innerY+height {
		return -1
	}
	itemOffset, _ := list.GetOffset()
	index := itemOffset + y - innerY
	if index < 0 || index >= list.GetItemCount() {
		return -1
	}
	return index
}

func (a *App) copySelectedTableName() {
	table, ok := a.selectedSidebarTable()
	if !ok {
		a.flashStatus("[yellow]Select a table name to copy[-]", a.currentResultRowCount(), 1600*time.Millisecond)
		return
	}
	a.copyTableName(table)
}

func (a *App) copyTableName(table string) {
	if a == nil || strings.TrimSpace(table) == "" {
		return
	}
	a.copyValueAsync(table, func(err error) {
		if err != nil {
			a.flashStatus(fmt.Sprintf("[yellow]Copied table %s inside dbterm (system clipboard unavailable)[-]", tview.Escape(table)), a.currentResultRowCount(), 2200*time.Millisecond)
		}
	})
	a.flashStatus(fmt.Sprintf("[green]Copied table %s[-]", tview.Escape(table)), a.currentResultRowCount(), 1600*time.Millisecond)
}

func (a *App) copyColumnName(column string) {
	if a == nil || strings.TrimSpace(column) == "" {
		return
	}
	a.copyValueAsync(column, func(err error) {
		if err != nil {
			a.flashStatus(fmt.Sprintf("[yellow]Copied column %s inside dbterm (system clipboard unavailable)[-]", tview.Escape(column)), a.currentResultRowCount(), 2200*time.Millisecond)
		}
	})
	a.flashStatus(fmt.Sprintf("[green]Copied column %s[-]", tview.Escape(column)), a.currentResultRowCount(), 1600*time.Millisecond)
}

func (a *App) openSidebarTable(selectedTable string, onSuccess func()) {
	if a == nil || strings.TrimSpace(selectedTable) == "" {
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
		onSuccess:    onSuccess,
		rollback: func() {
			a.restoreResultNavigationState(previous)
			a.resultNavStack = previousStack
			a.selectTableListIdentifier(previous.table)
		},
	})
}

func (a *App) openSidebarColumn(column sidebarColumnRef) {
	if a == nil || column.table == "" || column.column == "" {
		return
	}
	if a.tableResultsActive && a.activeTable == column.table {
		a.focusSidebarResultColumn(column)
		return
	}
	a.openSidebarTable(column.table, func() {
		a.focusSidebarResultColumn(column)
	})
}

func (a *App) focusSidebarResultColumn(column sidebarColumnRef) {
	if a == nil || a.focusResultColumnByName(column.column) {
		return
	}
	a.flashStatus(
		fmt.Sprintf("[yellow]Column %s is no longer in %s — refresh database objects and try again[-]", tview.Escape(column.column), tview.Escape(column.table)),
		a.currentResultRowCount(), 2600*time.Millisecond,
	)
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
	matches := a.tableSearchMatches(a.tableSearch)
	if len(matches) == 0 {
		a.renderTableSidebarSearchIndex(-1, false)
		return
	}
	a.renderTableSidebarSearchIndex(matches[0].index, true)
}

func (a *App) renderTableSidebarSearch() {
	if a == nil || a.tables == nil {
		return
	}
	matches := a.tableSearchMatches(a.tableSearch)
	if len(matches) == 0 {
		a.renderTableSidebarSearchIndex(-1, false)
		return
	}
	a.renderTableSidebarSearchIndex(matches[0].index, true)
}

func (a *App) renderTableSidebarSearchIndex(index int, matched bool) {
	if a == nil || a.tables == nil {
		return
	}
	next := sidebarSelection{}
	if matched && strings.TrimSpace(a.tableSearch) != "" {
		next = sidebarSelection{index: index, indexed: true}
	}
	if a.sidebarRenderedSearch != next {
		a.setSidebarSearchItemLabel(a.sidebarRenderedSearch, "")
	}
	query := strings.TrimSpace(a.tableSearch)
	if next.indexed {
		a.setSidebarSearchItemLabel(next, query)
		a.tables.SetCurrentItem(next.index)
	}
	a.sidebarRenderedSearch = next
	a.updateTableListTitle()
}

func (a *App) setSidebarSearchItemLabel(selection sidebarSelection, query string) {
	if selection.indexed {
		if identifier, ok := a.tableIdentifiers[selection.index]; ok {
			identifierLabel := tview.Escape(identifier)
			if query != "" {
				if highlighted, matched := highlightTableSearchMatch(identifier, query); matched {
					identifierLabel = highlighted
				}
			}
			a.tables.SetItemText(selection.index, a.tableSidebarLabel(identifier, identifierLabel), "")
			return
		}
		if object, ok := a.databaseObjects[selection.index]; ok {
			nameLabel := tview.Escape(object.name)
			if query != "" {
				if highlighted, matched := highlightTableSearchMatch(object.name, query); matched {
					nameLabel = highlighted
				}
			}
			a.tables.SetItemText(selection.index, fmt.Sprintf("  [#a6adc8]%s[-] %s", objectTypeIcon(object.objType), nameLabel), "")
		}
		return
	}
	if selection.column != "" {
		for index, column := range a.tableColumnItems {
			if column.table == selection.table && strings.EqualFold(column.column, selection.column) {
				a.tables.SetItemText(index, a.sidebarColumnLabel(column, column.last, query), "")
				return
			}
		}
		return
	}
	if selection.table == "" {
		return
	}
	for index, identifier := range a.tableIdentifiers {
		if identifier != selection.table {
			continue
		}
		identifierLabel := tview.Escape(identifier)
		if query != "" {
			if highlighted, ok := highlightTableSearchMatch(identifier, query); ok {
				identifierLabel = highlighted
			}
		}
		a.tables.SetItemText(index, a.tableSidebarLabel(identifier, identifierLabel), "")
		return
	}
}

// refreshTableSidebarState redraws table-only rows without disturbing schema
// headers or discovered database objects. The markers work without color:
// ▶ is the table currently shown, • means opened this connection, / means that
// table has a remembered filter, and 📌 means pinned to the top.
func (a *App) refreshTableSidebarState() {
	if a == nil || a.tables == nil {
		return
	}
	for index, identifier := range a.tableIdentifiers {
		a.tables.SetItemText(index, a.tableSidebarLabel(identifier, tview.Escape(identifier)), "")
	}
	for index, column := range a.tableColumnItems {
		a.tables.SetItemText(index, a.sidebarColumnLabel(column, column.last, ""), "")
	}
	a.sidebarRenderedSearch = sidebarSelection{}
	a.renderTableSidebarSearch()
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
	disclosure := "[#6c7086]▸[-]"
	if a.expandedSidebarTable == identifier {
		disclosure = "[#a6adc8]▾[-]"
	}
	if summary, ok := a.profilerTableChanges[identifier]; ok {
		return fmt.Sprintf("%s%s%s%s%s %s", stateMarker, filterMarker, pinMarker, profilerTableMarker(summary), disclosure, identifierLabel)
	}
	return fmt.Sprintf("%s%s%s%s %s", stateMarker, filterMarker, pinMarker, disclosure, identifierLabel)
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
	if a == nil || query == "" || a.tables == nil {
		return -1
	}
	matches := a.tableSearchMatches(query)
	if len(matches) == 0 {
		return -1
	}
	return matches[0].index
}

// handleWorkspaceMouse turns the shared border between Tables and the
// workspace into a drag handle on wide layouts.
func (a *App) handleWorkspaceMouse(event *tcell.EventMouse, action tview.MouseAction) (*tcell.EventMouse, tview.MouseAction) {
	if a == nil || event == nil || a.tables == nil || a.mainFlex == nil || a.pages == nil {
		return event, action
	}
	page, _ := a.pages.GetFrontPage()
	if page != "main" || a.tableExpanded || a.lastScreenW < 110 {
		if action == tview.MouseLeftUp {
			a.sidebarDragging = false
		}
		return event, action
	}

	x, y := event.Position()
	tableX, tableY, tableWidth, tableHeight := a.tables.GetRect()
	switch action {
	case tview.MouseLeftDown:
		borderX := tableX + tableWidth - 1
		if x == borderX && y >= tableY && y < tableY+tableHeight {
			a.sidebarDragging = true
			return nil, action
		}
	case tview.MouseMove:
		if a.sidebarDragging {
			mainX, _, mainWidth, _ := a.mainFlex.GetRect()
			a.sidebarWidth = clamp(x-mainX+1, 18, max(18, mainWidth-48))
			a.mainFlex.ResizeItem(a.tables, a.sidebarWidth, 0)
		}
	case tview.MouseLeftUp:
		if a.sidebarDragging {
			a.sidebarDragging = false
			return nil, action
		}
	}
	return event, action
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
	legend := fmt.Sprintf(" [yellow]→/←[-] schema  [yellow]Space[-] pin %s  [#6c7086]▶ • /  PK FK NN[-]", iconPin)
	a.tables.SetTitle(fmt.Sprintf(" %s %s%s%s [yellow](%s)[-] ", iconTables, count, search, legend, a.escapedActionShortcut(actionFocusTables)))
}
