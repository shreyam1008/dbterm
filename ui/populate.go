package ui

import (
	"database/sql"
	"fmt"

	"github.com/rivo/tview"
)

func populateTable(results *tview.Table, rows *sql.Rows) error {
	results.Clear()

	columnNames, err := rows.Columns()
	if err != nil {
		return err
	}

	for i, columnName := range columnNames {
		results.SetCell(0, i, &tview.TableCell{
			Text:  columnName,
			Color: peach,
		})
	}

	values := make([]any, len(columnNames))
	valuePtrs := make([]any, len(columnNames))

	for i := range values {
		valuePtrs[i] = &values[i]
	}

	rowIndex := 1
	for rows.Next() {
		if err := rows.Scan(valuePtrs...); err != nil {
			return err
		}

		for colIndex, val := range values {
			var cellValue string
			if val == nil {
				cellValue = "NULL"
			} else {
				cellValue = fmt.Sprintf("%v", val)
			}
			results.SetCell(rowIndex, colIndex, tview.NewTableCell(cellValue))
		}
		rowIndex++
	}

	return rows.Err()
}
