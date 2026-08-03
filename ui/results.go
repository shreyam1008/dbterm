package ui

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
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

type resultCellReference struct {
	// value remains the complete, lossless string representation used by
	// clipboard/export callers. Keep it separate from TableCell.Text, which is
	// intentionally shortened and formatted for terminal display.
	value string
	// rawValue and isNull preserve database semantics for typed operations. A
	// nil rawValue alone is not sufficient because legacy references only set
	// value, so isNull is the authoritative SQL NULL marker.
	rawValue     any
	isNull       bool
	displayValue string
	truncated    bool
	rowSelected  bool
}

type resultSelectionState struct {
	row             int
	col             int
	offsetRow       int
	offsetCol       int
	hasDataRow      bool
	selectedRowText []string
}

type tableResultRequest struct {
	db             *sql.DB
	dbType         config.DBType
	selectedTable  string
	quotedTable    string
	query          string
	countQuery     string
	queryArgs      []any
	requestedLimit int
	pageOffset     int
	generation     uint64
	selection      resultSelectionState
	startedAt      time.Time
}

type tableResultSnapshot struct {
	request     *tableResultRequest
	results     *tview.Table
	columnNames []string
	rowCount    int
	pageLimit   int
	elapsed     time.Duration
}

type tableResultAsyncCallbacks struct {
	rollback  func()
	onCancel  func()
	onError   func(error)
	onSuccess func()
}

// LoadResults loads data from the selected table into the results view
// using OFFSET/LIMIT pagination to bound memory usage.
func (a *App) LoadResults() error {
	request, err := a.prepareTableResultRequest()
	if err != nil {
		return err
	}
	if request == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	snapshot, err := fetchTableResultSnapshot(ctx, request)
	if err != nil {
		a.results.SetTitle(fmt.Sprintf(" %s Results — [red]%s error[-] ", iconResults, iconFail))
		return err
	}
	if !a.applyTableResultSnapshot(snapshot) {
		return fmt.Errorf("table results were superseded by a newer request")
	}
	return nil
}

func (a *App) prepareTableResultRequest() (*tableResultRequest, error) {
	if a.selectedTable == "" {
		return nil, nil
	}
	if a.db == nil {
		return nil, fmt.Errorf("not connected")
	}
	if a.results == nil {
		return nil, fmt.Errorf("results view is unavailable")
	}

	selectedTable := a.selectedTable
	dbType := a.dbType
	quotedTable := quoteIdentifier(dbType, selectedTable)
	requestedLimit := a.effectiveResultLimit()
	queryLimit := requestedLimit
	if queryLimit == adaptiveTablePreviewLimit || queryLimit > maxResultRows {
		queryLimit = maxResultRows
	}

	query := fmt.Sprintf("SELECT * FROM %s", quotedTable)
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s", quotedTable)
	queryArgs := []any(nil)
	if filter := a.activeResultFilter(selectedTable); filter != nil {
		clause, filterArgs := resultFilterSQL(dbType, filter)
		query += clause
		countQuery += clause
		queryArgs = append(queryArgs, filterArgs...)
	}
	if sortColumn := a.serverSortColumnName(); sortColumn != "" {
		direction := "ASC"
		if !a.sortAsc {
			direction = "DESC"
		}
		query = fmt.Sprintf("%s ORDER BY %s %s", query, quoteIdentifier(dbType, sortColumn), direction)
	}
	if queryLimit > 0 {
		query = fmt.Sprintf("%s LIMIT %d OFFSET %d", query, queryLimit, a.pageOffset)
	}

	return &tableResultRequest{
		db:             a.db,
		dbType:         dbType,
		selectedTable:  selectedTable,
		quotedTable:    quotedTable,
		query:          query,
		countQuery:     countQuery,
		queryArgs:      queryArgs,
		requestedLimit: requestedLimit,
		pageOffset:     a.pageOffset,
		// Every fetch owns a unique generation so a slower page, sort, or
		// filter request can never overwrite a newer result set.
		generation: a.advanceResultGeneration(),
		selection:  a.captureResultSelection(),
		startedAt:  time.Now(),
	}, nil
}

