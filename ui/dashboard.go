package ui

import (
	"fmt"
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

	// Get quick service status for the header
	mysqlStatus := getQuickStatus("mysql")
	pgStatus := getQuickStatus("postgresql")

	headerText := fmt.Sprintf(`
[::b][#cba6f7]╔══════════════════════════════════╗
║           d b t e r m            ║
╚══════════════════════════════════╝[-][-]
[#a6adc8]PostgreSQL  •  MySQL  •  SQLite[-]
%s  %s   [#6c7086]Press S for details[-]`, pgStatus, mysqlStatus)
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

	if connCount > 0 {
		connList.SetTitle(fmt.Sprintf(" Saved Connections (%d) ", connCount))

		for i, conn := range a.store.Connections {
			// Status icon
			statusIcon := "[#45475a]○[-]"
			if conn.Active {
				statusIcon = "[green]●[-]"
			}

			// DB type with color
			var typeTag string
			switch conn.Type {
			case config.PostgreSQL:
				typeTag = "[#89b4fa]PG[-]"
			case config.MySQL:
				typeTag = "[#f9e2af]MY[-]"
			case config.SQLite:
				typeTag = "[#a6e3a1]SL[-]"
			}

			label := fmt.Sprintf(" %s  %s  %s", statusIcon, typeTag, conn.Name)

			// Detail line
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

			// Shortcut key: 1-9 for first 9, then 0 for 10th, then none
			var shortcut rune
			if i < 9 {
				shortcut = rune('1' + i)
			} else if i == 9 {
				shortcut = '0'
			}

			connList.AddItem(label, detail, shortcut, nil)
		}
	} else {
		connList.SetTitle(" Saved Connections ")
		connList.AddItem("  [#6c7086]No saved connections yet[-]", "       Press [green]N[-] to add your first database", 0, nil)
	}

	// ── Footer Actions ──
	actions := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	actions.SetBackgroundColor(crust)

	if connCount > 0 {
		actions.SetText("  [green]Enter[-] Connect  │  [yellow]N[-] New  │  [blue]E[-] Edit  │  [red]D[-] Delete  │  [#94e2d5]S[-] Services  │  [teal]H[-] Help  │  [#cba6f7]Q[-] Quit")
	} else {
		actions.SetText("  [yellow]N[-] New Connection  │  [#94e2d5]S[-] Services  │  [teal]H[-] Help  │  [#cba6f7]Q[-] Quit")
	}

	// ── Layout ──
	screenW, screenH := a.getScreenSize()
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
				a.ShowAlert("No connections to edit.\n\nPress N to create one.", "dashboard")
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
		}

		if event.Key() == tcell.KeyEnter && connCount > 0 {
			idx := connList.GetCurrentItem()
			if idx >= 0 && idx < connCount {
				a.connectToSaved(idx)
			}
			return nil
		}

		if event.Key() == tcell.KeyEscape {
			if a.db != nil {
				// Go back to workspace if already connected
				a.pages.RemovePage("dashboard")
				a.pages.ShowPage("main")
				a.app.SetFocus(a.queryInput)
			}
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
		SetText(fmt.Sprintf("Delete [yellow]\"%s\"[-] (%s)?\n\nThis cannot be undone.", conn.Name, conn.TypeLabel())).
		AddButtons([]string{"  Delete  ", "  Cancel  "}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			if buttonIndex == 0 {
				a.store.Delete(index)
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
