package mcpserver

import (
	"database/sql"
	"io"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

const (
	defaultMaxRows        = 50
	maximumMaxRows        = 200
	defaultQueryTimeout   = 8 * time.Second
	maximumQueryTimeout   = 30 * time.Second
	defaultMaxQueryBytes  = 32 * 1024
	defaultMaxCellBytes   = 4 * 1024
	defaultMaxOutputBytes = 256 * 1024
	defaultMaxColumns     = 128
	defaultMaxTables      = 200
)

// Options controls the local MCP server. Zero values select conservative
// defaults. StoreLoader and Connector exist so the safety boundary can be
// tested without using a person's real profile.
type Options struct {
	Version            string
	ConnectionScope    string
	AllowProfileWrites bool
	MaxRows            int
	QueryTimeout       time.Duration
	AuditWriter        io.Writer
	StoreLoader        func() (*config.Store, error)
	Connector          func(*config.ConnectionConfig) (*sql.DB, error)
}

type limits struct {
	maxRows        int
	queryTimeout   time.Duration
	maxQueryBytes  int
	maxCellBytes   int
	maxOutputBytes int
	maxColumns     int
	maxTables      int
}

type connectionSummary struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Type     config.DBType `json:"type"`
	Database string        `json:"database,omitempty"`
	Endpoint string        `json:"endpoint,omitempty"`
	ReadOnly bool          `json:"read_only"`
	Active   bool          `json:"active"`
	LastUsed string        `json:"last_used,omitempty"`
}

type listConnectionsInput struct{}

type listConnectionsOutput struct {
	Scope       string              `json:"scope"`
	Connections []connectionSummary `json:"connections"`
}

type inspectDatabaseInput struct {
	ConnectionID string `json:"connection_id,omitempty" jsonschema:"saved dbterm connection ID; omit only when the configured scope resolves to one active connection"`
}

type inspectDatabaseOutput struct {
	Connection connectionSummary `json:"connection"`
	Schemas    []string          `json:"schemas"`
	Tables     []string          `json:"tables"`
	Truncated  bool              `json:"truncated"`
}

type inspectTableInput struct {
	ConnectionID string `json:"connection_id,omitempty" jsonschema:"saved dbterm connection ID"`
	Table        string `json:"table" jsonschema:"table name, optionally schema-qualified"`
}

type columnInfo struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Nullable   bool   `json:"nullable"`
	Default    string `json:"default,omitempty"`
	PrimaryKey bool   `json:"primary_key,omitempty"`
}

type foreignKeyColumn struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type foreignKeyInfo struct {
	Name        string             `json:"name"`
	SourceTable string             `json:"source_table"`
	TargetTable string             `json:"target_table"`
	Columns     []foreignKeyColumn `json:"columns"`
}

type inspectTableOutput struct {
	Table       string           `json:"table"`
	Columns     []columnInfo     `json:"columns"`
	ForeignKeys []foreignKeyInfo `json:"foreign_keys"`
	Truncated   bool             `json:"truncated"`
}

type readQueryInput struct {
	ConnectionID string `json:"connection_id,omitempty" jsonschema:"saved dbterm connection ID"`
	SQL          string `json:"sql" jsonschema:"one read-only SQL statement"`
	MaxRows      int    `json:"max_rows,omitempty" jsonschema:"maximum rows to return; server-enforced upper bound is 200"`
}

type queryOutput struct {
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	RowCount  int              `json:"row_count"`
	Truncated bool             `json:"truncated"`
	AuditID   string           `json:"audit_id"`
}

type explainQueryInput struct {
	ConnectionID string `json:"connection_id,omitempty" jsonschema:"saved dbterm connection ID"`
	SQL          string `json:"sql" jsonschema:"one SELECT or WITH SELECT statement to validate and explain without ANALYZE"`
}

type explainQueryOutput struct {
	Valid   bool        `json:"valid"`
	Message string      `json:"message"`
	Plan    queryOutput `json:"plan"`
}

type followRecordInput struct {
	ConnectionID string         `json:"connection_id,omitempty" jsonschema:"saved dbterm connection ID"`
	Table        string         `json:"table" jsonschema:"source table, optionally schema-qualified"`
	Key          map[string]any `json:"key" jsonschema:"one or more exact-match column values identifying the source record"`
	RowsPerLink  int            `json:"rows_per_link,omitempty" jsonschema:"maximum rows per relationship, up to 20"`
}

type relatedRows struct {
	Direction  string           `json:"direction"`
	Constraint string           `json:"constraint"`
	Table      string           `json:"table"`
	Rows       []map[string]any `json:"rows"`
	Truncated  bool             `json:"truncated"`
}

type followRecordOutput struct {
	SourceTable string         `json:"source_table"`
	Source      map[string]any `json:"source"`
	Related     []relatedRows  `json:"related"`
	Truncated   bool           `json:"truncated"`
	AuditID     string         `json:"audit_id"`
}

type saveProfileInput struct {
	ID         string        `json:"id,omitempty" jsonschema:"existing connection ID to update; omit to create"`
	Name       string        `json:"name"`
	Type       config.DBType `json:"type" jsonschema:"postgresql, mysql, sqlite, turso, or d1"`
	Host       string        `json:"host,omitempty"`
	Port       string        `json:"port,omitempty"`
	User       string        `json:"user,omitempty"`
	Password   string        `json:"password,omitempty" jsonschema:"write-only password; never returned"`
	Database   string        `json:"database,omitempty"`
	ReadOnly   *bool         `json:"read_only,omitempty" jsonschema:"whether dbterm should treat this profile as read-only; defaults to true for new agent-created profiles"`
	FilePath   string        `json:"file_path,omitempty"`
	SSLMode    string        `json:"ssl_mode,omitempty"`
	AccountID  string        `json:"account_id,omitempty"`
	DatabaseID string        `json:"database_id,omitempty"`
	AuthToken  string        `json:"auth_token,omitempty" jsonschema:"write-only token; never returned"`
}

type saveProfileOutput struct {
	Created    bool              `json:"created"`
	Connection connectionSummary `json:"connection"`
	AuditID    string            `json:"audit_id"`
}
