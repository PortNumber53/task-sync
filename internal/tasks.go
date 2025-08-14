package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// isValidTaskStatus checks allowed task statuses
func isValidTaskStatus(status string) bool {
	valid := map[string]bool{"active": true, "inactive": true, "disabled": true, "running": true}
	return valid[status]
}

// ResetTaskContainers clears the containers and assigned_containers fields in a task's settings JSON
func ResetTaskContainers(db *sql.DB, taskID int) error {
	// Fetch current settings
	var currentSettingsJSON sql.NullString
	if err := db.QueryRow("SELECT settings FROM tasks WHERE id = $1", taskID).Scan(&currentSettingsJSON); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("task with ID %d not found", taskID)
		}
		return fmt.Errorf("failed to fetch settings: %w", err)
	}

	// Unmarshal, update, marshal
	settings := make(map[string]interface{})
	if currentSettingsJSON.Valid && strings.TrimSpace(currentSettingsJSON.String) != "" {
		if err := json.Unmarshal([]byte(currentSettingsJSON.String), &settings); err != nil {
			return fmt.Errorf("failed to unmarshal settings: %w", err)
		}
	}

	// Set legacy containers to empty array
	settings["containers"] = []interface{}{}
	// Set assigned_containers to empty object
	settings["assigned_containers"] = map[string]interface{}{}
	// Set new containers_map to empty object
	settings["containers_map"] = map[string]interface{}{}
	// Clear docker_run_parameters array
	settings["docker_run_parameters"] = []interface{}{}

	updatedSettingsJSON, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("failed to marshal updated settings: %w", err)
	}

	// Update DB
	_, err = db.Exec(`UPDATE tasks SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettingsJSON), taskID)
	if err != nil {
		return fmt.Errorf("failed to update task settings: %w", err)
	}
	return nil
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
	Settings  sql.NullString
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
	err = db.QueryRow(`SELECT id, name, status, local_path, created_at, updated_at, settings FROM tasks WHERE id = $1`, taskID).Scan(
		&t.ID, &t.Name, &t.Status, &localPath, &t.CreatedAt, &t.UpdatedAt, &t.Settings,
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
// Allowed fields to update are "name", "status", "localpath", "image_tag", and "image_hash".
func EditTask(db *sql.DB, taskID int, updates map[string]string) error {
	if len(updates) == 0 {
		return fmt.Errorf("no updates provided")
	}

	allowedFields := map[string]bool{
		"name":        true,
		"status":      true,
		"localpath":   true, // legacy/compat
		"local_path":  true, // preferred, matches DB and CLI
		"image_tag":   true,
		"image_hash":  true,
		"app_folder":  true, // allow editing app folder in settings
	}

	// Fetch and update settings JSON for image_tag and image_hash
	var currentSettingsJSON sql.NullString
	err := db.QueryRow("SELECT settings FROM tasks WHERE id = $1", taskID).Scan(&currentSettingsJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("task with ID %d not found", taskID)
		}
		return fmt.Errorf("failed to fetch settings: %w", err)
	}

	var taskSettings map[string]interface{}
	if currentSettingsJSON.Valid {
		if err := json.Unmarshal([]byte(currentSettingsJSON.String), &taskSettings); err != nil {
			return fmt.Errorf("failed to unmarshal settings: %w", err)
		}
	} else {
		taskSettings = make(map[string]interface{})
	}

	var setClauses []string
	var args []interface{}
	argCounter := 1

	for key, value := range updates {
		if !allowedFields[key] {
			return fmt.Errorf("invalid field: %s", key)
		}

		fieldName := key
		if key == "local_path" {
			fieldName = "local_path"
		} else if key == "localpath" {
			fieldName = "local_path"
		}

		switch key {
		case "name", "status", "localpath", "local_path":
			// Handle direct fields as before
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", fieldName, argCounter))
			args = append(args, value)
			argCounter++
		case "image_tag", "image_hash":
			if taskSettings["docker"] == nil {
				taskSettings["docker"] = make(map[string]interface{})
			}
			dockerMap, ok := taskSettings["docker"].(map[string]interface{})
			if !ok {
				dockerMap = make(map[string]interface{})
			}
			dockerMap[key] = value
			taskSettings["docker"] = dockerMap
		case "app_folder":
			// Store app_folder at the root of settings
			taskSettings["app_folder"] = value
		}
	}

	// Marshal updated settings
	updatedSettingsJSON, err := json.Marshal(taskSettings)
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}
	setClauses = append(setClauses, fmt.Sprintf("settings = $%d", argCounter))
	args = append(args, string(updatedSettingsJSON))
	argCounter++

	// Add updated_at and WHERE clause
	setClauses = append(setClauses, "updated_at = now()")
	query := fmt.Sprintf("UPDATE tasks SET %s WHERE id = $%d", strings.Join(setClauses, ", "), argCounter)
	args = append(args, taskID)

	// Execute update in transaction
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	res, err := tx.Exec(query, args...)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to update task: %w", err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		tx.Rollback()
		return fmt.Errorf("task not found or no changes made")
	}

	return tx.Commit()
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

// GetTaskID retrieves a task ID from a string, which can be either an ID or a name.
func GetTaskID(db *sql.DB, taskRef string) (int, error) {
	taskID, err := strconv.Atoi(taskRef)
	if err == nil {
		// It's a numeric ID, let's verify it exists
		var exists bool
		err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1)", taskID).Scan(&exists)
		if err != nil {
			return 0, fmt.Errorf("failed to verify task ID: %w", err)
		}
		if !exists {
			return 0, fmt.Errorf("no task found with ID %d", taskID)
		}
		return taskID, nil
	}

	// It's not a numeric ID, so treat it as a name
	err = db.QueryRow("SELECT id FROM tasks WHERE name = $1", taskRef).Scan(&taskID)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("no task found with name %q", taskRef)
		}
		return 0, fmt.Errorf("failed to find task by name: %w", err)
	}
	return taskID, nil
}
