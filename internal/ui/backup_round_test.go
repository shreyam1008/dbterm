package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
	"github.com/shreyam1008/dbterm/internal/config"
)

func backupRoundTestApp(t *testing.T) *App {
	t.Helper()
	store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	application := tview.NewApplication()
	pages := tview.NewPages()
	pages.AddPage("origin", tview.NewTextView(), true, true)
	application.SetRoot(pages, true)
	return &App{app: application, pages: pages, backupStore: store, store: &config.Store{}, settings: config.DefaultSettings()}
}

func backupRoundKey(a *App, key tcell.Key, r rune) {
	a.app.GetFocus().InputHandler()(tcell.NewEventKey(key, r, tcell.ModNone), func(p tview.Primitive) { a.app.SetFocus(p) })
}

func TestBackupActivityCombinesFiltersAndReturnsToOrigin(t *testing.T) {
	a := backupRoundTestApp(t)
	ctx := context.Background()
	job := backupcore.Job{Name: "Orders nightly", ConnectionID: "orders", Destination: t.TempDir(), Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual}}
	if err := a.backupStore.UpsertJob(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := a.backupStore.ClaimJob(ctx, job.ID, "fixture", time.Now()); err != nil {
		t.Fatal(err)
	}
	run, err := a.backupStore.StartRun(ctx, job.ID, backupcore.TriggerManual, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	run.Status, run.Error, run.FinishedAt = backupcore.RunFailed, "connection refused", time.Now()
	run.Attempts = []backupcore.BackupAttempt{{Number: 1, Phase: "dump", Error: run.Error, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt}}
	if err := a.backupStore.FinishRun(ctx, &run, "fixture"); err != nil {
		t.Fatal(err)
	}
	copyJob := backupcore.CopyJob{Name: "Vault mirror", Mode: backupcore.CopyModePush, Trigger: backupcore.CopyTriggerManual, Source: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: t.TempDir()}, Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: t.TempDir()}}
	if err := a.backupStore.UpsertCopyJob(ctx, &copyJob); err != nil {
		t.Fatal(err)
	}
	if _, err := a.backupStore.StartCopyRun(ctx, copyJob.ID, backupcore.CopyTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}
	a.showBackupCenter()
	backupRoundKey(a, tcell.KeyRune, 'h')
	list := a.app.GetFocus().(*tview.List)
	if list.GetItemCount() != 2 {
		t.Fatalf("activity items=%d", list.GetItemCount())
	}
	first, _ := list.GetItemText(0)
	if !strings.Contains(first, "Vault mirror") {
		t.Fatalf("not sorted newest first: %s", first)
	}
	backupRoundKey(a, tcell.KeyRune, 'f')
	if list.GetItemCount() != 1 {
		t.Fatal("failure filter kept running copy")
	}
	backupRoundKey(a, tcell.KeyRune, '/')
	query := a.app.GetFocus().(*tview.InputField)
	query.SetText(run.ID)
	backupRoundKey(a, tcell.KeyEnter, 0)
	if a.app.GetFocus() != list {
		t.Fatal("filter Enter did not return list focus")
	}
	backupRoundKey(a, tcell.KeyEnter, 0)
	detail := a.app.GetFocus().(*tview.TextView)
	if !strings.Contains(detail.GetText(true), "Generation attempts:") || !strings.Contains(detail.GetText(true), "connection refused") {
		t.Fatal("run detail lost attempt errors")
	}
	backupRoundKey(a, tcell.KeyEscape, 0)
	if a.app.GetFocus() != list {
		t.Fatal("detail close lost activity focus")
	}
	backupRoundKey(a, tcell.KeyEscape, 0)
	backupRoundKey(a, tcell.KeyEscape, 0)
	if front, _ := a.pages.GetFrontPage(); front != "origin" {
		t.Fatalf("center lost original workspace: %s", front)
	}
}

