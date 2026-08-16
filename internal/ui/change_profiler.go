package ui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	profiler "github.com/shreyam1008/dbterm/internal/changeprofiler"
	appformat "github.com/shreyam1008/dbterm/internal/format"
)

const (
	pageChangeProfiler      = "changeProfiler"
	pageProfilerName        = "changeProfilerName"
	pageProfilerTableReview = "changeProfilerTableReview"
	pageProfilerReport      = "changeProfilerReport"
	pageProfilerDiffDetail  = "changeProfilerDiffDetail"
	profilerReportRowLimit  = 5000
)

func (a *App) ensureProfilerStore() error {
	if a.profilerStore != nil {
		return nil
	}
	store, err := profiler.OpenDefaultStore()
	if err != nil {
		return err
	}
	a.profilerStore = store
	return nil
}

func (a *App) showChangeProfiler() {
	if err := a.ensureProfilerStore(); err != nil {
		returnPage, _ := a.pages.GetFrontPage()
		a.ShowAlert(fmt.Sprintf("%s Change Profiler is unavailable:\n\n%v", iconWarn, err), returnPage)
		return
	}
	if !a.pages.HasPage(pageChangeProfiler) {
		a.profilerReturnPage, _ = a.pages.GetFrontPage()
		a.profilerReturnFocus = a.app.GetFocus()
		if a.profilerReturnPage == "" {
			a.profilerReturnPage = "dashboard"
		}
	}
	anchors, err := a.profilerStore.ListAnchors(context.Background())
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not load anchors:\n\n%v", iconWarn, err), a.profilerReturnPage)
		return
	}

	header := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	header.SetBackgroundColor(bg)
	active := 0
	for _, anchor := range anchors {
		if anchor.Status == profiler.StatusActive {
			active++
		}
	}
	header.SetText(fmt.Sprintf("\n[::b][#cba6f7]Change Profiler[-][-]\n[#a6adc8]%d named anchors  │  %d active  │  no polling or server-side objects[-]", len(anchors), active))

	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).SetTitle(fmt.Sprintf(" Anchors (%d) ", len(anchors))).SetTitleColor(mauve).SetBorderColor(surface1)
	list.SetBackgroundColor(bg)
	list.SetMainTextColor(text).SetSecondaryTextColor(subtext0)
	list.SetSelectedBackgroundColor(surface0).SetSelectedTextColor(green)
	if len(anchors) == 0 {
		list.AddItem("  [::b][#f9e2af]No anchors yet[-][-]", "  Connect to a database and press N to capture a starting point.", 0, nil)
	} else {
		for _, anchor := range anchors {
			state := profilerStatusLabel(anchor.Status)
			counts := fmt.Sprintf("+%d  ~%d  -%d  Δ%d", anchor.Inserted, anchor.Updated, anchor.Deleted, anchor.SchemaChanges)
			list.AddItem(fmt.Sprintf("  %s  [::b]%s[-]", state, tview.Escape(anchor.Name)),
				fmt.Sprintf("  %s  │  %s  │  %s", tview.Escape(anchor.TargetLabel), anchor.StartedAt.Local().Format("02 Jan 15:04"), counts), 0, nil)
		}
	}
	selectedIndex := 0
	for index, anchor := range anchors {
		if anchor.ID == a.profilerSelectedAnchor {
			selectedIndex = index
			break
		}
	}

	detail := tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetWordWrap(true)
	detail.SetBorder(true).SetTitle(" Selected Anchor ").SetTitleColor(mauve).SetBorderColor(surface1).SetBackgroundColor(mantle)
	updateDetail := func(index int) {
		if index < 0 || index >= len(anchors) {
			detail.SetText(" [#89b4fa]N[-] starts a named anchor on the current database.\n [#a6adc8]Risky or keyless tables require explicit selection before capture.[-]")
			return
		}
		anchor := anchors[index]
		writer := "Unknown — no database audit trail"
		activity, _ := a.profilerStore.ListActivity(context.Background(), anchor.ID, 3)
		activityLabel := "none recorded"
		if len(activity) > 0 {
			latest := activity[0]
			activityLabel = fmt.Sprintf("latest of %d recorded: %s, %d rows — evidence only", len(activity), latest.OccurredAt.Local().Format("15:04:05"), latest.RowsAffected)
		}
		detail.SetText(fmt.Sprintf(" [#89b4fa]TARGET[-]      %s\n [#89b4fa]OBSERVED VIA[-] %s\n [#89b4fa]BASELINE[-]    %s\n [#89b4fa]CONSISTENCY[-] %s\n [#89b4fa]CHANGES[-]     [green]+%d[-]  [yellow]~%d[-]  [red]-%d[-]  [#cba6f7]Δ%d[-]\n [#89b4fa]DBTERM WRITES[-] %s\n [#89b4fa]WRITER[-]      %s",
			tview.Escape(anchor.TargetLabel), tview.Escape(anchor.ConnectionLabel),
			anchor.StartedAt.Local().Format("Mon 02 Jan 2006, 15:04:05 MST"), tview.Escape(string(anchor.Consistency)),
			anchor.Inserted, anchor.Updated, anchor.Deleted, anchor.SchemaChanges, tview.Escape(activityLabel), writer))
	}
	list.SetChangedFunc(func(index int, _, _ string, _ rune) {
		if index >= 0 && index < len(anchors) {
			a.profilerSelectedAnchor = anchors[index].ID
		}
		updateDetail(index)
	})
	if len(anchors) > 0 {
		list.SetCurrentItem(selectedIndex)
		a.profilerSelectedAnchor = anchors[selectedIndex].ID
	}
	updateDetail(selectedIndex)

	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(" [yellow]N[-] New  │  [yellow]S[-] Scan  │  [yellow]F[-] Finish  │  [yellow]Enter[-] Report  │  [yellow]E[-] Rename  │  [yellow]D[-] Delete  │  [yellow]Esc[-] Back ")

	closeCenter := func() {
		a.pages.RemovePage(pageChangeProfiler)
		returnPage, returnFocus := a.profilerReturnPage, a.profilerReturnFocus
		a.profilerReturnPage, a.profilerReturnFocus = "", nil
		if returnPage != "" && a.pages.HasPage(returnPage) {
			a.pages.ShowPage(returnPage).SendToFront(returnPage)
			if returnFocus != nil {
				a.app.SetFocus(returnFocus)
			}
			return
		}
		a.showDashboard()
	}
	selectedAnchor := func() (profiler.Anchor, bool) {
		index := list.GetCurrentItem()
		if index < 0 || index >= len(anchors) {
			a.ShowAlert(fmt.Sprintf("%s Create an anchor first (N).", iconInfo), pageChangeProfiler)
			return profiler.Anchor{}, false
		}
		return anchors[index], true
	}
	list.SetSelectedFunc(func(index int, _, _ string, _ rune) {
		if index >= 0 && index < len(anchors) {
			a.showProfilerReport(anchors[index])
		}
	})
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyEscape:
			closeCenter()
			return nil
		case event.Rune() == 'n' || event.Rune() == 'N':
			a.showProfilerNameForm("")
			return nil
		case event.Rune() == 's' || event.Rune() == 'S':
			if anchor, ok := selectedAnchor(); ok {
				a.runProfilerScan(anchor, false)
			}
			return nil
		case event.Rune() == 'f' || event.Rune() == 'F':
			if anchor, ok := selectedAnchor(); ok {
				a.runProfilerScan(anchor, true)
			}
			return nil
		case event.Rune() == 'e' || event.Rune() == 'E':
			if anchor, ok := selectedAnchor(); ok {
				a.showProfilerNameForm(anchor.ID)
			}
			return nil
		case event.Rune() == 'd' || event.Rune() == 'D':
			if anchor, ok := selectedAnchor(); ok {
				a.confirmDeleteProfilerAnchor(anchor)
			}
			return nil
		}
		return event
	})

	container := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 4, 0, false).
		AddItem(tview.NewFlex().AddItem(list, 0, 2, true).AddItem(detail, 0, 3, false), 0, 1, true).
		AddItem(footer, 1, 0, false)
	a.pages.RemovePage(pageChangeProfiler)
	a.pages.AddPage(pageChangeProfiler, container, true, true)
	a.app.SetFocus(list)
}

