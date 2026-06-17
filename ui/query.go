package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rivo/tview"
)

// ExecuteQuery runs a SQL query and displays results or affected row count.
func (a *App) ExecuteQuery(query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return
	}

	ctx, finish, ok := a.startQueryLifecycle()
	if !ok {
		a.queueUpdateDraw(func() {
			a.flashStatus("[yellow]Query already running — press Esc or Ctrl+C to cancel[-]", a.currentResultRowCount(), 1800*time.Millisecond)
		})
		return
	}
	defer finish()

	// Check connection health before executing.
	if a.db == nil {
		a.queueUpdateDraw(func() {
			a.ShowAlert(fmt.Sprintf("%s Not connected to any database.\n\nPress Alt+D to go to Dashboard and connect.", iconWarn), "main")
		})
		return
	}

	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	if err := a.db.PingContext(pingCtx); err != nil {
		if a.handleQueryCancellation(err) {
			return
		}
		a.queueUpdateDraw(func() {
			a.ShowAlert(fmt.Sprintf("%s Connection lost: %v\n\nPress Alt+D to reconnect from Dashboard.", iconWarn, err), "main")
		})
		return
	}

	firstToken := firstSQLToken(query)
	isRead := isReadSQLToken(firstToken)

	if a.activeConn != nil && a.activeConn.ReadOnly && !isRead {
		blockedToken := firstToken
		if blockedToken == "" {
			blockedToken = "UNKNOWN"
		}
		a.queueUpdateDraw(func() {
			a.ShowAlert(
				fmt.Sprintf(
					"%s Read-only connection \"%s\" blocks write queries.\n\nBlocked statement: %s\n\nRun a read query (SELECT/SHOW/EXPLAIN/PRAGMA) or disable Read-Only in connection settings.",
					iconWarn,
					a.dbName,
					blockedToken,
				),
				"main",
			)
		})
		return
	}

	if isRead {
		queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		rows, err := a.db.QueryContext(queryCtx, query)
		if err != nil {
			if a.handleQueryCancellation(err) {
				return
			}
			a.queueUpdateDraw(func() { a.showQueryError(err, query) })
			return
		}
		defer rows.Close()

		columnNames, err := rows.Columns()
		if err != nil {
			if a.handleQueryCancellation(err) {
				return
			}
			a.queueUpdateDraw(func() { a.ShowAlert(fmt.Sprintf("%s Could not read query columns:\n\n%v", iconWarn, err), "main") })
			return
		}

		requestedLimit := a.effectiveResultLimit()
		previewLimit := resolvedResultLimit(requestedLimit, len(columnNames))
		newResults := newResultTable()
		rowCount, truncated, err := populateTableWithLimit(newResults, rows, previewLimit)
		if err != nil {
			if a.handleQueryCancellation(err) {
				return
			}
			a.queueUpdateDraw(func() { a.ShowAlert(fmt.Sprintf("%s Error reading results:\n\n%v", iconWarn, err), "main") })
			return
		}

		elapsed := time.Since(a.queryStartedAt)
		a.recordQueryHistory(query)
		a.queueUpdateDraw(func() {
			a.results.Clear()
			for r := 0; r < newResults.GetRowCount(); r++ {
				for c := 0; c < newResults.GetColumnCount(); c++ {
					a.results.SetCell(r, c, newResults.GetCell(r, c))
				}
			}
			previewBadge := ""
			if truncated {
				previewBadge = fmt.Sprintf(" [#a6adc8](showing %d)[-]", rowCount)
			}
			a.results.SetTitle(fmt.Sprintf(" %s Results [yellow](Alt+R)[-] — [green]%d rows[-]%s in [teal]%s[-] ", iconResults, rowCount, previewBadge, formatDuration(elapsed)))
			a.results.ScrollToBeginning()
			a.applyColumnWidths()
			a.updateStatusBar(fmt.Sprintf("[teal]%s[-]", formatDuration(elapsed)), rowCount)
			if truncated {
				a.flashStatus(fmt.Sprintf("[yellow]%s Showing first %d rows (%s). Refine with LIMIT/OFFSET or use Alt+0 for auto max.[-]", iconInfo, rowCount, a.resultLimitReadable()), rowCount, 2200*time.Millisecond)
			}
			a.app.SetFocus(a.results)
		})
		return
	}

	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := a.db.ExecContext(queryCtx, query)
	if err != nil {
		if a.handleQueryCancellation(err) {
			return
		}
		a.queueUpdateDraw(func() { a.showQueryError(err, query) })
		return
	}

	rowsAffected, _ := res.RowsAffected()
	elapsed := time.Since(a.queryStartedAt)
	a.recordQueryHistory(query)
	a.queueUpdateDraw(func() {
		a.ShowAlert(fmt.Sprintf("%s Query executed successfully\n\nRows affected: %d\nTime: %s", iconSuccess, rowsAffected, formatDuration(elapsed)), "main")
		if err := a.refreshData(); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Query succeeded, but refresh failed:\n\n%v", iconWarn, err), "main")
		}
	})
}

func isReadSQLToken(firstToken string) bool {
	switch firstToken {
	case "SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "PRAGMA", "WITH":
		return true
	default:
		return false
	}
}

