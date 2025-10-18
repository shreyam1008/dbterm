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

// bufferedRow holds one row of cell text+color pairs read from sql.Rows.
type bufferedRow struct {
	cells []bufferedCell
}

type bufferedCell struct {
	text  string
	color tcell.Color
}

// populateTable fills the tview.Table with rows from a sql.Rows result set.
// Returns the number of data rows (excluding header).
func populateTable(results *tview.Table, rows *sql.Rows) (int, error) {
	rowCount, _, err := populateTableWithLimit(results, rows, -1)
	return rowCount, err
}

// populateTableWithLimit reads rows into a buffer first, then populates the table.
// This prevents data loss: the existing table is only cleared after all rows
// have been successfully read. maxRows <= 0 means unlimited.
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

	// ── Buffer all rows in memory before touching the table ──
	values := make([]any, len(columnNames))
	valuePtrs := make([]any, len(columnNames))
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	var buf []bufferedRow
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

		row := bufferedRow{cells: make([]bufferedCell, len(columnNames))}
		for colIndex, val := range values {
			cellValue, cellColor := formatCellValue(val)
			row.cells[colIndex] = bufferedCell{text: cellValue, color: cellColor}
		}
		buf = append(buf, row)
		rowIndex++
	}

	if !truncated {
		if err := rows.Err(); err != nil {
			return 0, false, fmt.Errorf("result iteration error: %w", err)
		}
	}

	// ── All rows read successfully — now safe to clear and populate ──
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

	for r, bRow := range buf {
		for c, bCell := range bRow.cells {
			expansion := 0
			if !hasMultipleColumns || c > 0 {
				expansion = 1
			}

			cell := tview.NewTableCell(bCell.text).
				SetTextColor(bCell.color).
				SetExpansion(expansion)
			if compactFirstCol && c == 0 {
				cell.SetMaxWidth(18)
			}
			results.SetCell(r+1, c, cell)
		}
	}

	// Empty result set
	if len(buf) == 0 {
		results.SetCell(1, 0, &tview.TableCell{
			Text:  iconInfo + " No rows returned",
			Color: overlay0,
		})
	}

	return len(buf), truncated, nil
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
