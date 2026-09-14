package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
)

type backupActivityEntry struct {
	name, jobID, runID, kind, searchable, detail string
	started                                      time.Time
	status                                       backupcore.RunStatus
	run                                          *backupcore.Run
	copyRun                                      *backupcore.CopyRun
}

func (a *App) showBackupActivity() {
	const page = "backupActivity"
	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).SetTitle(" Activity · backups and copies ").SetBorderColor(surface1).SetTitleColor(mauve)
	list.SetBackgroundColor(bg)
	list.SetMainTextColor(text).SetSecondaryTextColor(subtext0).SetSelectedBackgroundColor(surface0).SetSelectedTextColor(green)
	query := tview.NewInputField().SetLabel(" Filter: ").SetFieldWidth(0)
	query.SetFieldBackgroundColor(surface0).SetFieldTextColor(text).SetLabelColor(blue)
	query.SetBackgroundColor(bg)
	var entries, visible []backupActivityEntry
	failuresOnly := false
	filter := func() {
		selectedID := ""
		if index := list.GetCurrentItem(); index >= 0 && index < len(visible) {
			selectedID = visible[index].runID
		}
		list.Clear()
		visible = nil
		needle := strings.ToLower(strings.TrimSpace(query.GetText()))
		selected := 0
		for _, entry := range entries {
			if failuresOnly && entry.status != backupcore.RunFailed && entry.status != backupcore.RunCanceled {
				continue
			}
			if needle != "" && !strings.Contains(entry.searchable, needle) {
				continue
			}
			if entry.runID == selectedID {
				selected = len(visible)
			}
			visible = append(visible, entry)
			list.AddItem(tview.Escape(fmt.Sprintf(" %s  %s  %s · %s", entry.started.Local().Format("Jan 02 15:04"), strings.ToUpper(string(entry.status)), entry.kind, entry.name)), tview.Escape(" "+entry.detail), 0, nil)
		}
		mode := "all runs"
		if failuresOnly {
			mode = "failed / canceled"
		}
		list.SetTitle(fmt.Sprintf(" Activity · %s · %d matches (latest 250 of each kind) ", mode, len(visible)))
		if len(visible) == 0 {
			list.AddItem(" No matching runs", " Change the filter or press F to show all statuses.", 0, nil)
		} else {
			list.SetCurrentItem(selected)
		}
	}
	refresh := func() {
		ctx := context.Background()
		runs, err := a.backupStore.ListRuns(ctx, "", 250)
		if err != nil {
			a.ShowAlert(tview.Escape(err.Error()), page)
			return
		}
		copies, err := a.backupStore.ListCopyRuns(ctx, "", 250)
		if err != nil {
			a.ShowAlert(tview.Escape(err.Error()), page)
			return
		}
		jobs, err := a.backupStore.ListJobs(ctx)
		if err != nil {
			a.ShowAlert(tview.Escape(err.Error()), page)
			return
		}
		copyJobs, err := a.backupStore.ListCopyJobs(ctx)
		if err != nil {
			a.ShowAlert(tview.Escape(err.Error()), page)
			return
		}
		names := make(map[string]string)
		for _, job := range jobs {
			names[job.ID] = job.Name
		}
		for _, job := range copyJobs {
			names[job.ID] = job.Name
		}
		entries = nil
		for _, run := range runs {
			detail := nonEmptyOr(run.Error, nonEmptyOr(run.Artifact.Path, "no artifact recorded"))
			entry := backupActivityEntry{name: nonEmptyOr(names[run.JobID], run.JobID), jobID: run.JobID, runID: run.ID, kind: "backup", started: run.StartedAt, status: run.Status, detail: detail, run: &run}
			entry.searchable = strings.ToLower(entry.name + " " + entry.jobID + " " + entry.runID + " backup " + string(entry.status) + " " + detail + " " + run.RetentionError + " " + run.NotificationError)
			entries = append(entries, entry)
		}
		for _, run := range copies {
			detail := nonEmptyOr(run.Error, fmt.Sprintf("%d copied · %d already present · %s", len(run.Artifacts), run.AlreadyPresent, copyRunSpeedLabel(run, true)))
			entry := backupActivityEntry{name: nonEmptyOr(names[run.JobID], run.JobID), jobID: run.JobID, runID: run.ID, kind: "copy", started: run.StartedAt, status: run.Status, detail: detail, copyRun: &run}
			entry.searchable = strings.ToLower(entry.name + " " + entry.jobID + " " + entry.runID + " copy " + string(entry.status) + " " + detail + " " + strings.Join(run.Warnings, " "))
			entries = append(entries, entry)
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].started.After(entries[j].started) })
		filter()
	}
	selected := func() *backupActivityEntry {
		index := list.GetCurrentItem()
		if index < 0 || index >= len(visible) {
			return nil
		}
		return &visible[index]
	}
	open := func() {
		entry := selected()
		if entry == nil {
			return
		}
		if entry.run != nil {
			a.showBackupRunDetails(*entry.run, entry.name)
			return
		}
		run := entry.copyRun
		detail := fmt.Sprintf("Copy: %s\nRun: %s\nStatus: %s\n%s\n\nWarnings: %s\nRetention: %s\nEmail: %s", entry.name, run.ID, run.Status, entry.detail, strings.Join(run.Warnings, "\n"), run.RetentionError, run.NotificationError)
		for _, artifact := range run.Artifacts {
			detail += fmt.Sprintf("\n\nArtifact: %s\nDestination: %s\nPublication: %s\nVerification: %s", artifact.ArtifactID, artifact.Destination, artifact.PublicationState, artifact.Verification)
		}
		jobID := entry.jobID
		a.showBackupActivityDetails(tview.Escape(detail), func() { a.runBackupCopyNow(jobID) }, run.Status != backupcore.RunRunning)
	}
	list.SetSelectedFunc(func(int, string, string, rune) { open() })
	query.SetChangedFunc(func(string) { filter() })
	query.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter || key == tcell.KeyEscape || key == tcell.KeyTab {
			a.app.SetFocus(list)
		}
	})
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.pages.RemovePage(page)
			a.showBackupCenter()
			return nil
		}
		if event.Key() == tcell.KeyF5 {
			refresh()
			return nil
		}
		if event.Key() == tcell.KeyTab {
			a.app.SetFocus(query)
			return nil
		}
		if key, ok := plainShortcutRune(event); ok {
			switch key {
			case '/':
				a.app.SetFocus(query)
				return nil
			case 'f':
				failuresOnly = !failuresOnly
				filter()
				return nil
			case 'l':
				a.showBackupAgentLogs()
				return nil
			case 'r':
				if entry := selected(); entry != nil && entry.status != backupcore.RunRunning {
					if entry.kind == "backup" {
						a.runBackupJobNow(entry.jobID)
					} else {
						a.runBackupCopyNow(entry.jobID)
					}
				}
				return nil
			}
		}
		return event
	})
	footer := tview.NewTextView().SetDynamicColors(true).SetText(" [yellow]/[-] Filter · [yellow]F[-] Failures · [yellow]Enter[-] Details · [yellow]R[-] Run again · [yellow]L[-] Logs · [yellow]F5[-] Refresh · [yellow]Esc[-] Back")
	footer.SetBackgroundColor(crust)
	body := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(query, 1, 0, false).AddItem(list, 0, 1, true).AddItem(footer, 1, 0, false)
	layout := &backupAuxLayout{Flex: body, footer: footer, hints: []string{footer.GetText(false), " [yellow]/[-] Filter · [yellow]F[-] Failures · [yellow]Enter[-] Detail · [yellow]R[-] Rerun · [yellow]L[-] Logs · [yellow]F5[-] Refresh · [yellow]Esc[-] Back", " [yellow]/[-] Filter · [yellow]F[-] Failures · [yellow]R[-] Rerun · [yellow]Esc[-] Back"}}
	a.pages.AddAndSwitchToPage(page, layout, true)
	refresh()
	a.app.SetFocus(list)
}

