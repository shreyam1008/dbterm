package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/appdirs"
	"github.com/shreyam1008/dbterm/internal/config"
)

const pageSettings = "settings"

const (
	settingsLabelAgentScope         = "Agent Connection Scope"
	settingsLabelAgentProfileWrites = "Allow Agent Profile Writes"
	pageAgentSetup                  = "agentSetup"
)

var agentConnectionScopeOptions = []string{
	"Active connection only (recommended)",
	"All saved connections",
}

type keymapFieldSpec struct {
	Action string
	Label  string
}

var keymapFieldSpecs = []keymapFieldSpec{
	{Action: config.ActionFocusTables, Label: "Focus Tables"},
	{Action: config.ActionFocusQuery, Label: "Focus Query"},
	{Action: config.ActionFocusResults, Label: "Focus Results"},
	{Action: config.ActionDashboard, Label: "Go Dashboard"},
	{Action: config.ActionHelp, Label: "Open Help"},
	{Action: config.ActionServices, Label: "Open Services"},
	{Action: config.ActionFullscreen, Label: "Toggle Fullscreen"},
	{Action: config.ActionBackup, Label: "Open Backup"},
	{Action: config.ActionBackupCenter, Label: "Backup Center"},
	{Action: config.ActionExportCSV, Label: "Export CSV"},
	{Action: config.ActionHistory, Label: "Query History"},
	{Action: config.ActionSettings, Label: "Open Settings"},
	{Action: config.ActionImportDump, Label: "Import Dump"},
	{Action: config.ActionInspectSchema, Label: "Inspect Schema"},
	{Action: config.ActionSelectAll, Label: "Select All Rows"},
	{Action: config.ActionClearSelection, Label: "Clear Selection"},
	{Action: config.ActionCommandPalette, Label: "Command Palette"},
}

func settingsFooterText(width int) string {
	if width < 98 {
		return fmt.Sprintf("  [yellow]Ctrl+S[-] Save  │  [yellow]Esc[-] Dashboard %s", iconDashboard)
	}
	return fmt.Sprintf("  [yellow]Tab[-] Next field  │  [yellow]Ctrl+S[-] Save  │  [yellow]Esc[-] Dashboard %s", iconDashboard)
}

func keymapFieldValue(settings *config.Settings, action string) string {
	if settings == nil || settings.Keymap == nil {
		return ""
	}
	return strings.Join(settings.Keymap[action], " | ")
}

func parseBindingList(raw string) []string {
	parts := strings.Split(raw, "|")
	if len(parts) == 0 {
		return nil
	}

	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}

		key := strings.ToLower(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}

	return result
}

func cloneSettings(settings *config.Settings) *config.Settings {
	if settings == nil {
		return config.DefaultSettings()
	}

	cloned := &config.Settings{
		Keymap:                make(map[string][]string, len(settings.Keymap)),
		DashboardHealthChecks: settings.DashboardHealthChecks,
		AgentAccess:           settings.AgentAccess,
		TableColumnWidths:     make(map[string]map[string]map[string]int, len(settings.TableColumnWidths)),
		PinnedTables:          make(map[string][]string, len(settings.PinnedTables)),
	}

	for action, bindings := range settings.Keymap {
		copied := make([]string, len(bindings))
		copy(copied, bindings)
		cloned.Keymap[action] = copied
	}
	for connection, tables := range settings.TableColumnWidths {
		cloned.TableColumnWidths[connection] = make(map[string]map[string]int, len(tables))
		for table, columns := range tables {
			cloned.TableColumnWidths[connection][table] = make(map[string]int, len(columns))
			for column, width := range columns {
				cloned.TableColumnWidths[connection][table][column] = width
			}
		}
	}
	for connection, tables := range settings.PinnedTables {
		cloned.PinnedTables[connection] = append([]string(nil), tables...)
	}

	return cloned
}

func agentConnectionScopeIndex(scope string) int {
	if strings.EqualFold(strings.TrimSpace(scope), config.AgentConnectionScopeAll) {
		return 1
	}
	return 0
}

