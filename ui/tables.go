package ui

import (
	"fmt"

	"github.com/shreyam1008/dbterm/utils"
)

// LoadTables fetches the list of tables from the connected database
func (a *App) LoadTables() error {
	a.tables.Clear()
	a.tableCount = 0

	if a.db == nil {
		return fmt.Errorf("not connected to any database")
	}

	query := utils.ListTablesQuery(a.dbType)
	if query == "" {
		return fmt.Errorf("unsupported database type: %s", a.dbType)
	}

	rows, err := a.db.Query(query)
	if err != nil {
		return fmt.Errorf("could not list tables: %w", err)
	}
	defer rows.Close()

	currentSelection := a.selectedTable

	count := 0
	selectedIndex := 0
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return fmt.Errorf("could not read table name: %w", err)
		}
		a.tables.AddItem(tableName, "", 0, nil)
		if tableName == currentSelection {
			selectedIndex = count
		}
		count++
	}

	a.tableCount = count
	a.tables.SetMainTextColor(peach)
	a.tables.SetSelectedBackgroundColor(blue)

	// Update title with count
	a.tables.SetTitle(fmt.Sprintf(" Tables (%d) [yellow](Alt+T)[-] ", count))

	if count == 0 {
		a.tables.AddItem("[gray]No tables found[-]", "", 0, nil)
	} else {
		a.tables.SetCurrentItem(selectedIndex)
	}

	a.tables.SetSelectedFunc(func(_ int, selectedTable string, _ string, _ rune) {
		a.selectedTable = selectedTable
		if err := a.LoadResults(); err != nil {
			a.ShowAlert(fmt.Sprintf("Could not load table \"%s\":\n\n%v", selectedTable, err), "main")
		}
	})

	return rows.Err()
}
