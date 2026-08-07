package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/shreyam1008/dbterm/internal/config"
	_ "github.com/tursodatabase/libsql-client-go/libsql"
	_ "modernc.org/sqlite"
)

// Connect opens a database connection, verifies it, and configures a small pool.
func Connect(cfg *config.ConnectionConfig) (*sql.DB, error) {
	driver := cfg.DriverName()
	connStr := cfg.BuildConnString()

	if driver == "" || connStr == "" {
		return nil, fmt.Errorf("unsupported database type: %q — supported: postgresql, mysql, sqlite, turso, d1", cfg.Type)
	}

	db, err := sql.Open(driver, connStr)
	if err != nil {
		return nil, fmt.Errorf("could not open %s connection: %w", cfg.TypeLabel(), err)
	}

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
