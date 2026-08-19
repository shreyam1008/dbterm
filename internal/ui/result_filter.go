package ui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

const pageResultFilter = "resultFilter"

type resultFilterOperator string

const (
	resultFilterEqual        resultFilterOperator = "="
	resultFilterNotEqual     resultFilterOperator = "!="
	resultFilterGreater      resultFilterOperator = ">"
	resultFilterGreaterEqual resultFilterOperator = ">="
	resultFilterLess         resultFilterOperator = "<"
	resultFilterLessEqual    resultFilterOperator = "<="
	resultFilterContains     resultFilterOperator = "contains"
	resultFilterStartsWith   resultFilterOperator = "starts-with"
	resultFilterIsNull       resultFilterOperator = "IS NULL"
	resultFilterIsNotNull    resultFilterOperator = "IS NOT NULL"
)

var resultFilterOperators = []resultFilterOperator{
	resultFilterEqual,
	resultFilterNotEqual,
	resultFilterGreater,
	resultFilterGreaterEqual,
	resultFilterLess,
	resultFilterLessEqual,
	resultFilterContains,
	resultFilterStartsWith,
	resultFilterIsNull,
	resultFilterIsNotNull,
}

type resultFilterPredicate struct {
	column   string
	operator resultFilterOperator
	value    any
}

type resultValueFilter struct {
	table string
	// column/value/operator retain compatibility with the original single
	// exact-value filter. New code reads orderedPredicates, which falls back to
	// these fields when loading older state or tests.
	column     string
	value      string
	operator   resultFilterOperator
	predicates []resultFilterPredicate
}

type resultFilterViewState struct {
	filter        *resultValueFilter
	pageOffset    int
	pageSize      int
	totalRowCount int
	selection     resultSelectionState
}

func (a *App) copyCurrentResultCell() {
	row, col, column, value, ok := a.currentResultCell()
	if !ok {
		a.flashStatus("[yellow]Select a data cell to copy[-]", a.currentResultRowCount(), 1600*time.Millisecond)
		return
	}

	displayValue := tview.Escape(resultValuePreview(value, 28))
	a.copyValueAsync(value, func(err error) {
		if err != nil {
			a.flashStatus(fmt.Sprintf("[yellow]Copied %s=%s inside dbterm (system clipboard unavailable)[-]",
				tview.Escape(column), displayValue), a.currentResultRowCount(), 2200*time.Millisecond)
		}
	})
	a.cacheCopiedResultCellIdentity(row, col)
	a.flashStatus(fmt.Sprintf("[green]Copied %s=%s[-]", tview.Escape(column), displayValue), a.currentResultRowCount(), 1600*time.Millisecond)
}

func (a *App) cacheCopiedResultCellIdentity(row, col int) {
	if a == nil {
		return
	}
	a.copiedCellIsNull = a.resultCellIsSQLNull(row, col)
}

func (a *App) resultCellIsSQLNull(row, col int) bool {
	if a == nil || a.results == nil || row <= 0 || row >= a.results.GetRowCount() || col < 0 || col >= a.results.GetColumnCount() {
		return false
	}
	cell := a.results.GetCell(row, col)
	if cell == nil {
		return false
	}
	reference, ok := cell.GetReference().(resultCellReference)
	return ok && reference.isNull
}

func (a *App) currentResultCell() (row, col int, column, value string, ok bool) {
	if a == nil || a.results == nil || a.currentResultRowCount() == 0 {
		return 0, 0, "", "", false
	}
	row, col = a.results.GetSelection()
	if row <= 0 || row >= a.results.GetRowCount() || col < 0 || col >= a.results.GetColumnCount() {
		return 0, 0, "", "", false
	}
	cell := a.results.GetCell(row, col)
	if cell == nil {
		return 0, 0, "", "", false
	}
	column = a.resultColumnName(col)
	if column == "" {
		return 0, 0, "", "", false
	}
	value = cell.Text
	if ref, hasRef := cell.GetReference().(resultCellReference); hasRef {
		value = ref.value
	}
	return row, col, column, value, true
}

