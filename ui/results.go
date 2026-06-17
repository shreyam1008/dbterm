package ui

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

const (
	defaultTablePreviewLimit   = 100
	adaptiveTablePreviewLimit  = -1
	resultSelectionTitlePrefix = " [#f9e2af](selected "

	maxResultRows           = 1000
	maxResultCells          = 12000
	maxEstimatedResultBytes = 2 * 1024 * 1024
	estimatedCellOverhead   = 96
)

var tablePreviewSteps = []int{50, 100, 250, 500, 1000}

type resultRowSelectionRef string

const resultRowRefSelected resultRowSelectionRef = "selected"

type resultSelectionState struct {
	row             int
	col             int
	offsetRow       int
	offsetCol       int
	hasDataRow      bool
	selectedRowText []string
}

// LoadResults loads data from the selected table into the results view
// using OFFSET/LIMIT pagination to bound memory usage.
func (a *App) LoadResults() error {
	if a.selectedTable == "" {
		return nil
	}

	if a.db == nil {
		return fmt.Errorf("not connected")
	}

	// Reset per-column width overrides when loading a new table
	a.clearColumnOverrides()

	// DB-specific quoting for identifiers
	quotedTable := quoteIdentifier(a.dbType, a.selectedTable)
	requestedLimit := a.effectiveResultLimit()
	queryLimit := requestedLimit
	if queryLimit == adaptiveTablePreviewLimit || queryLimit > maxResultRows {
		queryLimit = maxResultRows
	}
	query := fmt.Sprintf("SELECT * FROM %s", quotedTable)
	if queryLimit > 0 {
		query = fmt.Sprintf("%s LIMIT %d OFFSET %d", query, queryLimit, a.pageOffset)
	}

	a.queryStart = time.Now()

	selection := a.captureResultSelection()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	rows, err := a.db.QueryContext(ctx, query)
	if err != nil {
		a.results.SetTitle(fmt.Sprintf(" %s Results — [red]%s error[-] ", iconResults, iconFail))
		return err
	}
	defer rows.Close()

	columnNames, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("could not read columns: %w", err)
	}

	pageLimit := resolvedResultLimit(requestedLimit, len(columnNames))
	a.pageSize = pageLimit

	rowCount, _, err := populateTableWithLimit(a.results, rows, pageLimit)
	if err != nil {
		return err
	}

	// Re-apply sort if active
	if a.sortColumn != -1 {
		a.applySort()
	}

	// Re-apply zoom / column width overrides
	a.applyColumnWidths()

	a.restoreResultSelection(selection, rowCount)

	// Fetch total row count asynchronously for pagination display
	go a.fetchTotalRowCount(quotedTable)

	elapsed := time.Since(a.queryStart)
	a.results.SetTitle(a.paginatedResultTitle(rowCount, elapsed))
	a.updateStatusBar("", rowCount)

	return nil
}

// fetchTotalRowCount queries COUNT(*) for the selected table and updates the title.
func (a *App) fetchTotalRowCount(quotedTable string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var total int
	err := a.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", quotedTable)).Scan(&total)
	if err != nil {
		return
	}

	a.app.QueueUpdateDraw(func() {
		a.totalRowCount = total
		a.results.SetTitle(a.paginatedResultTitle(a.currentResultRowCount(), time.Since(a.queryStart)))
		a.updateStatusBar("", a.currentResultRowCount())
	})
}

// paginatedResultTitle builds the results panel title with page info.
func (a *App) paginatedResultTitle(rowCount int, elapsed time.Duration) string {
	limit := a.currentPageLimit()
	base := fmt.Sprintf(" %s [yellow]%s[-] — [green]%d rows[-] in [teal]%s[-]",
		iconResults, a.selectedTable, rowCount, formatDuration(elapsed))

	if limit > 0 && a.totalRowCount >= 0 {
		page := (a.pageOffset / limit) + 1
		totalPages := (a.totalRowCount + limit - 1) / limit
		if totalPages < 1 {
			totalPages = 1
		}
		return fmt.Sprintf("%s [#a6adc8](page %d/%d, %d total)[-] ", base, page, totalPages, a.totalRowCount)
	}
	if limit > 0 && a.pageOffset > 0 {
		page := (a.pageOffset / limit) + 1
		return fmt.Sprintf("%s [#a6adc8](page %d)[-] ", base, page)
	}
	return base + " "
}

// resetPagination resets page offset and cached total (call when switching tables).
func (a *App) resetPagination() {
	a.pageOffset = 0
	a.pageSize = 0
	a.totalRowCount = -1
}

// nextPage advances to the next page of results.
func (a *App) nextPage() {
	limit := a.currentPageLimit()
	if limit <= 0 {
		return
	}
	// Don't advance past the last page
	if a.totalRowCount >= 0 && a.pageOffset+limit >= a.totalRowCount {
		return
	}
	a.pageOffset += limit
	if err := a.LoadResults(); err != nil {
		a.pageOffset -= limit
		a.showErrorStatus("Could not load next page", err.Error(), a.currentResultRowCount())
	}
}

