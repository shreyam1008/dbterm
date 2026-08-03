package ui

import (
	"strings"
	"testing"
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
		"Tab / Shift+Tab",
		"[yellow]Ctrl++ / Ctrl+-[-]",
		"[yellow]Alt++ / Alt+-[-]",
		"[yellow]> / <[-]",
		"[yellow]0 / Ctrl+0[-]",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("keyboard help is missing %q", expected)
		}
	}
}