func (a *App) resultColumnName(col int) string {
	if a == nil || a.results == nil || col < 0 || col >= a.results.GetColumnCount() {
		return ""
	}
	header := a.results.GetCell(0, col)
	if header == nil {
		return ""
	}
	if name, ok := header.GetReference().(string); ok && strings.TrimSpace(name) != "" {
		return name
	}
	return stripSortIndicator(header.Text)
}

func (a *App) selectedResultFilterColumn() (string, bool) {
	if a == nil || !a.tableResultsActive || a.selectedTable == "" || a.results == nil || a.results.GetColumnCount() == 0 {
		return "", false
	}
	_, col := a.results.GetSelection()
	column := a.resultColumnName(col)
	return column, column != ""
}

func (a *App) showResultFilterModal() {
	column, ok := a.selectedResultFilterColumn()
	if !ok {
		a.ShowAlert(fmt.Sprintf("%s Column filtering is available while browsing a table.\n\nSelect a table and a target column first.", iconInfo), "main")
		return
	}

	activeFilter := a.activeResultFilter(a.selectedTable)
	initialPredicate, hasInitialPredicate := latestResultFilterPredicateForColumn(activeFilter, column)
	initialValue := ""
	initialOperatorIndex := 0
	if hasInitialPredicate {
		if resultFilterOperatorNeedsValue(initialPredicate.operator) {
			initialValue = resultFilterValueString(initialPredicate.value)
		}
		for index, operator := range resultFilterOperators {
			if operator == initialPredicate.operator {
				initialOperatorIndex = index
				break
			}
		}
	}

	form := tview.NewForm()
	form.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Filters: %s.%s ", iconResults, a.selectedTable, column)).
		SetTitleColor(mauve).
		SetBorderColor(surface1)
	form.SetBackgroundColor(bg)
	form.SetFieldBackgroundColor(mantle).
		SetFieldTextColor(text).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetLabelColor(text)

	valueInput := tview.NewInputField().
		SetLabel("Value").
		SetText(initialValue).
		SetFieldWidth(46).
		SetPlaceholder("type an exact value")
	form.AddFormItem(valueInput)

	operatorLabels := make([]string, len(resultFilterOperators))
	for index, operator := range resultFilterOperators {
		operatorLabels[index] = string(operator)
	}
	operatorInput := tview.NewDropDown().
		SetLabel("Operator").
		SetOptions(operatorLabels, nil).
		SetCurrentOption(initialOperatorIndex)
	form.AddFormItem(operatorInput)
	updateValuePlaceholder := func(index int) {
		operator := resultFilterOperatorAt(index)
		if resultFilterOperatorNeedsValue(operator) {
			valueInput.SetPlaceholder("type a comparison value")
			return
		}
		valueInput.SetPlaceholder("not used for NULL operators")
	}
	operatorInput.SetSelectedFunc(func(_ string, index int) {
		updateValuePlaceholder(index)
	})
	updateValuePlaceholder(initialOperatorIndex)

	closeModal := func() {
		a.pages.RemovePage(pageResultFilter)
		a.app.SetFocus(a.results)
	}
	selectedOperator := func() resultFilterOperator {
		index, _ := operatorInput.GetCurrentOption()
		return resultFilterOperatorAt(index)
	}
	applyTypedValue := func(addAND bool) {
		operator := selectedOperator()
		value := any(valueInput.GetText())
		if !resultFilterOperatorNeedsValue(operator) {
			value = nil
		}
		closeModal()
		if addAND {
			a.addResultPredicateAND(column, operator, value)
			return
		}
		a.applyResultPredicate(column, operator, value)
	}
	applyClipboardValue := func() {
		operator := selectedOperator()
		if !resultFilterOperatorNeedsValue(operator) {
			closeModal()
			a.applyResultPredicate(column, operator, nil)
			return
		}
		closeModal()
		a.withClipboardResultPredicate(column, operator, func(predicate resultFilterPredicate) {
			a.applyResultPredicate(predicate.column, predicate.operator, predicate.value)
		})
	}
	valueInput.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter && event.Modifiers() == tcell.ModNone {
			applyTypedValue(false)
			return nil
		}
		return event
	})

	form.AddButton("Apply", func() { applyTypedValue(false) })
	form.AddButton("Add AND", func() { applyTypedValue(true) })
	form.AddButton("Use Clipboard", applyClipboardValue)
	form.AddButton("Remove Last", func() {
		closeModal()
		a.removeLastResultFilterAndReload()
	})
	form.AddButton("Clear All", func() {
		closeModal()
		a.clearResultFilterAndReload()
	})
	form.AddButton("Cancel", closeModal)
	form.SetCancelFunc(closeModal)
	form.SetFocus(0)

	activeView := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(true).
		SetWordWrap(true).
		SetText(resultFilterModalSummary(activeFilter))
	activeView.SetBackgroundColor(mantle)

	modalW, modalH := a.modalSize(72, 104, 16, 23)
	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(resultFilterFooterText(modalW))
	footer.SetBackgroundColor(crust)

	activeHeight := resultFilterModalSummaryHeight(activeFilter)
	container := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true).
		AddItem(activeView, activeHeight, 0, false).
		AddItem(footer, 1, 0, false)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(container, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageResultFilter, grid, true, true)
	a.app.SetFocus(form)
}

