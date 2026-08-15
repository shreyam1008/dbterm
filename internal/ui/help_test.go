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
		"Tab / Shift+Tab",
		"[yellow]Ctrl++ / Ctrl+-[-]",
		"[yellow]Alt++ / Alt+-[-]",
		"[yellow]> / <[-]",
		"[yellow]0 / Ctrl+0[-]",
		"[yellow]Ctrl+P (default)[-]",
		"Search documented actions, database objects, and recent queries",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("keyboard help is missing %q", expected)
		}
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