func (a *App) showProfilerNameForm(anchorID string) {
	if anchorID == "" && a.db == nil {
		a.ShowAlert(fmt.Sprintf("%s Connect to a database before starting an anchor.", iconInfo), pageChangeProfiler)
		return
	}
	defaultName := "Anchor " + time.Now().Format("2006-01-02 15:04")
	if anchorID != "" {
		if anchor, err := a.profilerStore.GetAnchor(context.Background(), anchorID); err == nil {
			defaultName = anchor.Name
		}
	}
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" Name Anchor ").SetTitleColor(mauve).SetBorderColor(surface1)
	form.SetBackgroundColor(bg)
	form.SetFieldBackgroundColor(mantle).SetFieldTextColor(text).SetLabelColor(text)
	form.AddInputField("Name", defaultName, 48, nil, nil)
	form.AddButton("Save", func() {
		name := strings.TrimSpace(form.GetFormItemByLabel("Name").(*tview.InputField).GetText())
		if name == "" {
			a.ShowAlert(fmt.Sprintf("%s Anchor name is required.", iconWarn), pageProfilerName)
			return
		}
		if anchorID != "" {
			if err := a.profilerStore.RenameAnchor(context.Background(), anchorID, name); err != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not rename anchor:\n\n%v", iconWarn, err), pageProfilerName)
				return
			}
			a.pages.RemovePage(pageProfilerName)
			a.showChangeProfiler()
			return
		}
		a.pages.RemovePage(pageProfilerName)
		a.runProfilerPreflight(name)
	})
	form.AddButton("Cancel", func() { a.pages.RemovePage(pageProfilerName); a.showChangeProfiler() })
	form.SetCancelFunc(func() { a.pages.RemovePage(pageProfilerName); a.showChangeProfiler() })
	modalW, modalH := a.modalSize(54, 76, 10, 14)
	grid := tview.NewGrid().SetColumns(0, modalW, 0).SetRows(0, modalH, 0).AddItem(form, 1, 1, 1, 1, 0, 0, true)
	a.pages.AddPage(pageProfilerName, grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) runProfilerPreflight(name string) {
	ctx, cancel := context.WithCancel(context.Background())
	const title = "Inspecting tables and stable row keys..."
	const cancelText = "Press Esc to cancel table inspection."
	token := a.showLoadingModal(title, withLoadingCancelOutcome(cancelText, cancel))
	db, engine := a.db, a.dbType
	localPath := ""
	if cfg := a.currentConnectionConfig(); cfg != nil && engine == "sqlite" {
		localPath = strings.TrimSpace(cfg.FilePath)
	}
	started := time.Now()
	go func() {
		plans, err := profiler.PreflightWithProgress(ctx, db, engine, func(progress profiler.Progress) {
			a.updateProfilerLoadingProgress(token, title, cancelText, progress, time.Since(started))
		})
		var sourceBytes int64
		if err == nil && localPath != "" {
			if info, statErr := os.Stat(localPath); statErr == nil {
				sourceBytes = info.Size()
				if sourceBytes >= 100<<20 {
					for index := range plans {
						if plans[index].EstimatedBytes == 0 {
							plans[index].Risks = append(plans[index].Risks, profiler.RiskLargeDB)
							plans[index].Included = false
						}
					}
				}
			}
		}
		a.queueUpdateDraw(func() {
			if !a.finishLoadingModal(token) {
				return
			}
			if err != nil {
				if errors.Is(err, context.Canceled) {
					a.showChangeProfiler()
					return
				}
				a.ShowAlert(fmt.Sprintf("%s Could not inspect tables:\n\n%v", iconWarn, err), pageChangeProfiler)
				return
			}
			a.showProfilerTableReview(name, plans, sourceBytes)
		})
	}()
}

