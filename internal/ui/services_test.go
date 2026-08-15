package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

func newServiceConnectionTestForm(serviceIndex int, database, password string) *tview.Form {
	form := tview.NewForm()
	form.AddDropDown(serviceLabelType, []string{"MySQL", "PostgreSQL"}, serviceIndex, nil)
	form.AddInputField(connLabelHost, "localhost", 30, nil, nil)
	form.AddInputField(connLabelPort, "", 10, nil, nil)
	form.AddInputField(connLabelDatabase, database, 30, nil, nil)
	form.AddInputField(connLabelUser, "dbuser", 30, nil, nil)
	form.AddPasswordField(connLabelPassword, password, 30, '*', nil)
	return form
}

func TestBuildServiceConnectionConfigUsesVisibleCredentials(t *testing.T) {
	form := newServiceConnectionTestForm(0, "shop", "  exact password  ")

	cfg, err := buildServiceConnectionConfig(form)
	if err != nil {
		t.Fatalf("buildServiceConnectionConfig() error = %v", err)
	}
	if cfg.Type != config.MySQL || cfg.Host != "localhost" || cfg.Port != "3306" {
		t.Fatalf("config endpoint = %#v, want local MySQL on 3306", cfg)
	}
	if cfg.User != "dbuser" || cfg.Password != "  exact password  " || cfg.Database != "shop" {
		t.Fatalf("config credentials/database = %#v, want exact visible form values", cfg)
	}
}

func TestBuildServiceDiscoveryConfigUsesPostgresMaintenanceDatabase(t *testing.T) {
	form := newServiceConnectionTestForm(1, "", "secret")

	cfg, err := buildServiceConnectionConfig(form)
	if err != nil {
		t.Fatalf("buildServiceConnectionConfig() error = %v", err)
	}
	if cfg.Type != config.PostgreSQL || cfg.Port != "5432" || cfg.Database != "" || cfg.SSLMode != "disable" {
		t.Fatalf("server config = %#v, want blank optional database", cfg)
	}
}

func TestBuildServiceConnectionConfigAllowsDatabaseChoiceAfterLogin(t *testing.T) {
	form := newServiceConnectionTestForm(0, "", "secret")

	cfg, err := buildServiceConnectionConfig(form)
	if err != nil {
		t.Fatalf("server login was rejected without a database: %v", err)
	}
	if cfg.Database != "" || cfg.Name != "MySQL server" {
		t.Fatalf("server login = %+v", cfg)
	}
}

func TestServiceInstallPackageUsesPlatformPackageNames(t *testing.T) {
	if got := serviceInstallPackage("PostgreSQL"); got != "postgresql" {
		t.Fatalf("PostgreSQL package = %q", got)
	}
	if got := serviceInstallPackage("MySQL"); got != "mysql-server" {
		t.Fatalf("MySQL package = %q", got)
	}
}

func TestCompactServiceVersionKeepsDashboardOnOneLine(t *testing.T) {
	tests := []struct {
		service string
		raw     string
		want    string
	}{
		{"MySQL", "mysql  Ver 8.0.46-0ubuntu0.24.04.3 for Linux on x86_64 ((Ubuntu))", "MySQL 8.0.46-0ubuntu0.24.04.3"},
		{"MySQL", "mariadb  Ver 15.1 Distrib 10.11.14-MariaDB, for debian-linux-gnu", "MariaDB 10.11.14-MariaDB"},
		{"PostgreSQL", "psql (PostgreSQL) 18.4 (Ubuntu 18.4-1.pgdg24.04+1)", "PostgreSQL 18.4"},
	}
	for _, test := range tests {
		if got := compactServiceVersion(test.service, test.raw); got != test.want {
			t.Errorf("compactServiceVersion(%q, %q) = %q, want %q", test.service, test.raw, got, test.want)
		}
	}
}

func TestLocalServiceLoginsIncludesOnlyLocalServerConnections(t *testing.T) {
	store := &config.Store{Connections: []config.ConnectionConfig{
		{Name: "local mysql", Type: config.MySQL, Host: "localhost", User: "root", Password: "secret", Database: "shop"},
		{Name: "local postgres", Type: config.PostgreSQL, Host: "127.0.0.1", User: "app", Database: "appdb"},
		{Name: "remote", Type: config.MySQL, Host: "db.example.com", User: "app", Database: "prod"},
		{Name: "file", Type: config.SQLite, FilePath: "/tmp/app.db"},
	}}

	logins := localServiceLogins(store)
	if len(logins) != 2 || logins[0].Name != "local mysql" || logins[1].Name != "local postgres" {
		t.Fatalf("localServiceLogins() = %#v", logins)
	}
	if label := serviceLoginLabel(logins[0]); strings.Contains(label, "secret") {
		t.Fatalf("serviceLoginLabel() exposed password: %q", label)
	}
}

func TestLocalAuthenticationHintExplainsSudoBoundary(t *testing.T) {
	cfg := &config.ConnectionConfig{Type: config.MySQL, Host: "localhost"}
	hint := connectionHint(errors.New("access denied"), cfg)
	if !strings.Contains(hint, "database name is optional") || !strings.Contains(hint, "socket-only") || !strings.Contains(hint, "Do not use sudo") {
		t.Fatalf("connectionHint() = %q", hint)
	}
}
