package ui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type queryCell struct {
	text  string
	color tcell.Color
}

type queryExecutionResult struct {
	query          string
	isRead         bool
	columns        []string
	rows           [][]queryCell
	rowCount       int
	truncated      bool
	rowsAffected   int64
	elapsed        time.Duration
	err            error
	canceled       bool
	timedOut       bool
	requestedLimit int
	previewLimit   int
}

// ExecuteQuery starts SQL query execution without blocking the TUI input handler.
func (a *App) ExecuteQuery(query string) {
	a.startQueryExecution(query)
}

func (a *App) startQueryExecution(query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		a.ShowAlert(fmt.Sprintf("%s No query to execute.\n\nType a SQL query and press Enter.", iconInfo), "main")
		return
	}

	a.queryMu.Lock()
	if a.queryRunning {
		a.queryMu.Unlock()
		a.ShowAlert(fmt.Sprintf("%s A query is already running.\n\nPress Esc or Ctrl+C to cancel it first.", iconInfo), "main")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	a.queryRunning = true
	a.queryCancel = cancel
	a.queryStart = time.Now()
	started := a.queryStart
	a.queryMu.Unlock()

	a.results.SetTitle(fmt.Sprintf(" %s Results [yellow](Alt+R)[-] — [yellow]Running…[-] ", iconResults))
	a.updateStatusBar("[yellow]Running… (Esc/Ctrl+C to cancel)[-]", a.currentResultRowCount())

	go a.runQueryWorker(ctx, query, started)
}

func (a *App) cancelRunningQuery() bool {
	a.queryMu.Lock()
	cancel := a.queryCancel
	running := a.queryRunning
	a.queryMu.Unlock()
	if !running || cancel == nil {
		return false
	}
	cancel()
	return true
}

func (a *App) runQueryWorker(ctx context.Context, query string, started time.Time) {
	result := a.executeQueryWorker(ctx, query, started)
	a.app.QueueUpdateDraw(func() {
		a.finishQueryExecution(result)
	})
}

func (a *App) executeQueryWorker(ctx context.Context, query string, started time.Time) queryExecutionResult {
	result := queryExecutionResult{query: query, elapsed: time.Since(started)}
	defer func() { result.elapsed = time.Since(started) }()

	if a.db == nil {
		result.err = errors.New("not connected to any database")
		return result
	}

	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	if err := a.db.PingContext(pingCtx); err != nil {
		result.err = fmt.Errorf("connection lost: %w", err)
		result.canceled, result.timedOut = classifyQueryContextError(ctx, err)
		return result
	}

	firstToken := firstSQLToken(query)
	isRead := firstToken == "SELECT" || firstToken == "SHOW" || firstToken == "DESCRIBE" || firstToken == "DESC" || firstToken == "EXPLAIN" || firstToken == "PRAGMA" || firstToken == "WITH"
	result.isRead = isRead

	if a.activeConn != nil && a.activeConn.ReadOnly && !isRead {
		blockedToken := firstToken
		if blockedToken == "" {
			blockedToken = "UNKNOWN"
		}
		result.err = fmt.Errorf("read-only connection %q blocks write query (%s)", a.dbName, blockedToken)
		return result
	}

	if isRead {
		rows, err := a.db.QueryContext(ctx, query)
		if err != nil {
			result.err = err
			result.canceled, result.timedOut = classifyQueryContextError(ctx, err)
			return result
		}
		defer rows.Close()
		if err := a.collectQueryRows(rows, &result); err != nil {
			result.err = err
			result.canceled, result.timedOut = classifyQueryContextError(ctx, err)
		}
		return result
	}

	res, err := a.db.ExecContext(ctx, query)
	if err != nil {
		result.err = err
		result.canceled, result.timedOut = classifyQueryContextError(ctx, err)
		return result
	}
	result.rowsAffected, _ = res.RowsAffected()
	return result
}

func (a *App) collectQueryRows(rows *sql.Rows, result *queryExecutionResult) error {
	columnNames, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("could not read query columns: %w", err)
	}
	result.columns = columnNames
	result.requestedLimit = a.effectiveResultLimit()
	result.previewLimit = resolvedResultLimit(result.requestedLimit, len(columnNames))

	values := make([]any, len(columnNames))
	valuePtrs := make([]any, len(columnNames))
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	for rows.Next() {
		if result.previewLimit > 0 && result.rowCount >= result.previewLimit {
			result.truncated = true
			break
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return fmt.Errorf("row %d scan error: %w", result.rowCount+1, err)
		}
		row := make([]queryCell, len(columnNames))
		for c, val := range values {
			cellValue, cellColor := formatCellValue(val)
			row[c] = queryCell{text: cellValue, color: cellColor}
		}
		result.rows = append(result.rows, row)
		result.rowCount++
	}
	if !result.truncated {
		if err := rows.Err(); err != nil {
			return fmt.Errorf("result iteration error: %w", err)
		}
	}
	return nil
}

