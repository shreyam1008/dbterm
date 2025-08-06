package ui

import (
	"fmt"
	"strings"
)

func (a *App) ExecuteQuery(query string) {
	if query == "" {
		return
	}

	queryTrim := strings.TrimSpace(strings.ToUpper(query))
	if strings.HasPrefix(queryTrim, "SELECT") {
		rows, err := a.db.Query(query)
		if err != nil {
			a.ShowAlert(fmt.Sprintf("Error executing query: %v", err), "main")
			return
		}
		defer rows.Close()

		if err := populateTable(a.results, rows); err != nil {
			a.ShowAlert(fmt.Sprintf("Error processing results: %v", err), "main")
			return
		}

		a.results.ScrollToBeginning()
	} else {
		res, err := a.db.Exec(query)
		if err != nil {
			a.ShowAlert(fmt.Sprintf("Error executing query: %v", err), "main")
			return
		}

		rowsAffected, _ := res.RowsAffected()
		a.ShowAlert(fmt.Sprintf("Query executed successfully. Rows affected: %d", rowsAffected), "main")
		a.LoadResults()
		a.LoadTables()
	}
}
