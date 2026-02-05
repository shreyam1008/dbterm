package utils

import (
	"database/sql"

	_ "github.com/lib/pq"
)

// ConnectDB opens a connection to the database and verifies it with a ping.
func ConnectDB(connStr string) (*sql.DB, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		return nil, err
	}

	return db, nil
}
