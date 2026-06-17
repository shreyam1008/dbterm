package ui

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/gdamore/tcell/v2"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
	"github.com/shreyam1008/dbterm/utils"
)

// DirectConnect connects to a database using provided parameters
func (a *App) DirectConnect(dbType config.DBType, dsn, name string) error {
	// Create a temporary config
	cfg := &config.ConnectionConfig{
		Name: name,
		Type: dbType,
	}

	// For DirectConnect, we need to handle DSN differently since utils.ConnectDB expects
	// specific fields for some DBs, or we can patch it to use DSN if available.
	// But looking at ui/connect.go, parseConnectionString fills the fields.
	// Since we constructed the DSN in services.go, let's parse it back to config fields!
	// This ensures utils.ConnectDB (which uses fields) works correctly.

	parsed, err := parseConnectionString(dbType, dsn)
	if err != nil {
		return err
	}
	// Copy parsed fields to our config
	cfg.Host = parsed.Host
	cfg.Port = parsed.Port
	cfg.User = parsed.User
	cfg.Password = parsed.Password
	cfg.Database = parsed.Database
	cfg.SSLMode = parsed.SSLMode

	// Connect
	db, err := utils.ConnectDB(cfg)
	if err != nil {
		return err
	}

	// Update App state
	a.cleanup()
	a.db = db
	a.dbType = cfg.Type
	a.dbName = cfg.Name
	a.activeConn = cloneConnectionConfig(cfg)

	// Load tables
	if err := a.LoadTables(); err != nil {
		// We connected but failed to list tables. This is a partial success/warning state.
		// For DirectConnect, let's just return the error so the UI shows it,
		// but we keep the connection open.
		return fmt.Errorf("connected but failed to list tables: %w", err)
	}

	// Reset page title for results
	a.results.SetTitle(fmt.Sprintf(" %s Results [yellow](Alt+R)[-] ", iconResults))

	return nil
}

func cloneConnectionConfig(cfg *config.ConnectionConfig) *config.ConnectionConfig {
	if cfg == nil {
		return nil
	}
	copyCfg := *cfg
	return &copyCfg
}

const (
	connLabelName       = "Name (*)"
	connLabelType       = "Type (*) " + iconDropdown
	connLabelReadOnly   = "Read-Only (block writes)"
	connLabelDSN        = "Connection String (Optional)"
	connLabelHost       = "Host"
	connLabelPort       = "Port"
	connLabelUser       = "User"
	connLabelPassword   = "Password"
	connLabelDatabase   = "Database"
	connLabelFilePath   = "File Path (SQLite)"
	connLabelAuthToken  = "Auth Token"
	connLabelAccountID  = "Account ID"
	connLabelDatabaseID = "Database ID (UUID)"
)

type connectFieldKey string

const (
	connFieldName       connectFieldKey = "name"
	connFieldType       connectFieldKey = "type"
	connFieldReadOnly   connectFieldKey = "read_only"
	connFieldDSN        connectFieldKey = "dsn"
	connFieldHost       connectFieldKey = "host"
	connFieldPort       connectFieldKey = "port"
	connFieldUser       connectFieldKey = "user"
	connFieldPassword   connectFieldKey = "password"
	connFieldDatabase   connectFieldKey = "database"
	connFieldFilePath   connectFieldKey = "file_path"
	connFieldAuthToken  connectFieldKey = "auth_token"
	connFieldAccountID  connectFieldKey = "account_id"
	connFieldDatabaseID connectFieldKey = "database_id"
)

var connectFieldLabels = map[connectFieldKey]string{
	connFieldName:       connLabelName,
	connFieldType:       connLabelType,
	connFieldReadOnly:   connLabelReadOnly,
	connFieldDSN:        connLabelDSN,
	connFieldHost:       connLabelHost,
	connFieldPort:       connLabelPort,
	connFieldUser:       connLabelUser,
	connFieldPassword:   connLabelPassword,
	connFieldDatabase:   connLabelDatabase,
	connFieldFilePath:   connLabelFilePath,
	connFieldAuthToken:  connLabelAuthToken,
	connFieldAccountID:  connLabelAccountID,
	connFieldDatabaseID: connLabelDatabaseID,
}