func resultFilterFooterText(width int) string {
	return footerTextThatFits(width,
		" [yellow]Enter[-] Apply / choose  │  [yellow]Tab / Shift+Tab[-] Move  │  [yellow]Esc[-] Cancel ",
		" [yellow]Enter[-] Apply  │  [yellow]Tab[-] Move  │  [yellow]Esc[-] Cancel ",
		" [yellow]Enter[-] Apply  │  [yellow]Esc[-] Cancel ",
		" [yellow]Esc[-] Cancel ",
	)
}

func (a *App) filterSelectedResultColumnByClipboard() {
	column, ok := a.selectedResultFilterColumn()
	if !ok {
		a.ShowAlert(fmt.Sprintf("%s Clipboard filtering is available while browsing a table.\n\nSelect a table and target column first.", iconInfo), "main")
		return
	}
	// An internal C-copy retains SQL NULL identity even though the system
	// clipboard can represent it only as the text "NULL".
	a.withClipboardResultPredicate(column, resultFilterEqual, func(predicate resultFilterPredicate) {
		a.applyResultPredicate(predicate.column, predicate.operator, predicate.value)
	})
}

func (a *App) withClipboardResultPredicate(column string, operator resultFilterOperator, usePredicate func(resultFilterPredicate)) {
	if usePredicate == nil {
		return
	}
	operator = normalizeResultFilterOperator(operator)
	nullPredicate, copiedSQLNull := a.cachedCopiedSQLNullPredicate(column)
	if copiedSQLNull && operator == resultFilterEqual && !a.copiedCellSystem {
		usePredicate(nullPredicate)
		return
	}
	a.withClipboardFilterValue(func(value string) {
		if copiedSQLNull && operator == resultFilterEqual && value == a.copiedCellValue {
			usePredicate(nullPredicate)
			return
		}
		// The system clipboard changed after dbterm copied a SQL NULL. Retire
		// the cached NULL identity so subsequent V actions use the new value.
		if copiedSQLNull && value != a.copiedCellValue {
			a.copiedCellIsNull = false
		}
		usePredicate(resultFilterPredicate{column: column, operator: operator, value: value})
	})
}

func (a *App) cachedCopiedSQLNullPredicate(column string) (resultFilterPredicate, bool) {
	column = strings.TrimSpace(column)
	if column == "" || !a.cachedCopiedCellIsNull() {
		return resultFilterPredicate{}, false
	}
	return resultFilterPredicate{column: column, operator: resultFilterIsNull}, true
}

// applyResultFilter preserves the original exact-value behavior used by Enter
// and V. Applying the same column again updates its predicate; filters on
// other columns remain composed with AND.
func (a *App) applyResultFilter(column, value string) {
	a.applyResultPredicate(column, resultFilterEqual, value)
}

func (a *App) applyResultPredicate(column string, operator resultFilterOperator, value any) {
	a.changeResultPredicate(column, operator, value, false)
}

