package ui

import (
	"fmt"

	"github.com/rivo/tview"
)

func (a *App) GetFormData(form *tview.Form) string {
	getText := func(label string) string {
		if input, ok := form.GetFormItemByLabel(label).(*tview.InputField); ok {
			return input.GetText()
		}
		return ""
	}

	hostString := getText("Host (*)")
	portString := getText("Port (*)")
	userString := getText("User (*)")
	dbString := getText("Database (*)")

	if hostString == "" || portString == "" || userString == "" || dbString == "" {
		a.pages.HidePage("connectModal")
		a.ShowAlert("Please fill out all the fields", "connectModal")
		return ""
	}

	connStr := fmt.Sprintf(
		"host='%s' port='%s' user='%s' password='%s' dbname='%s' sslmode=disable",
		hostString,
		portString,
		userString,
		form.GetFormItemByLabel("Password").(*tview.InputField).GetText(),
		dbString,
	)

	return connStr
}
