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