func (a *App) showBackupActivityDetails(message string, rerun func(), canRun bool) {
	const page = "backupRunDetails"
	returnPage, _ := a.pages.GetFrontPage()
	returnFocus := a.app.GetFocus()
	view := tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetScrollable(true).SetText(message)
	view.SetBorder(true).SetTitle(" Run Details ").SetBorderColor(surface1).SetTitleColor(mauve).SetBackgroundColor(bg)
	view.SetTextColor(text)
	closeDetails := func() {
		a.pages.RemovePage(page)
		if a.pages.HasPage(returnPage) {
			a.pages.ShowPage(returnPage)
			a.app.SetFocus(returnFocus)
		} else {
			a.showBackupCenter()
		}
	}
	view.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			closeDetails()
			return nil
		}
		if key, ok := plainShortcutRune(event); ok {
			switch key {
			case 'r':
				if canRun {
					closeDetails()
					rerun()
				}
				return nil
			case 'l':
				a.showBackupAgentLogs()
				return nil
			}
		}
		return event
	})
	footerText := " [yellow]Arrow / Page keys[-] Scroll · [yellow]L[-] Agent logs · [yellow]Esc[-] Back"
	if canRun {
		footerText = " [yellow]Arrow / Page keys[-] Scroll · [yellow]R[-] Run again · [yellow]L[-] Agent logs · [yellow]Esc[-] Back"
	}
	footer := tview.NewTextView().SetDynamicColors(true).SetText(footerText)
	footer.SetBackgroundColor(crust)
	layout := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(view, 0, 1, true).AddItem(footer, 1, 0, false)
	a.pages.AddAndSwitchToPage(page, &backupAuxLayout{Flex: layout, footer: footer, hints: []string{footerText, " [yellow]↑/↓[-] Scroll · [yellow]L[-] Logs · [yellow]Esc[-] Back"}}, true)
	a.app.SetFocus(view)
}
