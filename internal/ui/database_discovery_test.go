package ui

import (
	"strings"
	"testing"

	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
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

func TestDatabasesWithCurrentFirst(t *testing.T) {
	got := databasesWithCurrentFirst([]string{"analytics", "orders", "analytics", ""}, "orders")
	want := []string{"orders", "analytics"}
	if len(got) != len(want) {
		t.Fatalf("databasesWithCurrentFirst() = %q, want %q", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("databasesWithCurrentFirst() = %q, want %q", got, want)
		}
	}
}

func TestNewConnectionForDatabasePreservesServerSettings(t *testing.T) {
	base := config.ConnectionConfig{
		ID: "saved-id", Name: "Production", Type: config.PostgreSQL,
		Host: "pg.example.com", Port: "5432", User: "alice", Password: "secret",
		Database: "orders", SSLMode: "require", ReadOnly: true, LastUsed: "yesterday", Active: true,
	}
	got := newConnectionForDatabase(base, "analytics")
	if got.ID != "" || got.LastUsed != "" || got.Active {
		t.Fatalf("new connection retained saved identity/state: %+v", got)
	}
	if got.Name != "Production / analytics" || got.Database != "analytics" {
		t.Fatalf("new connection identity = %q / %q", got.Name, got.Database)
	}
	if got.Host != base.Host || got.Port != base.Port || got.User != base.User || got.Password != base.Password || got.SSLMode != base.SSLMode || !got.ReadOnly {
		t.Fatalf("new connection did not preserve server settings: %+v", got)
	}
}

func TestConnectionForDatabaseReusesOneServerProfile(t *testing.T) {
	base := config.ConnectionConfig{
		ID: "server-id", Name: "Production server", Type: config.PostgreSQL,
		Host: "pg.example.com", Port: "5432", User: "alice", Password: "secret",
		SSLMode: "require", ReadOnly: true,
	}
	selected := connectionForDatabase(base, "analytics")
	if selected.ID != base.ID || selected.Database != "analytics" || selected.Name != "analytics" {
		t.Fatalf("selected database identity = %+v", selected)
	}
	if selected.Host != base.Host || selected.User != base.User || selected.Password != base.Password || selected.SSLMode != base.SSLMode || !selected.ReadOnly {
		t.Fatalf("selected database did not reuse the server login: %+v", selected)
	}
	if base.Database != "" {
		t.Fatalf("selecting a database mutated the saved server profile: %+v", base)
	}
}

func TestSavedDatabaseIndexUsesSameServerLogin(t *testing.T) {
	base := config.ConnectionConfig{Type: config.MySQL, Host: "db.example.com", Port: "3306", User: "alice", Database: "orders"}
	connections := []config.ConnectionConfig{
		{Type: config.MySQL, Host: "db.example.com", Port: "3306", User: "bob", Database: "analytics"},
		{Type: config.MySQL, Host: "DB.EXAMPLE.COM", Port: "3306", User: "alice", Database: "analytics"},
	}
	if got := savedDatabaseIndex(connections, base, "analytics"); got != 1 {
		t.Fatalf("savedDatabaseIndex() = %d, want 1", got)
	}
}

func TestNewConnectionInSameScopeClearsOnlyDatabaseIdentity(t *testing.T) {
	d1 := newConnectionInSameScope(config.ConnectionConfig{
		ID: "one", Name: "D1 Orders", Type: config.CloudflareD1,
		AccountID: "account", DatabaseID: "orders-id", AuthToken: "token", ReadOnly: true,
	})
	if d1.ID != "" || d1.Name != "" || d1.DatabaseID != "" || d1.AccountID != "account" || d1.AuthToken != "token" || !d1.ReadOnly {
		t.Fatalf("D1 scope prefill = %+v", d1)
	}
}