func (a *App) showProfilerTableReview(name string, plans []profiler.TablePlan, sourceBytes int64) {
	summary := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	summary.SetBackgroundColor(mantle)
	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).SetTitle(" Tables to Track ").SetTitleColor(mauve).SetBorderColor(surface1)
	list.SetBackgroundColor(bg)
	list.SetMainTextColor(text).SetSecondaryTextColor(subtext0)
	list.SetSelectedBackgroundColor(surface0).SetSelectedTextColor(green)
	refresh := func() {
		list.Clear()
		selected, risky, unknown := 0, 0, 0
		var estimatedRows, estimatedBytes int64
		for _, plan := range plans {
			mark := "[#6c7086]○[-]"
			if plan.Included {
				mark = "[green]●[-]"
				selected++
				estimatedRows += plan.EstimatedRows
				estimatedBytes += plan.EstimatedBytes
				if plan.EstimatedRows == 0 && plan.EstimatedBytes == 0 {
					unknown++
				}
			}
			key := string(plan.KeyKind)
			risk := "ready"
			if len(plan.Risks) > 0 {
				risky++
				parts := make([]string, len(plan.Risks))
				for index := range plan.Risks {
					parts[index] = string(plan.Risks[index])
				}
				risk = "review: " + strings.Join(parts, "; ")
			}
			list.AddItem(fmt.Sprintf("  %s  %s", mark, tview.Escape(plan.Name)), fmt.Sprintf("  key: %s  │  %s", key, risk), 0, nil)
		}
		estimate := "size estimate unavailable"
		if estimatedRows > 0 || estimatedBytes > 0 {
			parts := []string{}
			if estimatedRows > 0 {
				parts = append(parts, fmt.Sprintf("~%d rows", estimatedRows))
			}
			if estimatedBytes > 0 {
				parts = append(parts, "~"+appformat.FormatBytes(uint64(estimatedBytes))+" source data")
			}
			estimate = strings.Join(parts, " · ")
			if unknown > 0 {
				estimate += fmt.Sprintf(" · %d selected table(s) unknown", unknown)
			}
		}
		if sourceBytes > 0 && estimatedBytes == 0 {
			estimate = appformat.FormatBytes(uint64(sourceBytes)) + " database file · selected-table size unknown"
		}
		summary.SetText(fmt.Sprintf(" [::b]%d/%d tables selected[-] · %s · %d risky table(s)\n [#a6adc8]Exact before-values are stored locally with per-row adaptive compression.[-]", selected, len(plans), estimate, risky))
	}
	refresh()
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(" [yellow]Space[-] Toggle table  │  [yellow]A[-] Include/exclude all  │  risky tables start excluded  │  [yellow]Enter[-] Start  │  [yellow]Esc[-] Cancel ")
	start := func() {
		selected := 0
		for _, plan := range plans {
			if plan.Included {
				selected++
			}
		}
		if selected == 0 {
			a.ShowAlert(fmt.Sprintf("%s Select at least one table.", iconWarn), pageProfilerTableReview)
			return
		}
		a.pages.RemovePage(pageProfilerTableReview)
		a.runProfilerStart(name, plans)
	}
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.pages.RemovePage(pageProfilerTableReview)
			a.showChangeProfiler()
			return nil
		}
		if event.Rune() == ' ' {
			index := list.GetCurrentItem()
			if index >= 0 && index < len(plans) {
				plans[index].Included = !plans[index].Included
				refresh()
				list.SetCurrentItem(index)
			}
			return nil
		}
		if event.Rune() == 'a' || event.Rune() == 'A' {
			includeAll := false
			for _, plan := range plans {
				if !plan.Included {
					includeAll = true
					break
				}
			}
			for index := range plans {
				plans[index].Included = includeAll
			}
			refresh()
			return nil
		}
		if event.Key() == tcell.KeyEnter {
			start()
			return nil
		}
		return event
	})
	container := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(summary, 3, 0, false).AddItem(list, 0, 1, true).AddItem(footer, 1, 0, false)
	modalW, modalH := a.modalSize(76, 118, 16, 34)
	grid := tview.NewGrid().SetColumns(0, modalW, 0).SetRows(0, modalH, 0).AddItem(container, 1, 1, 1, 1, 0, 0, true)
	a.pages.AddPage(pageProfilerTableReview, grid, true, true)
	a.app.SetFocus(list)
}

