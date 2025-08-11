package ui

import (
	"github.com/shreyam1008/dbterm/utils"
)

func (a *App) LoadTables() error {
	a.tables.Clear()

	query := utils.ListTablesQuery(a.dbType)
	if query == "" {
		return nil
	}

	rows, err := a.db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return err
		}
		a.tables.AddItem(tableName, "", 0, nil).
			SetMainTextColor(peach).
			SetSelectedBackgroundColor(blue)
	}

	a.tables.SetSelectedFunc(func(_ int, selectedTable string, _ string, _ rune) {
		a.selectedTable = selectedTable
		a.LoadResults()
	})

	return rows.Err()
}
