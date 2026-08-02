package ui

import (
	"strings"
	"testing"

	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

func newDatabaseDiscoveryTestForm(typeIndex int) *tview.Form {
	form := newConnectionTestForm(typeIndex)
	setFormInputValue(form, connFieldName, "")
	form.AddInputField(connLabelDSN, "", 72, nil, nil)
	form.AddInputField(connLabelHost, "localhost", 30, nil, nil)
	form.AddInputField(connLabelPort, "", 10, nil, nil)
	form.AddInputField(connLabelUser, "root", 30, nil, nil)
	form.AddPasswordField(connLabelPassword, "secret", 30, '*', nil)
	form.AddInputField(connLabelDatabase, "", 30, nil, nil)
	return form
}

func TestBuildDatabaseDiscoveryConfigMySQLAllowsBlankDatabase(t *testing.T) {
	form := newDatabaseDiscoveryTestForm(1)

	cfg, err := buildDatabaseDiscoveryConfig(form)
	if err != nil {
		t.Fatalf("buildDatabaseDiscoveryConfig returned error: %v", err)
	}
	if cfg.Type != config.MySQL || cfg.Port != "3306" || cfg.Database != "" {
		t.Fatalf("unexpected MySQL discovery config: %+v", cfg)
	}
}

func TestBuildDatabaseDiscoveryConfigPostgreSQLUsesMaintenanceDatabase(t *testing.T) {
	form := newDatabaseDiscoveryTestForm(0)

	cfg, err := buildDatabaseDiscoveryConfig(form)
	if err != nil {
		t.Fatalf("buildDatabaseDiscoveryConfig returned error: %v", err)
	}
	if cfg.Type != config.PostgreSQL || cfg.Port != "5432" || cfg.Database != "postgres" {
		t.Fatalf("unexpected PostgreSQL discovery config: %+v", cfg)
	}
}

func TestBuildDatabaseDiscoveryConfigUsesDSNCredentials(t *testing.T) {
	form := newDatabaseDiscoveryTestForm(1)
	setFormInputValue(form, connFieldDSN, "admin:dsn-secret@tcp(mysql.example.com:3307)/old_database")

	cfg, err := buildDatabaseDiscoveryConfig(form)
	if err != nil {
		t.Fatalf("buildDatabaseDiscoveryConfig returned error: %v", err)
	}
	if cfg.Host != "mysql.example.com" || cfg.Port != "3307" || cfg.User != "admin" || cfg.Password != "dsn-secret" {
		t.Fatalf("discovery did not use DSN credentials: %+v", cfg)
	}
	if cfg.Database != "" {
		t.Fatalf("MySQL discovery database = %q, want server-level connection", cfg.Database)
	}
}

func TestApplyDiscoveredDatabaseFillsNameAndUpdatesDSN(t *testing.T) {
	form := newDatabaseDiscoveryTestForm(1)
	setFormInputValue(form, connFieldDSN, "root:secret@tcp(localhost:3306)/old_database")
	discoveryCfg := &config.ConnectionConfig{
		Type: config.MySQL, Host: "localhost", Port: "3306", User: "root", Password: "secret",
	}

	applyDiscoveredDatabase(form, discoveryCfg, "orders")

	if got := formInputValue(form, connFieldDatabase); got != "orders" {
		t.Fatalf("database field = %q, want orders", got)
	}
	if got := formInputValue(form, connFieldName); got != "orders" {
		t.Fatalf("blank connection name was not filled from selected database: %q", got)
	}
	parsed, err := parseMySQLConnectionString(formInputValue(form, connFieldDSN))
	if err != nil {
		t.Fatalf("updated DSN is invalid: %v", err)
	}
	if parsed.Database != "orders" {
		t.Fatalf("updated DSN database = %q, want orders", parsed.Database)
	}
}

func TestBuildDatabaseDiscoveryConfigRejectsUnsupportedType(t *testing.T) {
	form := newDatabaseDiscoveryTestForm(2)
	_, err := buildDatabaseDiscoveryConfig(form)
	if err == nil || !strings.Contains(err.Error(), "MySQL and PostgreSQL") {
		t.Fatalf("unsupported discovery error = %v", err)
	}
}