func selectedAgentConnectionScope(form *tview.Form) string {
	item := form.GetFormItemByLabel(settingsLabelAgentScope)
	dropdown, ok := item.(*tview.DropDown)
	if !ok {
		return config.AgentConnectionScopeActive
	}
	index, _ := dropdown.GetCurrentOption()
	if index == 1 {
		return config.AgentConnectionScopeAll
	}
	return config.AgentConnectionScopeActive
}

func settingsFormCheckboxChecked(form *tview.Form, label string) bool {
	item := form.GetFormItemByLabel(label)
	checkbox, ok := item.(*tview.Checkbox)
	return ok && checkbox.IsChecked()
}

func agentMCPSetupText() string {
	return `LOCAL MCP · STDIO

Command:   dbterm
Arguments: mcp serve

Codex CLI
codex mcp add dbterm -- dbterm mcp serve

Claude Code
claude mcp add dbterm -- dbterm mcp serve

VS Code · .vscode/mcp.json
{"servers":{"dbterm":{"type":"stdio","command":"dbterm","args":["mcp","serve"]}}}

No password, token, or DSN belongs in client config. dbterm reads its local saved profiles. Database tools stay read-only; profile creation is exposed only when explicitly allowed in Settings.`
}

func sortedFieldSpecs() []keymapFieldSpec {
	sorted := make([]keymapFieldSpec, len(keymapFieldSpecs))
	copy(sorted, keymapFieldSpecs)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Label < sorted[j].Label
	})
	return sorted
}

