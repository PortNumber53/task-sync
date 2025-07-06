package models

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/lib/pq"
)

// GetPgURLFromEnv builds a PostgreSQL connection string from environment variables.
func GetPgURLFromEnv() (string, error) {
	host := os.Getenv("PGHOST")
	port := os.Getenv("PGPORT")
	user := os.Getenv("PGUSER")
	pass := os.Getenv("PGPASSWORD")
	db := os.Getenv("PGDATABASE")
	ssl := os.Getenv("PGSSLMODE")
	if ssl == "" {
		ssl = "disable"
	}
	if host == "" || port == "" || user == "" || db == "" {
		return "", fmt.Errorf("missing required PG* env vars (PGHOST, PGPORT, PGUSER, PGDATABASE)")
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s", user, pass, host, port, db, ssl), nil
}

// OpenDB opens a PostgreSQL database using the given URL.
func OpenDB(pgURL string) (*sql.DB, error) {
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
