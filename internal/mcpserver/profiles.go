package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func (s *service) listConnections() (listConnectionsOutput, error) {
	_, connections, err := s.loadScopedConnections()
	if err != nil {
		return listConnectionsOutput{}, err
	}
	result := listConnectionsOutput{Scope: normalizedScope(s.options.ConnectionScope), Connections: make([]connectionSummary, 0, len(connections))}
	for _, connection := range connections {
		result.Connections = append(result.Connections, summary(connection))
	}
	return result, nil
}

func normalizedScope(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return "active"
	}
	return scope
}

func (s *service) saveProfile(ctx context.Context, input saveProfileInput) (saveProfileOutput, error) {
	auditID := s.auditID()
	started := time.Now()
	if !s.options.AllowProfileWrites {
		s.logAudit(auditID, "save_connection_profile", input.ID, "denied", "profile writes disabled", started)
		return saveProfileOutput{}, fmt.Errorf("profile writes are disabled [%s]", auditID)
	}
	store, err := s.options.StoreLoader()
	if err != nil {
		return saveProfileOutput{}, fmt.Errorf("load profiles [%s]: %w", auditID, err)
	}
	if store == nil {
		return saveProfileOutput{}, fmt.Errorf("load profiles [%s]: empty store", auditID)
	}

	created := strings.TrimSpace(input.ID) == ""
	index := -1
	var existing config.ConnectionConfig
	if !created {
		for i, connection := range store.Connections {
			if connection.ID == input.ID {
				index, existing = i, connection
				break
			}
		}
		if index < 0 {
			return saveProfileOutput{}, fmt.Errorf("connection profile %q not found [%s]", input.ID, auditID)
		}
		if _, err := s.resolveConnection(input.ID); err != nil {
			return saveProfileOutput{}, fmt.Errorf("profile %q is outside the configured MCP scope [%s]", input.ID, auditID)
		}
	}

	candidate := config.ConnectionConfig{
		ID: input.ID, Name: strings.TrimSpace(input.Name), Type: input.Type,
		Host: strings.TrimSpace(input.Host), Port: strings.TrimSpace(input.Port), User: input.User,
		Password: input.Password, Database: strings.TrimSpace(input.Database),
		FilePath: strings.TrimSpace(input.FilePath), SSLMode: strings.TrimSpace(input.SSLMode),
		AccountID: strings.TrimSpace(input.AccountID), DatabaseID: strings.TrimSpace(input.DatabaseID),
		AuthToken: input.AuthToken,
	}
	if input.ReadOnly == nil {
		candidate.ReadOnly = true
	} else {
		candidate.ReadOnly = *input.ReadOnly
	}
	if !created {
		candidate.Active, candidate.LastUsed = existing.Active, existing.LastUsed
		if candidate.Password == "" {
			candidate.Password = existing.Password
		}
		if candidate.AuthToken == "" {
			candidate.AuthToken = existing.AuthToken
		}
	}
	applyProfileDefaults(&candidate)
	if err := validateProfile(candidate); err != nil {
		s.logAudit(auditID, "save_connection_profile", input.ID, "denied", err.Error(), started)
		return saveProfileOutput{}, fmt.Errorf("invalid profile [%s]: %w", auditID, err)
	}

	// Refuse to persist credentials that do not connect. Connector errors are
	// scrubbed before returning and the raw input is never logged.
	db, err := s.options.Connector(&candidate)
	if err != nil {
		s.logAudit(auditID, "save_connection_profile", input.ID, "error", "connection validation failed", started)
		return saveProfileOutput{}, fmt.Errorf("connection validation failed [%s]: %s", auditID, redactError(err, candidate))
	}
	db.Close()
	if created {
		err = store.Add(candidate)
	} else {
		err = store.Update(index, candidate)
	}
	if err != nil {
		s.logAudit(auditID, "save_connection_profile", input.ID, "error", "persistence failed", started)
		return saveProfileOutput{}, fmt.Errorf("save profile failed [%s]: %s", auditID, redactError(err, candidate))
	}
	stored := candidate
	if created && len(store.Connections) > 0 {
		stored = store.Connections[len(store.Connections)-1]
	}
	if !created {
		stored = store.Connections[index]
	}
	s.logAudit(auditID, "save_connection_profile", stored.ID, "ok", map[bool]string{true: "created", false: "updated"}[created], started)
	return saveProfileOutput{Created: created, Connection: summary(stored), AuditID: auditID}, nil
}

func applyProfileDefaults(candidate *config.ConnectionConfig) {
	if candidate == nil {
		return
	}
	switch candidate.Type {
	case config.PostgreSQL:
		candidate.FilePath, candidate.AuthToken, candidate.AccountID, candidate.DatabaseID = "", "", "", ""
		if candidate.Port == "" {
			candidate.Port = "5432"
		}
		if candidate.SSLMode == "" {
			candidate.SSLMode = "require"
		}
	case config.MySQL:
		candidate.FilePath, candidate.SSLMode, candidate.AuthToken, candidate.AccountID, candidate.DatabaseID = "", "", "", "", ""
		if candidate.Port == "" {
			candidate.Port = "3306"
		}
	case config.SQLite:
		candidate.Host, candidate.Port, candidate.User, candidate.Password, candidate.Database = "", "", "", "", ""
		candidate.SSLMode, candidate.AuthToken, candidate.AccountID, candidate.DatabaseID = "", "", "", ""
	case config.Turso:
		candidate.Port, candidate.User, candidate.Password, candidate.Database, candidate.FilePath = "", "", "", "", ""
		candidate.SSLMode, candidate.AccountID, candidate.DatabaseID = "", "", ""
	case config.CloudflareD1:
		candidate.Host, candidate.Port, candidate.User, candidate.Password, candidate.Database = "", "", "", "", ""
		candidate.FilePath, candidate.SSLMode = "", ""
	}
}

func validateProfile(candidate config.ConnectionConfig) error {
	if candidate.Name == "" {
		return fmt.Errorf("name is required")
	}
	switch candidate.Type {
	case config.PostgreSQL, config.MySQL:
		if candidate.Host == "" {
			return fmt.Errorf("host is required for %s", candidate.Type)
		}
		if candidate.Port == "" {
			return fmt.Errorf("port is required for %s", candidate.Type)
		}
		if candidate.User == "" {
			return fmt.Errorf("user is required for %s", candidate.Type)
		}
	case config.SQLite:
		if candidate.FilePath == "" {
			return fmt.Errorf("file_path is required for sqlite")
		}
	case config.Turso:
		if candidate.Host == "" {
			return fmt.Errorf("host is required for turso")
		}
	case config.CloudflareD1:
		if candidate.AccountID == "" || candidate.DatabaseID == "" || candidate.AuthToken == "" {
			return fmt.Errorf("account_id, database_id, and auth_token are required for d1")
		}
	default:
		return fmt.Errorf("unsupported database type %q", candidate.Type)
	}
	return nil
}
