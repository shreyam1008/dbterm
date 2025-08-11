package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

const tablePreviewLimit = 100

type resultSelectionState struct {
	row             int
	col             int
	offsetRow       int
	offsetCol       int
	hasDataRow      bool
	selectedRowText []string
}

// LoadResults loads data from the selected table into the results view
func (a *App) LoadResults() error {
	if a.selectedTable == "" {
		return nil
	}

	if a.db == nil {
		return fmt.Errorf("not connected")
	}

	// DB-specific quoting for identifiers
	quotedTable := quoteIdentifier(a.dbType, a.selectedTable)
	query := fmt.Sprintf("SELECT * FROM %s LIMIT %d", quotedTable, tablePreviewLimit)

	a.queryStart = time.Now()

	selection := a.captureResultSelection()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	rows, err := a.db.QueryContext(ctx, query)
	if err != nil {
		a.results.SetTitle(" Results — [red]error[-] ")
		return err
	}
	defer rows.Close()

	rowCount, err := populateTable(a.results, rows)
	if err != nil {
		return err
	}

	// Re-apply sort if active
	if a.sortColumn != -1 {
		a.applySort()
	}

	a.restoreResultSelection(selection, rowCount)

	elapsed := time.Since(a.queryStart)
	a.results.SetTitle(fmt.Sprintf(" [yellow]%s[-] — [green]%d rows[-] in [teal]%s[-] ",
		a.selectedTable, rowCount, formatDuration(elapsed)))

	a.updateStatusBar("", rowCount)

	return nil
}

func (a *App) captureResultSelection() resultSelectionState {
	row, col := a.results.GetSelection()
	offsetRow, offsetCol := a.results.GetOffset()
	state := resultSelectionState{
		row:       row,
		col:       col,
		offsetRow: offsetRow,
		offsetCol: offsetCol,
	}

	rowCount := a.results.GetRowCount()
	colCount := a.results.GetColumnCount()
	if row > 0 && row < rowCount && colCount > 0 {
		state.hasDataRow = true
		state.selectedRowText = tableRowSignature(a.results, row, colCount)
	}

	return state
}

func (a *App) restoreResultSelection(state resultSelectionState, rowCount int) {
	colCount := a.results.GetColumnCount()
	if rowCount <= 0 || colCount == 0 {
		a.results.Select(0, 0)
		a.results.SetOffset(0, 0)
		return
	}

	targetRow := 1
	targetCol := clamp(state.col, 0, colCount-1)

	if state.hasDataRow {
		targetRow = clamp(state.row, 1, rowCount)
		if len(state.selectedRowText) > 0 {
			if matched := findMatchingRow(a.results, state.selectedRowText, rowCount, colCount); matched > 0 {
				targetRow = matched
			}
		}
	}

	a.results.Select(targetRow, targetCol)

	maxOffsetRow := rowCount - 1
	if maxOffsetRow < 0 {
		maxOffsetRow = 0
	}
	offsetRow := clamp(state.offsetRow, 0, maxOffsetRow)
	offsetCol := clamp(state.offsetCol, 0, max(0, colCount-1))
	a.results.SetOffset(offsetRow, offsetCol)
}

func tableRowSignature(table *tview.Table, row, colCount int) []string {
	signature := make([]string, colCount)
	for c := 0; c < colCount; c++ {
		cell := table.GetCell(row, c)
		if cell == nil {
			signature[c] = ""
			continue
		}
		signature[c] = cell.Text
	}
	return signature
}

func findMatchingRow(table *tview.Table, signature []string, rowCount, colCount int) int {
	if len(signature) != colCount {
		return 0
	}
	for row := 1; row <= rowCount; row++ {
		current := tableRowSignature(table, row, colCount)
		match := true
		for c := 0; c < colCount; c++ {
			if current[c] != signature[c] {
				match = false
				break
			}
		}
		if match {
			return row
		}
	}
	return 0
}

func quoteIdentifier(dbType config.DBType, identifier string) string {
	switch dbType {
	case config.MySQL:
		return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
	default:
		return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
	}
}