func (a *App) runProfilerStart(name string, plans []profiler.TablePlan) {
	connectionKey, ok := a.activeConnectionKey()
	if !ok || a.db == nil {
		a.ShowAlert(fmt.Sprintf("%s The active database connection is unavailable.", iconWarn), pageChangeProfiler)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	const title = "Capturing compact anchor baseline..."
	const cancelText = "Press Esc to cancel; partial baseline data will be removed."
	token := a.showLoadingModal(title, withLoadingCancelOutcome(cancelText, cancel))
	db, engine := a.db, a.dbType
	request := profiler.StartRequest{Name: name, ConnectionKey: connectionKey, ConnectionLabel: a.profilerConnectionLabel(),
		TargetLabel: a.profilerTargetLabel(), Engine: engine, Tables: plans}
	started := time.Now()
	go func() {
		anchor, err := a.profilerStore.Start(ctx, db, request, func(progress profiler.Progress) {
			a.updateProfilerLoadingProgress(token, title, cancelText, progress, time.Since(started))
		})
		a.queueUpdateDraw(func() {
			if !a.finishLoadingModal(token) {
				return
			}
			if err != nil {
				if errors.Is(err, context.Canceled) {
					a.showChangeProfiler()
					return
				}
				a.ShowAlert(fmt.Sprintf("%s Could not start anchor:\n\n%v", iconWarn, err), pageChangeProfiler)
				return
			}
			a.profilerSelectedAnchor = anchor.ID
			a.showChangeProfiler()
		})
	}()
}

func (a *App) runProfilerScan(anchor profiler.Anchor, finish bool) {
	if anchor.Status != profiler.StatusActive {
		a.ShowAlert(fmt.Sprintf("%s Only an active anchor can be scanned or finished.", iconInfo), pageChangeProfiler)
		return
	}
	connectionKey, ok := a.activeConnectionKey()
	if !ok || a.db == nil || connectionKey != anchor.ConnectionKey {
		a.ShowAlert(fmt.Sprintf("%s Connect to the anchor's original database before scanning it.\n\nExpected: %s", iconWarn, tview.Escape(anchor.TargetLabel)), pageChangeProfiler)
		return
	}
	verb := "Scanning changes"
	if finish {
		verb = "Finishing anchor"
	}
	ctx, cancel := context.WithCancel(context.Background())
	title := verb + "..."
	const cancelText = "Press Esc to cancel; the last successful report will remain intact."
	token := a.showLoadingModal(title, withLoadingCancelOutcome(cancelText, cancel))
	db := a.db
	started := time.Now()
	go func() {
		updated, err := a.profilerStore.Scan(ctx, db, anchor.ID, finish, func(progress profiler.Progress) {
			a.updateProfilerLoadingProgress(token, title, cancelText, progress, time.Since(started))
		})
		a.queueUpdateDraw(func() {
			if !a.finishLoadingModal(token) {
				return
			}
			if err != nil {
				if errors.Is(err, context.Canceled) {
					a.showChangeProfiler()
					return
				}
				a.ShowAlert(fmt.Sprintf("%s Change scan failed:\n\n%v", iconWarn, err), pageChangeProfiler)
				return
			}
			a.activateProfilerReport(updated.ID)
			a.profilerSelectedAnchor = updated.ID
			a.showChangeProfiler()
		})
	}()
}

func (a *App) activateProfilerReport(anchorID string) {
	if a.profilerStore == nil {
		return
	}
	summaries, err := a.profilerStore.TableSummaries(context.Background(), anchorID)
	if err != nil {
		return
	}
	a.profilerAnchorID = anchorID
	a.profilerTableChanges = make(map[string]profiler.TableSummary, len(summaries))
	for _, summary := range summaries {
		a.profilerTableChanges[summary.Name] = summary
	}
	a.refreshTableSidebarState()
	if a.tableResultsActive && a.activeTable != "" {
		a.applyProfilerHighlightsToResults(a.activeTable)
	}
}

func (a *App) confirmDeleteProfilerAnchor(anchor profiler.Anchor) {
	modal := tview.NewModal().SetText(fmt.Sprintf("Delete local anchor %q?\n\nIts baseline and saved diff report will be permanently removed. The tracked database is not changed.", anchor.Name)).
		AddButtons([]string{"Delete", "Cancel"})
	modal.SetBackgroundColor(bg).SetTextColor(text).SetButtonBackgroundColor(surface1).SetButtonTextColor(green)
	modal.SetDoneFunc(func(index int, _ string) {
		a.pages.RemovePage("profilerDelete")
		if index != 0 {
			a.showChangeProfiler()
			return
		}
		if err := a.profilerStore.DeleteAnchor(context.Background(), anchor.ID); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not delete anchor:\n\n%v", iconWarn, err), pageChangeProfiler)
			return
		}
		if a.profilerAnchorID == anchor.ID {
			a.profilerAnchorID = ""
			a.profilerTableChanges = nil
			a.refreshTableSidebarState()
		}
		a.profilerSelectedAnchor = ""
		a.showChangeProfiler()
	})
	a.pages.AddPage("profilerDelete", modal, true, true)
	a.app.SetFocus(modal)
}

