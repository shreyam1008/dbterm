package ui

import (
	"fmt"
	"time"
)

func (a *App) LoadResults() error {
	// Use backtick quoting for MySQL, double-quote for PG, and plain for SQLite
	var query string
	switch a.dbType {
	case "mysql":
		query = fmt.Sprintf("SELECT * FROM `%s` LIMIT 100", a.selectedTable)
	case "sqlite":
		query = fmt.Sprintf("SELECT * FROM \"%s\" LIMIT 100", a.selectedTable)
	default:
		query = fmt.Sprintf("SELECT * FROM %q LIMIT 100", a.selectedTable)
	}

	a.queryStart = time.Now()

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
