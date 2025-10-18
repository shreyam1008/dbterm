package utils

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/shreyam1008/dbterm/config"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/peterheb/cfd1"
	_ "github.com/tursodatabase/libsql-client-go/libsql"
	_ "modernc.org/sqlite"
)

// ConnectDB opens a database connection, pings it, and sets sensible defaults.
func ConnectDB(cfg *config.ConnectionConfig) (*sql.DB, error) {
	driver := cfg.DriverName()
	connStr := cfg.BuildConnString()

	if driver == "" || connStr == "" {
		return nil, fmt.Errorf("unsupported database type: %q — supported: postgresql, mysql, sqlite, turso, d1", cfg.Type)
	}

	db, err := sql.Open(driver, connStr)
	if err != nil {
		return nil, fmt.Errorf("could not open %s connection: %w", cfg.TypeLabel(), err)
	}

	// Connection pool defaults — keep it light for a TUI
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(90 * time.Second)
	db.SetConnMaxLifetime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("could not reach %s at %s: %w", cfg.TypeLabel(), cfg.Host, err)
	}

	return db, nil
}

// ListTablesQuery returns the appropriate SQL to list user tables for a DB type
func ListTablesQuery(dbType config.DBType) string {
	switch dbType {
	case config.PostgreSQL:
		return `SELECT table_schema || '.' || table_name AS table_name
FROM information_schema.tables
WHERE table_type = 'BASE TABLE'
  AND table_schema NOT IN ('pg_catalog', 'information_schema')
ORDER BY table_schema, table_name`
	case config.MySQL:
		return `SELECT CONCAT(table_schema, '.', table_name) AS table_name
FROM information_schema.tables
WHERE table_type = 'BASE TABLE'
  AND table_schema NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
ORDER BY table_schema, table_name`
	case config.SQLite, config.Turso, config.CloudflareD1:
		return `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`
	default:
		return ""
	}
}

// DBObjectType represents a category of database objects for introspection.
type DBObjectType string

const (
	ObjViews            DBObjectType = "Views"
	ObjFunctions        DBObjectType = "Functions"
	ObjTriggers         DBObjectType = "Triggers"
	ObjStoredProcedures DBObjectType = "Procedures"
	ObjExtensions       DBObjectType = "Extensions"
)

// ListObjectsQuery returns the SQL to list objects of a given type, or "" if unsupported.
func ListObjectsQuery(dbType config.DBType, objType DBObjectType) string {
	switch dbType {
	case config.PostgreSQL:
		switch objType {
		case ObjViews:
			return `SELECT table_schema || '.' || table_name AS name
FROM information_schema.views
WHERE table_schema NOT IN ('pg_catalog', 'information_schema')
ORDER BY table_schema, table_name`
		case ObjFunctions:
			return `SELECT routine_schema || '.' || routine_name AS name
FROM information_schema.routines
WHERE routine_schema NOT IN ('pg_catalog', 'information_schema')
  AND routine_type = 'FUNCTION'
ORDER BY routine_schema, routine_name`
		case ObjTriggers:
			return `SELECT trigger_schema || '.' || trigger_name AS name
FROM information_schema.triggers
WHERE trigger_schema NOT IN ('pg_catalog', 'information_schema')
ORDER BY trigger_schema, trigger_name`
		case ObjStoredProcedures:
			return `SELECT routine_schema || '.' || routine_name AS name
FROM information_schema.routines
WHERE routine_schema NOT IN ('pg_catalog', 'information_schema')
  AND routine_type = 'PROCEDURE'
ORDER BY routine_schema, routine_name`
		case ObjExtensions:
			return `SELECT extname FROM pg_extension WHERE extname != 'plpgsql' ORDER BY extname`
		}
	case config.MySQL:
		switch objType {
		case ObjViews:
			return `SELECT CONCAT(table_schema, '.', table_name) AS name
FROM information_schema.views
WHERE table_schema NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
ORDER BY table_schema, table_name`
		case ObjFunctions:
			return `SELECT CONCAT(routine_schema, '.', routine_name) AS name
FROM information_schema.routines
WHERE routine_schema NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
  AND routine_type = 'FUNCTION'
ORDER BY routine_schema, routine_name`
		case ObjTriggers:
			return `SELECT CONCAT(trigger_schema, '.', trigger_name) AS name
FROM information_schema.triggers
WHERE trigger_schema NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
ORDER BY trigger_schema, trigger_name`
		case ObjStoredProcedures:
			return `SELECT CONCAT(routine_schema, '.', routine_name) AS name
FROM information_schema.routines
WHERE routine_schema NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
  AND routine_type = 'PROCEDURE'
ORDER BY routine_schema, routine_name`
		}
	case config.SQLite, config.Turso, config.CloudflareD1:
		switch objType {
		case ObjViews:
			return `SELECT name FROM sqlite_master WHERE type='view' ORDER BY name`
		case ObjTriggers:
			return `SELECT name FROM sqlite_master WHERE type='trigger' ORDER BY name`
		}
	}
	return ""
}

// SupportedObjectTypes returns the object types supported for introspection by a DB type.
func SupportedObjectTypes(dbType config.DBType) []DBObjectType {
	switch dbType {
	case config.PostgreSQL:
		return []DBObjectType{ObjViews, ObjFunctions, ObjTriggers, ObjStoredProcedures, ObjExtensions}
	case config.MySQL:
		return []DBObjectType{ObjViews, ObjFunctions, ObjTriggers, ObjStoredProcedures}
	case config.SQLite, config.Turso, config.CloudflareD1:
		return []DBObjectType{ObjViews, ObjTriggers}
	default:
		return nil
	}
}

// ListDatabasesQuery returns the SQL to list databases in a server instance.
func ListDatabasesQuery(dbType config.DBType) string {
	switch dbType {
	case config.PostgreSQL:
		return `SELECT datname FROM pg_database WHERE datistemplate = false ORDER BY datname`
	case config.MySQL:
		return `SHOW DATABASES`
	default:
		return ""
	}
}

// ListDatabaseSizesQuery returns the SQL to list databases with their estimated sizes.
func ListDatabaseSizesQuery(dbType config.DBType) string {
	switch dbType {
	case config.PostgreSQL:
		return `SELECT datname, pg_database_size(datname) AS size FROM pg_database WHERE datistemplate = false ORDER BY datname`
	case config.MySQL:
		return `SELECT table_schema AS datname, SUM(data_length + index_length) AS size FROM information_schema.tables GROUP BY table_schema ORDER BY table_schema`
	default:
		return ""
	}
}