// prevPage goes back one page of results.
func (a *App) prevPage() {
	limit := a.currentPageLimit()
	if limit <= 0 || a.pageOffset <= 0 {
		return
	}
	a.pageOffset -= limit
	if a.pageOffset < 0 {
		a.pageOffset = 0
	}
	if err := a.LoadResults(); err != nil {
		a.showErrorStatus("Could not load previous page", err.Error(), a.currentResultRowCount())
	}
}

// firstPage jumps to the first page.
func (a *App) firstPage() {
	if a.pageOffset == 0 {
		return
	}
	a.pageOffset = 0
	if err := a.LoadResults(); err != nil {
		a.showErrorStatus("Could not load first page", err.Error(), a.currentResultRowCount())
	}
}

// lastPage jumps to the last page.
func (a *App) lastPage() {
	limit := a.currentPageLimit()
	if limit <= 0 || a.totalRowCount < 0 {
		return
	}
	lastOffset := ((a.totalRowCount - 1) / limit) * limit
	if lastOffset < 0 {
		lastOffset = 0
	}
	if a.pageOffset == lastOffset {
		return
	}
	a.pageOffset = lastOffset
	if err := a.LoadResults(); err != nil {
		a.showErrorStatus("Could not load last page", err.Error(), a.currentResultRowCount())
	}
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

func resolvedResultLimit(requestedLimit, columnCount int) int {
	if columnCount < 1 {
		columnCount = 1
	}

	limit := maxResultRows
	if requestedLimit > 0 && requestedLimit < limit {
		limit = requestedLimit
	}

	rowsByCells := maxResultCells / columnCount
	if rowsByCells > 0 && rowsByCells < limit {
		limit = rowsByCells
	}

	perCellEstimate := maxCellPreviewRunes + estimatedCellOverhead
	rowsByMemory := maxEstimatedResultBytes / max(1, columnCount*perCellEstimate)
	if rowsByMemory > 0 && rowsByMemory < limit {
		limit = rowsByMemory
	}

	if limit < 1 {
		return 1
	}
	return limit
}

func quoteIdentifier(dbType config.DBType, identifier string) string {
	parts := strings.Split(identifier, ".")
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		switch dbType {
		case config.MySQL:
			quoted = append(quoted, "`"+strings.ReplaceAll(part, "`", "``")+"`")
		default:
			quoted = append(quoted, `"`+strings.ReplaceAll(part, `"`, `""`)+`"`)
		}
	}
	if len(quoted) == 0 {
		return identifier
	}
	return strings.Join(quoted, ".")
}

func (a *App) hasResultDataRows() bool {
	return a.results != nil && a.results.GetColumnCount() > 0 && a.currentResultRowCount() > 0
}

func (a *App) isSelectableResultRow(row int) bool {
	return a.hasResultDataRows() && row > 0 && row < a.results.GetRowCount()
}

func (a *App) resultRowIsSelected(row int) bool {
	if !a.isSelectableResultRow(row) {
		return false
	}

	cell := a.results.GetCell(row, 0)
	if cell == nil {
		return false
	}

	ref, ok := cell.GetReference().(resultRowSelectionRef)
	return ok && ref == resultRowRefSelected
}

func (a *App) setResultRowSelected(row int, selected bool) bool {
	if !a.isSelectableResultRow(row) {
		return false
	}

	currentlySelected := a.resultRowIsSelected(row)
	if currentlySelected == selected {
		return false
	}

	colCount := a.results.GetColumnCount()
	var anchor *tview.TableCell
	for col := 0; col < colCount; col++ {
		cell := a.results.GetCell(row, col)
		if cell == nil {
			continue
		}
		if anchor == nil {
			anchor = cell
		}

		if selected {
			cell.SetBackgroundColor(surface0)
		} else {
			cell.SetBackgroundColor(tcell.ColorDefault)
			cell.SetTransparency(true)
		}
	}

	if anchor != nil {
		if selected {
			anchor.SetReference(resultRowRefSelected)
		} else {
			anchor.SetReference(nil)
		}
	}

	return true
}

func (a *App) toggleCurrentResultRowSelection() {
	if a.results == nil {
		return
	}
	row, _ := a.results.GetSelection()
	if !a.isSelectableResultRow(row) {
		return
	}

	_ = a.setResultRowSelected(row, !a.resultRowIsSelected(row))
	a.refreshResultSelectionIndicators()
}

func (a *App) selectAllResultRows() {
	if !a.hasResultDataRows() {
		return
	}

	for row := 1; row < a.results.GetRowCount(); row++ {
		_ = a.setResultRowSelected(row, true)
	}
	a.refreshResultSelectionIndicators()
}

