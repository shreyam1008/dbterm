package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

const pageSettings = "settings"

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
	{Action: config.ActionExportCSV, Label: "Export CSV"},
	{Action: config.ActionHistory, Label: "Query History"},
	{Action: config.ActionSettings, Label: "Open Settings"},
	{Action: config.ActionImportDump, Label: "Import Dump"},
	{Action: config.ActionSelectAll, Label: "Select All Rows"},
	{Action: config.ActionClearSelection, Label: "Clear Selection"},
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
	}

	for action, bindings := range settings.Keymap {
		copied := make([]string, len(bindings))
		copy(copied, bindings)
		cloned.Keymap[action] = copied
	}

	return cloned
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
	header.SetText(fmt.Sprintf(" [::b][#cba6f7]%s Settings[-][-]  [#a6adc8]Keymap (backend config)[-]", iconDashboard))

	summary := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	summary.SetBackgroundColor(bg)
	summary.SetText("[#6c7086]Use | to separate bindings. Dashboard health checks: auto or manual. Saved at ~/.config/dbterm/settings.json[-]")

	form := tview.NewForm()
	form.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Key Bindings ", iconDashboard)).
		SetTitleColor(mauve).
		SetBorderColor(surface1)
	form.SetBackgroundColor(bg)
	form.SetFieldBackgroundColor(mantle).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetLabelColor(text).
		SetFieldTextColor(text)

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

		for _, field := range fields {
			bindings := parseBindingList(formInputValue(form, field.Label))
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
		a.ShowAlert(fmt.Sprintf("%s Settings saved.\n\nKeymap updated in ~/.config/dbterm/settings.json.", iconSuccess), pageSettings)
	}

	resetFunc := func() {
		defaults := config.DefaultSettings()
		setFormInputValue(form, "Dashboard Health Checks", defaults.DashboardHealthChecks)
		for _, field := range fields {
			setFormInputValue(form, field.Label, keymapFieldValue(defaults, field.Action))
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
		a.ShowAlert(fmt.Sprintf("%s Loaded defaults because settings could not be read.\n\n%v", iconWarn, loadErr), pageSettings)
	}
}
