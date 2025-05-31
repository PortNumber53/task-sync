package internal

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/lib/pq"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/joho/godotenv"
)

// RunMigrate runs DB migrations up or down
func getPgURLFromEnv() (string, error) {
	_ = godotenv.Load()
	host := os.Getenv("DB_HOST")
	port := os.Getenv("DB_PORT")
	user := os.Getenv("DB_USER")
	password := os.Getenv("DB_PASSWORD")
	dbname := os.Getenv("DB_NAME")
	ssl := os.Getenv("DB_SSL")
	if host == "" || port == "" || user == "" || password == "" || dbname == "" {
		return "", fmt.Errorf("missing one or more required DB environment variables")
	}
	sslmode := "disable"
	if ssl == "true" || ssl == "1" {
		sslmode = "require"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s", user, password, host, port, dbname, sslmode), nil
}

func RunMigrate(direction string) error {
	pgURL, err := getPgURLFromEnv()
	if err != nil {
		return err
	}
	m, err := migrate.New(
		"file://./migrations",
		pgURL,
	)
	if err != nil {
		return fmt.Errorf("failed to init migrate: %w", err)
	}
	defer m.Close()

	switch direction {
	case "up":
		err = m.Up()
	case "down":
		err = m.Down()
	default:
		return fmt.Errorf("unknown migration direction: %s", direction)
	}
	if err != nil && err != migrate.ErrNoChange {
		return err
	}
	fmt.Println("Migration", direction, "completed.")
	return nil
}

// RunMigrateReset resets the database by dropping everything and rerunning all migrations
func RunMigrateReset() error {
	pgURL, err := getPgURLFromEnv()
	if err != nil {
		return err
	}
	m, err := migrate.New(
		"file://./migrations",
		pgURL,
	)
	if err != nil {
		return fmt.Errorf("failed to init migrate: %w", err)
	}
	defer m.Close()

	if err := m.Drop(); err != nil {
		return fmt.Errorf("failed to drop database objects: %w", err)
	}
	fmt.Println("All tables dropped. Re-applying migrations...")
	err = m.Up()
	if err != nil && err.Error() != "no change" {
		if err.Error() == "first .: file does not exist" || err.Error() == "no migration files found" {
			fmt.Println("No migration files found in ./migrations. Please create at least one migration file (e.g., 0001_init.up.sql) before running 'migrate reset'.")
			return nil
		}
		return fmt.Errorf("failed to re-apply migrations: %w", err)
	}
	fmt.Println("Database reset and all migrations reapplied.")
	return nil
}

// RunMigrateStatus prints the status of migrations using golang-migrate's schema_migrations table
func RunMigrateStatus() error {
	pgURL, err := getPgURLFromEnv()
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return fmt.Errorf("failed to connect to db: %w", err)
	}
	defer db.Close()

	var version int64
	var dirty bool
	err = db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty)
	if err != nil {
		return fmt.Errorf("failed to query schema_migrations: %w", err)
	}
	fmt.Printf("Current migration version: %d\n", version)
	fmt.Printf("Dirty state: %v\n", dirty)
	return nil
}
