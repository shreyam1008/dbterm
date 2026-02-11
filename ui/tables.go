package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

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
			foundSelection = true
		}
		count++
	}

	if !foundSelection && currentIndex >= 0 && currentIndex < count {
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
		if err := a.LoadResults(); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not load table \"%s\":\n\n%v", iconWarn, selectedTable, err), "main")
		}
	})

	return rows.Err()
}
