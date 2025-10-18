package ui

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ExecuteQuery runs a SQL query and displays results or affected row count
func (a *App) ExecuteQuery(query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return
	}

	// Check connection health before executing
	if a.db == nil {
		a.ShowAlert(fmt.Sprintf("%s Not connected to any database.\n\nPress Alt+D to go to Dashboard and connect.", iconWarn), "main")
		return
	}

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if err := a.db.PingContext(pingCtx); err != nil {
		a.ShowAlert(fmt.Sprintf("%s Connection lost: %v\n\nPress Alt+D to reconnect from Dashboard.", iconWarn, err), "main")
		return
	}

	firstToken := firstSQLToken(query)

	// Detect read queries (SELECT, SHOW, DESCRIBE, EXPLAIN, PRAGMA, WITH)
	isRead := firstToken == "SELECT" ||
		firstToken == "SHOW" ||
		firstToken == "DESCRIBE" ||
		firstToken == "DESC" ||
		firstToken == "EXPLAIN" ||
		firstToken == "PRAGMA" ||
		firstToken == "WITH"

	if a.activeConn != nil && a.activeConn.ReadOnly && !isRead {
		blockedToken := firstToken
		if blockedToken == "" {
			blockedToken = "UNKNOWN"
		}
		a.ShowAlert(
			fmt.Sprintf(
				"%s Read-only connection \"%s\" blocks write queries.\n\nBlocked statement: %s\n\nRun a read query (SELECT/SHOW/EXPLAIN/PRAGMA) or disable Read-Only in connection settings.",
				iconWarn,
				a.dbName,
				blockedToken,
			),
			"main",
		)
		return
	}

	if isRead {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		rows, err := a.db.QueryContext(ctx, query)
		if err != nil {
			a.showQueryError(err, query)
			return
		}
		defer rows.Close()

		columnNames, err := rows.Columns()
		if err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not read query columns:\n\n%v", iconWarn, err), "main")
			return
		}

		requestedLimit := a.effectiveResultLimit()
		previewLimit := resolvedResultLimit(requestedLimit, len(columnNames))
		rowCount, truncated, err := populateTableWithLimit(a.results, rows, previewLimit)
		if err != nil {
			a.ShowAlert(fmt.Sprintf("%s Error reading results:\n\n%v", iconWarn, err), "main")
			return
		}

		elapsed := time.Since(a.queryStart)
		previewBadge := ""
		if truncated {
			previewBadge = fmt.Sprintf(" [#a6adc8](showing %d)[-]", rowCount)
		}
		a.results.SetTitle(fmt.Sprintf(" %s Results [yellow](Alt+R)[-] — [green]%d rows[-]%s in [teal]%s[-] ", iconResults, rowCount, previewBadge, formatDuration(elapsed)))
		a.results.ScrollToBeginning()
		a.applyColumnWidths()
		a.updateStatusBar(fmt.Sprintf("[teal]%s[-]", formatDuration(elapsed)), rowCount)
		a.recordQueryHistory(query)
		if truncated {
			a.flashStatus(
				fmt.Sprintf("[yellow]%s Showing first %d rows (%s). Refine with LIMIT/OFFSET or use Alt+0 for auto max.[-]", iconInfo, rowCount, a.resultLimitReadable()),
				rowCount,
				2200*time.Millisecond,
			)
		}
		a.app.SetFocus(a.results)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		res, err := a.db.ExecContext(ctx, query)
		if err != nil {
			a.showQueryError(err, query)
			return
		}

		rowsAffected, _ := res.RowsAffected()
		elapsed := time.Since(a.queryStart)
		a.recordQueryHistory(query)

		a.ShowAlert(
			fmt.Sprintf("%s Query executed successfully\n\nRows affected: %d\nTime: %s", iconSuccess, rowsAffected, formatDuration(elapsed)),
			"main",
		)

		// Refresh tables & results in case schema changed
		if err := a.refreshData(); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Query succeeded, but refresh failed:\n\n%v", iconWarn, err), "main")
		}
	}
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
