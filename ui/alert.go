package ui

import "github.com/rivo/tview"

// ShowAlert displays a modal alert and returns to returnPage when dismissed
func (a *App) ShowAlert(message string, returnPage string) {
	modal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"  OK  "}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			a.pages.RemovePage("alert")
			if returnPage != "" {
				a.pages.ShowPage(returnPage)
			}
		})

	modal.SetBackgroundColor(bg).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetTextColor(text)

	a.pages.AddPage("alert", modal, true, true)
	a.app.SetFocus(modal)
}
