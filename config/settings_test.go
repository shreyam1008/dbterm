package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSettingsCreatesDefaultsWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

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

	path := filepath.Join(home, ".config", "dbterm", "settings.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("default settings file not created at %s: %v", path, err)
	}
}

func TestLoadSettingsMergesPartialOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(home, ".config", "dbterm", "settings.json")
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
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(home, ".config", "dbterm", "settings.json")
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