func fetchTableResultSnapshot(ctx context.Context, request *tableResultRequest) (*tableResultSnapshot, error) {
	if request == nil || request.db == nil {
		return nil, fmt.Errorf("table result request is unavailable")
	}
	rows, err := request.db.QueryContext(ctx, request.query, request.queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columnNames, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("could not read columns: %w", err)
	}
	pageLimit := resolvedResultLimit(request.requestedLimit, len(columnNames))
	results := newResultTable()
	rowCount, _, err := populateTableWithLimit(results, rows, pageLimit)
	if err != nil {
		return nil, err
	}
	return &tableResultSnapshot{
		request:     request,
		results:     results,
		columnNames: columnNames,
		rowCount:    rowCount,
		pageLimit:   pageLimit,
		elapsed:     time.Since(request.startedAt),
	}, nil
}

func (a *App) applyTableResultSnapshot(snapshot *tableResultSnapshot) bool {
	if snapshot == nil || snapshot.request == nil || snapshot.results == nil || a.results == nil {
		return false
	}
	request := snapshot.request
	if !a.tableResultRequestIsCurrent(request) {
		return false
	}

	preserveWidths := resultHeadersMatch(a.results, snapshot.columnNames)
	a.results.Clear()
	for row := 0; row < snapshot.results.GetRowCount(); row++ {
		for col := 0; col < snapshot.results.GetColumnCount(); col++ {
			a.results.SetCell(row, col, snapshot.results.GetCell(row, col))
		}
	}
	a.pageSize = snapshot.pageLimit
	a.tableResultsActive = true
	a.queryStart = request.startedAt
	if a.sortColumn >= len(snapshot.columnNames) {
		a.resetSort()
	} else if a.sortColumn != -1 {
		a.sortMode = "server"
		a.setSortHeaderIndicator()
	}
	if !preserveWidths {
		a.clearColumnOverrides()
	}
	a.applyColumnWidths()
	a.restoreResultSelection(request.selection, snapshot.rowCount)

	countArgs := append([]any(nil), request.queryArgs...)
	go a.fetchTotalRowCount(request.db, request.selectedTable, request.quotedTable, request.dbType, snapshot.pageLimit, request.pageOffset, request.generation, request.countQuery, countArgs)
	a.results.SetTitle(a.paginatedResultTitle(snapshot.rowCount, snapshot.elapsed))
	a.updateStatusBar("", snapshot.rowCount)
	return true
}

func resultHeadersMatch(table *tview.Table, columnNames []string) bool {
	if table == nil || len(columnNames) == 0 || table.GetColumnCount() != len(columnNames) {
		return false
	}
	for column, name := range columnNames {
		cell := table.GetCell(0, column)
		if cell == nil {
			return false
		}
		current := stripSortIndicator(cell.Text)
		if reference, ok := cell.GetReference().(string); ok && strings.TrimSpace(reference) != "" {
			current = reference
		}
		if current != name {
			return false
		}
	}
	return true
}

func (a *App) tableResultRequestIsCurrent(request *tableResultRequest) bool {
	return a != nil && request != nil &&
		a.db == request.db &&
		a.dbType == request.dbType &&
		a.selectedTable == request.selectedTable &&
		a.pageOffset == request.pageOffset &&
		a.currentResultGeneration() == request.generation
}

func (a *App) runTableResultRequestAsync(request *tableResultRequest, loadingText, cancelText string, callbacks tableResultAsyncCallbacks) {
	if request == nil {
		if callbacks.onError != nil {
			callbacks.onError(fmt.Errorf("table result request is unavailable"))
		}
		return
	}
	// Any table-result request is a newer data intent than a manual SQL query
	// still running behind the visible grid.
	a.cancelActiveQuery()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	var canceledByUser atomic.Bool
	rollbackIfOwned := func() bool {
		if !a.tableResultRequestIsCurrent(request) {
			return false
		}
		if callbacks.rollback != nil {
			callbacks.rollback()
		}
		a.advanceResultGeneration()
		a.restartTotalRowCountFetchIfNeeded()
		return true
	}

	loadingToken := a.showLoadingModal(loadingText, withLoadingCancel(cancelText, func() {
		canceledByUser.Store(true)
		cancel()
		if rollbackIfOwned() && callbacks.onCancel != nil {
			callbacks.onCancel()
		}
	}))

	go func() {
		defer cancel()
		snapshot, fetchErr := fetchTableResultSnapshot(ctx, request)
		a.queueUpdateDraw(func() {
			// A canceled or superseded worker must not remove another
			// operation's loading page or restore older state.
			if canceledByUser.Load() {
				return
			}
			if !a.tableResultRequestIsCurrent(request) {
				// Retire this loader only if it is still the visible one. A newer
				// operation has a different ownership token and remains untouched.
				a.finishLoadingModal(loadingToken)
				return
			}

			if !a.finishLoadingModal(loadingToken) {
				rollbackIfOwned()
				return
			}
			if fetchErr != nil {
				if !rollbackIfOwned() {
					return
				}
				if callbacks.onError != nil {
					callbacks.onError(fetchErr)
				}
				return
			}

			if !a.applyTableResultSnapshot(snapshot) {
				return
			}
			if callbacks.onSuccess != nil {
				callbacks.onSuccess()
			}
		})
	}()
}

