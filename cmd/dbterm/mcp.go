package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/mcpserver"
)

type mcpServeFlags struct {
	connectionScope  string
	denyProfileWrite bool
	maxRows          int
	timeout          time.Duration
}

func runMCPCommand(args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		printMCPHelp(os.Stdout)
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(args[0]), "serve") {
		return fmt.Errorf("unknown MCP command %q (expected: serve)", args[0])
	}
	if len(args) > 1 && isHelpArg(args[1]) {
		printMCPHelp(os.Stdout)
		return nil
	}
	settings, err := config.LoadSettings()
	if err != nil {
		return fmt.Errorf("load dbterm settings: %w", err)
	}
	parsed, err := parseMCPServeFlags(args[1:], settings.AgentAccess.ConnectionScope, os.Stderr)
	if err != nil {
		return err
	}
	allowProfileWrites := settings.AgentAccess.AllowProfileWrites
	if parsed.denyProfileWrite {
		allowProfileWrites = false
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Fprintf(os.Stderr, "dbterm MCP: local stdio, scope=%s, database=read-only, profile-writes=%t\n", parsed.connectionScope, allowProfileWrites)
	return mcpserver.RunStdio(ctx, mcpserver.Options{
		Version: buildVersion(), ConnectionScope: parsed.connectionScope,
		AllowProfileWrites: allowProfileWrites, MaxRows: parsed.maxRows,
		QueryTimeout: parsed.timeout, AuditWriter: os.Stderr,
	})
}

func parseMCPServeFlags(args []string, defaultScope string, output io.Writer) (mcpServeFlags, error) {
	if strings.TrimSpace(defaultScope) == "" {
		defaultScope = config.AgentConnectionScopeActive
	}
	parsed := mcpServeFlags{}
	flags := flag.NewFlagSet("dbterm mcp serve", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&parsed.connectionScope, "connection", defaultScope, "connection scope: active, all, or a saved connection ID")
	flags.BoolVar(&parsed.denyProfileWrite, "deny-profile-write", false, "hide profile writes even when enabled in dbterm settings")
	flags.IntVar(&parsed.maxRows, "max-rows", defaultMaxMCPRows, "maximum rows returned by one query (1-200)")
	flags.DurationVar(&parsed.timeout, "timeout", 8*time.Second, "per-tool database timeout (maximum 30s)")
	if err := flags.Parse(args); err != nil {
		return mcpServeFlags{}, err
	}
	if flags.NArg() != 0 {
		return mcpServeFlags{}, fmt.Errorf("unexpected MCP arguments: %s", strings.Join(flags.Args(), " "))
	}
	parsed.connectionScope = strings.TrimSpace(parsed.connectionScope)
	if parsed.connectionScope == "" {
		return mcpServeFlags{}, fmt.Errorf("--connection cannot be empty")
	}
	if parsed.maxRows < 1 || parsed.maxRows > 200 {
		return mcpServeFlags{}, fmt.Errorf("--max-rows must be between 1 and 200")
	}
	if parsed.timeout <= 0 || parsed.timeout > 30*time.Second {
		return mcpServeFlags{}, fmt.Errorf("--timeout must be greater than zero and at most 30s")
	}
	return parsed, nil
}

const defaultMaxMCPRows = 50

func printMCPHelp(writer io.Writer) {
	fmt.Fprint(writer, `
  dbterm MCP — connect trusted local agents to saved database profiles

  USAGE
    dbterm mcp serve [options]

  OPTIONS
    --connection active|all|ID  Limit which saved profiles an agent can use
    --max-rows N                Query row ceiling (default 50, maximum 200)
    --timeout 8s                Per-tool database timeout (maximum 30s)
    --deny-profile-write        Force profile writes off for this run

  SAFETY
    Database SQL is always read-only. The server uses local stdio only, returns
    bounded results, never returns passwords/tokens, and audits calls to stderr.
    Profile writes require opt-in through dbterm Settings. Prefer a database
    account with SELECT-only grants.
`)
}
