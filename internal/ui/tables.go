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
	a.tableIdentifiers = map[int]string{}
	a.tableSearch = ""
	a.databaseObjectCount = 0
	a.tableCount = 0
	if snapshot == nil {
		return
	}

	for index, item := range snapshot.items {
		a.tables.AddItem(item.label, "", 0, nil)
		if item.identifier != "" {
			a.tableIdentifiers[index] = item.identifier
		}
	}

	a.tableCount = snapshot.tableCount
	a.tables.SetMainTextColor(peach)
	a.tables.SetSelectedBackgroundColor(blue)
	a.updateTableListTitle()
	if snapshot.tableCount == 0 {
		a.selectedTable = ""
	} else {
		a.tables.SetCurrentItem(snapshot.selectedIndex)
		a.selectedTable = snapshot.selectedTable
	}

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
		a.selectedTable = selectedTable
		a.clearResultNavigation()
		a.resultFilter = nil
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
	return !strings.HasPrefix(strings.TrimSpace(label), "[")
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

		label := identifier
		if a.tableSearch != "" {
			if highlighted, matched := highlightTableSearchMatch(identifier, a.tableSearch); matched {
				label = highlighted
				if firstMatch < 0 {
					firstMatch = index
				}
			}
		}
		a.tables.SetItemText(index, label, "")
	}

	if firstMatch >= 0 {
		a.tables.SetCurrentItem(firstMatch)
	}
	a.updateTableListTitle()
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
	a.tables.SetTitle(fmt.Sprintf(" %s %s%s [yellow](Alt+T)[-] ", iconTables, count, search))
}
