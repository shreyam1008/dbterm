package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
	"github.com/shreyam1008/dbterm/utils"
)

// showConnectionForm displays a form for new or editing a connection
func (a *App) showConnectionForm(editConn *config.ConnectionConfig, editIndex int) {
	isEdit := editConn != nil

	form := tview.NewForm()

	// ── Name ──
	nameDefault := ""
	if isEdit {
		nameDefault = editConn.Name
	}
	form.AddInputField("Name (*)", nameDefault, 30, nil, nil)

	// ── Type Dropdown ──
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

	// Default values
	hostDefault, portDefault, userDefault, passDefault, dbDefault, fileDefault := "localhost", "5432", "", "", "", ""
	if isEdit {
		hostDefault = editConn.Host
		portDefault = editConn.Port
		userDefault = editConn.User
		passDefault = editConn.Password
		dbDefault = editConn.Database
		fileDefault = editConn.FilePath
	}

	// Track selected type for auto-port and hints
	form.AddDropDown("Type (*)", dbTypes, initialType, func(option string, index int) {
		// Auto-set port when type changes (only if user hasn't typed a custom port)
		portItem := form.GetFormItemByLabel("Port")
		if portField, ok := portItem.(*tview.InputField); ok {
			currentPort := portField.GetText()
			switch option {
			case "PostgreSQL":
				if currentPort == "" || currentPort == "3306" {
					portField.SetText("5432")
				}
			case "MySQL":
				if currentPort == "" || currentPort == "5432" {
					portField.SetText("3306")
				}
			case "SQLite":
				portField.SetText("")
			}
		}
	})

	form.AddInputField("Host", hostDefault, 30, nil, nil)
	form.AddInputField("Port", portDefault, 10, nil, nil)
	form.AddInputField("User", userDefault, 30, nil, nil)
	form.AddPasswordField("Password", passDefault, 30, '*', nil)
	form.AddInputField("Database", dbDefault, 30, nil, nil)
	form.AddInputField("File Path (SQLite)", fileDefault, 45, nil, nil)

	// ── Buttons ──
	title := " ⊕ New Connection "
	btnLabel := "Save & Connect"
	if isEdit {
		title = " ✎ Edit Connection "
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
		a.pages.RemovePage("connectModal")
		a.pages.RemovePage("dashboard")
		a.showDashboard()
	})

	form.AddButton("Test", func() {
		cfg := a.buildConfigFromForm(form)
		if cfg == nil {
			return
		}
		a.testConnection(cfg)
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

	// ── Footer ──
	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(" [yellow]Tab[-] Navigate  │  [yellow]Esc[-] Cancel  │  [gray]SQLite: only File Path needed[-]")
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
		SetColumns(0, 70, 0).
		SetRows(0, 24, 0).
		AddItem(formWithFooter, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage("connectModal", connectModal, true, true)
	a.app.SetFocus(form)
}

// testConnection tries to connect and shows a result toast
func (a *App) testConnection(cfg *config.ConnectionConfig) {
	db, err := utils.ConnectDB(cfg)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("✗ Connection failed\n\n%s\n\n%s",
			err.Error(), connectionHint(err, cfg)), "connectModal")
		return
	}
	db.Close()
	a.ShowAlert(fmt.Sprintf("✓ Connection successful!\n\n%s → %s",
		cfg.TypeLabel(), cfg.Name), "connectModal")
}

