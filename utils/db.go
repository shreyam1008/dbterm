package utils

import (
	"database/sql"
	"fmt"

	"github.com/shreyam1008/dbterm/config"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// ConnectDB opens a connection using the given config and verifies with a ping.
func ConnectDB(cfg *config.ConnectionConfig) (*sql.DB, error) {
	driver := cfg.DriverName()
	connStr := cfg.BuildConnString()

	if driver == "" || connStr == "" {
		return nil, fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	db, err := sql.Open(driver, connStr)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		return nil, err
	}

	return db, nil
}

// ListTablesQuery returns the appropriate SQL query to list tables for each DB type
func ListTablesQuery(dbType config.DBType) string {
	switch dbType {
	case config.PostgreSQL:
		return `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' ORDER BY table_name`
	case config.MySQL:
		return `SHOW TABLES`
	case config.SQLite:
		return `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`
	default:
		return ""
	}
}
