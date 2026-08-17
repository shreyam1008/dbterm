package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParseMCPServeFlags(t *testing.T) {
	t.Parallel()
	parsed, err := parseMCPServeFlags([]string{"--connection", "profile-id", "--max-rows", "20", "--timeout", "4s"}, "active", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.connectionScope != "profile-id" || parsed.maxRows != 20 || parsed.timeout != 4*time.Second {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestParseMCPServeFlagsRejectsUnsafeBounds(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--max-rows", "201"}, {"--timeout", "31s"}, {"--allow-profile-write"}} {
		if _, err := parseMCPServeFlags(args, "active", &bytes.Buffer{}); err == nil {
			t.Fatalf("expected %v to fail", args)
		}
	}
}

func TestMCPHelpStatesReadOnlyContract(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	printMCPHelp(&output)
	if !strings.Contains(output.String(), "Database SQL is always read-only") || !strings.Contains(output.String(), "local stdio") {
		t.Fatalf("help missing safety contract: %s", output.String())
	}
}

func TestMCPServeHelpDoesNotRequireSettingsOrStartServer(t *testing.T) {
	t.Setenv("DBTERM_CONFIG_DIR", t.TempDir())
	t.Setenv("DBTERM_STATE_DIR", t.TempDir())
	if err := runMCPCommand([]string{"serve", "--help"}); err != nil {
		t.Fatalf("serve help returned an error: %v", err)
	}
}