var dynamicConnectFields = []connectFieldKey{
	connFieldDSN,
	connFieldHost,
	connFieldPort,
	connFieldUser,
	connFieldPassword,
	connFieldDatabase,
	connFieldFilePath,
	connFieldAuthToken,
	connFieldAccountID,
	connFieldDatabaseID,
}

func connectFieldLabel(key connectFieldKey) string {
	return connectFieldLabels[key]
}

// showConnectionForm displays a form for new or editing a connection
func (a *App) showConnectionForm(editConn *config.ConnectionConfig, editIndex int) {
	isEdit := editConn != nil

	form := tview.NewForm()

	dbTypes := []string{"PostgreSQL", "MySQL", "SQLite", "Turso", "Cloudflare D1"}
	initialType := 0
	nameDefault := ""
	connStringDefault := ""
	hostDefault, portDefault, userDefault, passDefault, dbDefault, fileDefault := "localhost", "5432", "", "", "", ""
	readOnlyDefault := false
	authTokenDefault, accountIDDefault, dbIDDefault := "", "", ""
	if isEdit {
		nameDefault = editConn.Name
		switch editConn.Type {
		case config.MySQL:
			initialType = 1
		case config.SQLite:
			initialType = 2
		case config.Turso:
			initialType = 3
		case config.CloudflareD1:
			initialType = 4
		}
		hostDefault = editConn.Host
		portDefault = editConn.Port
		userDefault = editConn.User
		passDefault = editConn.Password
		dbDefault = editConn.Database
		fileDefault = editConn.FilePath
		readOnlyDefault = editConn.ReadOnly
		authTokenDefault = editConn.AuthToken
		accountIDDefault = editConn.AccountID
		dbIDDefault = editConn.DatabaseID
	}

	form.AddInputField(connLabelName, nameDefault, 30, nil, nil)
	form.AddDropDown(connLabelType, dbTypes, initialType, nil)
	form.AddCheckbox(connLabelReadOnly, readOnlyDefault, nil)

	fieldValues := map[connectFieldKey]string{
		connFieldDSN:        connStringDefault,
		connFieldHost:       hostDefault,
		connFieldPort:       portDefault,
		connFieldUser:       userDefault,
		connFieldPassword:   passDefault,
		connFieldDatabase:   dbDefault,
		connFieldFilePath:   fileDefault,
		connFieldAuthToken:  authTokenDefault,
		connFieldAccountID:  accountIDDefault,
		connFieldDatabaseID: dbIDDefault,
	}

	removeDynamicFields := func() {
		// Preserve latest typed values before rebuilding type-specific fields.
		for _, key := range dynamicConnectFields {
			if form.GetFormItemIndex(connectFieldLabel(key)) >= 0 {
				fieldValues[key] = formInputValue(form, key)
			}
		}
		for _, key := range dynamicConnectFields {
			idx := form.GetFormItemIndex(connectFieldLabel(key))
			if idx >= 0 {
				form.RemoveFormItem(idx)
			}
		}
	}

	addNetworkFields := func() {
		form.AddInputField(connLabelDSN, fieldValues[connFieldDSN], 72, nil, nil)
		form.AddInputField(connLabelHost, fieldValues[connFieldHost], 30, nil, nil)
		form.AddInputField(connLabelPort, fieldValues[connFieldPort], 10, nil, nil)
		form.AddInputField(connLabelUser, fieldValues[connFieldUser], 30, nil, nil)
		form.AddPasswordField(connLabelPassword, fieldValues[connFieldPassword], 30, '*', nil)
		form.AddInputField(connLabelDatabase, fieldValues[connFieldDatabase], 30, nil, nil)
	}

	addSQLiteFields := func() {
		form.AddInputField(connLabelFilePath, fieldValues[connFieldFilePath], 60, nil, nil)
	}

	addTursoFields := func() {
		// Re-use 'Host' for the Database URL
		form.AddInputField(connLabelHost, fieldValues[connFieldHost], 60, nil, nil)
		form.AddPasswordField(connLabelAuthToken, fieldValues[connFieldAuthToken], 60, '*', nil)
	}

	addD1Fields := func() {
		form.AddInputField(connLabelAccountID, fieldValues[connFieldAccountID], 40, nil, nil)
		form.AddInputField(connLabelDatabaseID, fieldValues[connFieldDatabaseID], 40, nil, nil)
		form.AddPasswordField(connLabelAuthToken, fieldValues[connFieldAuthToken], 60, '*', nil)
	}

	_, initialTypeName := form.GetFormItemByLabel(connLabelType).(*tview.DropDown).GetCurrentOption()
	currentTypeName := initialTypeName

	var footer *tview.TextView
	updateFooter := func() {
		if footer == nil {
			return
		}
		screenW, _ := a.getScreenSize()
		footer.SetText(connectFooterText(screenW, dbTypeFromName(currentTypeName)))
	}

	applyFieldsForType := func(typeName string) {
		removeDynamicFields()
		currentTypeName = typeName
		switch typeName {
		case "SQLite":
			addSQLiteFields()
		case "Turso":
			addTursoFields()
		case "Cloudflare D1":
			addD1Fields()
		default:
			// Auto-default ports for network DBs if empty or swapped.
			switch typeName {
			case "PostgreSQL":
				if fieldValues[connFieldPort] == "" || fieldValues[connFieldPort] == "3306" {
					fieldValues[connFieldPort] = "5432"
				}
			case "MySQL":
				if fieldValues[connFieldPort] == "" || fieldValues[connFieldPort] == "5432" {
					fieldValues[connFieldPort] = "3306"
				}
			}
			addNetworkFields()
		}
		updateFooter()
	}

	typeDropDown := form.GetFormItemByLabel(connLabelType).(*tview.DropDown)
	typeDropDown.SetSelectedFunc(func(option string, _ int) {
		applyFieldsForType(option)
	})
	applyFieldsForType(initialTypeName)

	// ── Buttons ──
	title := fmt.Sprintf(" %s New Connection ", iconConnect)
	btnLabel := "Save & Connect"
	if isEdit {
		title = fmt.Sprintf(" ✎ Edit %s ", iconConnect)
		btnLabel = "Update & Connect"
	}

	form.AddButton(btnLabel, func() {
		cfg := a.buildConfigFromForm(form)
		if cfg == nil {
			return
		}
		if isEdit {
			if err := a.store.Update(editIndex, *cfg); err != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not update connection:\n\n%v", iconWarn, err), "connectModal")
				return
			}
			a.connectWithConfig(cfg, editIndex)
		} else {
			if err := a.store.Add(*cfg); err != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not save connection:\n\n%v", iconWarn, err), "connectModal")
				return
			}
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
			if err := a.store.Update(editIndex, *cfg); err != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not update connection:\n\n%v", iconWarn, err), "connectModal")
				return
			}
		} else {
			if err := a.store.Add(*cfg); err != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not save connection:\n\n%v", iconWarn, err), "connectModal")
				return
			}
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

	form.AddButton("Parse DSN", func() {
		if _, err := a.applyConnectionStringToForm(form); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not parse connection string:\n\n%v", iconWarn, err), "connectModal")
		}
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
	footer = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	updateFooter()

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

	modalW, modalH := a.modalSize(56, 88, 18, 28)
	connectModal := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(formWithFooter, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage("connectModal", connectModal, true, true)
	a.app.SetFocus(form)
}

// testConnection tries to connect and shows a result toast
func (a *App) testConnection(cfg *config.ConnectionConfig) {
	a.showLoadingModal(fmt.Sprintf("Testing %s connection...", cfg.TypeLabel()))

	go func() {
		db, err := utils.ConnectDB(cfg)
		if db != nil {
			db.Close()
		}

		a.app.QueueUpdateDraw(func() {
			a.pages.RemovePage("loading")
			if err != nil {
				a.ShowAlert(fmt.Sprintf("%s Connection failed\n\n%s\n\n%s", iconFail,
					err.Error(), connectionHint(err, cfg)), "connectModal")
				return
			}
			a.ShowAlert(fmt.Sprintf("%s Connection successful\n\n%s -> %s", iconSuccess,
				cfg.TypeLabel(), cfg.Name), "connectModal")
		})
	}()
}

// buildConfigFromForm builds and validates a ConnectionConfig from form fields
func (a *App) buildConfigFromForm(form *tview.Form) *config.ConnectionConfig {
	getText := func(key connectFieldKey) string { return formInputValue(form, key) }

	name := getText(connFieldName)
	if name == "" {
		a.ShowAlert(fmt.Sprintf("%s Connection name is required.\n\nGive it a short, descriptive name like \"local-dev\" or \"prod-db\".", iconInfo), "connectModal")
		return nil
	}

	_, typeName := form.GetFormItemByLabel(connLabelType).(*tview.DropDown).GetCurrentOption()
	dbType := dbTypeFromName(typeName)

	cfg := &config.ConnectionConfig{
		Name:       name,
		Type:       dbType,
		Host:       getText(connFieldHost),
		Port:       getText(connFieldPort),
		User:       getText(connFieldUser),
		Password:   getText(connFieldPassword),
		Database:   getText(connFieldDatabase),
		ReadOnly:   formCheckboxChecked(form, connFieldReadOnly),
		FilePath:   getText(connFieldFilePath),
		AuthToken:  getText(connFieldAuthToken),
		AccountID:  getText(connFieldAccountID),
		DatabaseID: getText(connFieldDatabaseID),
	}

	// Optional network DSN: if present, parse and auto-fill individual fields.
	if dbType != config.SQLite && dbType != config.Turso && dbType != config.CloudflareD1 {
		if connString := getText(connFieldDSN); connString != "" {
			parsedCfg, err := parseConnectionString(dbType, connString)
			if err != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not parse connection string:\n\n%v", iconWarn, err), "connectModal")
				return nil
			}
			if parsedCfg.Host != "" {
				cfg.Host = parsedCfg.Host
				setFormInputValue(form, connFieldHost, parsedCfg.Host)
			}
			if parsedCfg.Port != "" {
				cfg.Port = parsedCfg.Port
				setFormInputValue(form, connFieldPort, parsedCfg.Port)
			}
			if parsedCfg.User != "" {
				cfg.User = parsedCfg.User
				setFormInputValue(form, connFieldUser, parsedCfg.User)
			}
			if parsedCfg.Password != "" {
				cfg.Password = parsedCfg.Password
				setFormInputValue(form, connFieldPassword, parsedCfg.Password)
			}
			if parsedCfg.Database != "" {
				cfg.Database = parsedCfg.Database
				setFormInputValue(form, connFieldDatabase, parsedCfg.Database)
			}
			if parsedCfg.SSLMode != "" {
				cfg.SSLMode = parsedCfg.SSLMode
			}
		}
	}

	// ── Validation ──
	switch dbType {
	case config.SQLite:
		if cfg.FilePath == "" {
			a.ShowAlert(fmt.Sprintf("%s File path is required for SQLite.\n\nExample: /home/user/data.db\nA new file will be created if it doesn't exist.", iconInfo), "connectModal")
			return nil
		}
		// Check parent directory exists for new files
		dir := filepath.Dir(cfg.FilePath)
		if dir != "." && dir != "" {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				a.ShowAlert(fmt.Sprintf("%s Directory does not exist:\n%s\n\nPlease create it first.", iconWarn, dir), "connectModal")
				return nil
			}
		}
	case config.Turso:
		if cfg.Host == "" {
			a.ShowAlert(fmt.Sprintf("%s Database URL is required for Turso.\n\nExample: libsql://mydb-user.turso.io", iconInfo), "connectModal")
			return nil
		}
		// Auth token is usually required for remote, but maybe not for local dev?
		// We'll leave it optional in validation but robust in practice.
	case config.CloudflareD1:
		if cfg.AccountID == "" || cfg.DatabaseID == "" || cfg.AuthToken == "" {
			a.ShowAlert(fmt.Sprintf("%s Account ID, Database ID, and API Token are required for D1.", iconInfo), "connectModal")
			return nil
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
			a.ShowAlert(fmt.Sprintf("%s Required fields missing:\n\n• %s\n\nFill these to connect to %s.", iconInfo, strings.Join(missing, "\n• "), typeName), "connectModal")
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

func dbTypeFromName(typeName string) config.DBType {
	switch typeName {
	case "PostgreSQL":
		return config.PostgreSQL
	case "MySQL":
		return config.MySQL
	case "SQLite":
		return config.SQLite
	case "Turso":
		return config.Turso
	case "Cloudflare D1":
		return config.CloudflareD1
	default:
		return config.PostgreSQL
	}
}

func connectFooterText(width int, dbType config.DBType) string {
	switch dbType {
	case config.SQLite:
		switch {
		case width < 78:
			return fmt.Sprintf(" [yellow]Tab[-] Next  │  [yellow]Esc[-] Back %s", iconBack)
		default:
			return fmt.Sprintf(" [yellow]Tab[-] Navigate  │  [yellow]Esc[-] Back %s  │  [gray]SQLite: only File Path needed[-]", iconBack)
		}
	case config.Turso:
		return fmt.Sprintf(" [yellow]Tab[-] Navigate  │  [yellow]Esc[-] Back %s  │  [gray]Turso: URL + Auth Token[-]", iconBack)
	case config.CloudflareD1:
		return fmt.Sprintf(" [yellow]Tab[-] Navigate  │  [yellow]Esc[-] Back %s  │  [gray]D1: Account ID + DB ID + Token[-]", iconBack)
	default:
		switch {
		case width < 78:
			return fmt.Sprintf(" [yellow]Tab[-] Next  │  [yellow]Esc[-] Back %s  │  [yellow]Parse DSN[-] %s", iconBack, iconDropdown)
		default:
			return fmt.Sprintf(" [yellow]Tab[-] Navigate  │  [yellow]Esc[-] Back %s  │  [yellow]Parse DSN[-] %s auto-fills host/user/db[-]", iconBack, iconDropdown)
		}
	}
}

func formInputValue(form *tview.Form, key connectFieldKey) string {
	return formInputValueByLabel(form, connectFieldLabel(key))
}

func formInputValueByLabel(form *tview.Form, label string) string {
	item := form.GetFormItemByLabel(label)
	if input, ok := item.(*tview.InputField); ok {
		return strings.TrimSpace(input.GetText())
	}
	return ""
}

func formCheckboxChecked(form *tview.Form, key connectFieldKey) bool {
	item := form.GetFormItemByLabel(connectFieldLabel(key))
	if checkbox, ok := item.(*tview.Checkbox); ok {
		return checkbox.IsChecked()
	}
	return false
}

func setFormInputValue(form *tview.Form, key connectFieldKey, value string) {
	setFormInputValueByLabel(form, connectFieldLabel(key), value)
}

func setFormInputValueByLabel(form *tview.Form, label, value string) {
	item := form.GetFormItemByLabel(label)
	if input, ok := item.(*tview.InputField); ok {
		input.SetText(value)
	}
}

func (a *App) applyConnectionStringToForm(form *tview.Form) (*config.ConnectionConfig, error) {
	dsn := formInputValue(form, connFieldDSN)
	if dsn == "" {
		return nil, fmt.Errorf("connection string is empty")
	}

	typeItem := form.GetFormItemByLabel(connLabelType)
	typeDropDown, ok := typeItem.(*tview.DropDown)
	if !ok {
		return nil, fmt.Errorf("could not read selected database type")
	}

	_, typeName := typeDropDown.GetCurrentOption()
	dbType := dbTypeFromName(typeName)
	if dbType == config.SQLite || dbType == config.Turso || dbType == config.CloudflareD1 {
		return nil, fmt.Errorf("this database type does not support DSN parsing here")
	}

	parsedCfg, err := parseConnectionString(dbType, dsn)
	if err != nil {
		return nil, err
	}

	setFormInputValue(form, connFieldHost, parsedCfg.Host)
	setFormInputValue(form, connFieldPort, parsedCfg.Port)
	setFormInputValue(form, connFieldUser, parsedCfg.User)
	setFormInputValue(form, connFieldPassword, parsedCfg.Password)
	setFormInputValue(form, connFieldDatabase, parsedCfg.Database)
	return parsedCfg, nil
}

func parseConnectionString(dbType config.DBType, connString string) (*config.ConnectionConfig, error) {
	switch dbType {
	case config.PostgreSQL:
		return parsePostgresConnectionString(connString)
	case config.MySQL:
		return parseMySQLConnectionString(connString)
	default:
		return nil, fmt.Errorf("connection strings are supported for PostgreSQL and MySQL only")
	}
}

func parseMySQLConnectionString(connString string) (*config.ConnectionConfig, error) {
	cfg, err := mysql.ParseDSN(strings.TrimSpace(connString))
	if err != nil {
		return nil, fmt.Errorf("invalid MySQL DSN: %w", err)
	}

	host, port := splitHostPortWithDefault(cfg.Addr, "3306")
	if cfg.Net == "unix" {
		host = "localhost"
		port = "3306"
	}

	return &config.ConnectionConfig{
		Type:     config.MySQL,
		Host:     host,
		Port:     port,
		User:     cfg.User,
		Password: cfg.Passwd,
		Database: cfg.DBName,
	}, nil
}

func parsePostgresConnectionString(connString string) (*config.ConnectionConfig, error) {
	dsn := strings.TrimSpace(connString)
	lower := strings.ToLower(dsn)
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		return parsePostgresURL(dsn)
	}
	return parsePostgresKeyValueDSN(dsn)
}

func parsePostgresURL(connString string) (*config.ConnectionConfig, error) {
	u, err := url.Parse(connString)
	if err != nil {
		return nil, fmt.Errorf("invalid PostgreSQL URL: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return nil, fmt.Errorf("unsupported PostgreSQL URL scheme: %s", u.Scheme)
	}

	host, port := splitHostPortWithDefault(u.Host, "5432")
	database := strings.TrimPrefix(u.Path, "/")

	user := ""
	password := ""
	if u.User != nil {
		user = u.User.Username()
		password, _ = u.User.Password()
	}

	return &config.ConnectionConfig{
		Type:     config.PostgreSQL,
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		Database: database,
		SSLMode:  u.Query().Get("sslmode"),
	}, nil
}

func parsePostgresKeyValueDSN(connString string) (*config.ConnectionConfig, error) {
	fields := splitConnectionStringTokens(connString)
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		keyValue := strings.SplitN(field, "=", 2)
		if len(keyValue) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(keyValue[0]))
		value := strings.TrimSpace(keyValue[1])
		value = strings.Trim(value, `"'`)
		values[key] = value
	}

	database := values["dbname"]
	if database == "" {
		database = values["database"]
	}

	if values["host"] == "" && values["user"] == "" && values["password"] == "" && database == "" {
		return nil, fmt.Errorf("invalid PostgreSQL DSN")
	}

	host := values["host"]
	if host == "" {
		host = "localhost"
	}
	port := values["port"]
	if port == "" {
		port = "5432"
	}

	return &config.ConnectionConfig{
		Type:     config.PostgreSQL,
		Host:     host,
		Port:     port,
		User:     values["user"],
		Password: values["password"],
		Database: database,
		SSLMode:  values["sslmode"],
	}, nil
}

