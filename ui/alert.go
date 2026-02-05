package ui

import "github.com/rivo/tview"

func (a *App) ShowAlert(message string, returnPage string) {
	alertModal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"  OK  "}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			a.pages.HidePage("alert")
			a.pages.ShowPage(returnPage)
		})

	alertModal.SetButtonBackgroundColor(mantle).SetButtonTextColor(green).SetTextColor(red)

	a.pages.AddPage("alert", alertModal, true, true)
}