func (a *App) addResultPredicateAND(column string, operator resultFilterOperator, value any) {
	a.changeResultPredicate(column, operator, value, true)
}

func (a *App) changeResultPredicate(column string, operator resultFilterOperator, value any, addAND bool) {
	if a == nil || a.selectedTable == "" || strings.TrimSpace(column) == "" {
		return
	}

	previous := a.captureResultFilterViewState()
	predicate := normalizedResultFilterPredicate(resultFilterPredicate{
		column:   column,
		operator: operator,
		value:    value,
	})
	predicates := []resultFilterPredicate(nil)
	if active := a.activeResultFilter(a.selectedTable); active != nil {
		predicates = active.orderedPredicates()
	}
	predicates, changedIndex := changedResultFilterPredicates(predicates, predicate, addAND)
	a.setCurrentResultFilter(newResultValueFilter(a.selectedTable, predicates))
	a.resetPagination()
	predicateText := resultFilterPredicateText(predicate, 34)
	action := "Applying"
	completedAction := "Filter"
	if addAND {
		action = "Adding AND"
		completedAction = "Added AND"
	} else if changedIndex >= 0 {
		action = "Updating"
		completedAction = "Updated"
	}
	a.reloadResultFilterAsync(
		previous,
		fmt.Sprintf("%s filter %s...", action, tview.Escape(predicateText)),
		fmt.Sprintf("[green]%s (%d): %s[-]", completedAction, len(predicates), tview.Escape(predicateText)),
		fmt.Sprintf("Could not apply filter %s", tview.Escape(predicateText)),
		false,
	)
}

func changedResultFilterPredicates(existing []resultFilterPredicate, predicate resultFilterPredicate, addAND bool) ([]resultFilterPredicate, int) {
	predicates := make([]resultFilterPredicate, len(existing))
	for index, current := range existing {
		predicates[index] = cloneResultFilterPredicate(current)
	}
	predicate = normalizedResultFilterPredicate(predicate)
	changedIndex := -1
	if !addAND {
		changedIndex = resultFilterPredicateReplacementIndex(predicates, predicate)
	}
	if changedIndex >= 0 {
		predicates[changedIndex] = predicate
		return predicates, changedIndex
	}
	return append(predicates, predicate), -1
}

func resultFilterPredicateReplacementIndex(predicates []resultFilterPredicate, replacement resultFilterPredicate) int {
	replacement = normalizedResultFilterPredicate(replacement)
	// Prefer updating the same operator, which makes reopening an equality
	// filter behave exactly like the original single-filter modal.
	for index := len(predicates) - 1; index >= 0; index-- {
		predicate := normalizedResultFilterPredicate(predicates[index])
		if predicate.column == replacement.column && predicate.operator == replacement.operator {
			return index
		}
	}
	// Apply is intentionally a replacement action for the selected column.
	// Users who want a range or another same-column condition choose Add AND.
	for index := len(predicates) - 1; index >= 0; index-- {
		if strings.TrimSpace(predicates[index].column) == replacement.column {
			return index
		}
	}
	return -1
}

func (a *App) removeLastResultFilterAndReload() {
	if a == nil {
		return
	}
	active := a.activeResultFilter(a.selectedTable)
	if active == nil {
		a.flashStatus("[yellow]No active filter to remove[-]", a.currentResultRowCount(), 1400*time.Millisecond)
		return
	}
	predicates := active.orderedPredicates()
	if len(predicates) == 0 {
		return
	}
	removed := predicates[len(predicates)-1]
	previous := a.captureResultFilterViewState()
	predicates = predicates[:len(predicates)-1]
	a.setCurrentResultFilter(newResultValueFilter(a.selectedTable, predicates))
	a.resetPagination()
	a.reloadResultFilterAsync(
		previous,
		"Removing the last filter...",
		fmt.Sprintf("[green]Removed filter: %s[-]", tview.Escape(resultFilterPredicateText(removed, 34))),
		"Could not remove the last table filter",
		false,
	)
}

