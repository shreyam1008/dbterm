package ui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rivo/tview"
)

// refreshDataAsync refreshes the object list and active table without running
// database I/O on tview's event loop. It deliberately uses two visible phases:
// schema discovery, followed by the normal cancellable result loader.
func (a *App) refreshDataAsync() {
	a.refreshDataAsyncWithCallbacks(refreshDataCallbacks{})
}

type refreshDataCallbacks struct {
	onSuccess func()
	onCancel  func()
	onError   func(error)
}

func (a *App) refreshDataAsyncWithCallbacks(callbacks refreshDataCallbacks) {
	if a == nil || a.db == nil {
		return
	}
	// Claim result ownership before object discovery starts. Otherwise a query
	// finishing during this first phase can replace the grid and steal focus
	// from the visible refresh loader.
	a.cancelActiveQuery()
	a.advanceResultGeneration()

	db := a.db
	dbType := a.dbType
	a.rememberCurrentResultFilter()
	previous := a.captureResultNavigationState()
	previousStack := append([]resultNavigationState(nil), a.resultNavStack...)
	currentIndex := a.tables.GetCurrentItem()
	returnFocus := a.app.GetFocus()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	var canceled atomic.Bool

	restoreFocus := func(target tview.Primitive) {
		switch target {
		case a.tables, a.queryInput, a.results:
			a.setFocusWithColor(target)
		case nil:
			return
		default:
			a.app.SetFocus(target)
		}
	}

	loadingToken := a.showLoadingModal(
		"Refreshing database objects...",
		withLoadingCancel("Press Esc to cancel refreshing.", func() {
			canceled.Store(true)
			cancel()
			a.restartTotalRowCountFetchIfNeeded()
			restoreFocus(returnFocus)
			if callbacks.onCancel != nil {
				callbacks.onCancel()
				return
			}
			a.flashStatus("[yellow]Database refresh canceled[-]", a.currentResultRowCount(), 1600*time.Millisecond)
		}),
	)

	go func() {
		defer cancel()
		snapshot, err := loadTableListSnapshotContext(ctx, db, dbType, previous.table, currentIndex)
		a.queueUpdateDraw(func() {
			if canceled.Load() {
				return
			}
			if !a.finishLoadingModal(loadingToken) {
				return
			}
			if a.db != db || a.dbType != dbType {
				return
			}
			if err != nil {
				a.restartTotalRowCountFetchIfNeeded()
				restoreFocus(returnFocus)
				if callbacks.onError != nil {
					callbacks.onError(err)
					return
				}
				a.ShowAlert(fmt.Sprintf("%s Could not refresh database objects:\n\n%v", iconWarn, err), "main")
				return
			}

			a.applyTableListSnapshot(snapshot)
			a.loadDatabaseObjects()
			if snapshot == nil || snapshot.tableCount == 0 || a.selectedTable == "" {
				a.clearResultNavigation()
				a.resultFilter = nil
				a.results.Clear()
				a.results.SetTitle(fmt.Sprintf(" %s Results [yellow](Alt+R)[-] ", iconResults))
				a.updateStatusBar(fmt.Sprintf("[green]%s Database refreshed[-]", iconRefresh), 0)
				restoreFocus(returnFocus)
				if callbacks.onSuccess != nil {
					callbacks.onSuccess()
				}
				return
			}

			previousTableAvailable := tableListSnapshotContains(snapshot, previous.table)
			if previousTableAvailable && a.selectedTable == previous.table {
				a.restoreResultNavigationState(previous)
				a.resultNavStack = previousStack
			} else {
				a.clearResultNavigation()
				a.restoreRememberedResultFilter(a.selectedTable)
				a.resetSort()
				a.resetPagination()
			}
			a.totalRowCount = -1
			fallback := a.captureResultNavigationState()
			restoreFocus(returnFocus)
			a.loadCurrentTableAsync(tableLoadOptions{
				loadingText:  fmt.Sprintf("Refreshing %s...", a.selectedTable),
				cancelText:   "Press Esc to cancel loading refreshed rows.",
				canceledText: "Result refresh canceled",
				errorText:    "Could not load refreshed table results",
				successText:  fmt.Sprintf("%s Database refreshed", iconRefresh),
				onCancel:     callbacks.onCancel,
				onError:      callbacks.onError,
				onSuccess:    callbacks.onSuccess,
				rollback: func() {
					// The object list is already fresh; retain the last complete
					// result view only when its table still exists in that list.
					if previousTableAvailable {
						a.restoreResultNavigationState(previous)
						a.resultNavStack = previousStack
						a.selectTableListIdentifier(previous.table)
						return
					}
					a.restoreResultNavigationState(fallback)
					a.clearResultNavigation()
					a.tableResultsActive = false
					a.results.Clear()
					a.results.SetTitle(fmt.Sprintf(" %s Results — [yellow]%s not loaded[-] ", iconResults, a.selectedTable))
					a.updateStatusBar("[yellow]Previous table no longer exists[-]", 0)
					a.selectTableListIdentifier(fallback.table)
				},
			})
		})
	}()
}

func tableListSnapshotContains(snapshot *tableListSnapshot, identifier string) bool {
	identifier = strings.TrimSpace(identifier)
	if snapshot == nil || identifier == "" {
		return false
	}
	for _, item := range snapshot.items {
		if item.identifier == identifier {
			return true
		}
	}
	return false
}
