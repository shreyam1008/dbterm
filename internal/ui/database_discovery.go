package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
)

const (
	pageConnectionDatabasePicker = "connectionDatabasePicker"
	pageServerDatabasePicker     = "serverDatabasePicker"
)

func buildDatabaseDiscoveryConfig(form *tview.Form) (*config.ConnectionConfig, error) {
	if form == nil {
		return nil, fmt.Errorf("connection form is unavailable")
	}

	typeItem := form.GetFormItemByLabel(connLabelType)
	typeDropDown, ok := typeItem.(*tview.DropDown)
	if !ok {
		return nil, fmt.Errorf("could not read selected database type")
	}

	_, typeName := typeDropDown.GetCurrentOption()
	dbType := dbTypeFromName(typeName)
	if dbType != config.MySQL && dbType != config.PostgreSQL {
		return nil, fmt.Errorf("database discovery is available for MySQL and PostgreSQL connections")
	}

	cfg := &config.ConnectionConfig{
		Name:     "database discovery",
		Type:     dbType,
		Host:     formInputValue(form, connFieldHost),
		Port:     formInputValue(form, connFieldPort),
		User:     formInputValue(form, connFieldUser),
		Password: formInputValue(form, connFieldPassword),
		Database: formInputValue(form, connFieldDatabase),
		SSLMode:  formInputValue(form, connFieldSSLMode),
	}

	if dsn := formInputValue(form, connFieldDSN); dsn != "" {
		parsed, err := parseConnectionString(dbType, dsn)
		if err != nil {
			return nil, err
		}
		if parsed.Host != "" {
			cfg.Host = parsed.Host
		}
		if parsed.Port != "" {
			cfg.Port = parsed.Port
		}
		if parsed.User != "" {
			cfg.User = parsed.User
		}
		if parsed.Password != "" {
			cfg.Password = parsed.Password
		}
		if parsed.Database != "" {
			cfg.Database = parsed.Database
		}
		if parsed.SSLMode != "" {
			cfg.SSLMode = parsed.SSLMode
		}
	}

	if cfg.Host == "" {
		return nil, fmt.Errorf("host is required before finding databases")
	}
	if cfg.User == "" {
		return nil, fmt.Errorf("user is required before finding databases")
	}
	if cfg.Port == "" {
		if dbType == config.MySQL {
			cfg.Port = "3306"
		} else {
			cfg.Port = "5432"
		}
	}

	// MySQL permits a server-level connection without a selected database.
	// PostgreSQL requires one, so use its conventional maintenance database
	// when the user has intentionally left the field blank.
	if dbType == config.MySQL {
		cfg.Database = ""
	} else if cfg.Database == "" {
		cfg.Database = "postgres"
	}

	return cfg, nil
}

func applyDiscoveryConnectionFields(form *tview.Form, cfg *config.ConnectionConfig) {
	if form == nil || cfg == nil {
		return
	}
	setFormInputValue(form, connFieldHost, cfg.Host)
	setFormInputValue(form, connFieldPort, cfg.Port)
	setFormInputValue(form, connFieldUser, cfg.User)
	setFormInputValue(form, connFieldPassword, cfg.Password)
	setFormInputValue(form, connFieldSSLMode, cfg.SSLMode)
}

func applyDiscoveredDatabase(form *tview.Form, discoveryCfg *config.ConnectionConfig, databaseName string) {
	if form == nil || discoveryCfg == nil {
		return
	}

	databaseName = strings.TrimSpace(databaseName)
	if databaseName == "" {
		return
	}

	applyDiscoveryConnectionFields(form, discoveryCfg)
	setFormInputValue(form, connFieldDatabase, databaseName)
	if formInputValue(form, connFieldName) == "" {
		setFormInputValue(form, connFieldName, databaseName)
	}

	// If discovery used a DSN, keep it authoritative while updating its
	// database component so Save does not restore the old DSN database.
	if formInputValue(form, connFieldDSN) != "" {
		selectedCfg := *discoveryCfg
		selectedCfg.Database = databaseName
		setFormInputValue(form, connFieldDSN, selectedCfg.BuildConnString())
	}
}

