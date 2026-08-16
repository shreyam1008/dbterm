package changeprofiler

import (
	"context"
	"database/sql"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

type Status string

const (
	StatusCapturing Status = "capturing"
	StatusActive    Status = "active"
	StatusComparing Status = "comparing"
	StatusComplete  Status = "complete"
	StatusFailed    Status = "failed"
)

type Consistency string

const (
	ConsistencySnapshot   Consistency = "consistent snapshot"
	ConsistencyBestEffort Consistency = "best effort, table by table"
)

type DiffKind string

const (
	DiffInserted DiffKind = "inserted"
	DiffUpdated  DiffKind = "updated"
	DiffDeleted  DiffKind = "deleted"
)

type KeyKind string

const (
	KeyPrimary KeyKind = "primary"
	KeyUnique  KeyKind = "unique"
	KeyRowID   KeyKind = "rowid"
	KeyFullRow KeyKind = "full_row"
)

type Risk string

const (
	RiskKeyless     Risk = "no stable key; updates appear as removed plus added"
	RiskLargeRows   Risk = "large estimated row count"
	RiskLargeBytes  Risk = "large estimated data size"
	RiskLargeDB     Risk = "large SQLite database; per-table size is unknown"
	RiskUnknownSize Risk = "remote table size is unknown"
)

type Column struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Nullable   bool   `json:"nullable"`
	Default    string `json:"default,omitempty"`
	PrimaryPos int    `json:"primary_pos,omitempty"`
}

type TablePlan struct {
	Name           string   `json:"name"`
	Columns        []Column `json:"columns"`
	KeyColumns     []string `json:"key_columns,omitempty"`
	KeyKind        KeyKind  `json:"key_kind"`
	EstimatedRows  int64    `json:"estimated_rows,omitempty"`
	EstimatedBytes int64    `json:"estimated_bytes,omitempty"`
	Risks          []Risk   `json:"risks,omitempty"`
	Included       bool     `json:"included"`
}

type Anchor struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	ConnectionKey   string        `json:"connection_key"`
	ConnectionLabel string        `json:"connection_label"`
	Engine          config.DBType `json:"engine"`
	TargetLabel     string        `json:"target_label"`
	Status          Status        `json:"status"`
	Consistency     Consistency   `json:"consistency"`
	StartedAt       time.Time     `json:"started_at"`
	LastScannedAt   time.Time     `json:"last_scanned_at,omitempty"`
	FinishedAt      time.Time     `json:"finished_at,omitempty"`
	Inserted        int64         `json:"inserted"`
	Updated         int64         `json:"updated"`
	Deleted         int64         `json:"deleted"`
	SchemaChanges   int64         `json:"schema_changes"`
	Error           string        `json:"error,omitempty"`
}

type TableSummary struct {
	Name          string
	Inserted      int64
	Updated       int64
	Deleted       int64
	SchemaChanged bool
	KeyKind       KeyKind
	KeyColumns    []string
	Risk          string
}

type Value struct {
	Kind string
	Type string
	Text string
	Null bool
}

type DiffRow struct {
	Table          string
	Kind           DiffKind
	Key            string
	Before         map[string]Value
	After          map[string]Value
	ChangedColumns []string
}

type Activity struct {
	OccurredAt      time.Time
	ConnectionLabel string
	SQL             string
	RowsAffected    int64
}

type Progress struct {
	Phase         string
	Table         string
	TableIndex    int
	TableCount    int
	Rows          int64
	EstimatedRows int64
	Bytes         int64
	Percent       int
	Approximate   bool
	Complete      bool
}

type ProgressFunc func(Progress)

type StartRequest struct {
	Name            string
	ConnectionKey   string
	ConnectionLabel string
	TargetLabel     string
	Engine          config.DBType
	Tables          []TablePlan
}

// Queryer is implemented by sql.DB and sql.Tx.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
