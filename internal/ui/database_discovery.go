package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
)

const pageConnectionDatabasePicker = "connectionDatabasePicker"

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