func splitConnectionStringTokens(connString string) []string {
	var (
		tokens  []string
		current strings.Builder
		quote   rune
		escaped bool
	)

	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}

	for _, r := range connString {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return tokens
}

func splitHostPortWithDefault(address, defaultPort string) (string, string) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "localhost", defaultPort
	}

	if host, port, err := net.SplitHostPort(address); err == nil {
		if host == "" {
			host = "localhost"
		}
		if port == "" {
			port = defaultPort
		}
		return host, port
	}

	// Handle plain host:port forms that may not be bracketed IPv6 values.
	if strings.Count(address, ":") == 1 && !strings.HasPrefix(address, "[") {
		parts := strings.SplitN(address, ":", 2)
		host := strings.TrimSpace(parts[0])
		port := strings.TrimSpace(parts[1])
		if host == "" {
			host = "localhost"
		}
		if port == "" {
			port = defaultPort
		}
		return host, port
	}

	return address, defaultPort
}

// connectWithConfig connects and transitions to the main workspace
func (a *App) connectWithConfig(cfg *config.ConnectionConfig, storeIndex int) {
	a.showLoadingModal(fmt.Sprintf("%s Connecting to %s...", iconConnect, cfg.Name))

	selectedTable := a.selectedTable
	currentTableIndex := a.tables.GetCurrentItem()

	go func() {
		db, err := utils.ConnectDB(cfg)
		var snapshot *tableListSnapshot
		var tableLoadErr error
		if err == nil {
			snapshot, tableLoadErr = loadTableListSnapshot(db, cfg.Type, selectedTable, strings.TrimSpace(cfg.Database), currentTableIndex)
		}

		a.app.QueueUpdateDraw(func() {
			a.pages.RemovePage("loading")

			if err != nil {
				a.ShowAlert(fmt.Sprintf("%s Connection failed\n\n%s\n\n%s", iconFail,
					err.Error(), connectionHint(err, cfg)), "connectModal")
				return
			}

			// Close previous connection only after the new one is ready.
			a.cleanup()
			a.db = db
			a.dbType = cfg.Type
			a.dbName = cfg.Name
			a.activeConn = cloneConnectionConfig(cfg)

			if storeIndex >= 0 {
				if err := a.store.MarkUsed(storeIndex); err != nil {
					a.ShowAlert(fmt.Sprintf("%s Connected, but failed to update saved state:\n\n%v", iconWarn, err), "main")
				}
			}

			if tableLoadErr != nil {
				a.applyTableListSnapshot(&tableListSnapshot{
					items: []tableListSnapshotItem{{label: fmt.Sprintf("[gray]%s Tables could not be loaded[-]", iconWarn)}},
				})
				a.ShowAlert(fmt.Sprintf("%s Connected, but could not load tables:\n\n%v\n\nYou can still run queries manually.", iconWarn, tableLoadErr), "main")
			} else {
				a.applyTableListSnapshot(snapshot)
				a.loadDatabaseObjects()
			}

			a.updateStatusBar("", 0)
			a.results.SetTitle(fmt.Sprintf(" %s Results [yellow](Alt+R)[-] ", iconResults))

			a.pages.RemovePage("connectModal")
			a.pages.RemovePage("dashboard")
			a.pages.ShowPage("main")
			a.app.SetFocus(a.tables)
		})
	}()
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
