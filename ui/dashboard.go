package ui

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

// showDashboard displays the saved connections landing page
func (a *App) showDashboard() {
	// ── Header ──
	header := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	header.SetBackgroundColor(bg)

	connCount := len(a.store.Connections)
	connections := append([]config.ConnectionConfig(nil), a.store.Connections...)

	// Get quick service status for the header
	mysqlStatus := getQuickStatus("mysql")
	pgStatus := getQuickStatus("postgresql")

	headerText := fmt.Sprintf(`
[::b][#cba6f7]╔══════════════════════════════════╗
║           d b t e r m            ║
╚══════════════════════════════════╝[-][-]
[#a6adc8]%s PostgreSQL  •  MySQL  •  SQLite[-]
%s  %s   [#6c7086]Press S for %s[-]`, iconConnect, pgStatus, mysqlStatus, iconServices+" services")
	header.SetText(headerText)

	// ── Connection List ──
	connList := tview.NewList().ShowSecondaryText(true)
	connList.SetBorder(true).
		SetBorderColor(surface1).
		SetTitleColor(mauve)
	connList.SetBackgroundColor(bg)
	connList.SetMainTextColor(text)
	connList.SetSecondaryTextColor(subtext0)
	connList.SetSelectedBackgroundColor(surface0)
	connList.SetSelectedTextColor(green)

	baseDetails := make([]string, connCount)
	if connCount > 0 {
		connList.SetTitle(fmt.Sprintf(" %s Saved Connections (%d) ", iconDashboard, connCount))

		for i, conn := range connections {
			label := dashboardConnectionLabel(conn)
			detail := dashboardConnectionDetail(conn)
			baseDetails[i] = detail

			// Shortcut key: 1-9 for first 9, then 0 for 10th, then none
			var shortcut rune
			if i < 9 {
				shortcut = rune('1' + i)
			} else if i == 9 {
				shortcut = '0'
			}

			connList.AddItem(label, detail+"  │  [#6c7086]"+iconRefresh+" checking[-]", shortcut, nil)
		}
		a.runDashboardConnectionChecks(connList, connections, baseDetails)
	} else {
		connList.SetTitle(fmt.Sprintf(" %s Saved Connections ", iconDashboard))
		connList.AddItem(fmt.Sprintf("  [#6c7086]%s No saved connections yet[-]", iconInfo), "       Press [green]N[-] to add your first database "+iconConnect, 0, nil)
	}

	// ── Footer Actions ──
	actions := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	actions.SetBackgroundColor(crust)

	// ── Layout ──
	screenW, screenH := a.getScreenSize()
	hasWorkspace := a.db != nil

	if connCount > 0 {
		actions.SetText(dashboardFooterText(true, hasWorkspace, screenW))
	} else {
		actions.SetText(dashboardFooterText(false, hasWorkspace, screenW))
	}

	headerHeight := 8
	if screenW < 100 || screenH < 30 {
		headerHeight = 6
	}
	if screenH < 22 {
		headerHeight = 5
	}

	layout := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(header, headerHeight, 0, false).
		AddItem(connList, 0, 1, true).
		AddItem(actions, 1, 0, false)

	backToWorkspace := func() bool {
		if a.db == nil {
			return false
		}
		a.pages.RemovePage("dashboard")
		a.pages.ShowPage("main")
		a.app.SetFocus(a.queryInput)
		return true
	}

	// ── Key Handling ──
	connList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Rune() {
		case 'n', 'N':
			a.showConnectionForm(nil, -1)
			return nil
		case 'e', 'E':
			if connCount > 0 {
				idx := connList.GetCurrentItem()
				if idx >= 0 && idx < connCount {
					conn := a.store.Connections[idx]
					a.showConnectionForm(&conn, idx)
				}
			} else {
				a.ShowAlert(fmt.Sprintf("%s No connections to edit.\n\nPress N to create one.", iconInfo), "dashboard")
			}
			return nil
		case 'd', 'D':
			if connCount > 0 {
				idx := connList.GetCurrentItem()
				if idx >= 0 && idx < connCount {
					a.confirmDelete(idx)
				}
			}
			return nil
		case 'q', 'Q':
			a.cleanup()
			a.app.Stop()
			return nil
		case 'h', 'H':
			a.showHelp()
			return nil
		case 's', 'S':
			a.showServiceDashboard()
			return nil
		case 'w', 'W':
			if !backToWorkspace() {
				a.ShowAlert(fmt.Sprintf("%s No workspace open yet.\n\nConnect to a database first.", iconInfo), "dashboard")
			}
			return nil
		case 'b', 'B':
			if !backToWorkspace() {
				a.ShowAlert(fmt.Sprintf("%s No workspace open yet.\n\nConnect to a database first.", iconInfo), "dashboard")
			}
			return nil
		case 'r', 'R':
			// Reopen dashboard to refresh live availability checks.
			a.pages.RemovePage("dashboard")
			a.showDashboard()
			return nil
		}

		if event.Key() == tcell.KeyEnter && connCount > 0 {
			idx := connList.GetCurrentItem()
			if idx >= 0 && idx < connCount {
				a.connectToSaved(idx)
			}
			return nil
		}

		if event.Key() == tcell.KeyEscape {
			backToWorkspace()
			return nil
		}

		return event
	})

	a.pages.AddAndSwitchToPage("dashboard", layout, true)
	a.app.SetFocus(connList)
}

