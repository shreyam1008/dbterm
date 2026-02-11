package ui

import (
	"fmt"
	"time"
)

// LoadResults loads data from the selected table into the results view
func (a *App) LoadResults() error {
	if a.selectedTable == "" {
		return nil
	}

	if a.db == nil {
		return fmt.Errorf("not connected")
	}

	// DB-specific quoting for identifiers
	var query string
	switch a.dbType {
	case "mysql":
		query = fmt.Sprintf("SELECT * FROM `%s` LIMIT 100", a.selectedTable)
	default:
		query = fmt.Sprintf(`SELECT * FROM "%s" LIMIT 100`, a.selectedTable)
	}

	a.queryStart = time.Now()

	rows, err := a.db.Query(query)
	if err != nil {
		a.results.SetTitle(" Results — [red]error[-] ")
		return err
	}
	defer rows.Close()

	rowCount, err := populateTable(a.results, rows)
	if err != nil {
		return err
	}

	elapsed := time.Since(a.queryStart)
	a.results.SetTitle(fmt.Sprintf(" [yellow]%s[-] — [green]%d rows[-] in [teal]%s[-] ",
		a.selectedTable, rowCount, formatDuration(elapsed)))
	a.results.ScrollToBeginning()
	a.updateStatusBar("", rowCount)

	return nil
}
