package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

func TestKeyboardHelpHighlightsCoreWorkflows(t *testing.T) {
	help := keyboardHelpText()
	for _, expected := range []string{
		"START HERE — COMMON WORKFLOWS",
		"Cross-table lookup",
		"[yellow]C[-]",
		"[yellow]V[-]",
		"[yellow]/[-]",
		"Clear a filter",
		"Pin/unpin the selected table",
		"Copy the selected table or column name",
		"Find a column",
		"Type (headers)",
		"Copy the complete selected column name",
		"[yellow]Shift+C / Right-click[-]",
		"Tab / Shift+Tab",
		"[yellow]Ctrl++ / Ctrl+-[-]",
		"[yellow]Alt++ / Alt+-[-]",
		"[yellow]> / <[-]",
		"[yellow]0 / Ctrl+0[-]",
		"[yellow]Ctrl+P[-]",
		"Search documented actions, tables, collapsed columns, database objects, and recent queries",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("keyboard help is missing %q", expected)
		}
	}
}

func TestKeyboardHelpUsesEffectiveConfiguredShortcuts(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Keymap[config.ActionFocusTables] = []string{"ctrl+g", "alt+t"}
	settings.Keymap[config.ActionChangeProfiler] = []string{"f8"}
	settings.Keymap[config.ActionCommandPalette] = []string{"alt+p"}

	help := keyboardHelpTextFor(&App{settings: settings})
	for _, expected := range []string{
		"[yellow]Ctrl+G / Alt+T[-]",
		"[yellow]F8[-]",
		"[yellow]Alt+P[-] Search documented actions",
		"CHANGE PROFILER (F8)",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("configured keyboard help is missing %q", expected)
		}
	}
	if strings.Contains(help, "Ctrl+P (default)") {
		t.Fatal("configured keyboard help still labels the default palette shortcut")
	}
}

func TestManualGuideSectionsAreCompleteAndUnique(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Keymap[config.ActionHelp] = []string{"f8"}
	sections := manualGuideSections(&App{settings: settings})
	if len(sections) < 20 {
		t.Fatalf("manual sections = %d, want at least 20", len(sections))
	}

	titles := make(map[string]struct{}, len(sections))
	var content strings.Builder
	for _, section := range sections {
		if strings.TrimSpace(section.title) == "" || strings.TrimSpace(section.body) == "" {
			t.Fatalf("manual contains an empty section: %#v", section)
		}
		if _, exists := titles[section.title]; exists {
			t.Fatalf("duplicate manual section title %q", section.title)
		}
		titles[section.title] = struct{}{}
		content.WriteString(section.body)
		content.WriteByte('\n')
	}

	for _, title := range []string{
		"Keyboard & workflows",
		"Create and manage connections",
		"Work with result rows and columns",
		"Back up and restore databases",
		"Connect trusted AI agents through MCP",
		"CLI reference",
		"Troubleshooting",
		"Security, privacy, and deliberate limits",
		"Backup Center · full handbook",
		"Agent/MCP · full handbook",
		"Change Profiler · technical reference",
	} {
		if _, exists := titles[title]; !exists {
			t.Fatalf("manual is missing section %q", title)
		}
	}

	all := content.String()
	for _, expected := range []string{
		"PostgreSQL, MySQL/MariaDB, SQLite, Turso/LibSQL, and Cloudflare D1",
		"Read-Only Guard",
		"IS NOT NULL",
		"Change Profiler",
		"dbterm backup service status --all",
		"list_connections",
		"smtp.gmail.com",
		"Portable exact mode",
		"recover-sudo",
		"F8[-] This guide",
	} {
		if !strings.Contains(all, expected) {
			t.Fatalf("manual content is missing %q", expected)
		}
	}
}

