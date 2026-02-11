package utils

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/shreyam1008/dbterm/config"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// ConnectDB opens a database connection, pings it, and sets sensible defaults.
func ConnectDB(cfg *config.ConnectionConfig) (*sql.DB, error) {
	driver := cfg.DriverName()
	connStr := cfg.BuildConnString()

	if driver == "" || connStr == "" {
		return nil, fmt.Errorf("unsupported database type: %q — supported: postgresql, mysql, sqlite", cfg.Type)
	}

	db, err := sql.Open(driver, connStr)
	if err != nil {
		return nil, fmt.Errorf("could not open %s connection: %w", cfg.TypeLabel(), err)
	}

	// Connection pool defaults — keep it light for a TUI
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("could not reach %s at %s: %w", cfg.TypeLabel(), cfg.Host, err)
	}

	return db, nil
}

// ListTablesQuery returns the appropriate SQL to list user tables for a DB type
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
