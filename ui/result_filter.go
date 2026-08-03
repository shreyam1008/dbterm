package ui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

const pageResultFilter = "resultFilter"

type resultValueFilter struct {
	table  string
	column string
	value  string
}

type resultFilterViewState struct {
	filter        *resultValueFilter
	pageOffset    int
	pageSize      int
	totalRowCount int
}

func (a *App) copyCurrentResultCell() {
	_, _, column, value, ok := a.currentResultCell()
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
	a.flashStatus(fmt.Sprintf("[green]Copied %s=%s[-]", tview.Escape(column), displayValue), a.currentResultRowCount(), 1600*time.Millisecond)
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

	initialValue := ""
	if filter := a.activeResultFilter(a.selectedTable); filter != nil && filter.column == column {
		initialValue = filter.value
	}

	form := tview.NewForm()
	form.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Exact Filter: %s.%s ", iconResults, a.selectedTable, column)).
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
		SetFieldWidth(52).
		SetPlaceholder("type an exact value")
	form.AddFormItem(valueInput)

	closeModal := func() {
		a.pages.RemovePage(pageResultFilter)
		a.app.SetFocus(a.results)
	}
	applyTypedValue := func() {
		value := valueInput.GetText()
		closeModal()
		a.applyResultFilter(column, value)
	}
	applyClipboardValue := func() {
		closeModal()
		a.withClipboardFilterValue(func(value string) {
			a.applyResultFilter(column, value)
		})
	}
	valueInput.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter && event.Modifiers() == tcell.ModNone {
			applyTypedValue()
			return nil
		}
		return event
	})

	form.AddButton("Search", applyTypedValue)
	form.AddButton("Use Clipboard", applyClipboardValue)
	form.AddButton("Clear", func() {
		closeModal()
		a.clearResultFilterAndReload()
	})
	form.AddButton("Cancel", closeModal)
	form.SetCancelFunc(closeModal)
	form.SetFocus(0)

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(" [yellow]Enter[-] Search  │  [yellow]Tab / Shift+Tab[-] Next / Previous  │  [yellow]Esc[-] Cancel ")
	footer.SetBackgroundColor(crust)

	container := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true).
		AddItem(footer, 1, 0, false)
	modalW, modalH := a.modalSize(66, 96, 10, 13)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(container, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageResultFilter, grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) filterSelectedResultColumnByClipboard() {
	column, ok := a.selectedResultFilterColumn()
	if !ok {
		a.ShowAlert(fmt.Sprintf("%s Clipboard filtering is available while browsing a table.\n\nSelect a table and target column first.", iconInfo), "main")
		return
	}
	a.withClipboardFilterValue(func(value string) {
		a.applyResultFilter(column, value)
	})
}

func (a *App) applyResultFilter(column, value string) {
	if a == nil || a.selectedTable == "" || strings.TrimSpace(column) == "" {
		return
	}

	previous := a.captureResultFilterViewState()
	a.resultFilter = &resultValueFilter{table: a.selectedTable, column: column, value: value}
	a.resetPagination()
	a.reloadResultFilterAsync(
		previous,
		fmt.Sprintf("Filtering %s = %s...", tview.Escape(column), tview.Escape(resultValuePreview(value, 30))),
		fmt.Sprintf("[green]Filter: %s = %s[-]", tview.Escape(column), tview.Escape(resultValuePreview(value, 28))),
		fmt.Sprintf("Could not filter %s by %q", tview.Escape(column), tview.Escape(resultValuePreview(value, 60))),
	)
}

func (a *App) clearResultFilterAndReload() {
	if a == nil || a.activeResultFilter(a.selectedTable) == nil {
		return
	}
	previous := a.captureResultFilterViewState()
	a.resultFilter = nil
	a.resetPagination()
	a.reloadResultFilterAsync(
		previous,
		"Clearing the column filter...",
		"[green]Column filter cleared[-]",
		"Could not clear the table filter",
	)
}

func (a *App) captureResultFilterViewState() resultFilterViewState {
	state := resultFilterViewState{
		pageOffset:    a.pageOffset,
		pageSize:      a.pageSize,
		totalRowCount: a.totalRowCount,
	}
	if a.resultFilter != nil {
		filter := *a.resultFilter
		state.filter = &filter
	}
	return state
}

func (a *App) restoreResultFilterViewState(state resultFilterViewState) {
	a.resultFilter = nil
	if state.filter != nil {
		filter := *state.filter
		a.resultFilter = &filter
	}
	a.pageOffset = state.pageOffset
	a.pageSize = state.pageSize
	a.totalRowCount = state.totalRowCount
}

func (a *App) reloadResultFilterAsync(previous resultFilterViewState, loadingText, successText, failureText string) {
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
	a.showLoadingModal("Reading the clipboard...", withLoadingCancel("Press Esc to cancel clipboard reading.", func() {
		canceledByUser.Store(true)
		cancel()
		a.setFocusWithColor(a.results)
	}))

	go func() {
		defer cancel()
		value, err := readFromClipboardContext(ctx)
		a.queueUpdateDraw(func() {
			if canceledByUser.Load() {
				return
			}
			a.pages.RemovePage("loading")
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
	filter := *a.resultFilter
	return &filter
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

func (a *App) resultFilterBadge() string {
	filter := a.activeResultFilter(a.selectedTable)
	if filter == nil {
		return ""
	}
	return fmt.Sprintf(" [#cba6f7](%s = %s • Esc clear)[-]", tview.Escape(filter.column), tview.Escape(resultValuePreview(filter.value, 24)))
}

func resultValuePreview(value string, maxRunes int) string {
	value = strings.NewReplacer("\r", `\r`, "\n", `\n`, "\t", `\t`).Replace(value)
	return truncateForDisplay(value, maxRunes)
}