func (a *App) clearResultRowSelection() {
	if a.results == nil {
		return
	}

	for row := 1; row < a.results.GetRowCount(); row++ {
		_ = a.setResultRowSelected(row, false)
	}
	a.refreshResultSelectionIndicators()
}

func (a *App) selectedResultRowCount() int {
	if !a.hasResultDataRows() {
		return 0
	}

	count := 0
	for row := 1; row < a.results.GetRowCount(); row++ {
		if a.resultRowIsSelected(row) {
			count++
		}
	}
	return count
}

func (a *App) selectedResultRows() []int {
	if !a.hasResultDataRows() {
		return nil
	}

	selectedRows := make([]int, 0, a.results.GetRowCount()-1)
	for row := 1; row < a.results.GetRowCount(); row++ {
		if a.resultRowIsSelected(row) {
			selectedRows = append(selectedRows, row)
		}
	}
	return selectedRows
}

func (a *App) refreshResultSelectionIndicators() {
	if a.results == nil {
		return
	}

	selectedCount := a.selectedResultRowCount()
	baseTitle := stripResultSelectionSuffix(a.results.GetTitle())
	if selectedCount > 0 {
		a.results.SetTitle(fmt.Sprintf("%s%s%d)[-] ", strings.TrimRight(baseTitle, " "), resultSelectionTitlePrefix, selectedCount))
	} else {
		a.results.SetTitle(baseTitle)
	}

	a.updateStatusBar("", a.currentResultRowCount())
}

func stripResultSelectionSuffix(title string) string {
	if idx := strings.Index(title, resultSelectionTitlePrefix); idx >= 0 {
		return title[:idx]
	}
	return title
}

// exportCurrentResultsToCSV writes the currently visible results table to CSV.
func (a *App) exportCurrentResultsToCSV() {
	if a.currentResultRowCount() == 0 {
		a.showStatusMessage(statusInfo, "No result rows to export. Run a query or load a table first.", a.currentResultRowCount())
		return
	}

	path, rows, err := a.writeCurrentResultsToCSV()
	if err != nil {
		a.showErrorStatus("CSV export failed", err.Error(), a.currentResultRowCount())
		return
	}

	a.showStatusMessage(statusSuccess, fmt.Sprintf("CSV export complete: %d rows -> %s", rows, path), a.currentResultRowCount())
}

func (a *App) writeCurrentResultsToCSV() (string, int, error) {
	fileName := fmt.Sprintf("dbterm_results_%s.csv", time.Now().Format("20060102_150405"))

	candidatePaths := make([]string, 0, 2)
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		candidatePaths = append(candidatePaths, filepath.Join(cwd, fileName))
	}

	tmpPath := filepath.Join(os.TempDir(), fileName)
	if len(candidatePaths) == 0 || candidatePaths[0] != tmpPath {
		candidatePaths = append(candidatePaths, tmpPath)
	}

	var lastErr error
	for _, path := range candidatePaths {
		rows, err := a.writeResultsCSVToPath(path)
		if err == nil {
			return path, rows, nil
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("could not determine a writable export path")
	}
	return "", 0, lastErr
}

func (a *App) writeResultsCSVToPath(path string) (int, error) {
	rowCount := a.results.GetRowCount()
	colCount := a.results.GetColumnCount()
	if rowCount <= 1 || colCount == 0 || a.currentResultRowCount() == 0 {
		return 0, fmt.Errorf("no result data available")
	}

	rowsToExport := a.selectedResultRows()
	if len(rowsToExport) == 0 {
		rowsToExport = make([]int, 0, rowCount-1)
		for row := 1; row < rowCount; row++ {
			rowsToExport = append(rowsToExport, row)
		}
	}

	file, err := os.Create(path)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", path, err)
	}

	writer := csv.NewWriter(file)
	if err := writer.Write(a.resultCSVRecord(0, colCount, true)); err != nil {
		_ = file.Close()
		return 0, fmt.Errorf("write csv header: %w", err)
	}

	for i, row := range rowsToExport {
		if err := writer.Write(a.resultCSVRecord(row, colCount, false)); err != nil {
			_ = file.Close()
			return 0, fmt.Errorf("write csv row %d: %w", i+2, err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		_ = file.Close()
		return 0, fmt.Errorf("flush csv writer: %w", err)
	}

	if err := file.Close(); err != nil {
		return 0, fmt.Errorf("close %s: %w", path, err)
	}

	return len(rowsToExport), nil
}

func (a *App) resultCSVRecord(row, colCount int, header bool) []string {
	record := make([]string, colCount)
	for col := 0; col < colCount; col++ {
		cell := a.results.GetCell(row, col)
		if cell == nil {
			continue
		}

		text := cell.Text
		if header {
			// Header cells can include sort arrows in the UI; export clean column names.
			text = strings.TrimSuffix(strings.TrimSuffix(text, " ▲"), " ▼")
		}
		record[col] = text
	}
	return record
}
