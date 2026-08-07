package ui

import (
	"fmt"
	"time"
)

// tableLoadOptions describes one user-initiated table/view refresh. Callers
// update their small piece of view state first, then provide rollback so a
// canceled or failed request leaves the last complete result set intact.
type tableLoadOptions struct {
	loadingText  string
	cancelText   string
	canceledText string
	errorText    string
	successText  string
	rollback     func()
	onCancel     func()
	onError      func(error)
	onSuccess    func()
}

// loadCurrentTableAsync runs the same immutable request/snapshot pipeline used
// by filtering without blocking tview's input loop. The loading page owns focus
// until the request completes or the user presses Esc.
func (a *App) loadCurrentTableAsync(options tableLoadOptions) {
	if a == nil {
		return
	}
	// A table navigation is a newer result-view intent than a running manual
	// query. Cancel its database work as well as invalidating its generation so
	// it cannot overwrite the table when it eventually returns.
	a.cancelActiveQuery()

	request, err := a.prepareTableResultRequest()
	if err != nil || request == nil {
		if options.rollback != nil {
			options.rollback()
		}
		if err == nil {
			err = fmt.Errorf("table result request is unavailable")
		}
		if options.onError != nil {
			options.onError(err)
			return
		}
		a.ShowAlert(fmt.Sprintf("%s %s:\n\n%v", iconWarn, fallbackText(options.errorText, "Could not load table results"), err), "main")
		return
	}

	returnFocus := a.app.GetFocus()
	restoreFocus := func() {
		switch returnFocus {
		case a.tables, a.queryInput, a.results:
			a.setFocusWithColor(returnFocus)
		case nil:
			return
		default:
			a.app.SetFocus(returnFocus)
		}
	}

	a.runTableResultRequestAsync(
		request,
		fallbackText(options.loadingText, "Loading table results..."),
		fallbackText(options.cancelText, "Press Esc to cancel loading."),
		tableResultAsyncCallbacks{
			rollback: options.rollback,
			onCancel: func() {
				restoreFocus()
				if options.onCancel != nil {
					options.onCancel()
					return
				}
				a.flashStatus(
					fmt.Sprintf("[yellow]%s[-]", fallbackText(options.canceledText, "Loading canceled")),
					a.currentResultRowCount(),
					1400*time.Millisecond,
				)
			},
			onError: func(loadErr error) {
				restoreFocus()
				if options.onError != nil {
					options.onError(loadErr)
					return
				}
				a.ShowAlert(
					fmt.Sprintf("%s %s:\n\n%v", iconWarn, fallbackText(options.errorText, "Could not load table results"), loadErr),
					"main",
				)
			},
			onSuccess: func() {
				restoreFocus()
				if options.onSuccess != nil {
					options.onSuccess()
				}
				if options.successText != "" {
					a.flashStatus(
						fmt.Sprintf("[green]%s[-]", options.successText),
						a.currentResultRowCount(),
						1400*time.Millisecond,
					)
				}
			},
		},
	)
}

func fallbackText(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (a *App) loadResultPageAsync(targetOffset int, label string) {
	if a == nil {
		return
	}
	previousOffset := a.pageOffset
	a.pageOffset = max(0, targetOffset)
	a.loadCurrentTableAsync(tableLoadOptions{
		loadingText:  fmt.Sprintf("Loading %s...", label),
		cancelText:   "Press Esc to cancel changing pages.",
		canceledText: "Page change canceled",
		errorText:    fmt.Sprintf("Could not load %s", label),
		rollback: func() {
			a.pageOffset = previousOffset
		},
	})
}

func (a *App) refreshCurrentTableAsync() {
	if a == nil {
		return
	}
	if a.selectedTable == "" {
		a.flashStatus("[yellow]No active table to refresh[-]", a.currentResultRowCount(), 1400*time.Millisecond)
		return
	}
	previousTotal := a.totalRowCount
	a.totalRowCount = -1
	a.loadCurrentTableAsync(tableLoadOptions{
		loadingText:  "Refreshing table...",
		cancelText:   "Press Esc to cancel refreshing.",
		canceledText: "Table refresh canceled",
		errorText:    "Could not refresh table",
		successText:  fmt.Sprintf("%s Table refreshed", iconRefresh),
		rollback: func() {
			a.totalRowCount = previousTotal
		},
	})
}
