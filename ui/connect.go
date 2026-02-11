package ui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/shreyam1008/dbterm/config"
	"github.com/shreyam1008/dbterm/utils"
	"github.com/rivo/tview"
)

// showConnectionForm displays a form for adding or editing a connection.
// If editConn is non-nil, the form is pre-filled (edit mode).
// editIndex is the index in the store (-1 for new connections).
func (a *App) showConnectionForm(editConn *config.ConnectionConfig, editIndex int) {
	isEdit := editConn != nil

	form := tview.NewForm()

	// ── Connection Name ──
	nameDefault := ""
	if isEdit {
		nameDefault = editConn.Name
	}
	form.AddInputField("Name (*)", nameDefault, 30, nil, nil)

	// ── DB Type Dropdown ──
	dbTypes := []string{"PostgreSQL", "MySQL", "SQLite"}
	initialType := 0
	if isEdit {
		switch editConn.Type {
		case config.MySQL:
			initialType = 1
		case config.SQLite:
			initialType = 2
		}
	}
	form.AddDropDown("Type (*)", dbTypes, initialType, nil)

	// ── Server fields (shown for PG/MySQL) ──
	hostDefault, portDefault, userDefault, passDefault, dbDefault, fileDefault := "localhost", "5432", "", "", "", ""
	if isEdit {
		hostDefault = editConn.Host
		portDefault = editConn.Port
		userDefault = editConn.User
		passDefault = editConn.Password
		dbDefault = editConn.Database
		fileDefault = editConn.FilePath
	}

	form.AddInputField("Host", hostDefault, 30, nil, nil)
	form.AddInputField("Port", portDefault, 10, nil, nil)
	form.AddInputField("User", userDefault, 30, nil, nil)
	form.AddPasswordField("Password", passDefault, 30, '*', nil)
	form.AddInputField("Database", dbDefault, 30, nil, nil)
	form.AddInputField("File Path (SQLite)", fileDefault, 40, nil, nil)

	// ── Buttons ──
	title := " New Connection "
	btnLabel := "Save & Connect"
	if isEdit {
		title = " Edit Connection "
		btnLabel = "Update & Connect"
	}

	form.AddButton(btnLabel, func() {
		cfg := a.buildConfigFromForm(form)
		if cfg == nil {
			return
		}

		if isEdit {
			a.store.Update(editIndex, *cfg)
			a.connectWithConfig(cfg, editIndex)
		} else {
			a.store.Add(*cfg)
			idx := len(a.store.Connections) - 1
			a.connectWithConfig(cfg, idx)
		}
	})

	form.AddButton("Save Only", func() {
		cfg := a.buildConfigFromForm(form)
		if cfg == nil {
			return
		}
		if isEdit {
			a.store.Update(editIndex, *cfg)
		} else {
			a.store.Add(*cfg)
		}
		// Go back to dashboard
		a.pages.RemovePage("connectModal")
		a.pages.RemovePage("dashboard")
		a.showDashboard()
	})

	form.AddButton("Cancel", func() {
		a.pages.RemovePage("connectModal")
		front, _ := a.pages.GetFrontPage()
		if front == "" {
			a.showDashboard()
		}
	})

	form.SetBorder(true).SetTitle(title).SetTitleColor(mauve).SetBorderColor(surface1)
	form.SetFieldBackgroundColor(mantle).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetLabelColor(text)

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(" [yellow]Tab/Shift+Tab[-]: Navigate  │  [yellow]Esc[-]: Cancel")
	footer.SetBackgroundColor(crust)

	formWithFooter := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true).
		AddItem(footer, 1, 0, false)

	// Esc to cancel
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.pages.RemovePage("connectModal")
			front, _ := a.pages.GetFrontPage()
			if front == "" {
				a.showDashboard()
			}
			return nil
		}
		return event
	})

	connectModal := tview.NewGrid().
		SetColumns(0, 65, 0).
		SetRows(0, 22, 0).
		AddItem(formWithFooter, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage("connectModal", connectModal, true, true)
	a.app.SetFocus(form)
}

// buildConfigFromForm extracts a ConnectionConfig from the form fields
func (a *App) buildConfigFromForm(form *tview.Form) *config.ConnectionConfig {
	getText := func(label string) string {
		item := form.GetFormItemByLabel(label)
		if input, ok := item.(*tview.InputField); ok {
			return input.GetText()
		}
		return ""
	}

	name := getText("Name (*)")
	if name == "" {
		a.ShowAlert("Name is required", "connectModal")
		return nil
	}

	_, typeName := form.GetFormItemByLabel("Type (*)").(*tview.DropDown).GetCurrentOption()
	var dbType config.DBType
	switch typeName {
	case "PostgreSQL":
		dbType = config.PostgreSQL
	case "MySQL":
		dbType = config.MySQL
	case "SQLite":
		dbType = config.SQLite
	}

	cfg := &config.ConnectionConfig{
		Name:     name,
		Type:     dbType,
		Host:     getText("Host"),
		Port:     getText("Port"),
		User:     getText("User"),
		Password: getText("Password"),
		Database: getText("Database"),
		FilePath: getText("File Path (SQLite)"),
	}

	// Validation
	switch dbType {
	case config.SQLite:
		if cfg.FilePath == "" {
			a.ShowAlert("File path is required for SQLite", "connectModal")
			return nil
		}
	default:
		if cfg.Host == "" || cfg.Port == "" || cfg.User == "" || cfg.Database == "" {
			a.ShowAlert("Host, Port, User, and Database are required", "connectModal")
			return nil
		}
	}

	// Set default ports
	if cfg.Port == "" {
		switch dbType {
		case config.PostgreSQL:
			cfg.Port = "5432"
		case config.MySQL:
			cfg.Port = "3306"
		}
	}

	return cfg
}

// connectWithConfig connects to a database using a config and transitions to main UI
func (a *App) connectWithConfig(cfg *config.ConnectionConfig, storeIndex int) {
	db, err := utils.ConnectDB(cfg)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("Connection failed: %v", err), "connectModal")
		return
	}

	a.db = db
	a.dbType = cfg.Type

	if storeIndex >= 0 {
		a.store.MarkUsed(storeIndex)
	}

	// Load tables
	if err := a.LoadTables(); err != nil {
		a.ShowAlert(fmt.Sprintf("Error loading tables: %v", err), "connectModal")
		return
	}

	a.updateStatusBar()
	a.pages.RemovePage("connectModal")
	a.pages.RemovePage("dashboard")
	a.pages.ShowPage("main")
	a.app.SetFocus(a.tables)
}