func (a *App) finishQueryExecution(result queryExecutionResult) {
	a.queryMu.Lock()
	if a.queryCancel != nil {
		a.queryCancel()
	}
	a.queryRunning = false
	a.queryCancel = nil
	a.queryMu.Unlock()

	switch {
	case result.canceled:
		a.updateStatusBar("[yellow]Canceled[-]", a.currentResultRowCount())
		a.results.SetTitle(fmt.Sprintf(" %s Results [yellow](Alt+R)[-] — [yellow]Canceled[-] ", iconResults))
		return
	case result.timedOut:
		a.updateStatusBar("[red]Timed out after 30s[-]", a.currentResultRowCount())
		a.results.SetTitle(fmt.Sprintf(" %s Results [yellow](Alt+R)[-] — [red]Timed out after 30s[-] ", iconResults))
		a.ShowAlert(fmt.Sprintf("%s Timed out after 30s", iconWarn), "main")
		return
	case result.err != nil:
		a.showQueryError(result.err, result.query)
		return
	}

	a.recordQueryHistory(result.query)
	if result.isRead {
		a.applyQueryResultTable(result)
		previewBadge := ""
		if result.truncated {
			previewBadge = fmt.Sprintf(" [#a6adc8](showing %d)[-]", result.rowCount)
		}
		a.results.SetTitle(fmt.Sprintf(" %s Results [yellow](Alt+R)[-] — [green]%d rows[-]%s in [teal]%s[-] ", iconResults, result.rowCount, previewBadge, formatDuration(result.elapsed)))
		a.results.ScrollToBeginning()
		a.applyColumnWidths()
		a.updateStatusBar(fmt.Sprintf("[teal]%s[-]", formatDuration(result.elapsed)), result.rowCount)
		if result.truncated {
			a.flashStatus(fmt.Sprintf("[yellow]%s Showing first %d rows (%s). Refine with LIMIT/OFFSET or use Alt+0 for auto max.[-]", iconInfo, result.rowCount, a.resultLimitReadable()), result.rowCount, 2200*time.Millisecond)
		}
		a.app.SetFocus(a.results)
		return
	}

	a.updateStatusBar(fmt.Sprintf("[green]Success in %s[-]", formatDuration(result.elapsed)), a.currentResultRowCount())
	a.ShowAlert(fmt.Sprintf("%s Query executed successfully\n\nRows affected: %d\nTime: %s", iconSuccess, result.rowsAffected, formatDuration(result.elapsed)), "main")
}

func (a *App) applyQueryResultTable(result queryExecutionResult) {
	a.results.Clear()
	if len(result.columns) == 0 {
		a.results.SetCell(0, 0, &tview.TableCell{Text: iconInfo + " No columns returned", Color: overlay0})
		return
	}
	hasMultipleColumns := len(result.columns) > 1
	compactFirstCol := hasMultipleColumns && isLikelyCompactColumn(result.columns[0])
	for i, name := range result.columns {
		expansion := 0
		if !hasMultipleColumns || i > 0 {
			expansion = 1
		}
		cell := tview.NewTableCell(strings.ToUpper(name)).SetTextColor(peach).SetSelectable(false).SetBackgroundColor(mantle).SetExpansion(expansion)
		if compactFirstCol && i == 0 {
			cell.SetMaxWidth(18)
		}
		a.results.SetCell(0, i, cell)
	}
	for r, row := range result.rows {
		for c, val := range row {
			expansion := 0
			if !hasMultipleColumns || c > 0 {
				expansion = 1
			}
			cell := tview.NewTableCell(val.text).SetTextColor(val.color).SetExpansion(expansion)
			if compactFirstCol && c == 0 {
				cell.SetMaxWidth(18)
			}
			a.results.SetCell(r+1, c, cell)
		}
	}
	if result.rowCount == 0 {
		a.results.SetCell(1, 0, &tview.TableCell{Text: iconInfo + " No rows returned", Color: overlay0})
	}
}

func classifyQueryContextError(ctx context.Context, err error) (bool, bool) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return false, true
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return true, false
	}
	return false, false
}
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