func discoverDatabaseNames(ctx context.Context, cfg *config.ConnectionConfig) ([]string, error) {
	if cfg == nil {
		return nil, fmt.Errorf("connection details are unavailable")
	}

	query := database.ListDatabasesQuery(cfg.Type)
	if query == "" {
		return nil, fmt.Errorf("database discovery is not supported for %s", cfg.TypeLabel())
	}

	db, err := database.Connect(cfg)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("could not list accessible databases: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("could not read a database name: %w", err)
		}
		name = strings.TrimSpace(name)
		if name != "" {
			names = append(names, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("could not finish listing databases: %w", err)
	}

	return names, nil
}

func (a *App) discoverConnectionDatabases(form *tview.Form) {
	cfg, err := buildDatabaseDiscoveryConfig(form)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not find databases:\n\n%v", iconWarn, err), "connectModal")
		return
	}
	applyDiscoveryConnectionFields(form, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	loadingToken := a.showLoadingModal(
		fmt.Sprintf("Finding databases on %s:%s...", cfg.Host, cfg.Port),
		withLoadingCancel("Press Esc to cancel database discovery.", cancel),
	)

	go func() {
		defer cancel()
		names, discoveryErr := discoverDatabaseNames(ctx, cfg)

		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(loadingToken) {
				return
			}
			if errors.Is(discoveryErr, context.Canceled) {
				return
			}
			if discoveryErr != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not find databases on %s:%s:\n\n%v\n\n%s",
					iconWarn, cfg.Host, cfg.Port, discoveryErr, connectionHint(discoveryErr, cfg)), "connectModal")
				return
			}
			if len(names) == 0 {
				a.ShowAlert(fmt.Sprintf("%s No user databases are visible to %q on %s:%s.",
					iconInfo, cfg.User, cfg.Host, cfg.Port), "connectModal")
				return
			}

			a.showConnectionDatabasePicker(form, cfg, names)
		})
	}()
}

func (a *App) showConnectionDatabasePicker(form *tview.Form, discoveryCfg *config.ConnectionConfig, names []string) {
	list := tview.NewList().ShowSecondaryText(false)
	list.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Select Database (%s) ", iconDatabase, discoveryCfg.TypeLabel())).
		SetBorderColor(blue).
		SetTitleColor(mauve).
		SetBackgroundColor(bg)
	list.SetMainTextColor(text)
	list.SetSelectedBackgroundColor(surface0)
	list.SetSelectedTextColor(green)

	returnToForm := func() {
		a.pages.RemovePage(pageConnectionDatabasePicker)
		if databaseIndex := form.GetFormItemIndex(connLabelDatabase); databaseIndex >= 0 {
			form.SetFocus(databaseIndex)
		}
		a.app.SetFocus(form)
	}

	for _, name := range names {
		databaseName := name
		list.AddItem(fmt.Sprintf("  %s  %s", iconDatabase, tview.Escape(databaseName)), "", 0, func() {
			applyDiscoveredDatabase(form, discoveryCfg, databaseName)
			returnToForm()
		})
	}

	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			returnToForm()
			return nil
		}
		return event
	})

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(fmt.Sprintf(" [yellow]↑/↓[-] Choose  │  [yellow]Enter[-] Select  │  [yellow]Esc[-] Back %s  │  [#6c7086]%d found[-]", iconBack, len(names)))
	footer.SetBackgroundColor(crust)

	picker := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(list, 0, 1, true).
		AddItem(footer, 1, 0, false)

	modalW, modalH := a.modalSize(42, 64, 10, 24)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(picker, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageConnectionDatabasePicker, grid, true, true)
	a.app.SetFocus(list)
}