func TestEmbeddedGuideUsesEffectiveShortcutsWithoutRewritingCompletionKeys(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Keymap[config.ActionFocusTables] = []string{"alt+q"}
	settings.Keymap[config.ActionFocusQuery] = []string{"alt+t"}
	settings.Keymap[config.ActionFocusResults] = []string{"f6"}
	settings.Keymap[config.ActionHelp] = []string{"f8"}
	settings.Keymap[config.ActionSettings] = []string{"f9"}
	settings.Keymap[config.ActionCommandPalette] = []string{"alt+p"}
	settings.Keymap[config.ActionBackupCenter] = []string{"f10"}
	settings.Keymap[config.ActionBackup] = []string{"f11"}

	sections := manualGuideSections(&App{settings: settings})
	byTitle := make(map[string]string, len(sections))
	for _, section := range sections {
		byTitle[section.title] = section.body
	}

	for title, expected := range map[string]string{
		"Start here": "use Alt+T to focus Query",
		"Navigate the workspace and command palette": "Alt+Q, Alt+T, and F6 focus",
		"Configure Settings and shortcuts":           "Effective configurable actions:",
		"Troubleshooting":                            "search through Alt+P",
	} {
		if !strings.Contains(byTitle[title], expected) {
			t.Fatalf("guide section %q is missing effective text %q:\n%s", title, expected, byTitle[title])
		}
	}
	if !strings.Contains(byTitle["Start here"], "Press F8 at any time") {
		t.Fatalf("start section did not use effective Guide shortcut:\n%s", byTitle["Start here"])
	}
	if !strings.Contains(byTitle["Configure Settings and shortcuts"], "[::b][#89b4fa]Effective:[-][-] F9") {
		t.Fatalf("settings table did not use its effective shortcut:\n%s", byTitle["Configure Settings and shortcuts"])
	}
	for _, expected := range []string{"Open Backup Center with F10", "available with F11"} {
		if !strings.Contains(byTitle["Backup Center · full handbook"], expected) {
			t.Fatalf("embedded Backup handbook is missing effective text %q:\n%s", expected, byTitle["Backup Center · full handbook"])
		}
	}
	if !strings.Contains(byTitle["Write SQL, use autocomplete, and query history"], "Ctrl+P/Ctrl+N changes the selection") {
		t.Fatalf("contextual completion keys were rewritten:\n%s", byTitle["Write SQL, use autocomplete, and query history"])
	}
}

func TestGuideMarkdownRendersTablesAndNumberedListsForTerminal(t *testing.T) {
	rendered := renderGuideMarkdown(`1. First step
2. Second step

| Key | Action |
| --- | --- |
| N | New connection |
| Enter | Open selection |`)

	for _, expected := range []string{
		"[#cba6f7]1.[-] First step",
		"[#cba6f7]2.[-] Second step",
		"[::b][#89b4fa]Key:[-][-] N",
		"[#a6adc8]Action:[-] New connection",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered guide is missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "| --- |") {
		t.Fatalf("rendered guide leaked a Markdown separator row:\n%s", rendered)
	}
}

func TestGuideFooterAlwaysFitsOneLine(t *testing.T) {
	for _, width := range []int{1, 2, 3, 10, 30, 40, 60, 80, 120, 180} {
		for _, reading := range []bool{false, true} {
			footer := guideFooterText(width, "Ctrl+G / Alt+H", reading)
			if got := tview.TaggedStringWidth(footer); got > width {
				t.Fatalf("guide footer width = %d, available = %d, reading=%t: %q", got, width, reading, footer)
			}
			if width >= 3 && !strings.Contains(footer, "Esc") {
				t.Fatalf("guide footer omitted Esc close at width %d, reading=%t: %q", width, reading, footer)
			}
		}
	}
	if footer := guideFooterText(180, "Ctrl+G / Alt+H", true); !strings.Contains(footer, "Ctrl+G / Alt+H") {
		t.Fatalf("wide guide footer omitted effective shortcut: %q", footer)
	}
}

func TestAltHClosesHelpBackToItsExactCaller(t *testing.T) {
	keymap, err := newActionKeymap(config.DefaultSettings())
	if err != nil {
		t.Fatalf("build keymap: %v", err)
	}
	application := tview.NewApplication()
	pages := tview.NewPages()
	center := tview.NewList()
	pages.AddPage(pageBackupCenter, center, true, true)
	application.SetRoot(pages, true).SetFocus(center)
	app := &App{app: application, pages: pages, keymap: keymap}
	app.setupKeyBindings()
	app.showHelp()

	if returned := application.GetInputCapture()(tcell.NewEventKey(tcell.KeyRune, 'h', tcell.ModAlt)); returned != nil {
		t.Fatalf("Alt+H close was not consumed: %#v", returned)
	}
	frontPage, _ := pages.GetFrontPage()
	if frontPage != pageBackupCenter {
		t.Fatalf("front page = %q, want Backup Center", frontPage)
	}
	if application.GetFocus() != center {
		t.Fatalf("focus = %T, want original Backup Center list", application.GetFocus())
	}
}

