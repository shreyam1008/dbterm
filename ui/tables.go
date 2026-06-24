package ui

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/config"
	"github.com/shreyam1008/dbterm/utils"
)

type tableListSnapshotItem struct {
	label string
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
	snapshot, err := loadTableListSnapshot(a.db, a.dbType, a.selectedTable, activeDatabaseName(a), currentIndex)
	if err != nil {
		return err
	}
	a.applyTableListSnapshot(snapshot)

	// Load database objects (views, functions, triggers, etc.) asynchronously
	a.loadDatabaseObjects()

	return nil
}

func loadTableListSnapshot(db *sql.DB, dbType config.DBType, selectedTable, activeDatabase string, currentIndex int) (*tableListSnapshot, error) {
	if db == nil {
		return nil, fmt.Errorf("not connected to any database")
	}

	query := utils.ListTablesQuery(dbType)
	if query == "" {
		return nil, fmt.Errorf("unsupported database type: %s", dbType)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("could not list tables: %w", err)
	}
	defer rows.Close()

	snapshot := &tableListSnapshot{selectedIndex: 0}
	foundSelection := false

	if dbType == config.PostgreSQL {
		appendInstanceDatabasesSectionSnapshot(ctx, db, dbType, selectedTable, activeDatabase, snapshot)
	}

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

		snapshot.items = append(snapshot.items, tableListSnapshotItem{label: tableName})
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
		tableName := snapshot.items[snapshot.selectedIndex].label
		if !strings.HasPrefix(tableName, "[") {
			snapshot.selectedTable = tableName
		}
	}

	return snapshot, nil
}

func (a *App) applyTableListSnapshot(snapshot *tableListSnapshot) {
	a.tables.Clear()
	a.databaseObjects = map[int]databaseObjectListItem{}
	a.tableCount = 0
	if snapshot == nil {
		return
	}

	for _, item := range snapshot.items {
		a.tables.AddItem(item.label, "", 0, nil)
	}

	a.tableCount = snapshot.tableCount
	a.tables.SetMainTextColor(peach)
	a.tables.SetSelectedBackgroundColor(blue)
	a.tables.SetTitle(fmt.Sprintf(" %s Tables (%d) [yellow](Alt+T)[-] ", iconTables, snapshot.tableCount))
	if snapshot.tableCount == 0 {
		a.selectedTable = ""
	} else {
		a.tables.SetCurrentItem(snapshot.selectedIndex)
		a.selectedTable = snapshot.selectedTable
	}

	a.tables.SetSelectedFunc(func(index int, selectedTable string, _ string, _ rune) {
		if obj, ok := a.databaseObjects[index]; ok {
			a.onDatabaseObjectSelected(obj.objType, obj.name)
			return
		}
		if !isSelectableTableLabel(selectedTable) {
			return
		}
		a.selectedTable = selectedTable
		a.resetSort()
		a.resetPagination()
		if err := a.LoadResults(); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not load table \"%s\":\n\n%v", iconWarn, selectedTable, err), "main")
		}
	})
}

func appendInstanceDatabasesSectionSnapshot(ctx context.Context, db *sql.DB, dbType config.DBType, currentSelection, activeDatabase string, snapshot *tableListSnapshot) {
	query := utils.ListDatabasesQuery(dbType)
	if query == "" {
		return
	}

	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	rows, err := db.QueryContext(dbCtx, query)
	if err != nil {
		return
	}
	defer rows.Close()

	var databases []string
	for rows.Next() {
		var databaseName string
		if err := rows.Scan(&databaseName); err != nil {
			return
		}
		databases = append(databases, databaseName)
	}
	if len(databases) == 0 {
		return
	}

	snapshot.items = append(snapshot.items, tableListSnapshotItem{label: fmt.Sprintf("[#6c7086]── %s Instance Databases (%d) ──[-]", iconDatabase, len(databases))})
	for _, databaseName := range databases {
		label := databaseName
		if strings.EqualFold(databaseName, currentSelection) || strings.EqualFold(databaseName, activeDatabase) {
			label += " [green](current)[-]"
		}
		snapshot.items = append(snapshot.items, tableListSnapshotItem{label: fmt.Sprintf("[#6c7086]%s[-]", label)})
	}
	snapshot.items = append(snapshot.items, tableListSnapshotItem{label: "[#6c7086]────────[-]"})
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

func activeDatabaseName(a *App) string {
	if a == nil || a.activeConn == nil {
		return ""
	}
	return strings.TrimSpace(a.activeConn.Database)
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
	return isSelectableTableLabel(snapshot.items[index].label)
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
