package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/shreyam1008/dbterm/config"
)

func TestNormalizeBinding(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "alt rune", input: "Alt + Q", want: "alt+q"},
		{name: "ctrl function key with dash separator", input: "CTRL-F5", want: "ctrl+f5"},
		{name: "shift enter", input: "shift+enter", want: "shift+enter"},
		{name: "plus keyword", input: "alt+plus", want: "alt++"},
		{name: "empty binding", input: " ", wantErr: true},
		{name: "unknown token", input: "alt+super", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeBinding(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeBinding(%q) error = %v, wantErr=%v", tt.input, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Fatalf("normalizeBinding(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNewActionKeymapRejectsDuplicateBindings(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Keymap[config.ActionExportCSV] = []string{"alt+b"} // conflicts with backup

	_, err := newActionKeymap(settings)
	if err == nil {
		t.Fatal("newActionKeymap() expected duplicate binding error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "duplicate") {
		t.Fatalf("newActionKeymap() error = %v, expected duplicate message", err)
	}
}

func TestNewActionKeymapRejectsUnknownActions(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Keymap["unknown-action"] = []string{"alt+u"}

	_, err := newActionKeymap(settings)
	if err == nil {
		t.Fatal("newActionKeymap() expected unknown action error, got nil")
	}
}

func TestActionKeymapResolve(t *testing.T) {
	resolver, err := newActionKeymap(config.DefaultSettings())
	if err != nil {
		t.Fatalf("newActionKeymap(defaults) error = %v", err)
	}

	event := tcell.NewEventKey(tcell.KeyRune, 'Q', tcell.ModAlt)
	action, ok := resolver.Resolve(event)
	if !ok {
		t.Fatal("Resolve(Alt+Q) did not match any action")
	}
	if action != actionFocusQuery {
		t.Fatalf("Resolve(Alt+Q) action = %q, want %q", action, actionFocusQuery)
	}

	importEvent := tcell.NewEventKey(tcell.KeyRune, 'i', tcell.ModAlt)
	action, ok = resolver.Resolve(importEvent)
	if !ok || action != actionImportDump {
		t.Fatalf("Resolve(Alt+I) = (%q,%v), want (%q,true)", action, ok, actionImportDump)
	}

	schemaEvent := tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModAlt)
	action, ok = resolver.Resolve(schemaEvent)
	if !ok || action != actionInspectSchema {
		t.Fatalf("Resolve(Alt+M) = (%q,%v), want (%q,true)", action, ok, actionInspectSchema)
	}
}

func TestActionKeymapResolveNilSafe(t *testing.T) {
	var resolver *actionKeymap
	if _, ok := resolver.Resolve(nil); ok {
		t.Fatal("Resolve(nil) on nil resolver should not match")
	}

	nonNil := &actionKeymap{}
	if _, ok := nonNil.Resolve(nil); ok {
		t.Fatal("Resolve(nil) on empty resolver should not match")
	}
}
