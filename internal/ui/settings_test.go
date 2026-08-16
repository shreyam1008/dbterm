package ui

import (
	"strings"
	"testing"

	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

func TestAgentConnectionScopeIndexDefaultsToActive(t *testing.T) {
	if got := agentConnectionScopeIndex(""); got != 0 {
		t.Fatalf("empty scope index = %d, want 0", got)
	}
	if got := agentConnectionScopeIndex("unexpected"); got != 0 {
		t.Fatalf("unknown scope index = %d, want 0", got)
	}
	if got := agentConnectionScopeIndex(config.AgentConnectionScopeAll); got != 1 {
		t.Fatalf("all scope index = %d, want 1", got)
	}
}

func TestSelectedAgentConnectionScope(t *testing.T) {
	form := tview.NewForm()
	form.AddDropDown(settingsLabelAgentScope, agentConnectionScopeOptions, 1, nil)
	if got := selectedAgentConnectionScope(form); got != config.AgentConnectionScopeAll {
		t.Fatalf("selected scope = %q, want %q", got, config.AgentConnectionScopeAll)
	}
}

func TestAgentMCPSetupTextContainsNoCredentialPlaceholders(t *testing.T) {
	setup := agentMCPSetupText()
	for _, want := range []string{"dbterm mcp serve", "codex mcp add", "claude mcp add", `"type":"stdio"`} {
		if !strings.Contains(setup, want) {
			t.Fatalf("setup instructions missing %q", want)
		}
	}
	for _, forbidden := range []string{"PASSWORD=", "TOKEN=", "DSN="} {
		if strings.Contains(strings.ToUpper(setup), forbidden) {
			t.Fatalf("setup instructions contain credential field %q", forbidden)
		}
	}
}