func (a *App) clearResultFilterAndReload() {
	if a == nil {
		return
	}
	if a.activeResultFilter(a.selectedTable) == nil {
		a.flashStatus("[yellow]No active filters to clear[-]", a.currentResultRowCount(), 1400*time.Millisecond)
		return
	}
	previous := a.captureResultFilterViewState()
	a.resetCurrentResultPosition()
	a.setCurrentResultFilter(nil)
	a.resetPagination()
	a.reloadResultFilterAsync(
		previous,
		"Clearing all table filters...",
		"[green]All table filters cleared[-]",
		"Could not clear the table filters",
		true,
	)
}

func (a *App) captureResultFilterViewState() resultFilterViewState {
	state := resultFilterViewState{
		pageOffset:    a.pageOffset,
		pageSize:      a.pageSize,
		totalRowCount: a.totalRowCount,
	}
	if a.results != nil {
		state.selection = cloneResultSelectionState(a.captureResultSelection())
	}
	if a.resultFilter != nil {
		state.filter = cloneResultValueFilter(a.resultFilter)
	}
	return state
}

func (a *App) restoreResultFilterViewState(state resultFilterViewState) {
	a.setCurrentResultFilter(state.filter)
	a.pageOffset = state.pageOffset
	a.pageSize = state.pageSize
	a.totalRowCount = state.totalRowCount
	if a.results != nil {
		a.restoreResultSelection(state.selection, a.currentResultRowCount())
	}
}

func (a *App) reloadResultFilterAsync(previous resultFilterViewState, loadingText, successText, failureText string, resetSelection bool) {
	request, err := a.prepareTableResultRequest()
	if err != nil || request == nil {
		a.restoreResultFilterViewState(previous)
		a.advanceResultGeneration()
		if err == nil {
			err = fmt.Errorf("table result request is unavailable")
		}
		a.ShowAlert(fmt.Sprintf("%s %s:\n\n%v", iconWarn, failureText, err), "main")
		return
	}
	if resetSelection {
		request.selection = defaultResultSelectionState()
	}

	a.runTableResultRequestAsync(request, loadingText, "Press Esc to cancel filtering.", tableResultAsyncCallbacks{
		rollback: func() {
			a.restoreResultFilterViewState(previous)
		},
		onCancel: func() {
			a.setFocusWithColor(a.results)
			a.flashStatus("[yellow]Filter change canceled[-]", a.currentResultRowCount(), 1400*time.Millisecond)
		},
		onError: func(fetchErr error) {
			a.setFocusWithColor(a.results)
			a.ShowAlert(fmt.Sprintf("%s %s:\n\n%v", iconWarn, failureText, fetchErr), "main")
		},
		onSuccess: func() {
			a.setFocusWithColor(a.results)
			a.flashStatus(successText, a.currentResultRowCount(), 1800*time.Millisecond)
		},
	})
}

func (a *App) withClipboardFilterValue(useValue func(string)) {
	if useValue == nil {
		return
	}
	if value, ok := a.copiedCellClipboardValue(); ok {
		useValue(value)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), clipboardReadTimeout)
	var canceledByUser atomic.Bool
	loadingToken := a.showLoadingModal("Reading the clipboard...", withLoadingCancel("Press Esc to cancel clipboard reading.", func() {
		canceledByUser.Store(true)
		cancel()
		a.setFocusWithColor(a.results)
		a.flashStatus("[yellow]Clipboard read canceled[-]", a.currentResultRowCount(), 1400*time.Millisecond)
	}))

	go func() {
		defer cancel()
		value, err := readFromClipboardContext(ctx)
		a.queueUpdateDraw(func() {
			if canceledByUser.Load() {
				return
			}
			if !a.finishLoadingModal(loadingToken) {
				return
			}
			a.setFocusWithColor(a.results)
			if err != nil {
				if fallback, ok := a.cachedCopiedCellValue(); ok {
					useValue(fallback)
					return
				}
				a.ShowAlert(fmt.Sprintf("%s Could not read the clipboard:\n\n%v\n\nCopy a cell with C first, or install a clipboard utility.", iconWarn, err), "main")
				return
			}
			useValue(value)
		})
	}()
}