// buildConfigFromForm builds and validates a ConnectionConfig from form fields
func (a *App) buildConfigFromForm(form *tview.Form) *config.ConnectionConfig {
	getText := func(label string) string {
		item := form.GetFormItemByLabel(label)
		if input, ok := item.(*tview.InputField); ok {
			return strings.TrimSpace(input.GetText())
		}
		return ""
	}

	name := getText("Name (*)")
	if name == "" {
		a.ShowAlert("Connection name is required.\n\nGive it a short, descriptive name like \"local-dev\" or \"prod-db\".", "connectModal")
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

	// ── Validation ──
	switch dbType {
	case config.SQLite:
		if cfg.FilePath == "" {
			a.ShowAlert("File path is required for SQLite.\n\nExample: /home/user/data.db\nA new file will be created if it doesn't exist.", "connectModal")
			return nil
		}
		// Check parent directory exists for new files
		dir := cfg.FilePath[:strings.LastIndex(cfg.FilePath, "/")+1]
		if dir != "" {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				a.ShowAlert(fmt.Sprintf("Directory does not exist:\n%s\n\nPlease create it first.", dir), "connectModal")
				return nil
			}
		}
	default:
		missing := []string{}
		if cfg.Host == "" {
			missing = append(missing, "Host")
		}
		if cfg.User == "" {
			missing = append(missing, "User")
		}
		if cfg.Database == "" {
			missing = append(missing, "Database")
		}
		if len(missing) > 0 {
			a.ShowAlert(fmt.Sprintf("Required fields missing:\n\n• %s\n\nFill these to connect to %s.", strings.Join(missing, "\n• "), typeName), "connectModal")
			return nil
		}
		// Default port
		if cfg.Port == "" {
			switch dbType {
			case config.PostgreSQL:
				cfg.Port = "5432"
			case config.MySQL:
				cfg.Port = "3306"
			}
		}
	}

	return cfg
}

// connectWithConfig connects and transitions to the main workspace
func (a *App) connectWithConfig(cfg *config.ConnectionConfig, storeIndex int) {
	// Close previous connection if any
	a.cleanup()

	db, err := utils.ConnectDB(cfg)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("✗ Connection failed\n\n%s\n\n%s",
			err.Error(), connectionHint(err, cfg)), "connectModal")
		return
	}

	a.db = db
	a.dbType = cfg.Type
	a.dbName = cfg.Name

	if storeIndex >= 0 {
		a.store.MarkUsed(storeIndex)
	}

	if err := a.LoadTables(); err != nil {
		a.ShowAlert(fmt.Sprintf("Connected, but could not load tables:\n\n%v\n\nYou can still run queries manually.", err), "main")
	}

	a.updateStatusBar("", 0)
	a.results.SetTitle(" Results [yellow](Alt+R)[-] ")

	a.pages.RemovePage("connectModal")
	a.pages.RemovePage("dashboard")
	a.pages.ShowPage("main")
	a.app.SetFocus(a.tables)
}

// connectionHint provides a helpful suggestion based on the error
func connectionHint(err error, cfg *config.ConnectionConfig) string {
	errStr := strings.ToLower(err.Error())

	switch {
	case strings.Contains(errStr, "connection refused"):
		return fmt.Sprintf("💡 Is %s running on %s:%s?", cfg.TypeLabel(), cfg.Host, cfg.Port)
	case strings.Contains(errStr, "no such host") || strings.Contains(errStr, "lookup"):
		return fmt.Sprintf("💡 Could not resolve hostname \"%s\". Check spelling.", cfg.Host)
	case strings.Contains(errStr, "password") || strings.Contains(errStr, "authentication"):
		return "💡 Check your username and password."
	case strings.Contains(errStr, "does not exist") || strings.Contains(errStr, "unknown database"):
		return fmt.Sprintf("💡 Database \"%s\" not found. Check the name.", cfg.Database)
	case strings.Contains(errStr, "timeout") || strings.Contains(errStr, "timed out"):
		return "💡 Connection timed out. Check if the server is reachable."
	case strings.Contains(errStr, "no such file") || strings.Contains(errStr, "unable to open"):
		return fmt.Sprintf("💡 SQLite file not found: %s", cfg.FilePath)
	case strings.Contains(errStr, "permission"):
		return "💡 Permission denied. Check file/user permissions."
	default:
		return "💡 Double-check your connection details."
	}
}
