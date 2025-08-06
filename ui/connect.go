package ui

import (
	"fmt"

	"github.com/nabsk911/pgterm/utils"
	"github.com/rivo/tview"
)

func (a *App) connectDB(form *tview.Form) error {

	connStr := a.GetFormData(form)
	if connStr == "" {
		a.ShowAlert("Please fill out all the fields", "connectModal")
		return fmt.Errorf("Error connecting to database")
	}

	db, err := utils.ConnectDB(connStr)
	if err != nil {
		a.pages.HidePage("connectModal")
		a.ShowAlert(fmt.Sprintf("Error connecting to database: %v", err), "connectModal")
		return err
	}

	a.db = db
	return nil
}

func (a *App) showConnectionForm() *tview.Form {
	form := tview.NewForm().
		AddInputField("Host (*)", "localhost", 20, nil, nil).
		AddInputField("Port (*)", "5432", 20, nil, nil).
		AddInputField("User (*)", "", 20, nil, nil).
		AddPasswordField("Password", "", 20, '*', nil).
		AddInputField("Database (*)", "", 20, nil, nil)

	// Connect to database
	form.AddButton("Connect", func() {
		if err := a.connectDB(form); err != nil {
			return
		}

		// Load tables from database
		if err := a.LoadTables(); err != nil {
			a.pages.HidePage("connectModal")
			a.ShowAlert(fmt.Sprintf("Error loading tables: %v", err), "connectModal")
			return
		}

		a.pages.HidePage("connectModal")
		a.pages.ShowPage("main")
		a.app.SetFocus(a.tables)
	}).AddButton("Quit", func() {
		a.app.Stop()
	})

	form.SetBorder(true).SetTitle(" Connect to Database ")
	form.
		SetFieldBackgroundColor(mantle).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green)

	footer := tview.NewTextView().
		SetText(" [yellow]Tab/Shift+Tab[-]: Navigate").
		SetTextAlign(tview.AlignCenter).
		SetDynamicColors(true)

	formWithFooter := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true).
		AddItem(footer, 1, 0, false)

	connectModal := tview.NewGrid().
		SetColumns(0, 60, 0).
		SetRows(0, 16, 0).
		AddItem(formWithFooter, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage("connectModal", connectModal, true, true)
	return form
}
