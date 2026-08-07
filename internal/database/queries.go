package database

import (
	"github.com/shreyam1008/dbterm/internal/config"
)

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
		return `SELECT table_name
FROM information_schema.tables
WHERE table_type = 'BASE TABLE'
  AND table_schema = DATABASE()
ORDER BY table_name`
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
			return `SELECT table_name
FROM information_schema.views
WHERE table_schema = DATABASE()
ORDER BY table_name`
		case ObjFunctions:
			return `SELECT routine_name
FROM information_schema.routines
WHERE routine_schema = DATABASE()
  AND routine_type = 'FUNCTION'
ORDER BY routine_name`
		case ObjTriggers:
			return `SELECT trigger_name
FROM information_schema.triggers
WHERE trigger_schema = DATABASE()
ORDER BY trigger_name`
		case ObjStoredProcedures:
			return `SELECT routine_name
FROM information_schema.routines
WHERE routine_schema = DATABASE()
  AND routine_type = 'PROCEDURE'
ORDER BY routine_name`
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
		return `SELECT datname
FROM pg_database
WHERE datistemplate = false
  AND has_database_privilege(datname, 'CONNECT')
ORDER BY datname`
	case config.MySQL:
		return `SELECT schema_name
FROM information_schema.schemata
WHERE schema_name NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
ORDER BY schema_name`
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