func (a *App) showProfilerReport(anchor profiler.Anchor) {
	summaries, err := a.profilerStore.TableSummaries(context.Background(), anchor.ID)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not load report:\n\n%v", iconWarn, err), pageChangeProfiler)
		return
	}
	a.activateProfilerReport(anchor.ID)
	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).SetTitle(" Changed Tables ").SetTitleColor(mauve).SetBorderColor(surface1)
	list.SetBackgroundColor(bg)
	list.SetMainTextColor(text).SetSecondaryTextColor(subtext0)
	list.SetSelectedBackgroundColor(surface0).SetSelectedTextColor(green)
	for _, summary := range summaries {
		list.AddItem(fmt.Sprintf("  %s  %s", profilerTableMarker(summary), tview.Escape(summary.Name)),
			fmt.Sprintf("  [green]+%d[-]  [yellow]~%d[-]  [red]-%d[-]", summary.Inserted, summary.Updated, summary.Deleted), 0, nil)
	}
	if len(summaries) == 0 {
		list.AddItem("  [green]No changes[-]", "  The latest comparison matches the anchor baseline.", 0, nil)
	}
	grid := tview.NewTable().SetBorders(true).SetSelectable(true, false).SetFixed(1, 0)
	grid.SetBorder(true).SetTitle(fmt.Sprintf(" Row and Cell Changes (up to %d) ", profilerReportRowLimit)).SetTitleColor(mauve).SetBorderColor(surface1)
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(" [yellow]↑/↓[-] Table  │  [yellow]Enter[-] Full before/after row  │  [yellow]Esc[-] Anchors ")
	var diffRows []profiler.DiffRow
	loadTable := func(index int) {
		grid.Clear()
		diffRows = nil
		for col, title := range []string{"CHANGE", "KEY", "CHANGED COLUMNS", "BEFORE → AFTER"} {
			grid.SetCell(0, col, tview.NewTableCell(title).SetTextColor(peach).SetSelectable(false).SetBackgroundColor(mantle))
		}
		if index < 0 || index >= len(summaries) {
			return
		}
		rows, err := a.profilerStore.ListDiffRows(context.Background(), anchor.ID, summaries[index].Name, profilerReportRowLimit)
		if err != nil {
			grid.SetCell(1, 0, tview.NewTableCell("Could not load changes: "+err.Error()).SetTextColor(red))
			return
		}
		diffRows = rows
		for rowIndex, row := range rows {
			color, background, label := profilerDiffStyle(row.Kind)
			changed := strings.Join(row.ChangedColumns, ", ")
			if changed == "" {
				changed = "all values"
			}
			preview := profilerDiffPreview(row)
			values := []string{label, truncateForDisplay(row.Key, 38), changed, preview}
			for col, value := range values {
				cell := tview.NewTableCell(tview.Escape(value)).SetTextColor(color).SetBackgroundColor(background)
				if col == 3 {
					cell.SetExpansion(1)
				}
				grid.SetCell(rowIndex+1, col, cell)
			}
		}
		if len(rows) == 0 {
			grid.SetCell(1, 0, tview.NewTableCell("Structure changed; no row values changed").SetTextColor(mauve))
		}
	}
	list.SetChangedFunc(func(index int, _, _ string, _ rune) { loadTable(index) })
	grid.SetSelectedFunc(func(row, _ int) {
		if row > 0 && row-1 < len(diffRows) {
			a.showProfilerDiffDetail(diffRows[row-1])
		}
	})
	grid.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.pages.RemovePage(pageProfilerReport)
			a.showChangeProfiler()
			return nil
		}
		return event
	})
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.pages.RemovePage(pageProfilerReport)
			a.showChangeProfiler()
			return nil
		}
		if event.Key() == tcell.KeyEnter {
			a.app.SetFocus(grid)
			if grid.GetRowCount() > 1 {
				grid.Select(1, 0)
			}
			return nil
		}
		return event
	})
	loadTable(0)
	container := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(tview.NewFlex().AddItem(list, 0, 2, true).AddItem(grid, 0, 5, false), 0, 1, true).
		AddItem(footer, 1, 0, false)
	a.pages.AddPage(pageProfilerReport, container, true, true)
	a.app.SetFocus(list)
}

