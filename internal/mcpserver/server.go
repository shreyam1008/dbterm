// Package mcpserver exposes dbterm's saved database profiles to trusted local
// agent clients over the Model Context Protocol. The server is stdio-only and
// read-only by default.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const instructionsURI = "dbterm://mcp/instructions"

// New builds a configured MCP server without starting a transport.
func New(options Options) *mcp.Server {
	service := newService(options)
	version := strings.TrimSpace(options.Version)
	if version == "" {
		version = "dev"
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name: "dbterm", Version: version, WebsiteURL: "https://dbterm.shreyam1008.com.np/",
	}, &mcp.ServerOptions{Instructions: serverInstructions(options.AllowProfileWrites)})

	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(false)}
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_connections", Description: "List saved dbterm connection profiles in the configured scope. Passwords and tokens are never returned.", Annotations: readOnly,
	}, func(context.Context, *mcp.CallToolRequest, listConnectionsInput) (*mcp.CallToolResult, listConnectionsOutput, error) {
		output, err := service.listConnections()
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "inspect_database", Description: "List accessible schemas and user tables for one saved connection without reading table rows.", Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input inspectDatabaseInput) (*mcp.CallToolResult, inspectDatabaseOutput, error) {
		output, err := service.inspectDatabase(ctx, input)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "inspect_table", Description: "Inspect columns, primary-key markers, and declared outgoing foreign keys for a table.", Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input inspectTableInput) (*mcp.CallToolResult, inspectTableOutput, error) {
		output, err := service.inspectTable(ctx, input)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "query_read_only", Description: "Run one bounded read-only SQL statement. Writes, multi-statements, EXPLAIN ANALYZE, unsafe PRAGMAs, and oversized output are rejected.", Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input readQueryInput) (*mcp.CallToolResult, queryOutput, error) {
		output, err := service.queryReadOnly(ctx, input)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "explain_query", Description: "Validate a SELECT against dbterm's read-only policy and request a database execution plan without ANALYZE or query execution.", Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input explainQueryInput) (*mcp.CallToolResult, explainQueryOutput, error) {
		output, err := service.explainQuery(ctx, input)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "follow_record", Description: "Load one record by exact key and follow declared outgoing and incoming foreign keys by one hop. Results and relationships are bounded.", Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input followRecordInput) (*mcp.CallToolResult, followRecordOutput, error) {
		output, err := service.followRecord(ctx, input)
		return nil, output, err
	})
	if options.AllowProfileWrites {
		mcp.AddTool(server, &mcp.Tool{
			Name: "save_connection_profile", Description: "Create or fully replace a saved dbterm connection profile after validating connectivity. On update, all non-secret fields are replaced; empty password and auth_token preserve their existing stored values. Secrets are write-only and never returned.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPointer(true), IdempotentHint: false, OpenWorldHint: boolPointer(true)},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input saveProfileInput) (*mcp.CallToolResult, saveProfileOutput, error) {
			output, err := service.saveProfile(ctx, input)
			return nil, output, err
		})
	}

	server.AddResource(&mcp.Resource{
		Name: "dbterm MCP instructions", Description: "Local client setup, scope, and safety contract for this dbterm MCP server.", MIMEType: "text/markdown", URI: instructionsURI,
	}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if request.Params.URI != instructionsURI {
			return nil, fmt.Errorf("unknown dbterm resource %q", request.Params.URI)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: instructionsURI, MIMEType: "text/markdown", Text: serverInstructions(options.AllowProfileWrites)}}}, nil
	})
	return server
}

// RunStdio serves MCP over this process's stdin/stdout. Protocol output must
// remain on stdout; audit events and diagnostics go to stderr.
func RunStdio(ctx context.Context, options Options) error {
	return normalizeRunError(New(options).Run(ctx, &mcp.StdioTransport{}))
}

func normalizeRunError(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func serverInstructions(profileWrites bool) string {
	writeState := "Profile writes are disabled; the save_connection_profile tool is not exposed."
	if profileWrites {
		writeState = "Profile writes are explicitly enabled. save_connection_profile may persist credentials but never returns them."
	}
	return `# dbterm local MCP server

Use saved connection IDs from list_connections. Inspect metadata before writing SQL. query_read_only accepts one bounded read-only statement and never permits database writes. follow_record follows only declared foreign keys and returns bounded rows. Prefer a database account with SELECT-only grants; syntax checks cannot determine every user-defined SQL function's side effects.

This is a local stdio server started with dbterm mcp serve. It is not a hosted HTTP MCP endpoint. ` + writeState
}

func boolPointer(value bool) *bool { return &value }