func TestBackupLogFilterRefreshAndReturnFocus(t *testing.T) {
	a := backupRoundTestApp(t)
	logDir := t.TempDir()
	t.Setenv("DBTERM_LOG_DIR", logDir)
	logPath := filepath.Join(logDir, "dbterm-backup-agent.log")
	if err := os.WriteFile(logPath, []byte("Orders started\nVault FAILED\nOrders complete\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.showBackupCenter()
	origin := a.app.GetFocus()
	backupRoundKey(a, tcell.KeyRune, 'l')
	view := a.app.GetFocus().(*tview.TextView)
	backupRoundKey(a, tcell.KeyRune, '/')
	a.app.GetFocus().(*tview.InputField).SetText("failed")
	backupRoundKey(a, tcell.KeyEnter, 0)
	if got := view.GetText(false); got != "Vault FAILED" {
		t.Fatalf("filtered text=%q", got)
	}
	if err := os.WriteFile(logPath, []byte("New failed attempt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backupRoundKey(a, tcell.KeyRune, 'r')
	if got := view.GetText(false); got != "New failed attempt" {
		t.Fatalf("refresh lost filter: %q", got)
	}
	backupRoundKey(a, tcell.KeyRune, 'f')
	if !strings.Contains(view.GetTitle(), "following") {
		t.Fatal("follow not enabled")
	}
	backupRoundKey(a, tcell.KeyUp, 0)
	if !strings.Contains(view.GetTitle(), "paused") {
		t.Fatal("scrolling did not pause follow")
	}
	backupRoundKey(a, tcell.KeyEscape, 0)
	if a.app.GetFocus() != origin {
		t.Fatal("logs did not return to invoking focus")
	}
}

func TestBackupOverviewSeparatesGenerationAndCopyFailures(t *testing.T) {
	now := time.Now().UTC()
	job := backupcore.Job{ID: "orders", Name: "Orders", Enabled: true, Schedule: backupcore.Schedule{Kind: backupcore.ScheduleDaily}, NextRunAt: now.Add(time.Hour)}
	copyJob := backupcore.CopyJob{ID: "vault", Name: "Vault", Trigger: backupcore.CopyTriggerManual}
	latest := map[string]backupcore.Run{job.ID: {Status: backupcore.RunSucceeded}}
	verified := map[string]backupcore.Run{job.ID: {Status: backupcore.RunSucceeded, FinishedAt: now.Add(-time.Minute)}}
	copyRuns := map[string]backupcore.CopyRun{copyJob.ID: {Status: backupcore.RunFailed, Error: "connection refused"}}
	value := backupOverviewText([]backupcore.Job{job}, latest, verified, []backupcore.CopyJob{copyJob}, copyRuns, backupcore.AgentStatus{}, now)
	for _, want := range []string{"1 plans · 0 need attention", "1 copy job · 1 need attention", "agent off · scheduled work needs attention", "NEXT", "Orders"} {
		if !strings.Contains(value, want) {
			t.Fatalf("missing %s: %s", want, value)
		}
	}
	if strings.Contains(value, "restore tested") {
		t.Fatal("overview overstated verification")
	}
}

func TestBackupRetryEditorValidation(t *testing.T) {
	if attempts, initial, maximum, err := parseBackupRetryFields("3", "2", "60"); err != nil || attempts != 3 || initial != 2 || maximum != 60 {
		t.Fatalf("%d %d %d %v", attempts, initial, maximum, err)
	}
	for _, values := range [][3]string{{"0", "2", "60"}, {"11", "2", "60"}, {"3", "60", "2"}, {"3", "oops", "60"}} {
		if _, _, _, err := parseBackupRetryFields(values[0], values[1], values[2]); err == nil {
			t.Fatalf("accepted %v", values)
		}
	}
}

func TestBackupLogFollowReadsNewLinesOnTerminalLoop(t *testing.T) {
	a := backupRoundTestApp(t)
	logDir := t.TempDir()
	t.Setenv("DBTERM_LOG_DIR", logDir)
	logPath := filepath.Join(logDir, "dbterm-backup-agent.log")
	if err := os.WriteFile(logPath, []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	screen := tcell.NewSimulationScreen("UTF-8")
	a.app.SetScreen(screen)
	screen.SetSize(80, 24)
	a.showBackupCenter()
	a.showBackupAgentLogs()
	view := a.app.GetFocus().(*tview.TextView)
	changed := make(chan struct{}, 1)
	view.SetChangedFunc(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	done := make(chan error, 1)
	go func() { done <- a.app.Run() }()
	t.Cleanup(func() {
		a.app.QueueUpdateDraw(func() { backupRoundKey(a, tcell.KeyEscape, 0) })
		a.app.Stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("terminal did not stop")
		}
	})
	a.app.QueueUpdateDraw(func() { backupRoundKey(a, tcell.KeyRune, 'f') })
	if err := os.WriteFile(logPath, []byte("new line from agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-changed:
			found := false
			a.app.QueueUpdateDraw(func() { found = strings.Contains(view.GetText(false), "new line from agent") })
			if found {
				return
			}
		case <-deadline.C:
			t.Fatal("follow did not show appended log data")
		}
	}
}

func TestBackupPagesOwnRefreshKeys(t *testing.T) {
	a := backupRoundTestApp(t)
	a.setupKeyBindings()
	for _, name := range []string{pageBackupCenter, "backupActivity", "backupAgentLogs", "backupRunDetails"} {
		a.pages.AddAndSwitchToPage(name, tview.NewTextView(), true)
		event := tcell.NewEventKey(tcell.KeyF5, 0, tcell.ModNone)
		if got := a.app.GetInputCapture()(event); got != event {
			t.Fatalf("global workspace swallowed F5 on %s", name)
		}
	}
}