func (a *App) showProfilerDiffDetail(row profiler.DiffRow) {
	columns := map[string]bool{}
	for name := range row.Before {
		columns[name] = true
	}
	for name := range row.After {
		columns[name] = true
	}
	names := make([]string, 0, len(columns))
	for name := range columns {
		if name != "__dbterm_rowid" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	changed := make(map[string]bool, len(row.ChangedColumns))
	for _, name := range row.ChangedColumns {
		changed[name] = true
	}
	table := tview.NewTable().SetBorders(true).SetSelectable(true, true).SetFixed(1, 0)
	for col, title := range []string{"COLUMN", "BEFORE", "AFTER"} {
		table.SetCell(0, col, tview.NewTableCell(title).SetTextColor(peach).SetSelectable(false).SetBackgroundColor(mantle))
	}
	for index, name := range names {
		before, after := row.Before[name], row.After[name]
		background := tcell.ColorDefault
		if changed[name] || row.Kind != profiler.DiffUpdated {
			_, background, _ = profilerDiffStyle(row.Kind)
		}
		table.SetCell(index+1, 0, tview.NewTableCell(name).SetTextColor(blue).SetBackgroundColor(background))
		table.SetCell(index+1, 1, tview.NewTableCell(before.Text).SetTextColor(text).SetBackgroundColor(background).SetExpansion(1))
		table.SetCell(index+1, 2, tview.NewTableCell(after.Text).SetTextColor(text).SetBackgroundColor(background).SetExpansion(1))
	}
	table.SetBorder(true).SetTitle(fmt.Sprintf(" %s row — full before / after ", row.Kind)).SetTitleColor(mauve).SetBorderColor(surface1)
	table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyEnter {
			a.pages.RemovePage(pageProfilerDiffDetail)
			return nil
		}
		return event
	})
	modalW, modalH := a.modalSize(82, 130, 18, 38)
	grid := tview.NewGrid().SetColumns(0, modalW, 0).SetRows(0, modalH, 0).AddItem(table, 1, 1, 1, 1, 0, 0, true)
	a.pages.AddPage(pageProfilerDiffDetail, grid, true, true)
	a.app.SetFocus(table)
}

