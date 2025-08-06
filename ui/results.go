package ui

import "fmt"

func (a *App) LoadResults() error {
	query := fmt.Sprintf("SELECT * FROM %q LIMIT 100", a.selectedTable)

	rows, err := a.db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	if err := populateTable(a.results, rows); err != nil {
		return err
	}

	a.results.ScrollToBeginning()

	return nil
}