func TestNarrowGuideDrillsFromContentsIntoArticleAndBack(t *testing.T) {
	keymap, err := newActionKeymap(config.DefaultSettings())
	if err != nil {
		t.Fatalf("build keymap: %v", err)
	}
	application := tview.NewApplication()
	pages := tview.NewPages()
	dashboard := tview.NewList()
	pages.AddPage("dashboard", dashboard, true, true)
	application.SetRoot(pages, true).SetFocus(dashboard)
	app := &App{
		app:         application,
		pages:       pages,
		keymap:      keymap,
		lastScreenW: 60,
		lastScreenH: 24,
	}
	app.showHelp()

	_, primitive := pages.GetFrontPage()
	layout, ok := primitive.(*tview.Flex)
	if !ok {
		t.Fatalf("guide root = %T, want *tview.Flex", primitive)
	}
	body, ok := layout.GetItem(1).(*tview.Flex)
	if !ok {
		t.Fatalf("narrow guide body = %T, want *tview.Flex", layout.GetItem(1))
	}
	if body.GetItemCount() != 1 {
		t.Fatalf("narrow guide body has %d items, want one", body.GetItemCount())
	}
	contents, ok := body.GetItem(0).(*tview.List)
	if !ok {
		t.Fatalf("narrow guide first view = %T, want contents list", body.GetItem(0))
	}

	setFocus := func(primitive tview.Primitive) { application.SetFocus(primitive) }
	contents.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), setFocus)
	article, ok := body.GetItem(0).(*tview.TextView)
	if !ok {
		t.Fatalf("narrow guide selected view = %T, want article", body.GetItem(0))
	}
	if application.GetFocus() != article {
		t.Fatalf("narrow guide focus = %T, want article", application.GetFocus())
	}
	if app.guideResize == nil {
		t.Fatal("guide did not register responsive resize handling")
	}
	app.tableExpanded = true // Keep this test focused on the Guide resize hook.
	app.applyResponsiveLayout(120, 40)
	if body.GetItemCount() != 2 || body.GetItem(1) != article {
		t.Fatalf("wide guide body = (%d items, second %T), want contents + article", body.GetItemCount(), body.GetItem(1))
	}
	if application.GetFocus() != article {
		t.Fatalf("wide resize lost article focus: %T", application.GetFocus())
	}
	app.applyResponsiveLayout(60, 24)
	if body.GetItemCount() != 1 || body.GetItem(0) != article {
		t.Fatalf("narrow resize did not preserve article: count=%d item=%T", body.GetItemCount(), body.GetItem(0))
	}
	article.InputHandler()(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone), setFocus)
	if body.GetItem(0) != contents {
		t.Fatalf("narrow guide did not return to contents: %T", body.GetItem(0))
	}
	if application.GetFocus() != contents {
		t.Fatalf("narrow guide focus = %T, want contents", application.GetFocus())
	}
}

func TestCommandPaletteGuideRestoresItsExactCaller(t *testing.T) {
	application := tview.NewApplication()
	pages := tview.NewPages()
	settingsField := tview.NewInputField()
	settingsPage := tview.NewFlex().AddItem(settingsField, 0, 1, true)
	palette := tview.NewInputField()
	pages.AddPage(pageSettings, settingsPage, true, true)
	pages.AddPage(pageCommandPalette, palette, true, true)
	application.SetRoot(pages, true).SetFocus(palette)

	app := &App{
		app:                application,
		pages:              pages,
		settings:           config.DefaultSettings(),
		paletteReturnPage:  pageSettings,
		paletteReturnFocus: settingsField,
	}
	app.executeCommandPaletteSelection(commandPaletteItem{kind: commandPaletteAction, action: actionHelp})
	if app.helpReturnPage != pageSettings || app.helpReturnFocus != settingsField {
		t.Fatalf("Guide return state = (%q, %T), want Settings and its exact field", app.helpReturnPage, app.helpReturnFocus)
	}

	app.closeHelp()
	if front, _ := pages.GetFrontPage(); front != pageSettings {
		t.Fatalf("front page after Guide close = %q, want Settings", front)
	}
	if application.GetFocus() != settingsField {
		t.Fatalf("focus after Guide close = %T, want original Settings field", application.GetFocus())
	}
}
