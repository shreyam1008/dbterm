package ui

import "github.com/rivo/tview"

// Keep completion and failure actions reachable even when paths or diagnostics
// are longer than the terminal. The full result stays available in Details.
func (a *App) showBackupOutcome(summary, details, returnPage string) {
	const page = "backupOutcome"
	returnFocus := a.app.GetFocus()
	modal := tview.NewModal().SetText(summary).AddButtons([]string{"Details", "Done"})
	modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
	modal.SetDoneFunc(func(index int, _ string) {
		if index == 0 {
			a.showBackupActivityDetails(details, nil, false)
			return
		}
		a.pages.RemovePage(page)
		if a.pages.HasPage(returnPage) {
			a.pages.ShowPage(returnPage)
			a.app.SetFocus(returnFocus)
		}
	})
	a.pages.AddPage(page, modal, true, true)
	a.app.SetFocus(modal)
}
