package ui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

// showDashboard displays the saved connections landing page
func (a *App) showDashboard() {
	// ── Header ──
	header := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText("\n[::b][#cba6f7]╔══════════════════════════════╗\n║         d b t e r m          ║\n╚══════════════════════════════╝[-][-]\n[#a6adc8]Multi-database terminal client[-]")
	header.SetBackgroundColor(bg)

	// ── Connection List ──
	connList := tview.NewList().ShowSecondaryText(true)
	connList.SetBorder(true).
		SetTitle(" Saved Connections ").
		SetBorderColor(surface1).
		SetTitleColor(mauve)
	connList.SetBackgroundColor(bg)
	connList.SetMainTextColor(text)
	connList.SetSecondaryTextColor(subtext0)
	connList.SetSelectedBackgroundColor(surface0)
	connList.SetSelectedTextColor(green)

	// Populate saved connections
	if len(a.store.Connections) > 0 {
		for i, conn := range a.store.Connections {
			idx := i // capture
			statusIcon := "[red]●[-]"
			if conn.Active {
				statusIcon = "[green]●[-]"
			}
			label := fmt.Sprintf("%s  %s  %s", statusIcon, conn.TypeLabel(), conn.Name)

			var detail string
			if conn.Type == config.SQLite {
				detail = fmt.Sprintf("   %s", conn.FilePath)
			} else {
				detail = fmt.Sprintf("   %s@%s:%s/%s", conn.User, conn.Host, conn.Port, conn.Database)
			}
			if conn.LastUsed != "" {
				detail += fmt.Sprintf("  │  Last: %s", conn.LastUsed[:10])
			}

			connList.AddItem(label, detail, rune('1'+idx), nil)
		}
	} else {
		connList.AddItem("[gray]No saved connections[-]", "   Press [N] to add a new connection", 0, nil)
	}

	// ── Actions ──
	actions := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(fmt.Sprintf(
			"  [green]Enter[-] Connect   [yellow]N[-] New   [blue]E[-] Edit   [red]D[-] Delete   [#cba6f7]Q[-] Quit   [teal]H[-] Help   │   [gray]%d saved[-]",
			len(a.store.Connections),
		))
	actions.SetBackgroundColor(crust)

	// ── Layout ──
	layout := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(header, 6, 0, false).
		AddItem(connList, 0, 1, true).
		AddItem(actions, 1, 0, false)

	// ── Key Handling ──
	connList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Rune() {
		case 'n', 'N':
			a.showConnectionForm(nil, -1)
			return nil
		case 'e', 'E':
			if len(a.store.Connections) > 0 {
				idx := connList.GetCurrentItem()
				if idx >= 0 && idx < len(a.store.Connections) {
					conn := a.store.Connections[idx]
					a.showConnectionForm(&conn, idx)
				}
			}
			return nil
		case 'd', 'D':
			if len(a.store.Connections) > 0 {
				idx := connList.GetCurrentItem()
				if idx >= 0 && idx < len(a.store.Connections) {
					a.confirmDelete(idx)
				}
			}
			return nil
		case 'q', 'Q':
			a.app.Stop()
			return nil
		case 'h', 'H':
			a.showHelp()
			return nil
		}

		if event.Key() == tcell.KeyEnter && len(a.store.Connections) > 0 {
			idx := connList.GetCurrentItem()
			if idx >= 0 && idx < len(a.store.Connections) {
				a.connectToSaved(idx)
			}
			return nil
		}

		return event
	})

	a.pages.AddAndSwitchToPage("dashboard", layout, true)
	a.app.SetFocus(connList)
}

// confirmDelete shows a confirmation modal before deleting a connection
func (a *App) confirmDelete(index int) {
	name := a.store.Connections[index].Name
	modal := tview.NewModal().
		SetText(fmt.Sprintf("Delete connection [yellow]\"%s\"[-]?", name)).
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

// connectToSaved connects to a saved connection config
func (a *App) connectToSaved(index int) {
	conn := a.store.Connections[index]
	a.connectWithConfig(&conn, index)
}