// browseDatabasesForSavedConnection starts from the selected saved database.
// Server databases can be discovered directly; single-database local/cloud
// connections instead open a new form with their reusable scope credentials.
func (a *App) browseDatabasesForSavedConnection(storeIndex int) {
	if a.store == nil || storeIndex < 0 || storeIndex >= len(a.store.Connections) {
		a.ShowAlert(fmt.Sprintf("%s Select a saved connection first.", iconInfo), "dashboard")
		return
	}
	base := a.store.Connections[storeIndex]
	if base.Type != config.PostgreSQL && base.Type != config.MySQL {
		prefill := newConnectionInSameScope(base)
		title := fmt.Sprintf(" %s Add Another %s Database ", iconConnect, base.TypeLabel())
		a.showPrefilledConnectionForm(&prefill, title)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	loadingToken := a.showLoadingModal(
		fmt.Sprintf("Finding databases on %s:%s...", base.Host, base.Port),
		withLoadingCancel("Press Esc to cancel database discovery.", cancel),
	)

	go func() {
		defer cancel()
		names, discoveryErr := discoverDatabaseNames(ctx, &base)
		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(loadingToken) {
				return
			}
			if errors.Is(discoveryErr, context.Canceled) {
				return
			}
			if discoveryErr != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not list databases on %s:%s:\n\n%v\n\n%s",
					iconWarn, base.Host, base.Port, discoveryErr, connectionHint(discoveryErr, &base)), "dashboard")
				return
			}
			names = databasesWithCurrentFirst(names, base.Database)
			if len(names) == 0 {
				a.ShowAlert(fmt.Sprintf("%s No databases are visible to %q on %s:%s.",
					iconInfo, base.User, base.Host, base.Port), "dashboard")
				return
			}
			a.showServerDatabasePicker(storeIndex, base, names)
		})
	}()
}

func databasesWithCurrentFirst(names []string, current string) []string {
	current = strings.TrimSpace(current)
	seen := make(map[string]struct{}, len(names)+1)
	ordered := make([]string, 0, len(names)+1)
	if current != "" {
		seen[current] = struct{}{}
		ordered = append(ordered, current)
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		ordered = append(ordered, name)
	}
	return ordered
}