func (a *App) activeResultFilter(table string) *resultValueFilter {
	if a == nil || a.resultFilter == nil || a.resultFilter.table != table {
		return nil
	}
	filter := cloneResultValueFilter(a.resultFilter)
	if len(filter.orderedPredicates()) == 0 {
		return nil
	}
	return filter
}

// setCurrentResultFilter changes the visible filter and keeps the session
// cache for the selected table in sync. The cache is connection-scoped and is
// discarded by cleanup when the user disconnects or opens another database.
func (a *App) setCurrentResultFilter(filter *resultValueFilter) {
	if a == nil {
		return
	}
	table := strings.TrimSpace(a.selectedTable)
	a.resultFilter = cloneResultValueFilter(filter)
	if a.resultFilter != nil && table != "" {
		a.resultFilter.table = table
	}
	if table == "" {
		a.refreshTableSidebarState()
		return
	}
	if a.resultFilter == nil || len(a.resultFilter.orderedPredicates()) == 0 {
		if a.resultFilters != nil {
			delete(a.resultFilters, table)
		}
		a.refreshTableSidebarState()
		return
	}
	if a.resultFilters == nil {
		a.resultFilters = make(map[string]*resultValueFilter)
	}
	a.resultFilters[table] = cloneResultValueFilter(a.resultFilter)
	a.refreshTableSidebarState()
}

func (a *App) rememberCurrentResultFilter() {
	if a == nil {
		return
	}
	table := strings.TrimSpace(a.selectedTable)
	if table == "" {
		return
	}
	filter := a.activeResultFilter(table)
	if filter == nil {
		if a.resultFilters != nil {
			delete(a.resultFilters, table)
		}
		return
	}
	if a.resultFilters == nil {
		a.resultFilters = make(map[string]*resultValueFilter)
	}
	a.resultFilters[table] = filter
}

func (a *App) restoreRememberedResultFilter(table string) {
	if a == nil {
		return
	}
	table = strings.TrimSpace(table)
	a.resultFilter = nil
	if table == "" || a.resultFilters == nil {
		a.refreshTableSidebarState()
		return
	}
	if filter := a.resultFilters[table]; filter != nil {
		a.resultFilter = cloneResultValueFilter(filter)
		a.resultFilter.table = table
	}
	a.refreshTableSidebarState()
}

func (a *App) selectTableWithRememberedFilter(table string) {
	if a == nil {
		return
	}
	a.rememberCurrentResultPosition()
	a.rememberCurrentResultFilter()
	a.selectedTable = table
	a.restoreRememberedResultFilter(table)
}

func resultFilterPlaceholder(dbType config.DBType) string {
	if dbType == config.PostgreSQL {
		return "$1"
	}
	return "?"
}

func resultFilterClause(dbType config.DBType, column string) string {
	return fmt.Sprintf(" WHERE %s = %s", quoteIdentifier(dbType, column), resultFilterPlaceholder(dbType))
}

