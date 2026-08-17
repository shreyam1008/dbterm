package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSettingsCreatesDefaultsWhenMissing(t *testing.T) {
	configDir := useTestConfigDir(t)

	settings, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	if settings == nil {
		t.Fatal("LoadSettings() returned nil settings")
	}

	if got := settings.Keymap[ActionFocusTables]; len(got) != 1 || got[0] != "alt+t" {
		t.Fatalf("unexpected default binding for %s: %#v", ActionFocusTables, got)
	}

	path := filepath.Join(configDir, "settings.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("default settings file not created at %s: %v", path, err)
	}
	vault, err := profileRecoveryFilePath(settingsFileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(vault); err != nil {
		t.Fatalf("default settings recovery vault not created at %s: %v", vault, err)
	}
}

func TestLoadSettingsMergesPartialOverrides(t *testing.T) {
	configDir := useTestConfigDir(t)
	path := filepath.Join(configDir, "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	data := []byte(`{
  "keymap": {
    "export_csv": ["Ctrl-E"],
    "history": ["alt-h"]
  }
}
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}

	if got := settings.Keymap[ActionExportCSV]; len(got) != 1 || got[0] != "Ctrl-E" {
		t.Fatalf("expected override for %s, got %#v", ActionExportCSV, got)
	}

	if got := settings.Keymap[ActionBackup]; len(got) != 1 || got[0] != "alt+b" {
		t.Fatalf("expected default fallback for %s, got %#v", ActionBackup, got)
	}
}

func TestLoadSettingsInvalidJSONFallsBackToDefaults(t *testing.T) {
	configDir := useTestConfigDir(t)
	path := filepath.Join(configDir, "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"keymap":`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, err := LoadSettings()
	if err == nil {
		t.Fatal("LoadSettings() expected error for invalid JSON, got nil")
	}
	if settings == nil {
		t.Fatal("LoadSettings() returned nil settings on error")
	}

	if got := settings.Keymap[ActionFocusQuery]; len(got) != 1 || got[0] != "alt+q" {
		t.Fatalf("expected default fallback for %s, got %#v", ActionFocusQuery, got)
	}
}

func TestLoadSettingsIncludesDashboardHealthCheckDefault(t *testing.T) {
	useTestConfigDir(t)

	settings, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}

	if settings.DashboardHealthChecks != "auto" {
		t.Fatalf("expected default dashboard health checks to be auto, got %q", settings.DashboardHealthChecks)
	}
}

func TestLoadSettingsIncludesSafeAgentAccessDefaults(t *testing.T) {
	useTestConfigDir(t)

	settings, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}

	if settings.AgentAccess.ConnectionScope != AgentConnectionScopeActive {
		t.Fatalf("agent connection scope = %q, want %q", settings.AgentAccess.ConnectionScope, AgentConnectionScopeActive)
	}
	if settings.AgentAccess.AllowProfileWrites {
		t.Fatalf("agent access defaults must be read-only, got %+v", settings.AgentAccess)
	}
}

func TestLoadSettingsNormalizesAgentAccess(t *testing.T) {
	configDir := useTestConfigDir(t)
	path := filepath.Join(configDir, "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	data := []byte(`{
  "agent_access": {
    "connection_scope": "ALL",
    "allow_profile_writes": true
  },
  "keymap": {}
}
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	if settings.AgentAccess.ConnectionScope != AgentConnectionScopeAll || !settings.AgentAccess.AllowProfileWrites {
		t.Fatalf("unexpected agent access settings: %+v", settings.AgentAccess)
	}
}

func TestLoadSettingsRejectsUnknownAgentConnectionScope(t *testing.T) {
	configDir := useTestConfigDir(t)
	path := filepath.Join(configDir, "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	data := []byte(`{"agent_access":{"connection_scope":"some-profile"},"keymap":{}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	if settings.AgentAccess.ConnectionScope != AgentConnectionScopeActive {
		t.Fatalf("unknown agent scope should fail closed to active, got %q", settings.AgentAccess.ConnectionScope)
	}
}

func TestLoadSettingsMergesDashboardHealthCheckOverride(t *testing.T) {
	configDir := useTestConfigDir(t)
	path := filepath.Join(configDir, "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	data := []byte(`{
  "dashboard_health_checks": "disabled",
  "keymap": {}
}
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}

	if settings.DashboardHealthChecks != "manual" {
		t.Fatalf("expected disabled alias to normalize to manual, got %q", settings.DashboardHealthChecks)
	}
}

func TestSaveSettingsPreservesTableColumnWidths(t *testing.T) {
	useTestConfigDir(t)
	settings := DefaultSettings()
	settings.TableColumnWidths["connection"] = map[string]map[string]int{
		"users": {"id": 18, "profile": 42},
	}

	if err := SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}
	reloaded, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	if got := reloaded.TableColumnWidths["connection"]["users"]["profile"]; got != 42 {
		t.Fatalf("reloaded profile width = %d, want 42", got)
	}
}

func TestSaveSettingsPreservesPinnedTablesPerConnection(t *testing.T) {
	useTestConfigDir(t)
	settings := DefaultSettings()
	settings.PinnedTables["connection-a"] = []string{"orders", "users", "orders", " "}
	settings.PinnedTables["connection-b"] = []string{"events"}

	if err := SaveSettings(settings); err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}
	reloaded, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	if got := reloaded.PinnedTables["connection-a"]; len(got) != 2 || got[0] != "orders" || got[1] != "users" {
		t.Fatalf("connection-a pins = %#v, want [orders users]", got)
	}
	if got := reloaded.PinnedTables["connection-b"]; len(got) != 1 || got[0] != "events" {
		t.Fatalf("connection-b pins = %#v, want [events]", got)
	}
}

func TestLoadSettingsRestoresAfterWholeConfigDirectoryDisappears(t *testing.T) {
	configDir := useTestConfigDir(t)
	settings := DefaultSettings()
	settings.DashboardHealthChecks = "manual"
	settings.PinnedTables["production"] = []string{"orders"}
	if err := SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(configDir); err != nil {
		t.Fatal(err)
	}

	recovered, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	if recovered.DashboardHealthChecks != "manual" || len(recovered.PinnedTables["production"]) != 1 {
		t.Fatalf("recovered settings = %#v", recovered)
	}
	if _, err := os.Stat(filepath.Join(configDir, settingsFileName)); err != nil {
		t.Fatalf("primary settings were not rebuilt: %v", err)
	}
}

func TestLoadSettingsRestoresCorruptPrimaryAndPreservesIt(t *testing.T) {
	configDir := useTestConfigDir(t)
	settings := DefaultSettings()
	settings.DashboardHealthChecks = "manual"
	if err := SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, settingsFileName)
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, err := LoadSettings()
	if err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("LoadSettings() error = %v, want recovery notice", err)
	}
	if recovered.DashboardHealthChecks != "manual" {
		t.Fatalf("recovered settings = %#v", recovered)
	}
	matches, globErr := filepath.Glob(path + ".corrupt-*")
	if globErr != nil || len(matches) != 1 {
		t.Fatalf("preserved corrupt settings = %#v, %v", matches, globErr)
	}
}
