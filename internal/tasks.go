package internal

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
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

// Task represents a task in the system
// LocalPath is optional and may be empty
// CreatedAt and UpdatedAt are ISO8601 strings
type Task struct {
	ID        int
	Name      string
	Status    string
	LocalPath *string
	CreatedAt string
	UpdatedAt string
}

// GetTaskInfo fetches a task by ID. Returns (*Task, error). If not found, error is returned.
func GetTaskInfo(taskID int) (*Task, error) {
	pgURL, err := GetPgURLFromEnv()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var t Task
	var localPath sql.NullString
	err = db.QueryRow(`SELECT id, name, status, local_path, created_at, updated_at FROM tasks WHERE id = $1`, taskID).Scan(
		&t.ID, &t.Name, &t.Status, &localPath, &t.CreatedAt, &t.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no task found with ID %d", taskID)
	}
	if err != nil {
		return nil, err
	}
	if localPath.Valid {
		t.LocalPath = &localPath.String
	} else {
		t.LocalPath = nil
	}
	return &t, nil
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

// EditTask updates specified fields of an existing task.
// Allowed fields to update are "name", "status", and "localpath".
func EditTask(db *sql.DB, taskID int, updates map[string]string) error {

	if len(updates) == 0 {
		return fmt.Errorf("no updates provided")
	}

	var setClauses []string
	var args []interface{}
	argCounter := 1

	allowedFields := map[string]bool{
		"name":      true,
		"status":    true,
		"localpath": true,
	}

	for key, value := range updates {
		if !allowedFields[key] {
			return fmt.Errorf("invalid field to update: %s", key)
		}

		switch key {
		case "name":
			setClauses = append(setClauses, fmt.Sprintf("name = $%d", argCounter))
			args = append(args, value)
			argCounter++
		case "status":
			if !isValidTaskStatus(value) {
				return fmt.Errorf("invalid status: %s (must be one of active|inactive|disabled|running)", value)
			}
			setClauses = append(setClauses, fmt.Sprintf("status = $%d", argCounter))
			args = append(args, value)
			argCounter++
		case "localpath":
			if value == "" {
				setClauses = append(setClauses, fmt.Sprintf("local_path = $%d", argCounter))
				args = append(args, nil) // Use sql.NullString or pass nil directly for NULL
			} else {
				absPath, err := filepath.Abs(value)
				if err != nil {
					return fmt.Errorf("invalid local path '%s': %v", value, err)
				}
				setClauses = append(setClauses, fmt.Sprintf("local_path = $%d", argCounter))
				args = append(args, absPath)
			}
			argCounter++
		}
	}

	if len(setClauses) == 0 {
		// Should not happen if initial len(updates) > 0 and keys are valid, but as a safeguard.
		return fmt.Errorf("no valid fields to update provided")
	}

	// Add updated_at to the SET clauses
	setClauses = append(setClauses, "updated_at = now()")

	query := fmt.Sprintf("UPDATE tasks SET %s WHERE id = $%d", strings.Join(setClauses, ", "), argCounter)
	args = append(args, taskID)

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	result, err := tx.Exec(query, args...)
	if err != nil {
		tx.Rollback() // Rollback on exec error
		return fmt.Errorf("failed to update task: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		tx.Rollback() // Rollback on error getting rows affected
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		tx.Rollback() // Rollback if no rows affected
		return fmt.Errorf("task with ID %d not found or no changes made", taskID)
	}

	if err := tx.Commit(); err != nil {
		// If commit fails, the transaction is effectively rolled back by the DB.
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
