package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

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

// ListTasks prints all tasks in the DB
func ListTasks() error {
	pgURL, err := getPgURLFromEnv()
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, name, status, local_path, created_at, updated_at FROM tasks ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	fmt.Printf("%-4s %-20s %-10s %-30s %-25s %-25s\n", "ID", "Name", "Status", "Local Path", "Created At", "Updated At")
	for rows.Next() {
		var id int
		var name, status string
		var localPath sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&id, &name, &status, &localPath, &createdAt, &updatedAt); err != nil {
			return err
		}
		lp := ""
		if localPath.Valid {
			lp = localPath.String
		}
		fmt.Printf("%-4d %-20s %-10s %-30s %-25s %-25s\n", id, name, status, lp, createdAt, updatedAt)
	}
	return nil
}

// ListSteps prints all steps in the DB. If full is true, prints settings column too.
func ListSteps(full bool) error {
	pgURL, err := getPgURLFromEnv()
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return err
	}
	defer db.Close()
	var rows *sql.Rows
	if full {
		rows, err = db.Query(`SELECT id, task_id, title, status, settings, created_at, updated_at FROM steps ORDER BY id`)
	} else {
		rows, err = db.Query(`SELECT id, task_id, title, status, created_at, updated_at FROM steps ORDER BY id`)
	}
	if err != nil {
		return err
	}
	defer rows.Close()
	if full {
		fmt.Printf("%-4s %-7s %-20s %-10s %-30s %-25s %-25s\n", "ID", "TaskID", "Title", "Status", "Settings", "Created At", "Updated At")
		for rows.Next() {
			var id, taskID int
			var title, status, settings, createdAt, updatedAt string
			if err := rows.Scan(&id, &taskID, &title, &status, &settings, &createdAt, &updatedAt); err != nil {
				return err
			}
			fmt.Printf("%-4d %-7d %-20s %-10s %-30s %-25s %-25s\n", id, taskID, title, status, settings, createdAt, updatedAt)
		}
	} else {
		fmt.Printf("%-4s %-7s %-20s %-10s %-25s %-25s\n", "ID", "TaskID", "Title", "Status", "Created At", "Updated At")
		for rows.Next() {
			var id, taskID int
			var title, status, createdAt, updatedAt string
			if err := rows.Scan(&id, &taskID, &title, &status, &createdAt, &updatedAt); err != nil {
				return err
			}
			fmt.Printf("%-4d %-7d %-20s %-10s %-25s %-25s\n", id, taskID, title, status, createdAt, updatedAt)
		}
	}
	return nil
}



// CreateStep inserts a new step for a task. taskRef can be the task id or name. Settings must be a valid JSON string.
func CreateStep(taskRef, title, settings string) error {
	pgURL, err := getPgURLFromEnv()
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return err
	}
	defer db.Close()

	// Try to parse settings as JSON
	var js interface{}
	if err := json.Unmarshal([]byte(settings), &js); err != nil {
		return fmt.Errorf("settings must be valid JSON: %w", err)
	}

	// Find task_id
	var taskID int
	if id, err := strconv.Atoi(taskRef); err == nil {
		err = db.QueryRow("SELECT id FROM tasks WHERE id = $1", id).Scan(&taskID)
		if err != nil {
			return fmt.Errorf("no task found with id %d", id)
		}
	} else {
		err = db.QueryRow("SELECT id FROM tasks WHERE name = $1", taskRef).Scan(&taskID)
		if err != nil {
			return fmt.Errorf("no task found with name '%s'", taskRef)
		}
	}

	_, err = db.Exec(`INSERT INTO steps (task_id, title, status, settings, created_at, updated_at) VALUES ($1, $2, 'new', $3::jsonb, now(), now())`, taskID, title, settings)
	return err
}

// CreateTask inserts a new task with name and status. Only allows status: active, inactive, disabled, running.
func CreateTask(name, status string) error {
	valid := map[string]bool{"active": true, "inactive": true, "disabled": true, "running": true}
	if !valid[status] {
		return fmt.Errorf("invalid status: %s (must be one of active|inactive|disabled|running)", status)
	}
	pgURL, err := getPgURLFromEnv()
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO tasks (name, status, created_at, updated_at) VALUES ($1, $2, now(), now())`, name, status)
	return err
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
