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
		"[yellow]> / <[-]",
		"[yellow]0[-]",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("keyboard help is missing %q", expected)
		}
	}
}
