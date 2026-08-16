package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/shreyam1008/dbterm/internal/persist"
)

const (
	settingsFileName = "settings.json"

	AgentConnectionScopeActive = "active"
	AgentConnectionScopeAll    = "all"

	ActionFocusTables    = "focus_tables"
	ActionFocusQuery     = "focus_query"
	ActionFocusResults   = "focus_results"
	ActionDashboard      = "dashboard"
	ActionHelp           = "help"
	ActionServices       = "services"
	ActionFullscreen     = "fullscreen"
	ActionBackup         = "backup"
	ActionBackupCenter   = "backup_center"
	ActionChangeProfiler = "change_profiler"
	ActionExportCSV      = "export_csv"
	ActionHistory        = "history"
	ActionSettings       = "settings"
	ActionImportDump     = "import_dump"
	ActionInspectSchema  = "inspect_schema"
	ActionSelectAll      = "select_all"
	ActionClearSelection = "clear_selection"
	ActionCommandPalette = "command_palette"
)

// AgentAccessSettings controls the local, on-demand MCP server. Database
// access is intentionally read-only by default. Profile writes are a separate
// capability because a saved profile may contain credentials.
type AgentAccessSettings struct {
	ConnectionScope    string `json:"connection_scope"`
	AllowProfileWrites bool   `json:"allow_profile_writes"`
}

var defaultKeymapBindings = map[string][]string{
	ActionFocusTables:    {"alt+t"},
	ActionFocusQuery:     {"alt+q"},
	ActionFocusResults:   {"alt+r"},
	ActionDashboard:      {"alt+d"},
	ActionHelp:           {"alt+h"},
	ActionServices:       {"alt+s"},
	ActionFullscreen:     {"alt+f"},
	ActionBackup:         {"alt+b"},
	ActionBackupCenter:   {"alt+k"},
	ActionChangeProfiler: {"alt+w"},
	ActionExportCSV:      {"alt+e"},
	ActionHistory:        {"alt+y"},
	ActionSettings:       {"alt+,", "alt+g"},
	ActionImportDump:     {"alt+i"},
	ActionInspectSchema:  {"alt+m"},
	ActionSelectAll:      {"alt+a"},
	ActionClearSelection: {"alt+c"},
	ActionCommandPalette: {"ctrl+p"},
}

// Settings stores user-adjustable runtime settings.
type Settings struct {
	Keymap                map[string][]string                  `json:"keymap"`
	DashboardHealthChecks string                               `json:"dashboard_health_checks"`
	AgentAccess           AgentAccessSettings                  `json:"agent_access"`
	TableColumnWidths     map[string]map[string]map[string]int `json:"table_column_widths,omitempty"`
	PinnedTables          map[string][]string                  `json:"pinned_tables,omitempty"`
}

// DefaultSettings returns a deep-copied default settings value.
func DefaultSettings() *Settings {
	return &Settings{
		Keymap:                DefaultKeymapBindings(),
		DashboardHealthChecks: "auto",
		AgentAccess: AgentAccessSettings{
			ConnectionScope: AgentConnectionScopeActive,
		},
		TableColumnWidths: map[string]map[string]map[string]int{},
		PinnedTables:      map[string][]string{},
	}
}

// DefaultKeymapBindings returns a deep copy of default key bindings.
func DefaultKeymapBindings() map[string][]string {
	return cloneKeymapBindings(defaultKeymapBindings)
}

// LoadSettings loads settings from the OS-native dbterm config directory.
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

// SaveSettings saves settings to the OS-native dbterm config directory.
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
	return persist.DefaultConfigFile(settingsFileName)
}

func writeSettings(path string, settings *Settings) error {
	if path == "" {
		return fmt.Errorf("settings path is required")
	}

	if settings == nil {
		settings = DefaultSettings()
	}

	normalized := mergeSettings(DefaultSettings(), settings)
	if err := persist.SaveJSON(path, normalized); err != nil {
		return fmt.Errorf("write settings file: %w", err)
	}

	return nil
}

func mergeSettings(defaults, loaded *Settings) *Settings {
	merged := &Settings{
		Keymap:                map[string][]string{},
		DashboardHealthChecks: "auto",
		AgentAccess: AgentAccessSettings{
			ConnectionScope: AgentConnectionScopeActive,
		},
		TableColumnWidths: map[string]map[string]map[string]int{},
		PinnedTables:      map[string][]string{},
	}

	if defaults != nil {
		merged.Keymap = cloneKeymapBindings(defaults.Keymap)
		merged.DashboardHealthChecks = normalizeDashboardHealthChecks(defaults.DashboardHealthChecks)
		merged.AgentAccess = normalizeAgentAccess(defaults.AgentAccess)
		merged.TableColumnWidths = cloneTableColumnWidths(defaults.TableColumnWidths)
		merged.PinnedTables = clonePinnedTables(defaults.PinnedTables)
	}

	if loaded == nil {
		return merged
	}

	if mode := normalizeDashboardHealthChecks(loaded.DashboardHealthChecks); mode != "" {
		merged.DashboardHealthChecks = mode
	}
	merged.AgentAccess = normalizeAgentAccess(loaded.AgentAccess)
	merged.TableColumnWidths = cloneTableColumnWidths(loaded.TableColumnWidths)
	merged.PinnedTables = clonePinnedTables(loaded.PinnedTables)

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

func normalizeAgentAccess(access AgentAccessSettings) AgentAccessSettings {
	scope := strings.ToLower(strings.TrimSpace(access.ConnectionScope))
	if scope != AgentConnectionScopeAll {
		scope = AgentConnectionScopeActive
	}

	return AgentAccessSettings{
		ConnectionScope:    scope,
		AllowProfileWrites: access.AllowProfileWrites,
	}
}

func clonePinnedTables(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for connection, tables := range in {
		seen := make(map[string]bool, len(tables))
		for _, table := range tables {
			table = strings.TrimSpace(table)
			if table == "" || seen[table] {
				continue
			}
			seen[table] = true
			out[connection] = append(out[connection], table)
		}
		if len(out[connection]) == 0 {
			delete(out, connection)
		}
	}
	return out
}

func cloneTableColumnWidths(in map[string]map[string]map[string]int) map[string]map[string]map[string]int {
	out := make(map[string]map[string]map[string]int, len(in))
	for connection, tables := range in {
		out[connection] = make(map[string]map[string]int, len(tables))
		for table, columns := range tables {
			out[connection][table] = make(map[string]int, len(columns))
			for column, width := range columns {
				out[connection][table][column] = width
			}
		}
	}
	return out
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

func normalizeDashboardHealthChecks(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto":
		return "auto"
	case "manual", "disabled", "off":
		return "manual"
	default:
		return "auto"
	}
}