func (a *App) handleQueryCancellation(err error) bool {
	if err == nil || (!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)) {
		return false
	}
	a.queueUpdateDraw(func() { a.showQueryCanceled() })
	return true
}

// showQueryError formats SQL errors with helpful context
func (a *App) showQueryError(err error, query string) {
	errMsg := err.Error()

	// Truncate long query in error display
	displayQuery := query
	if len(displayQuery) > 80 {
		displayQuery = displayQuery[:77] + "..."
	}

	var hint string
	errLower := strings.ToLower(errMsg)

	switch {
	case strings.Contains(errLower, "does not exist") || strings.Contains(errLower, "no such table"):
		hint = "\n\n💡 Hint: Check table name spelling. Press Alt+T to see available tables."
	case strings.Contains(errLower, "syntax error") || strings.Contains(errLower, "near"):
		hint = "\n\n💡 Hint: Check your SQL syntax. Press Alt+H for cheatsheets."
	case strings.Contains(errLower, "permission denied") || strings.Contains(errLower, "access denied"):
		hint = "\n\n💡 Hint: Your user may not have sufficient privileges for this operation."
	case strings.Contains(errLower, "duplicate") || strings.Contains(errLower, "unique constraint"):
		hint = "\n\n💡 Hint: A record with this key already exists."
	case strings.Contains(errLower, "connection") || strings.Contains(errLower, "refused"):
		hint = "\n\n💡 Hint: Connection issue. Press Alt+D to check your connection."
	}

	message := fmt.Sprintf("%s Query error:\n\n%s", iconFail, errMsg)
	if displayQuery != "" {
		message += fmt.Sprintf("\n\nQuery: %s", displayQuery)
	}
	message += hint

	a.ShowAlert(message, "main")
}

// formatDuration formats a duration in a human-friendly way
func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%.0fμs", float64(d.Microseconds()))
	}
	if d < time.Second {
		return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

func firstSQLToken(query string) string {
	remaining := strings.TrimSpace(query)
	for remaining != "" {
		switch {
		case strings.HasPrefix(remaining, "--"):
			nextLine := strings.IndexByte(remaining, '\n')
			if nextLine < 0 {
				return ""
			}
			remaining = strings.TrimSpace(remaining[nextLine+1:])
		case strings.HasPrefix(remaining, "/*"):
			commentEnd := strings.Index(remaining, "*/")
			if commentEnd < 0 {
				return ""
			}
			remaining = strings.TrimSpace(remaining[commentEnd+2:])
		default:
			end := 0
			for end < len(remaining) {
				ch := remaining[end]
				if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
					end++
					continue
				}
				break
			}
			if end == 0 {
				return ""
			}
			return strings.ToUpper(remaining[:end])
		}
	}
	return ""
}

func (a *App) startQueryLifecycle() (context.Context, func(), bool) {
	a.queryMu.Lock()
	if a.queryRunning {
		a.queryMu.Unlock()
		return nil, func() {}, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := time.Now()
	a.queryRunning = true
	a.queryStartedAt = started
	a.queryStart = started
	a.queryCancel = cancel
	a.queryMu.Unlock()

	a.queueUpdateDraw(func() { a.updateStatusBar("", a.currentResultRowCount()) })
	stopTicker := make(chan struct{})
	go a.tickQueryStatus(stopTicker)

	finish := func() {
		close(stopTicker)
		canceled := ctx.Err() == context.Canceled
		a.queryMu.Lock()
		a.queryRunning = false
		a.queryCancel = nil
		a.queryMu.Unlock()
		a.queueUpdateDraw(func() {
			if canceled {
				a.showQueryCanceled()
				return
			}
			a.updateStatusBar("", a.currentResultRowCount())
		})
	}
	return ctx, finish, true
}

func (a *App) tickQueryStatus(stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !a.isQueryRunning() {
				return
			}
			a.queueUpdateDraw(func() { a.updateStatusBar("", a.currentResultRowCount()) })
		case <-stop:
			return
		}
	}
}

func (a *App) isQueryRunning() bool {
	a.queryMu.Lock()
	defer a.queryMu.Unlock()
	return a.queryRunning
}

func (a *App) cancelActiveQuery() bool {
	a.queryMu.Lock()
	cancel := a.queryCancel
	running := a.queryRunning
	a.queryMu.Unlock()
	if !running || cancel == nil {
		return false
	}
	cancel()
	a.showQueryCanceled()
	return true
}

func (a *App) showQueryCanceled() {
	a.updateStatusBar("[yellow]Query canceled[-]", a.currentResultRowCount())
}

func (a *App) queryRunningStatus(width int, now time.Time) string {
	a.queryMu.Lock()
	running := a.queryRunning
	started := a.queryStartedAt
	name := a.dbName
	a.queryMu.Unlock()
	if !running {
		return ""
	}
	if name == "" {
		name = "current connection"
	}
	age := formatDuration(now.Sub(started))
	if width < 72 {
		return fmt.Sprintf("[yellow]Running %s — Esc cancels[-]", age)
	}
	return fmt.Sprintf("[yellow]Running %s — Esc/Ctrl+C cancel — %s[-]", age, truncateForDisplay(name, 22))
}

func newResultTable() *tview.Table {
	return tview.NewTable().SetBorders(true).SetSelectable(true, true).SetFixed(1, 0)
}
