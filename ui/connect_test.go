package ui

import (
	"path/filepath"
	"testing"

	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

func newConnectionTestForm(typeIndex int) *tview.Form {
	dbTypes := []string{"PostgreSQL", "MySQL", "SQLite", "Turso", "Cloudflare D1"}
	form := tview.NewForm()
	form.AddInputField(connLabelName, "test-conn", 30, nil, nil)
	form.AddDropDown(connLabelType, dbTypes, typeIndex, nil)
	form.AddCheckbox(connLabelReadOnly, false, nil)
	return form
}

func TestBuildConfigFromFormPostgreSQL(t *testing.T) {
	form := newConnectionTestForm(0)
	form.AddInputField(connLabelDSN, "", 72, nil, nil)
	form.AddInputField(connLabelHost, "pg.example.com", 30, nil, nil)
	form.AddInputField(connLabelPort, "", 10, nil, nil)
	form.AddInputField(connLabelUser, "alice", 30, nil, nil)
	form.AddPasswordField(connLabelPassword, "secret", 30, '*', nil)
	form.AddInputField(connLabelDatabase, "appdb", 30, nil, nil)

	cfg := (&App{}).buildConfigFromForm(form)
	if cfg == nil {
		t.Fatal("buildConfigFromForm returned nil")
	}
	if cfg.Type != config.PostgreSQL || cfg.Host != "pg.example.com" || cfg.Port != "5432" || cfg.User != "alice" || cfg.Password != "secret" || cfg.Database != "appdb" {
		t.Fatalf("unexpected PostgreSQL config: %+v", cfg)
	}
}

func TestBuildConfigFromFormMySQL(t *testing.T) {
	form := newConnectionTestForm(1)
	form.AddInputField(connLabelDSN, "", 72, nil, nil)
	form.AddInputField(connLabelHost, "mysql.example.com", 30, nil, nil)
	form.AddInputField(connLabelPort, "", 10, nil, nil)
	form.AddInputField(connLabelUser, "root", 30, nil, nil)
	form.AddPasswordField(connLabelPassword, "secret", 30, '*', nil)
	form.AddInputField(connLabelDatabase, "shop", 30, nil, nil)

	cfg := (&App{}).buildConfigFromForm(form)
	if cfg == nil {
		t.Fatal("buildConfigFromForm returned nil")
	}
	if cfg.Type != config.MySQL || cfg.Host != "mysql.example.com" || cfg.Port != "3306" || cfg.User != "root" || cfg.Password != "secret" || cfg.Database != "shop" {
		t.Fatalf("unexpected MySQL config: %+v", cfg)
	}
}

func TestBuildConfigFromFormSQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "local.db")
	form := newConnectionTestForm(2)
	form.AddInputField(connLabelFilePath, dbPath, 60, nil, nil)

	cfg := (&App{}).buildConfigFromForm(form)
	if cfg == nil {
		t.Fatal("buildConfigFromForm returned nil")
	}
	if cfg.Type != config.SQLite || cfg.FilePath != dbPath {
		t.Fatalf("unexpected SQLite config: %+v", cfg)
	}
}

func TestBuildConfigFromFormTursoUsesHostFieldForDatabaseURL(t *testing.T) {
	form := newConnectionTestForm(3)
	form.AddInputField(connLabelHost, "libsql://mydb-user.turso.io", 60, nil, nil)
	form.AddPasswordField(connLabelAuthToken, "turso-token", 60, '*', nil)

	cfg := (&App{}).buildConfigFromForm(form)
	if cfg == nil {
		t.Fatal("buildConfigFromForm returned nil")
	}
	if cfg.Type != config.Turso || cfg.Host != "libsql://mydb-user.turso.io" || cfg.AuthToken != "turso-token" {
		t.Fatalf("unexpected Turso config: %+v", cfg)
	}
}

func TestBuildConfigFromFormCloudflareD1UsesAuthTokenField(t *testing.T) {
	form := newConnectionTestForm(4)
	form.AddInputField(connLabelAccountID, "account-id", 40, nil, nil)
	form.AddInputField(connLabelDatabaseID, "database-uuid", 40, nil, nil)
	form.AddPasswordField(connLabelAuthToken, "api-token", 60, '*', nil)

	cfg := (&App{}).buildConfigFromForm(form)
	if cfg == nil {
		t.Fatal("buildConfigFromForm returned nil")
	}
	if cfg.Type != config.CloudflareD1 || cfg.AccountID != "account-id" || cfg.DatabaseID != "database-uuid" || cfg.AuthToken != "api-token" {
		t.Fatalf("unexpected Cloudflare D1 config: %+v", cfg)
	}
}

func TestFormInputValueAndSetFormInputValueUseStableFieldKeys(t *testing.T) {
	form := tview.NewForm()
	form.AddInputField(connLabelHost, "initial", 60, nil, nil)
	form.AddPasswordField(connLabelAuthToken, "old-token", 60, '*', nil)

	setFormInputValue(form, connFieldHost, "libsql://stable-key.turso.io")
	setFormInputValue(form, connFieldAuthToken, "new-token")

	if got := formInputValue(form, connFieldHost); got != "libsql://stable-key.turso.io" {
		t.Fatalf("host value = %q", got)
	}
	if got := formInputValue(form, connFieldAuthToken); got != "new-token" {
		t.Fatalf("auth token value = %q", got)
	}
}
