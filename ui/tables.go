package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/config"
	"github.com/shreyam1008/dbterm/utils"
)

// LoadTables fetches the list of tables from the connected database
func (a *App) LoadTables() error {
	currentIndex := a.tables.GetCurrentItem()
	a.tables.Clear()
	a.tableCount = 0

	if a.db == nil {
		return fmt.Errorf("not connected to any database")
	}

	query := utils.ListTablesQuery(a.dbType)
	if query == "" {
		return fmt.Errorf("unsupported database type: %s", a.dbType)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := a.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("could not list tables: %w", err)
	}
	defer rows.Close()

	currentSelection := a.selectedTable
	foundSelection := false

	if a.dbType == config.PostgreSQL {
		appendInstanceDatabasesSection(a, currentSelection)
	}

	count := 0
	selectedIndex := 0
	lastNamespace := ""
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return fmt.Errorf("could not read table name: %w", err)
		}

		namespace := namespaceForTable(a.dbType, tableName)
		if namespace != "" && namespace != lastNamespace {
			a.tables.AddItem(
				fmt.Sprintf("[#6c7086]── %s %s (%s) ──[-]", iconTables, namespaceKindLabel(a.dbType), namespace),
				"",
				0,
				nil,
			)
			lastNamespace = namespace
		}

		a.tables.AddItem(tableName, "", 0, nil)
		itemIndex := a.tables.GetItemCount() - 1
		if tableName == currentSelection {
			selectedIndex = itemIndex
			foundSelection = true
		}
		count++
	}

	if !foundSelection && currentIndex >= 0 && currentIndex < a.tables.GetItemCount() {
		selectedIndex = currentIndex
	}

	a.tableCount = count
	a.tables.SetMainTextColor(peach)
	a.tables.SetSelectedBackgroundColor(blue)

	// Update title with count
	a.tables.SetTitle(fmt.Sprintf(" %s Tables (%d) [yellow](Alt+T)[-] ", iconTables, count))

	if count == 0 {
		a.selectedTable = ""
		a.tables.AddItem(fmt.Sprintf("[gray]%s No tables found[-]", iconInfo), "", 0, nil)
	} else {
		if !isSelectableTableListItem(a.tables, selectedIndex) {
			selectedIndex = firstSelectableTableIndex(a.tables)
		}
		a.tables.SetCurrentItem(selectedIndex)
		if tableName, _ := a.tables.GetItemText(selectedIndex); !strings.HasPrefix(tableName, "[") {
			a.selectedTable = tableName
		}
	}

	a.tables.SetSelectedFunc(func(_ int, selectedTable string, _ string, _ rune) {
		if strings.HasPrefix(selectedTable, "[") {
			return
		}
		a.selectedTable = selectedTable
		a.resetSort()
		a.resetPagination()
		if err := a.LoadResults(); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not load table \"%s\":\n\n%v", iconWarn, selectedTable, err), "main")
		}
	})

	// Load database objects (views, functions, triggers, etc.) asynchronously
	a.loadDatabaseObjects()

	return rows.Err()
}

func appendInstanceDatabasesSection(a *App, currentSelection string) {
	if a == nil || a.db == nil {
		return
	}

	query := utils.ListDatabasesQuery(a.dbType)
	if query == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := a.db.QueryContext(ctx, query)
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

	a.tables.AddItem(fmt.Sprintf("[#6c7086]── %s Instance Databases (%d) ──[-]", iconDatabase, len(databases)), "", 0, nil)
	for _, databaseName := range databases {
		label := databaseName
		if strings.EqualFold(databaseName, currentSelection) || strings.EqualFold(databaseName, activeDatabaseName(a)) {
			label += " [green](current)[-]"
		}
		a.tables.AddItem(fmt.Sprintf("[#6c7086]%s[-]", label), "", 0, nil)
	}
	a.tables.AddItem("[#6c7086]────────[-]", "", 0, nil)
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
	return !strings.HasPrefix(label, "[")
}
