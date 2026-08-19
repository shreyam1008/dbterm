package ui

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
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
	databaseTypes := resultDatabaseTypes(rows, len(columnNames))

	hasMultipleColumns := len(columnNames) > 1
	compactFirstCol := hasMultipleColumns && isLikelyCompactColumn(columnNames[0])
	for i, name := range columnNames {
		expansion := 0
		if !hasMultipleColumns || i > 0 {
			expansion = 1
		}

		cell := tview.NewTableCell(tview.Escape(strings.ToUpper(name))).
			SetReference(name).
			SetTextColor(peach).
			SetSelectable(true).
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
			cellValue, cellColor := formatCellValueForDatabaseType(val, databaseTypes[c])
			expansion := 0
			if !hasMultipleColumns || c > 0 {
				expansion = 1
			}

			cell := tview.NewTableCell(tview.Escape(cellValue)).
				SetTextColor(cellColor).
				SetReference(newResultCellReferenceForDatabaseType(val, cellValue, databaseTypes[c])).
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

func resultDatabaseTypes(rows *sql.Rows, columnCount int) []string {
	databaseTypes := make([]string, columnCount)
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return databaseTypes
	}
	for index := 0; index < len(columnTypes) && index < len(databaseTypes); index++ {
		if columnTypes[index] != nil {
			databaseTypes[index] = columnTypes[index].DatabaseTypeName()
		}
	}
	return databaseTypes
}

// newResultCellReference keeps rendering concerns out of the underlying cell
// value. In particular, SQL NULL is distinct from the string "NULL", and
// binary values are copied because some database drivers reuse scan buffers.
func newResultCellReference(rawValue any, displayValue string) resultCellReference {
	return newResultCellReferenceForDatabaseType(rawValue, displayValue, "")
}

func newResultCellReferenceForDatabaseType(rawValue any, displayValue, databaseType string) resultCellReference {
	cloned := cloneResultRawValue(rawValue)
	return resultCellReference{
		value:        fullCellValueForDatabaseType(cloned, databaseType),
		rawValue:     cloned,
		isNull:       rawValue == nil,
		databaseType: databaseType,
		displayValue: displayValue,
		truncated:    resultCellDisplayIsTruncated(rawValue, databaseType),
	}
}

func cloneResultRawValue(value any) any {
	if bytes, ok := value.([]byte); ok {
		cloned := make([]byte, len(bytes))
		copy(cloned, bytes)
		return cloned
	}
	return value
}

func resultCellDisplayIsTruncated(value any, databaseType string) bool {
	switch typed := value.(type) {
	case []byte:
		if databaseByteValueIsText(databaseType) {
			return utf8.RuneCount(typed) > maxCellPreviewRunes
		}
		return len(typed) > maxBinaryPreviewLen
	case string:
		return utf8.RuneCountInString(typed) > maxCellPreviewRunes
	default:
		return false
	}
}

func fullCellValue(val any) string {
	return fullCellValueForDatabaseType(val, "")
}

func fullCellValueForDatabaseType(val any, databaseType string) string {
	if val == nil {
		return "NULL"
	}
	if value, ok := val.(time.Time); ok {
		if formatted, recognized := formatDatabaseTime(value, databaseType); recognized {
			return formatted
		}
	}
	switch value := val.(type) {
	case []byte:
		return string(value)
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", value)
	}
}

func formatDatabaseTime(value time.Time, databaseType string) (string, bool) {
	switch normalizedDatabaseType(databaseType) {
	case "DATE":
		return formatDatabaseDate(value), true
	case "TIME", "TIME WITHOUT TIME ZONE":
		return formatDatabaseTimeOfDay(value, false), true
	case "TIMETZ", "TIME WITH TIME ZONE":
		return formatDatabaseTimeOfDay(value, true), true
	case "TIMESTAMP", "TIMESTAMP WITHOUT TIME ZONE", "DATETIME":
		return formatDatabaseTimestamp(value, false), true
	case "TIMESTAMPTZ", "TIMESTAMP WITH TIME ZONE":
		return formatDatabaseTimestamp(value, true), true
	default:
		return "", false
	}
}

func normalizedDatabaseType(databaseType string) string {
	return strings.ToUpper(strings.TrimSpace(databaseType))
}

func databaseByteValueIsText(databaseType string) bool {
	databaseType = normalizedDatabaseType(databaseType)
	if databaseType == "" {
		return false
	}
	switch databaseType {
	case "BYTEA", "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "BIT", "GEOMETRY", "VECTOR":
		return false
	default:
		return true
	}
}

func formatDatabaseDate(value time.Time) string {
	date, bc := formatDatabaseDatePart(value)
	if bc {
		return date + " BC"
	}
	return date
}

func formatDatabaseTimestamp(value time.Time, withTimezone bool) string {
	date, bc := formatDatabaseDatePart(value)
	formatted := date + " " + value.Format("15:04:05.999999999")
	if withTimezone {
		formatted += formatDatabaseTimezoneOffset(value)
	}
	if bc {
		formatted += " BC"
	}
	return formatted
}

func formatDatabaseDatePart(value time.Time) (string, bool) {
	year := value.Year()
	bc := year <= 0
	if bc {
		year = 1 - year
	}
	return fmt.Sprintf("%04d-%02d-%02d", year, int(value.Month()), value.Day()), bc
}

func formatDatabaseTimeOfDay(value time.Time, withTimezone bool) string {
	// lib/pq represents PostgreSQL's special 24:00:00 value as midnight on
	// day two of its zero-date time.Time container.
	hour := value.Hour()
	if value.Year() == 0 && value.Month() == time.January && value.Day() == 2 && hour == 0 {
		hour = 24
	}
	formatted := fmt.Sprintf("%02d:%02d:%02d", hour, value.Minute(), value.Second())
	if nanoseconds := value.Nanosecond(); nanoseconds != 0 {
		fraction := strings.TrimRight(fmt.Sprintf("%09d", nanoseconds), "0")
		formatted += "." + fraction
	}
	if withTimezone {
		formatted += formatDatabaseTimezoneOffset(value)
	}
	return formatted
}

func formatDatabaseTimezoneOffset(value time.Time) string {
	_, offsetSeconds := value.Zone()
	sign := "+"
	if offsetSeconds < 0 {
		sign = "-"
		offsetSeconds = -offsetSeconds
	}
	hours := offsetSeconds / 3600
	minutes := (offsetSeconds % 3600) / 60
	seconds := offsetSeconds % 60
	formatted := fmt.Sprintf("%s%02d:%02d", sign, hours, minutes)
	if seconds != 0 {
		formatted += fmt.Sprintf(":%02d", seconds)
	}
	return formatted
}

// formatCellValue converts a database value to a display string and color
func formatCellValue(val any) (string, tcell.Color) {
	return formatCellValueForDatabaseType(val, "")
}

func formatCellValueForDatabaseType(val any, databaseType string) (string, tcell.Color) {
	if val == nil {
		return "NULL", overlay0
	}

	switch v := val.(type) {
	case []byte:
		if databaseByteValueIsText(databaseType) {
			return truncateForDisplay(string(v), maxCellPreviewRunes), text
		}
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
		return truncateForDisplay(fullCellValueForDatabaseType(v, databaseType), maxCellPreviewRunes), text
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
