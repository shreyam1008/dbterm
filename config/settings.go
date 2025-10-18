package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	settingsFileName = "settings.json"

	ActionFocusTables    = "focus_tables"
	ActionFocusQuery     = "focus_query"
	ActionFocusResults   = "focus_results"
	ActionDashboard      = "dashboard"
	ActionHelp           = "help"
	ActionServices       = "services"
	ActionFullscreen     = "fullscreen"
	ActionBackup         = "backup"
	ActionExportCSV      = "export_csv"
	ActionHistory        = "history"
	ActionSettings       = "settings"
	ActionImportDump     = "import_dump"
	ActionInspectSchema  = "inspect_schema"
	ActionSelectAll      = "select_all"
	ActionClearSelection = "clear_selection"
)

var defaultKeymapBindings = map[string][]string{
	ActionFocusTables:    {"alt+t"},
	ActionFocusQuery:     {"alt+q"},
	ActionFocusResults:   {"alt+r"},
	ActionDashboard:      {"alt+d"},
	ActionHelp:           {"alt+h"},
	ActionServices:       {"alt+s"},
	ActionFullscreen:     {"alt+f"},
	ActionBackup:         {"alt+b"},
	ActionExportCSV:      {"alt+e"},
	ActionHistory:        {"alt+y"},
	ActionSettings:       {"alt+,", "alt+g"},
	ActionImportDump:     {"alt+i"},
	ActionInspectSchema:  {"alt+m"},
	ActionSelectAll:      {"alt+a"},
	ActionClearSelection: {"alt+c"},
}

// Settings stores user-adjustable runtime settings.
type Settings struct {
	Keymap map[string][]string `json:"keymap"`
}

// DefaultSettings returns a deep-copied default settings value.
func DefaultSettings() *Settings {
	return &Settings{
		Keymap: DefaultKeymapBindings(),
	}
}

// DefaultKeymapBindings returns a deep copy of default key bindings.
func DefaultKeymapBindings() map[string][]string {
	return cloneKeymapBindings(defaultKeymapBindings)
}

// LoadSettings loads settings from ~/.config/dbterm/settings.json.
// Missing or empty files are replaced with defaults on disk.
func LoadSettings() (*Settings, error) {
	defaults := DefaultSettings()

	path, err := settingsFilePath()
	if err != nil {
		return defaults, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if saveErr := writeSettings(path, defaults); saveErr != nil {
				return defaults, fmt.Errorf("create default settings: %w", saveErr)
			}
			return defaults, nil
		}
		return defaults, fmt.Errorf("read settings: %w", err)
	}

	if len(bytes.TrimSpace(data)) == 0 {
		if saveErr := writeSettings(path, defaults); saveErr != nil {
			return defaults, fmt.Errorf("write default settings: %w", saveErr)
		}
		return defaults, nil
	}

	var loaded Settings
	if err := json.Unmarshal(data, &loaded); err != nil {
		return defaults, fmt.Errorf("parse settings: %w", err)
	}

	return mergeSettings(defaults, &loaded), nil
}

// SaveSettings saves settings to ~/.config/dbterm/settings.json.
func SaveSettings(settings *Settings) error {
	if settings == nil {
		return fmt.Errorf("settings are required")
	}

	path, err := settingsFilePath()
	if err != nil {
		return err
	}

	merged := mergeSettings(DefaultSettings(), settings)
	return writeSettings(path, merged)
}

func settingsFilePath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, settingsFileName), nil
}

func writeSettings(path string, settings *Settings) error {
	if path == "" {
		return fmt.Errorf("settings path is required")
	}

	if settings == nil {
		settings = DefaultSettings()
	}

	normalized := mergeSettings(DefaultSettings(), settings)
	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write settings file: %w", err)
	}

	return nil
}

func mergeSettings(defaults, loaded *Settings) *Settings {
	merged := &Settings{
		Keymap: map[string][]string{},
	}

	if defaults != nil {
		merged.Keymap = cloneKeymapBindings(defaults.Keymap)
	}

	if loaded == nil {
		return merged
	}

	for action, bindings := range loaded.Keymap {
		name := strings.ToLower(strings.TrimSpace(action))
		if name == "" {
			continue
		}

		cleaned := cleanBindingList(bindings)
		if len(cleaned) == 0 {
			continue
		}

		merged.Keymap[name] = cleaned
	}

	return merged
}

func cloneKeymapBindings(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return map[string][]string{}
	}

	out := make(map[string][]string, len(in))
	for action, bindings := range in {
		out[action] = cleanBindingList(bindings)
	}
	return out
}

func cleanBindingList(bindings []string) []string {
	if len(bindings) == 0 {
		return nil
	}

	cleaned := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		item := strings.TrimSpace(binding)
		if item == "" {
			continue
		}
		cleaned = append(cleaned, item)
	}
	return cleaned
}