func (a *App) showSettings() {
	settings, loadErr := config.LoadSettings()
	if settings == nil {
		settings = config.DefaultSettings()
	}

	fields := sortedFieldSpecs()

	header := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	header.SetBackgroundColor(bg)
	header.SetText(fmt.Sprintf(" [::b][#cba6f7]%s Settings[-][-]  [#a6adc8]Agent access · keymap[-]", iconDashboard))

	summary := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	summary.SetBackgroundColor(bg)
	settingsPath := "the OS-native dbterm config directory"
	if configDir, err := appdirs.ConfigDir(); err == nil {
		settingsPath = filepath.Join(configDir, "settings.json")
	}
	summary.SetText(fmt.Sprintf("[#89b4fa]MCP: dbterm mcp serve[-]  [#6c7086]Local stdio · database read-only · saved at %s[-]", tview.Escape(settingsPath)))

	form := tview.NewForm()
	form.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Agent Access + Key Bindings ", iconDashboard)).
		SetTitleColor(mauve).
		SetBorderColor(surface1)
	form.SetBackgroundColor(bg)
	form.SetFieldBackgroundColor(mantle).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetLabelColor(text).
		SetFieldTextColor(text)

	form.AddDropDown(settingsLabelAgentScope, agentConnectionScopeOptions, agentConnectionScopeIndex(settings.AgentAccess.ConnectionScope), nil)
	form.AddCheckbox(settingsLabelAgentProfileWrites, settings.AgentAccess.AllowProfileWrites, func(allowed bool) {
		if allowed {
			a.ShowAlert(fmt.Sprintf("%s Agent profile writes can create or update saved connection profiles.\n\nProfiles may contain credentials. Only enable this for an agent you trust; database queries remain read-only.", iconWarn), pageSettings)
		}
	})
	form.AddButton("Agent Setup / Copy", func() {
		setup := agentMCPSetupText()
		modal := tview.NewModal().
			SetText(tview.Escape(setup)).
			AddButtons([]string{" Copy instructions ", " Close "}).
			SetDoneFunc(func(index int, _ string) {
				a.pages.RemovePage(pageAgentSetup)
				if index != 0 {
					a.app.SetFocus(form)
					return
				}
				a.copyValueAsync(setup, func(copyErr error) {
					if copyErr != nil {
						a.ShowAlert(fmt.Sprintf("%s Instructions are in dbterm's internal clipboard; system clipboard unavailable:\n\n%v", iconInfo, copyErr), pageSettings)
						return
					}
					a.ShowAlert(fmt.Sprintf("%s MCP setup instructions copied. No credentials were included.", iconSuccess), pageSettings)
				})
			})
		modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
		a.pages.AddPage(pageAgentSetup, modal, true, true)
	})

	form.AddInputField("Dashboard Health Checks", settings.DashboardHealthChecks, 48, nil, nil)

	for _, field := range fields {
		form.AddInputField(field.Label, keymapFieldValue(settings, field.Action), 48, nil, nil)
	}

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	screenW, _ := a.getScreenSize()
	footer.SetText(settingsFooterText(screenW))

	backToDashboard := func() {
		a.pages.RemovePage(pageSettings)
		a.pages.RemovePage("dashboard")
		a.showDashboard()
	}

	saveFunc := func() {
		updated := cloneSettings(settings)
		if updated.Keymap == nil {
			updated.Keymap = map[string][]string{}
		}

		mode := strings.ToLower(strings.TrimSpace(formInputValue(form, "Dashboard Health Checks")))
		switch mode {
		case "", "auto":
			updated.DashboardHealthChecks = "auto"
		case "manual", "disabled", "off":
			updated.DashboardHealthChecks = "manual"
		default:
			a.ShowAlert(fmt.Sprintf("%s Dashboard Health Checks must be auto or manual.", iconWarn), pageSettings)
			return
		}

		updated.AgentAccess.ConnectionScope = selectedAgentConnectionScope(form)
		updated.AgentAccess.AllowProfileWrites = settingsFormCheckboxChecked(form, settingsLabelAgentProfileWrites)

		for _, field := range fields {
			bindings := parseBindingList(formInputValueByLabel(form, field.Label))
			if len(bindings) == 0 {
				a.ShowAlert(fmt.Sprintf("%s %s binding is required.\n\nUse values like alt+t or ctrl+a.", iconWarn, field.Label), pageSettings)
				return
			}
			updated.Keymap[field.Action] = bindings
		}

		resolver, err := newActionKeymap(updated)
		if err != nil {
			a.ShowAlert(fmt.Sprintf("%s Keymap validation failed:\n\n%v", iconWarn, err), pageSettings)
			return
		}

		if err := config.SaveSettings(updated); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not save settings:\n\n%v", iconWarn, err), pageSettings)
			return
		}

		settings = updated
		a.settings = cloneSettings(updated)
		a.keymap = resolver
		agentMode := "read-only"
		if updated.AgentAccess.AllowProfileWrites {
			agentMode = "read-only database + profile writes allowed"
		}
		a.ShowAlert(fmt.Sprintf("%s Settings saved.\n\nAgent access: %s, scope: %s.\nKeymap updated in %s.", iconSuccess, agentMode, updated.AgentAccess.ConnectionScope, settingsPath), pageSettings)
	}

	resetFunc := func() {
		defaults := config.DefaultSettings()
		if item := form.GetFormItemByLabel(settingsLabelAgentScope); item != nil {
			if dropdown, ok := item.(*tview.DropDown); ok {
				dropdown.SetCurrentOption(agentConnectionScopeIndex(defaults.AgentAccess.ConnectionScope))
			}
		}
		if item := form.GetFormItemByLabel(settingsLabelAgentProfileWrites); item != nil {
			if checkbox, ok := item.(*tview.Checkbox); ok {
				checkbox.SetChecked(defaults.AgentAccess.AllowProfileWrites)
			}
		}
		setFormInputValue(form, "Dashboard Health Checks", defaults.DashboardHealthChecks)
		for _, field := range fields {
			setFormInputValueByLabel(form, field.Label, keymapFieldValue(defaults, field.Action))
		}
		a.ShowAlert(fmt.Sprintf("%s Defaults restored in form.\n\nPress Save to persist them.", iconInfo), pageSettings)
	}

	form.AddButton("Save", saveFunc)
	form.AddButton("Reset Defaults", resetFunc)
	form.AddButton("Back", backToDashboard)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEscape, tcell.KeyBackspace, tcell.KeyBackspace2:
			backToDashboard()
			return nil
		case tcell.KeyCtrlS:
			saveFunc()
			return nil
		}
		return event
	})

	layout := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(header, 1, 0, false).
		AddItem(summary, 1, 0, false).
		AddItem(form, 0, 1, true).
		AddItem(footer, 1, 0, false)

	a.pages.AddAndSwitchToPage(pageSettings, layout, true)
	a.app.SetFocus(form)

	if loadErr != nil {
		a.ShowAlert(fmt.Sprintf("%s Settings required recovery or attention. The safely loaded values are shown below.\n\n%v", iconWarn, loadErr), pageSettings)
	}
}
