package internal

import (
	"database/sql"
	"fmt"
	"path/filepath"
)

// CreateTask inserts a new task with name and status. Only allows status: active, inactive, disabled, running.
func isValidTaskStatus(status string) bool {
	valid := map[string]bool{"active": true, "inactive": true, "disabled": true, "running": true}
	return valid[status]
}

// CreateTask inserts a new task with name, status, and optional local path
// Status must be one of: active, inactive, disabled, running
// localPath is optional and can be an empty string
func CreateTask(name, status, localPath string) error {
	if !isValidTaskStatus(status) {
		return fmt.Errorf("invalid status: %s (must be one of active|inactive|disabled|running)", status)
	}
	pgURL, err := GetPgURLFromEnv()
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return err
	}
	defer db.Close()

	// If localPath is empty, set it to NULL in the database
	if localPath == "" {
		_, err = db.Exec(`
			INSERT INTO tasks (name, status, local_path, created_at, updated_at) 
			VALUES ($1, $2, NULL, now(), now())
		`, name, status)
	} else {
		// Convert to absolute path if it's not empty
		absPath, err := filepath.Abs(localPath)
		if err != nil {
			return fmt.Errorf("invalid local path: %v", err)
		}
		_, err = db.Exec(`
			INSERT INTO tasks (name, status, local_path, created_at, updated_at) 
			VALUES ($1, $2, $3, now(), now())
		`, name, status, absPath)
	}

	return err
}

// ListTasks prints all tasks in the DB
// DeleteTask deletes a task and all its associated steps by task ID
func DeleteTask(taskID int) error {
	pgURL, err := GetPgURLFromEnv()
	if err != nil {
		return fmt.Errorf("database configuration error: %w", err)
	}

	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return fmt.Errorf("database connection error: %w", err)
	}
	defer db.Close()

	// Start a transaction
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	// Delete the task (this will cascade to delete steps due to the foreign key constraint)
	result, err := tx.Exec(`DELETE FROM tasks WHERE id = $1`, taskID)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete task: %w", err)
	}

	// Check if any rows were affected
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to get affected rows: %w", err)
	}

	if rowsAffected == 0 {
		tx.Rollback()
		return fmt.Errorf("no task found with ID %d", taskID)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func ListTasks() error {
	pgURL, err := GetPgURLFromEnv()
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
