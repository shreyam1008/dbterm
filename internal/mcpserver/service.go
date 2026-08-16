package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
)

type service struct {
	options Options
	limits  limits
	audit   io.Writer
	seq     atomic.Uint64
}

func newService(options Options) *service {
	if options.StoreLoader == nil {
		options.StoreLoader = config.LoadStore
	}
	if options.Connector == nil {
		options.Connector = database.Connect
	}
	maxRows := options.MaxRows
	if maxRows <= 0 {
		maxRows = defaultMaxRows
	}
	if maxRows > maximumMaxRows {
		maxRows = maximumMaxRows
	}
	timeout := options.QueryTimeout
	if timeout <= 0 {
		timeout = defaultQueryTimeout
	}
	if timeout > maximumQueryTimeout {
		timeout = maximumQueryTimeout
	}
	if options.AuditWriter == nil {
		options.AuditWriter = os.Stderr
	}
	return &service{
		options: options,
		limits: limits{
			maxRows: maxRows, queryTimeout: timeout,
			maxQueryBytes: defaultMaxQueryBytes, maxCellBytes: defaultMaxCellBytes,
			maxOutputBytes: defaultMaxOutputBytes, maxColumns: defaultMaxColumns, maxTables: defaultMaxTables,
		},
		audit: options.AuditWriter,
	}
}

func (s *service) auditID() string {
	return fmt.Sprintf("mcp-%d-%04d", time.Now().UTC().Unix(), s.seq.Add(1))
}

func (s *service) logAudit(id, tool, connectionID, status, detail string, started time.Time) {
	record := map[string]any{
		"audit_id": id, "tool": tool, "connection_id": connectionID,
		"status": status, "duration_ms": time.Since(started).Milliseconds(),
	}
	if detail != "" {
		record["detail"] = detail
	}
	encoded, err := json.Marshal(record)
	if err == nil {
		fmt.Fprintln(s.audit, string(encoded))
	}
}

func (s *service) loadScopedConnections() (*config.Store, []config.ConnectionConfig, error) {
	store, err := s.options.StoreLoader()
	if err != nil {
		return nil, nil, fmt.Errorf("load saved connection profiles: %w", err)
	}
	if store == nil {
		return nil, nil, fmt.Errorf("load saved connection profiles: empty store")
	}
	scope := strings.TrimSpace(s.options.ConnectionScope)
	if scope == "" || strings.EqualFold(scope, "active") {
		for _, connection := range store.Connections {
			if connection.Active {
				return store, []config.ConnectionConfig{connection}, nil
			}
		}
		return store, nil, fmt.Errorf("connection scope is active, but no saved connection is active")
	}
	if strings.EqualFold(scope, "all") {
		return store, append([]config.ConnectionConfig(nil), store.Connections...), nil
	}
	for _, connection := range store.Connections {
		if connection.ID == scope {
			return store, []config.ConnectionConfig{connection}, nil
		}
	}
	return store, nil, fmt.Errorf("configured connection scope %q does not match a saved connection ID", scope)
}

func (s *service) resolveConnection(id string) (config.ConnectionConfig, error) {
	_, connections, err := s.loadScopedConnections()
	if err != nil {
		return config.ConnectionConfig{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		if len(connections) != 1 {
			return config.ConnectionConfig{}, fmt.Errorf("connection_id is required when %d profiles are in scope", len(connections))
		}
		return connections[0], nil
	}
	for _, connection := range connections {
		if connection.ID == id {
			return connection, nil
		}
	}
	return config.ConnectionConfig{}, fmt.Errorf("connection %q is not present in the configured MCP scope", id)
}

func (s *service) connect(ctx context.Context, cfg config.ConnectionConfig) (*sql.DB, context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, s.limits.queryTimeout)
	connectCfg := cfg
	if cfg.Type == config.SQLite {
		readOnlyPath, err := sqliteReadOnlyDSN(cfg.FilePath)
		if err != nil {
			cancel()
			return nil, nil, nil, err
		}
		connectCfg.FilePath = readOnlyPath
		connectCfg.ReadOnly = true
	}
	db, err := s.options.Connector(&connectCfg)
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("connect to profile %q: %s", cfg.ID, redactError(err, cfg))
	}
	return db, ctx, func() { cancel(); db.Close() }, nil
}

func sqliteReadOnlyDSN(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("SQLite file path is required")
	}
	if path == ":memory:" || strings.Contains(path, "mode=memory") {
		return "", fmt.Errorf("in-memory SQLite profiles are not available to the MCP server")
	}
	if strings.HasPrefix(path, "file:") {
		parsed, err := url.Parse(path)
		if err != nil {
			return "", fmt.Errorf("parse SQLite file URI: %w", err)
		}
		query := parsed.Query()
		query.Set("mode", "ro")
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve SQLite file path: %w", err)
	}
	parsed := &url.URL{Scheme: "file", Path: absolute}
	query := parsed.Query()
	query.Set("mode", "ro")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func redactError(err error, cfg config.ConnectionConfig) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	for _, secret := range []string{cfg.Password, cfg.AuthToken, cfg.BuildConnString()} {
		if strings.TrimSpace(secret) != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return text
}

func summary(connection config.ConnectionConfig) connectionSummary {
	endpoint := sanitizeEndpoint(connection.Host)
	if connection.Port != "" && endpoint != "" {
		endpoint += ":" + connection.Port
	}
	if connection.Type == config.SQLite {
		endpoint = connection.FilePath
	}
	if connection.Type == config.CloudflareD1 {
		endpoint = connection.DatabaseID
	}
	return connectionSummary{
		ID: connection.ID, Name: connection.Name, Type: connection.Type,
		Database: connection.Database, Endpoint: endpoint, ReadOnly: connection.ReadOnly,
		Active: connection.Active, LastUsed: connection.LastUsed,
	}
}

func sanitizeEndpoint(value string) string {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "://") {
		return strings.SplitN(value, "?", 2)[0]
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "[configured endpoint]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	// The path is not needed for discovery and can contain tenant or credential
	// material in arbitrary provider URLs.
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed.String()
}
