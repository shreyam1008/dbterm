package ui

func (a *App) LoadTables() error {
	// Clear existing items
	a.tables.Clear()

	// Query to get all tables from public schema
	query := `
		SELECT table_name 
		FROM information_schema.tables 
		WHERE table_schema = 'public'
		ORDER BY table_name
	`

	rows, err := a.db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	// Add each table to the list
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return err
		}
		a.tables.AddItem(tableName, "", 0, nil).SetMainTextColor(peach).SetSelectedBackgroundColor(blue)

	}

	a.tables.SetSelectedFunc(func(_ int, selectedTable string, _ string, _ rune) {
		a.selectedTable = selectedTable
		a.LoadResults()
	})

	return rows.Err()
}
