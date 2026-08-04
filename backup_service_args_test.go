package main

import (
	"strings"
	"testing"

	"github.com/shreyam1008/dbterm/internal/osservice"
)

func TestParseBackupServiceRequestAcceptsFlagsAfterAction(t *testing.T) {
	request, err := parseBackupServiceRequest([]string{
		"install", "--system", "--run-as", "alice",
		"--config-dir=/home/alice/.config/dbterm",
		"--state-dir", "/home/alice/.local/state/dbterm",
		"--log-dir", "/home/alice/.local/state/dbterm/logs",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Action != "install" || request.Scope != osservice.ScopeSystem || request.RunAsUser != "alice" {
		t.Fatalf("request = %#v", request)
	}
	if request.ConfigDir == "" || request.StateDir == "" || request.LogDir == "" {
		t.Fatalf("explicit paths missing: %#v", request)
	}
}

func TestParseBackupServiceRequestValidatesScopeAndSystemPaths(t *testing.T) {
	tests := [][]string{
		{"install", "--system"},
		{"start", "--all"},
		{"status", "--all", "--system"},
		{"status", "--user", "--run-as", "alice"},
		{"start", "--system", "--user"},
	}
	for _, args := range tests {
		if _, err := parseBackupServiceRequest(args); err == nil {
			t.Fatalf("parseBackupServiceRequest(%q) succeeded", strings.Join(args, " "))
		}
	}
	request, err := parseBackupServiceRequest([]string{"status", "--all"})
	if err != nil || !request.AllScopes {
		t.Fatalf("status --all = %#v, %v", request, err)
	}
}