func (a *App) restartTotalRowCountFetchIfNeeded() {
	if a == nil || a.totalRowCount >= 0 || a.db == nil || !a.isTableResultActive() {
		return
	}
	pageLimit := a.currentPageLimit()
	if pageLimit <= 0 {
		return
	}

	db := a.db
	dbType := a.dbType
	selectedTable := a.selectedTable
	quotedTable := quoteIdentifier(dbType, selectedTable)
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s", quotedTable)
	countArgs := []any(nil)
	if filter := a.activeResultFilter(selectedTable); filter != nil {
		clause, filterArgs := resultFilterSQL(dbType, filter)
		countQuery += clause
		countArgs = append(countArgs, filterArgs...)
	}
	generation := a.currentResultGeneration()
	pageOffset := a.pageOffset
	go a.fetchTotalRowCount(db, selectedTable, quotedTable, dbType, pageLimit, pageOffset, generation, countQuery, countArgs)
}

// fetchTotalRowCount queries COUNT(*) for the captured table and updates the title
// only if the result still belongs to the active table/connection generation.
func (a *App) fetchTotalRowCount(db *sql.DB, selectedTable, quotedTable string, dbType config.DBType, pageLimit, pageOffset int, generation uint64, countQuery string, countArgs []any) {
	if db == nil || selectedTable == "" || pageLimit <= 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var total int
	err := db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total)
	if err != nil {
		return
	}

	a.queueUpdateDraw(func() {
		if a.db != db || a.currentResultGeneration() != generation || a.selectedTable != selectedTable || a.dbType != dbType || quoteIdentifier(a.dbType, a.selectedTable) != quotedTable {
			return
		}

		a.totalRowCount = total
		a.results.SetTitle(a.paginatedResultTitle(a.currentResultRowCount(), time.Since(a.queryStart)))
		if a.statusBar != nil {
			a.updateStatusBar("", a.currentResultRowCount())
		}
		_ = pageOffset // captured for generation identity/debugging if pagination logic changes.
	})
}

// paginatedResultTitle builds the results panel title with page info.
func (a *App) paginatedResultTitle(rowCount int, elapsed time.Duration) string {
	limit := a.currentPageLimit()
	base := fmt.Sprintf(" %s [yellow]%s[-]%s — [green]%d rows[-] in [teal]%s[-]",
		iconResults, a.selectedTable, a.resultFilterBadge(), rowCount, formatDuration(elapsed))

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
	a.advanceResultGeneration()
	a.pageOffset = 0
	a.pageSize = 0
	a.totalRowCount = -1
}

func (a *App) serverSortColumnName() string {
	if a.sortColumn < 0 || a.sortMode != "server" || a.results == nil || a.sortColumn >= a.results.GetColumnCount() {
		return ""
	}
	cell := a.results.GetCell(0, a.sortColumn)
	if cell == nil {
		return ""
	}
	if name, ok := cell.GetReference().(string); ok && strings.TrimSpace(name) != "" {
		return name
	}
	return stripSortIndicator(cell.Text)
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
	a.loadResultPageAsync(a.pageOffset+limit, "next page")
}

// prevPage goes back one page of results.
func (a *App) prevPage() {
	limit := a.currentPageLimit()
	if limit <= 0 || a.pageOffset <= 0 {
		return
	}
	a.loadResultPageAsync(max(0, a.pageOffset-limit), "previous page")
}

// firstPage jumps to the first page.
func (a *App) firstPage() {
	if a.pageOffset == 0 {
		return
	}
	a.loadResultPageAsync(0, "first page")
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
	a.loadResultPageAsync(lastOffset, "last page")
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

	ref, ok := cell.GetReference().(resultCellReference)
	return ok && ref.rowSelected
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
		ref, ok := anchor.GetReference().(resultCellReference)
		if !ok {
			ref.value = anchor.Text
		}
		ref.rowSelected = selected
		anchor.SetReference(ref)
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