// confirmDelete shows a confirmation modal before deleting
func (a *App) confirmDelete(index int) {
	conn := a.store.Connections[index]
	modal := tview.NewModal().
		SetText(fmt.Sprintf("%s Delete [yellow]\"%s\"[-] (%s)?\n\nThis cannot be undone.", iconWarn, conn.Name, conn.TypeLabel())).
		AddButtons([]string{"  Delete  ", "  Cancel  "}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			if buttonIndex == 0 {
				if err := a.store.Delete(index); err != nil {
					a.pages.RemovePage("confirmDelete")
					a.ShowAlert(fmt.Sprintf("Could not delete connection:\n\n%v", err), "dashboard")
					return
				}
			}
			a.pages.RemovePage("confirmDelete")
			a.pages.RemovePage("dashboard")
			a.showDashboard()
		})
	modal.SetBackgroundColor(bg).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetTextColor(text)

	a.pages.AddPage("confirmDelete", modal, true, true)
}

// connectToSaved connects to a saved config by index
func (a *App) connectToSaved(index int) {
	conn := a.store.Connections[index]
	a.connectWithConfig(&conn, index)
}

// formatTimeAgo returns a human-readable relative time string
func formatTimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 min ago"
		}
		return fmt.Sprintf("%d mins ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	case d < 7*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "yesterday"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		return t.Format("Jan 02, 2006")
	}
}

func dashboardConnectionLabel(conn config.ConnectionConfig) string {
	statusIcon := "[#45475a]○[-]"
	activity := "[#6c7086]idle[-]"
	if conn.Active {
		statusIcon = "[green]●[-]"
		activity = "[green]ACTIVE[-]"
	}

	var typeTag string
	switch conn.Type {
	case config.PostgreSQL:
		typeTag = "[#89b4fa]PG[-]"
	case config.MySQL:
		typeTag = "[#f9e2af]MY[-]"
	case config.SQLite:
		typeTag = "[#a6e3a1]SL[-]"
	default:
		typeTag = "[#6c7086]DB[-]"
	}

	return fmt.Sprintf(" %s  %s  %s %s  %s", statusIcon, typeTag, iconConnect, conn.Name, activity)
}

func dashboardConnectionDetail(conn config.ConnectionConfig) string {
	var detail string
	if conn.Type == config.SQLite {
		detail = fmt.Sprintf("       ◆ %s", conn.FilePath)
	} else {
		detail = fmt.Sprintf("       %s@%s:%s/%s", conn.User, conn.Host, conn.Port, conn.Database)
	}

	if conn.LastUsed != "" {
		t, err := time.Parse(time.RFC3339, conn.LastUsed)
		if err == nil {
			detail += fmt.Sprintf("  │  %s", formatTimeAgo(t))
		}
	}
	return detail
}

func (a *App) runDashboardConnectionChecks(connList *tview.List, conns []config.ConnectionConfig, baseDetails []string) {
	if len(conns) == 0 {
		return
	}

	type checkResult struct {
		index     int
		reachable bool
	}

	go func() {
		results := make(chan checkResult, len(conns))
		for i, conn := range conns {
			idx := i
			cfg := conn
			go func() {
				results <- checkResult{
					index:     idx,
					reachable: dashboardConnectionReachable(cfg, 3*time.Second),
				}
			}()
		}

		for range conns {
			res := <-results
			a.app.QueueUpdateDraw(func() {
				if res.index < 0 || res.index >= connList.GetItemCount() || res.index >= len(baseDetails) {
					return
				}

				status := "[red]○ offline[-]"
				if res.reachable {
					status = "[green]● online[-]"
				}

				main, _ := connList.GetItemText(res.index)
				connList.SetItemText(res.index, main, baseDetails[res.index]+"  │  "+status)
			})
		}
	}()
}