func (a *App) profilerConnectionLabel() string {
	cfg := a.currentConnectionConfig()
	if cfg == nil {
		return "unknown connection"
	}
	parts := []string{cfg.Name, string(cfg.Type)}
	if strings.TrimSpace(cfg.User) != "" {
		parts = append(parts, "user "+cfg.User)
	}
	if strings.TrimSpace(cfg.Host) != "" {
		parts = append(parts, cfg.Host)
	}
	return strings.Join(parts, " · ")
}

func (a *App) profilerTargetLabel() string {
	if cfg := a.currentConnectionConfig(); cfg != nil {
		if cfg.Type == "sqlite" && strings.TrimSpace(cfg.FilePath) != "" {
			return cfg.FilePath
		}
		if strings.TrimSpace(cfg.Database) != "" {
			return cfg.Database
		}
	}
	if strings.TrimSpace(a.dbName) != "" {
		return a.dbName
	}
	return "current database"
}

func profilerStatusLabel(status profiler.Status) string {
	switch status {
	case profiler.StatusActive:
		return "[green]● active[-]"
	case profiler.StatusComplete:
		return "[#89b4fa]◆ complete[-]"
	case profiler.StatusFailed:
		return "[red]× failed[-]"
	default:
		return "[yellow]◐ " + tview.Escape(string(status)) + "[-]"
	}
}

func profilerTableMarker(summary profiler.TableSummary) string {
	if summary.SchemaChanged {
		return "[#cba6f7]Δ[-]"
	}
	if summary.Updated > 0 || (summary.Inserted > 0 && summary.Deleted > 0) {
		return "[yellow]~[-]"
	}
	if summary.Inserted > 0 {
		return "[green]+[-]"
	}
	return "[red]-[-]"
}

func profilerDiffStyle(kind profiler.DiffKind) (tcell.Color, tcell.Color, string) {
	switch kind {
	case profiler.DiffInserted:
		return green, insertRowBG, "+ inserted"
	case profiler.DiffDeleted:
		return red, deleteRowBG, "- deleted"
	default:
		return yellow, updateRowBG, "~ updated"
	}
}

