package changeprofiler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/shreyam1008/dbterm/internal/config"
)

// CaptureMode identifies whether a checkpoint reads full snapshots or records
// changes as writes commit. A journal requires explicit source-object consent.
type CaptureMode string

const (
	CaptureSnapshot CaptureMode = "snapshot"
	CaptureJournal  CaptureMode = "journal"
)

// JournalSpec is saved locally before reading the source log. Engine state
// contains object identities and continuity evidence, never credentials.
type JournalSpec struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Engine  config.DBType   `json:"engine"`
	Plans   []TablePlan     `json:"plans"`
	State   json.RawMessage `json:"state"`
	Frozen  bool            `json:"frozen"`
}

// JournalEvent contains lossless encodedRow images from one committed write.
// SQLite conflict probes provide initial before-evidence and a final point
// lookup image; the reducer applies their final images after ordinary events.
type JournalEvent struct {
	Sequence int64
	Table    string
	Before   []byte
	After    []byte
	Probe    bool
}

type journalRecorderKey struct{}

// Adapters record their planned object identities before creating source
// objects, so interrupted installation can be recovered after process restart.
func persistJournalInstall(ctx context.Context, spec JournalSpec) error {
	if record, ok := ctx.Value(journalRecorderKey{}).(func(JournalSpec) error); ok {
		return record(spec)
	}
	return nil
}

func SupportsJournal(engine config.DBType) bool {
	switch engine {
	case config.SQLite, config.Turso, config.PostgreSQL, config.MySQL:
		return true
	default:
		return false
	}
}

// ReleaseJournalConnections releases live metadata guards before a connection
// is closed. Durable source journals remain owned and visible for recovery;
// losing a required guard never silently produces a complete report.
func ReleaseJournalConnections(ctx context.Context, source *sql.DB) {
	releaseMySQLJournalGuards(ctx, source)
	releasePostgresJournalGuards(ctx, source)
}

func installJournal(ctx context.Context, source *sql.DB, id string, engine config.DBType, plans []TablePlan) (JournalSpec, error) {
	var spec JournalSpec
	var err error
	switch engine {
	case config.SQLite, config.Turso:
		spec, err = installSQLiteJournal(ctx, source, id, plans)
	case config.PostgreSQL:
		spec, err = installPostgresJournal(ctx, source, id, plans)
	case config.MySQL:
		spec, err = installMySQLJournal(ctx, source, id, plans)
	default:
		err = fmt.Errorf("incremental journals are not supported by %s; choose snapshot comparison explicitly", engine)
	}
	if err == nil {
		spec.Engine = engine
	}
	return spec, err
}

func freezeJournal(ctx context.Context, source *sql.DB, spec JournalSpec) (JournalSpec, error) {
	switch spec.Engine {
	case config.SQLite, config.Turso:
		return freezeSQLiteJournal(ctx, source, spec)
	case config.PostgreSQL:
		return freezePostgresJournal(ctx, source, spec)
	case config.MySQL:
		return freezeMySQLJournal(ctx, source, spec)
	default:
		return spec, fmt.Errorf("unknown journal engine %s", spec.Engine)
	}
}

func readJournal(ctx context.Context, source *sql.DB, spec JournalSpec, emit func(JournalEvent) error) error {
	switch spec.Engine {
	case config.SQLite, config.Turso:
		return readSQLiteJournal(ctx, source, spec, emit)
	case config.PostgreSQL:
		return readPostgresJournal(ctx, source, spec, emit)
	case config.MySQL:
		return readMySQLJournal(ctx, source, spec, emit)
	default:
		return fmt.Errorf("unknown journal engine %s", spec.Engine)
	}
}

func cleanupJournal(ctx context.Context, source *sql.DB, spec JournalSpec) error {
	switch spec.Engine {
	case config.SQLite, config.Turso:
		return cleanupSQLiteJournal(ctx, source, spec)
	case config.PostgreSQL:
		return cleanupPostgresJournal(ctx, source, spec)
	case config.MySQL:
		return cleanupMySQLJournal(ctx, source, spec)
	default:
		return fmt.Errorf("unknown journal engine %s", spec.Engine)
	}
}
