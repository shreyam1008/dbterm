package ui

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const (
	maxCellPreviewRunes = 100
	maxBinaryPreviewLen = 100
)

// populateTable fills the tview.Table with rows from a sql.Rows result set.
// Returns the number of data rows (excluding header).
func populateTable(results *tview.Table, rows *sql.Rows) (int, error) {
	rowCount, _, err := populateTableWithLimit(results, rows, -1)
	return rowCount, err
}

// populateTableWithLimit streams rows directly into the table.
// maxRows <= 0 means no explicit row cap.
func populateTableWithLimit(results *tview.Table, rows *sql.Rows, maxRows int) (int, bool, error) {
	columnNames, err := rows.Columns()
	if err != nil {
		return 0, false, fmt.Errorf("could not read columns: %w", err)
	}

	if len(columnNames) == 0 {
		results.Clear()
		results.SetCell(0, 0, &tview.TableCell{
			Text:  iconInfo + " No columns returned",
			Color: overlay0,
		})
		return 0, false, nil
	}

	results.Clear()

	hasMultipleColumns := len(columnNames) > 1
	compactFirstCol := hasMultipleColumns && isLikelyCompactColumn(columnNames[0])
	for i, name := range columnNames {
		expansion := 0
		if !hasMultipleColumns || i > 0 {
			expansion = 1
		}

		cell := tview.NewTableCell(strings.ToUpper(name)).
			SetTextColor(peach).
			SetSelectable(false).
			SetBackgroundColor(mantle).
			SetExpansion(expansion)

		if compactFirstCol && i == 0 {
			cell.SetMaxWidth(18)
		}
		results.SetCell(0, i, cell)
	}

	values := make([]any, len(columnNames))
	valuePtrs := make([]any, len(columnNames))
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	truncated := false
	rowIndex := 0
	for rows.Next() {
		if maxRows > 0 && rowIndex >= maxRows {
			truncated = true
			break
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return 0, false, fmt.Errorf("row %d scan error: %w", rowIndex+1, err)
		}

		for c, val := range values {
			cellValue, cellColor := formatCellValue(val)
			expansion := 0
			if !hasMultipleColumns || c > 0 {
				expansion = 1
			}

			cell := tview.NewTableCell(cellValue).
				SetTextColor(cellColor).
				SetExpansion(expansion)
			if compactFirstCol && c == 0 {
				cell.SetMaxWidth(18)
			}
			results.SetCell(rowIndex+1, c, cell)
		}
		rowIndex++
	}

	if !truncated {
		if err := rows.Err(); err != nil {
			return 0, false, fmt.Errorf("result iteration error: %w", err)
		}
	}

	// Empty result set
	if rowIndex == 0 {
		results.SetCell(1, 0, &tview.TableCell{
			Text:  iconInfo + " No rows returned",
			Color: overlay0,
		})
	}

	return rowIndex, truncated, nil
}

// formatCellValue converts a database value to a display string and color
func formatCellValue(val any) (string, tcell.Color) {
	if val == nil {
		return "NULL", overlay0
	}

	switch v := val.(type) {
	case []byte:
		if len(v) > maxBinaryPreviewLen {
			return string(v[:maxBinaryPreviewLen-3]) + "...", text
		}
		return string(v), text
	case string:
		return truncateForDisplay(v, maxCellPreviewRunes), text
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
		return truncateForDisplay(fmt.Sprintf("%v", v), maxCellPreviewRunes), text
	}
}

func truncateForDisplay(value string, maxRunes int) string {
	if maxRunes <= 0 || value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}

	runes := []rune(value)
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

func isLikelyCompactColumn(columnName string) bool {
	name := strings.ToLower(strings.TrimSpace(columnName))
	return name == "id" || strings.HasSuffix(name, "_id")
}
