package ui

import "github.com/rivo/tview"

// ShowAlert displays a modal alert message and returns to the given page when dismissed
func (a *App) ShowAlert(message string, returnPage string) {
	alertModal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"  OK  "}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			a.pages.RemovePage("alert")
			a.pages.ShowPage(returnPage)
		})

	alertModal.SetBackgroundColor(bg).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetTextColor(red)

	a.pages.AddPage("alert", alertModal, true, true)
}