// resultFilterSQL renders an ordered AND expression and its parameter values.
// Identifiers are quoted and values remain query parameters on every engine.
func resultFilterSQL(dbType config.DBType, filter *resultValueFilter) (string, []any) {
	if filter == nil {
		return "", nil
	}
	predicates := filter.orderedPredicates()
	if len(predicates) == 0 {
		return "", nil
	}

	conditions := make([]string, 0, len(predicates))
	args := make([]any, 0, len(predicates))
	for _, predicate := range predicates {
		predicate = normalizedResultFilterPredicate(predicate)
		if predicate.column == "" {
			continue
		}
		column := quoteIdentifier(dbType, predicate.column)
		switch predicate.operator {
		case resultFilterIsNull:
			conditions = append(conditions, column+" IS NULL")
		case resultFilterIsNotNull:
			conditions = append(conditions, column+" IS NOT NULL")
		case resultFilterContains, resultFilterStartsWith:
			placeholder := numberedResultFilterPlaceholder(dbType, len(args)+1)
			conditions = append(conditions, fmt.Sprintf("%s LIKE %s ESCAPE '='", resultFilterTextExpression(dbType, column), placeholder))
			value := escapeResultFilterLikeValue(resultFilterValueString(predicate.value))
			if predicate.operator == resultFilterContains {
				value = "%" + value + "%"
			} else {
				value += "%"
			}
			args = append(args, value)
		default:
			placeholder := numberedResultFilterPlaceholder(dbType, len(args)+1)
			operator := string(predicate.operator)
			if predicate.operator == resultFilterNotEqual {
				operator = "<>"
			}
			conditions = append(conditions, fmt.Sprintf("%s %s %s", column, operator, placeholder))
			args = append(args, predicate.value)
		}
	}
	if len(conditions) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func resultFilterTextExpression(dbType config.DBType, quotedColumn string) string {
	if dbType == config.MySQL {
		return fmt.Sprintf("CAST(%s AS CHAR)", quotedColumn)
	}
	return fmt.Sprintf("CAST(%s AS TEXT)", quotedColumn)
}

func numberedResultFilterPlaceholder(dbType config.DBType, position int) string {
	if dbType == config.PostgreSQL {
		return fmt.Sprintf("$%d", max(1, position))
	}
	return "?"
}

func escapeResultFilterLikeValue(value string) string {
	return strings.NewReplacer("=", "==", "%", "=%", "_", "=_").Replace(value)
}

func normalizeResultFilterOperator(operator resultFilterOperator) resultFilterOperator {
	switch strings.ToLower(strings.TrimSpace(string(operator))) {
	case "", "=", "==":
		return resultFilterEqual
	case "!=", "<>":
		return resultFilterNotEqual
	case ">":
		return resultFilterGreater
	case ">=":
		return resultFilterGreaterEqual
	case "<":
		return resultFilterLess
	case "<=":
		return resultFilterLessEqual
	case "contains":
		return resultFilterContains
	case "starts-with", "starts with", "startswith":
		return resultFilterStartsWith
	case "is null":
		return resultFilterIsNull
	case "is not null":
		return resultFilterIsNotNull
	default:
		return resultFilterEqual
	}
}

func normalizedResultFilterPredicate(predicate resultFilterPredicate) resultFilterPredicate {
	predicate.column = strings.TrimSpace(predicate.column)
	predicate.operator = normalizeResultFilterOperator(predicate.operator)
	if !resultFilterOperatorNeedsValue(predicate.operator) {
		predicate.value = nil
	}
	return predicate
}

func resultFilterOperatorNeedsValue(operator resultFilterOperator) bool {
	operator = normalizeResultFilterOperator(operator)
	return operator != resultFilterIsNull && operator != resultFilterIsNotNull
}

func resultFilterOperatorAt(index int) resultFilterOperator {
	if index < 0 || index >= len(resultFilterOperators) {
		return resultFilterEqual
	}
	return resultFilterOperators[index]
}

func newResultValueFilter(table string, predicates []resultFilterPredicate) *resultValueFilter {
	cleaned := make([]resultFilterPredicate, 0, len(predicates))
	for _, predicate := range predicates {
		predicate = normalizedResultFilterPredicate(predicate)
		if predicate.column != "" {
			cleaned = append(cleaned, cloneResultFilterPredicate(predicate))
		}
	}
	if len(cleaned) == 0 {
		return nil
	}
	last := cleaned[len(cleaned)-1]
	return &resultValueFilter{
		table:      table,
		column:     last.column,
		value:      resultFilterValueString(last.value),
		operator:   last.operator,
		predicates: cleaned,
	}
}

func (filter *resultValueFilter) orderedPredicates() []resultFilterPredicate {
	if filter == nil {
		return nil
	}
	if len(filter.predicates) > 0 {
		predicates := make([]resultFilterPredicate, 0, len(filter.predicates))
		for _, predicate := range filter.predicates {
			predicate = normalizedResultFilterPredicate(predicate)
			if predicate.column != "" {
				predicates = append(predicates, cloneResultFilterPredicate(predicate))
			}
		}
		return predicates
	}
	if strings.TrimSpace(filter.column) == "" {
		return nil
	}
	return []resultFilterPredicate{{
		column:   strings.TrimSpace(filter.column),
		operator: normalizeResultFilterOperator(filter.operator),
		value:    filter.value,
	}}
}

func cloneResultValueFilter(filter *resultValueFilter) *resultValueFilter {
	if filter == nil {
		return nil
	}
	clone := *filter
	clone.predicates = make([]resultFilterPredicate, len(filter.predicates))
	for index, predicate := range filter.predicates {
		clone.predicates[index] = cloneResultFilterPredicate(predicate)
	}
	return &clone
}

func cloneResultFilterPredicate(predicate resultFilterPredicate) resultFilterPredicate {
	predicate.value = cloneResultRawValue(predicate.value)
	return predicate
}

func resultFilterValueString(value any) string {
	if value == nil {
		return "NULL"
	}
	return fullCellValue(value)
}

func latestResultFilterValueForColumn(filter *resultValueFilter, column string) string {
	if filter == nil {
		return ""
	}
	predicates := filter.orderedPredicates()
	for index := len(predicates) - 1; index >= 0; index-- {
		predicate := predicates[index]
		if predicate.column == column && predicate.operator == resultFilterEqual {
			return resultFilterValueString(predicate.value)
		}
	}
	return ""
}

func latestResultFilterPredicateForColumn(filter *resultValueFilter, column string) (resultFilterPredicate, bool) {
	if filter == nil {
		return resultFilterPredicate{}, false
	}
	predicates := filter.orderedPredicates()
	for index := len(predicates) - 1; index >= 0; index-- {
		predicate := normalizedResultFilterPredicate(predicates[index])
		if predicate.column == column {
			return predicate, true
		}
	}
	return resultFilterPredicate{}, false
}

func resultFilterPredicateText(predicate resultFilterPredicate, maxRunes int) string {
	predicate = normalizedResultFilterPredicate(predicate)
	if !resultFilterOperatorNeedsValue(predicate.operator) {
		return fmt.Sprintf("%s %s", predicate.column, predicate.operator)
	}
	value := resultValuePreview(resultFilterValueString(predicate.value), maxRunes)
	return fmt.Sprintf("%s %s %s", predicate.column, predicate.operator, value)
}

func resultFilterModalSummary(filter *resultValueFilter) string {
	if filter == nil {
		return " [#6c7086]Active filters: none. Reopen / to add another AND predicate.[-]"
	}
	predicates := filter.orderedPredicates()
	if len(predicates) == 0 {
		return " [#6c7086]Active filters: none.[-]"
	}
	lines := []string{fmt.Sprintf(" [#a6adc8]Active filters (%d, combined with AND):[-]", len(predicates))}
	for index, predicate := range predicates {
		lines = append(lines, fmt.Sprintf(" [yellow]%d.[-] %s", index+1, tview.Escape(resultFilterPredicateText(predicate, 48))))
	}
	return strings.Join(lines, "\n")
}

func resultFilterModalSummaryHeight(filter *resultValueFilter) int {
	count := 0
	if filter != nil {
		count = len(filter.orderedPredicates())
	}
	return clamp(count+1, 2, 6)
}

func (a *App) resultFilterBadge() string {
	filter := a.activeResultFilter(a.selectedTable)
	if filter == nil {
		return ""
	}
	predicates := filter.orderedPredicates()
	previews := make([]string, 0, min(2, len(predicates)))
	for index, predicate := range predicates {
		if index == 2 {
			break
		}
		previews = append(previews, resultFilterPredicateText(predicate, 18))
	}
	summary := strings.Join(previews, " AND ")
	if len(predicates) > len(previews) {
		summary += fmt.Sprintf(" +%d", len(predicates)-len(previews))
	}
	return fmt.Sprintf(" [#cba6f7]FILTERED %d: %s • Esc clears[-]", len(predicates), tview.Escape(summary))
}

func resultValuePreview(value string, maxRunes int) string {
	value = strings.NewReplacer("\r", `\r`, "\n", `\n`, "\t", `\t`).Replace(value)
	return truncateForDisplay(value, maxRunes)
}