func profilerDiffPreview(row profiler.DiffRow) string {
	if row.Kind == profiler.DiffInserted {
		return "new row"
	}
	if row.Kind == profiler.DiffDeleted {
		return "row removed"
	}
	parts := make([]string, 0, len(row.ChangedColumns))
	for _, name := range row.ChangedColumns {
		before, after := row.Before[name], row.After[name]
		parts = append(parts, fmt.Sprintf("%s: %s → %s", name, truncateForDisplay(before.Text, 18), truncateForDisplay(after.Text, 18)))
		if len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, "  │  ")
}

func (a *App) recordProfilerActivity(sqlText string, rowsAffected int64) {
	if a.profilerStore == nil {
		return
	}
	connectionKey, ok := a.activeConnectionKey()
	if !ok {
		return
	}
	_ = a.profilerStore.RecordActivity(context.Background(), connectionKey, a.profilerConnectionLabel(), sqlText, rowsAffected, time.Now())
}

func (a *App) updateProfilerLoadingProgress(token uint64, title, cancelText string, progress profiler.Progress, elapsed time.Duration) {
	if a == nil || a.app == nil || a.pages == nil || a.loadingGeneration.Load() != token {
		return
	}
	a.app.QueueUpdateDraw(func() {
		if a.loadingGeneration.Load() != token {
			return
		}
		modal, ok := a.pages.GetPage("loading").(*tview.Modal)
		if !ok || modal == nil {
			return
		}
		modal.SetText(fmt.Sprintf("\n%s\n\n%s\n\n%s", tview.Escape(title), renderProfilerProgress(progress, elapsed, 34), tview.Escape(cancelText)))
		a.updateStatusBar(fmt.Sprintf("[yellow]%s: %s, %d rows, %d%%[-]", tview.Escape(progress.Table), progress.Phase, progress.Rows, progress.Percent), 0)
	})
}

func renderProfilerProgress(progress profiler.Progress, elapsed time.Duration, width int) string {
	width = max(width, 12)
	percent := max(0, min(progress.Percent, 100))
	filled := int(math.Round(float64(percent) / 100 * float64(width)))
	bar := make([]byte, width)
	for index := range bar {
		bar[index] = '-'
	}
	for index := 0; index < filled; index++ {
		bar[index] = '#'
	}
	if filled < width {
		bar[filled] = '>'
	}
	percentLabel := fmt.Sprintf("%d%%", percent)
	if progress.Approximate {
		percentLabel = "~" + percentLabel
	}
	phase := strings.ToUpper(strings.TrimSpace(progress.Phase))
	if phase == "" {
		phase = "WORKING"
	}
	table := strings.TrimSpace(progress.Table)
	if table == "" {
		table = "database"
	}
	position := ""
	if progress.TableCount > 0 {
		position = fmt.Sprintf("table %d/%d · ", progress.TableIndex, progress.TableCount)
	}
	statistics := []string{}
	if progress.Rows > 0 || progress.EstimatedRows > 0 {
		rowProgress := fmt.Sprintf("%d rows", progress.Rows)
		if progress.EstimatedRows > 0 {
			rowProgress = fmt.Sprintf("%d / ~%d rows", progress.Rows, progress.EstimatedRows)
		}
		statistics = append(statistics, rowProgress)
	}
	if progress.Bytes > 0 {
		statistics = append(statistics, appformat.FormatBytes(uint64(progress.Bytes)))
	}
	if elapsed >= 0 {
		statistics = append(statistics, "elapsed "+formatBackupProgressDuration(elapsed))
	}
	if elapsed >= time.Second && progress.Rows > 0 {
		rate := float64(progress.Rows) / elapsed.Seconds()
		statistics = append(statistics, fmt.Sprintf("%.0f rows/s", rate))
		if progress.EstimatedRows > progress.Rows && rate > 0 {
			eta := time.Duration(float64(progress.EstimatedRows-progress.Rows)/rate) * time.Second
			statistics = append(statistics, "table ETA "+formatBackupProgressDuration(eta))
		}
	}
	return fmt.Sprintf("%s\n%s%s\n|%s|  %s\n%s", tview.Escape(phase), tview.Escape(position), tview.Escape(table), string(bar), percentLabel, strings.Join(statistics, "  •  "))
}