func dashboardConnectionReachable(conn config.ConnectionConfig, timeout time.Duration) bool {
	switch conn.Type {
	case config.SQLite:
		if conn.FilePath == "" {
			return false
		}
		if _, err := os.Stat(conn.FilePath); err != nil {
			return false
		}
		return true
	default:
		driver := conn.DriverName()
		dsn := conn.BuildConnString()
		if driver == "" || dsn == "" {
			return false
		}

		db, err := sql.Open(driver, dsn)
		if err != nil {
			return false
		}
		defer db.Close()

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return db.PingContext(ctx) == nil
	}
}

func dashboardFooterText(hasConnections, hasWorkspace bool, width int) string {
	if hasConnections {
		switch {
		case width < 74:
			return fmt.Sprintf("  [yellow]N[-] New  │  [teal]H[-] Help %s  │  [#cba6f7]Q[-] Quit", iconHelp)
		case width < 100:
			if hasWorkspace {
				return fmt.Sprintf("  [yellow]Enter[-] Connect %s  │  [yellow]S[-] Services %s  │  [yellow]B/Esc[-] Back %s", iconConnect, iconServices, iconBack)
			}
			return fmt.Sprintf("  [yellow]Enter[-] Connect %s  │  [yellow]N[-] New  │  [teal]H[-] Help %s  │  [#cba6f7]Q[-] Quit", iconConnect, iconHelp)
		case width < 128:
			if hasWorkspace {
				return fmt.Sprintf("  [yellow]Enter[-] Connect %s  │  [yellow]N[-] New  │  [blue]E[-] Edit  │  [red]D[-] Delete  │  [yellow]B/Esc[-] Back %s", iconConnect, iconBack)
			}
			return fmt.Sprintf("  [yellow]Enter[-] Connect %s  │  [yellow]N[-] New  │  [blue]E[-] Edit  │  [red]D[-] Delete  │  [teal]H[-] Help %s", iconConnect, iconHelp)
		default:
			if hasWorkspace {
				return fmt.Sprintf("  [green]Enter[-] Connect %s  │  [yellow]N[-] New  │  [blue]E[-] Edit  │  [red]D[-] Delete  │  [#94e2d5]S[-] Services %s  │  [yellow]R[-] Recheck %s  │  [yellow]B/Esc[-] Back %s  │  [#cba6f7]Q[-] Quit",
					iconConnect, iconServices, iconRefresh, iconBack)
			}
			return fmt.Sprintf("  [green]Enter[-] Connect %s  │  [yellow]N[-] New  │  [blue]E[-] Edit  │  [red]D[-] Delete  │  [#94e2d5]S[-] Services %s  │  [yellow]R[-] Recheck %s  │  [teal]H[-] Help %s  │  [#cba6f7]Q[-] Quit",
				iconConnect, iconServices, iconRefresh, iconHelp)
		}
	}

	if width < 74 {
		return "  [yellow]N[-] New  │  [#cba6f7]Q[-] Quit"
	}
	if width < 104 {
		if hasWorkspace {
			return fmt.Sprintf("  [yellow]N[-] New  │  [yellow]B/Esc[-] Back %s  │  [#cba6f7]Q[-] Quit", iconBack)
		}
		return fmt.Sprintf("  [yellow]N[-] New  │  [teal]H[-] Help %s  │  [#cba6f7]Q[-] Quit", iconHelp)
	}
	if hasWorkspace {
		return fmt.Sprintf("  [yellow]N[-] New Connection  │  [#94e2d5]S[-] Services %s  │  [yellow]B/Esc[-] Back %s  │  [#cba6f7]Q[-] Quit", iconServices, iconBack)
	}
	return fmt.Sprintf("  [yellow]N[-] New Connection  │  [#94e2d5]S[-] Services %s  │  [teal]H[-] Help %s  │  [#cba6f7]Q[-] Quit", iconServices, iconHelp)
}
