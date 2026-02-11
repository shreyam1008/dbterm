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
	results.Clear()

	columnNames, err := rows.Columns()
	if err != nil {
		return 0, fmt.Errorf("could not read columns: %w", err)
	}

	if len(columnNames) == 0 {
		results.SetCell(0, 0, &tview.TableCell{
			Text:  iconInfo + " No columns returned",
			Color: overlay0,
		})
		return 0, nil
	}

	// Header row with column names
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

		// Keep the first column compact (often ID/index), so data columns can stretch.
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

	rowIndex := 1
	for rows.Next() {
		if err := rows.Scan(valuePtrs...); err != nil {
			return rowIndex - 1, fmt.Errorf("row %d scan error: %w", rowIndex, err)
		}

		for colIndex, val := range values {
			cellValue, cellColor := formatCellValue(val)
			expansion := 0
			if !hasMultipleColumns || colIndex > 0 {
				expansion = 1
			}

			cell := tview.NewTableCell(cellValue).
				SetTextColor(cellColor).
				SetExpansion(expansion)
			if compactFirstCol && colIndex == 0 {
				cell.SetMaxWidth(18)
			}
			results.SetCell(rowIndex, colIndex, cell)
		}
		rowIndex++
	}

	if err := rows.Err(); err != nil {
		return rowIndex - 1, fmt.Errorf("result iteration error: %w", err)
	}

	// Empty result set
	if rowIndex == 1 {
		results.SetCell(1, 0, &tview.TableCell{
			Text:  iconInfo + " No rows returned",
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
