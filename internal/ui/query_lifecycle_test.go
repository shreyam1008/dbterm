package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/rivo/tview"
)

func TestIsReadSQLToken(t *testing.T) {
	for _, token := range []string{"SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "PRAGMA", "WITH"} {
		if !isReadSQLToken(token) {
			t.Fatalf("expected %q to be read-only", token)
		}
	}

	for _, token := range []string{"INSERT", "UPDATE", "DELETE", "CREATE", ""} {
		if isReadSQLToken(token) {
			t.Fatalf("expected %q not to be read-only", token)
		}
	}
}

func TestQueryRunningStatus(t *testing.T) {
	app := &App{
		dbName:         "analytics-primary-connection",
		queryRunning:   true,
		queryStartedAt: time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC),
	}

	status := app.queryRunningStatus(100, app.queryStartedAt.Add(2500*time.Millisecond))
	if !strings.Contains(status, "Running 2.50s") {
		t.Fatalf("expected query age in status, got %q", status)
	}
	if !strings.Contains(status, "Esc/Ctrl+C cancel") {
		t.Fatalf("expected cancel shortcut in status, got %q", status)
	}
	if !strings.Contains(status, "analytics-primary") {
		t.Fatalf("expected connection name in status, got %q", status)
	}

	app.queryRunning = false
	if got := app.queryRunningStatus(100, time.Now()); got != "" {
		t.Fatalf("expected no status when query is not running, got %q", got)
	}
}

func TestStartQueryLifecyclePreventsDuplicateAndCancel(t *testing.T) {
	app := &App{
		app:       tview.NewApplication(),
		results:   tview.NewTable(),
		statusBar: tview.NewTextView(),
	}

	ctx, finish, ok := app.startQueryLifecycle()
	if !ok {
		t.Fatal("expected first query lifecycle to start")
	}
	if !app.isQueryRunning() {
		t.Fatal("expected query to be marked running")
	}

	_, duplicateFinish, ok := app.startQueryLifecycle()
	defer duplicateFinish()
	if ok {
		t.Fatal("expected duplicate query lifecycle to be rejected")
	}

	if !app.cancelActiveQuery() {
		t.Fatal("expected active query cancellation to be requested")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("expected query context to be canceled")
	}

	finish()
	if app.isQueryRunning() {
		t.Fatal("expected query to be marked idle after finish")
	}
}