func (a *App) showServerDatabasePicker(baseIndex int, base config.ConnectionConfig, names []string) {
	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Databases on %s:%s ", iconDatabase, tview.Escape(base.Host), tview.Escape(base.Port))).
		SetBorderColor(blue).
		SetTitleColor(mauve).
		SetBackgroundColor(bg)
	list.SetMainTextColor(text)
	list.SetSecondaryTextColor(subtext0)
	list.SetSelectedBackgroundColor(surface0)
	list.SetSelectedTextColor(green)

	returnToDashboard := func() {
		a.pages.RemovePage(pageServerDatabasePicker)
		a.pages.ShowPage("dashboard")
		a.app.SetFocus(a.pages)
	}

	for _, name := range names {
		databaseName := name
		savedIndex := savedDatabaseIndex(a.store.Connections, base, databaseName)
		secondary := "Accessible with this server login — Enter to open"
		status := "[#89b4fa]available[-]"
		if savedIndex >= 0 {
			status = "[green]saved[-]"
			secondary = fmt.Sprintf("Also saved as %s — Enter opens with the selected server login", tview.Escape(a.store.Connections[savedIndex].Name))
		}
		if databaseName == base.Database {
			status = "[yellow]current / default[-]"
			secondary = fmt.Sprintf("Default database for %s", tview.Escape(base.Name))
		}

		list.AddItem(fmt.Sprintf("  %s  %s  %s", iconDatabase, tview.Escape(databaseName), status), secondary, 0, func() {
			a.pages.RemovePage(pageServerDatabasePicker)
			selected := connectionForDatabase(base, databaseName)
			a.connectWithConfig(&selected, baseIndex)
		})
	}

	selectedDatabase := func() string {
		index := list.GetCurrentItem()
		if index < 0 || index >= len(names) {
			return ""
		}
		return names[index]
	}
	openNewSavedConnection := func() {
		databaseName := selectedDatabase()
		if databaseName == "" {
			return
		}
		a.pages.RemovePage(pageServerDatabasePicker)
		prefill := newConnectionForDatabase(base, databaseName)
		title := fmt.Sprintf(" %s Save %s on %s:%s ", iconConnect, tview.Escape(databaseName), tview.Escape(base.Host), tview.Escape(base.Port))
		a.showPrefilledConnectionForm(&prefill, title)
	}
	setDefaultDatabase := func() {
		databaseName := selectedDatabase()
		if databaseName == "" {
			return
		}
		updated := base
		updated.Database = databaseName
		if err := a.store.Update(baseIndex, updated); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not set the default database:\n\n%v", iconWarn, err), pageServerDatabasePicker)
			return
		}
		a.pages.RemovePage(pageServerDatabasePicker)
		a.pages.RemovePage("dashboard")
		a.showDashboard()
		a.ShowAlert(fmt.Sprintf("%s %s is now the default database for %s.", iconSuccess, tview.Escape(databaseName), tview.Escape(base.Name)), "dashboard")
	}

	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			returnToDashboard()
			return nil
		}
		if event.Key() == tcell.KeyRune && event.Modifiers()&(tcell.ModCtrl|tcell.ModAlt|tcell.ModMeta) == 0 {
			switch event.Rune() {
			case 'n', 'N':
				openNewSavedConnection()
				return nil
			case 'd', 'D':
				setDefaultDatabase()
				return nil
			}
		}
		return event
	})

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(fmt.Sprintf(" [yellow]Enter[-] Open  │  [yellow]D[-] Set default  │  [yellow]N[-] Save separately  │  [yellow]Esc[-] Dashboard %s  │  [#6c7086]%d accessible[-]", iconBack, len(names)))
	footer.SetBackgroundColor(crust)

	picker := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(list, 0, 1, true).
		AddItem(footer, 1, 0, false)
	modalW, modalH := a.modalSize(56, 82, 12, 28)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(picker, 1, 1, 1, 1, 0, 0, true)
	a.pages.AddPage(pageServerDatabasePicker, grid, true, true)
	a.app.SetFocus(list)
}

func savedDatabaseIndex(connections []config.ConnectionConfig, base config.ConnectionConfig, databaseName string) int {
	for index := range connections {
		connection := connections[index]
		if connection.Type == base.Type &&
			strings.EqualFold(strings.TrimSpace(connection.Host), strings.TrimSpace(base.Host)) &&
			strings.TrimSpace(connection.Port) == strings.TrimSpace(base.Port) &&
			connection.User == base.User &&
			connection.Database == databaseName {
			return index
		}
	}
	return -1
}

func newConnectionForDatabase(base config.ConnectionConfig, databaseName string) config.ConnectionConfig {
	connection := base
	connection.ID = ""
	connection.Database = strings.TrimSpace(databaseName)
	connection.LastUsed = ""
	connection.Active = false
	if strings.EqualFold(strings.TrimSpace(base.Name), strings.TrimSpace(base.Database)) || strings.TrimSpace(base.Name) == "" {
		connection.Name = connection.Database
	} else {
		connection.Name = fmt.Sprintf("%s / %s", strings.TrimSpace(base.Name), connection.Database)
	}
	return connection
}

func connectionForDatabase(base config.ConnectionConfig, databaseName string) config.ConnectionConfig {
	connection := base
	connection.Database = strings.TrimSpace(databaseName)
	connection.Name = connection.Database
	return connection
}

func newConnectionInSameScope(base config.ConnectionConfig) config.ConnectionConfig {
	connection := base
	connection.ID = ""
	connection.Name = ""
	connection.LastUsed = ""
	connection.Active = false
	switch connection.Type {
	case config.SQLite:
		if directory := filepath.Dir(connection.FilePath); directory != "." {
			connection.FilePath = directory + string(filepath.Separator)
		} else {
			connection.FilePath = ""
		}
	case config.Turso:
		connection.Host = ""
	case config.CloudflareD1:
		connection.DatabaseID = ""
	}
	return connection
}
