package ui

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// populateTable fills the tview.Table with rows from a sql.Rows result set.
// Returns the number of data rows (excluding header).
func populateTable(results *tview.Table, rows *sql.Rows) (int, error) {
	results.Clear()

	columnNames, err := rows.Columns()
	if err != nil {
		return 0, fmt.Errorf("could not read columns: %w", err)
	}

	if len(columnNames) == 0 {
		results.SetCell(0, 0, &tview.TableCell{
			Text:  "No columns returned",
			Color: overlay0,
		})
		return 0, nil
	}

	// Header row with column names
	for i, name := range columnNames {
		results.SetCell(0, i, &tview.TableCell{
			Text:            strings.ToUpper(name),
			Color:           peach,
			NotSelectable:   true,
			BackgroundColor: mantle,
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
			return rowIndex - 1, fmt.Errorf("row %d scan error: %w", rowIndex, err)
		}

		for colIndex, val := range values {
			cellValue, cellColor := formatCellValue(val)
			results.SetCell(rowIndex, colIndex, &tview.TableCell{
				Text:  cellValue,
				Color: cellColor,
			})
		}
		rowIndex++
	}

	if err := rows.Err(); err != nil {
		return rowIndex - 1, fmt.Errorf("result iteration error: %w", err)
	}

	// Empty result set
	if rowIndex == 1 {
		results.SetCell(1, 0, &tview.TableCell{
			Text:  "No rows returned",
			Color: overlay0,
		})
	}

	return rowIndex - 1, nil
}

// formatCellValue converts a database value to a display string and color
func formatCellValue(val any) (string, tcell.Color) {
	if val == nil {
		return "NULL", overlay0
	}

	switch v := val.(type) {
	case []byte:
		s := string(v)
		if len(s) > 100 {
			return s[:97] + "...", text
		}
		return s, text
	case bool:
		if v {
			return "true", green
		}
		return "false", red
	case int64:
		return fmt.Sprintf("%d", v), teal
	case float64:
		return fmt.Sprintf("%.4g", v), teal
	default:
		s := fmt.Sprintf("%v", v)
		if len(s) > 100 {
			return s[:97] + "...", text
		}
		return s, text
	}
}
