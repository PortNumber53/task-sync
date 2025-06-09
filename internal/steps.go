package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

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
		if err != nil {
			return err
		}
		defer rows.Close()
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
		rows, err = db.Query(`SELECT id, task_id, title, status, created_at, updated_at FROM steps ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
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

// Step execution logic (from migrate.go)
type stepExec struct {
	StepID    int
	TaskID    int
	Settings  string
	LocalPath string
}

func executePendingSteps() {
	pgURL, err := getPgURLFromEnv()
	if err != nil {
		stepLogger.Println("DB config error:", err)
		return
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		stepLogger.Println("DB open error:", err)
		return
	}
	defer db.Close()

	query := `SELECT s.id, s.task_id, s.settings, t.local_path FROM steps s JOIN tasks t ON s.task_id = t.id WHERE s.status = 'new' AND t.status = 'active' AND t.local_path IS NOT NULL AND t.local_path <> ''`
	rows, err := db.Query(query)
	if err != nil {
		stepLogger.Println("Query error:", err)
		return
	}
	defer rows.Close()

	var steps []stepExec
	for rows.Next() {
		var s stepExec
		if err := rows.Scan(&s.StepID, &s.TaskID, &s.Settings, &s.LocalPath); err != nil {
			stepLogger.Println("Row scan error:", err)
			continue
		}
		steps = append(steps, s)
	}
	for _, step := range steps {
		var settings map[string]interface{}
		if err := json.Unmarshal([]byte(step.Settings), &settings); err != nil {
			storeStepResult(db, step.StepID, map[string]interface{}{"result":"failure","message":"invalid settings json"})
			stepLogger.Printf("Step %d: invalid settings json\n", step.StepID)
			continue
		}
		filePath, ok := settings["file_exists"].(string)
		if ok {
			absPath := filepath.Join(step.LocalPath, filePath)
			if _, err := os.Stat(absPath); err == nil {
				storeStepResult(db, step.StepID, map[string]interface{}{"result":"success"})
				stepLogger.Printf("Step %d: file_exists '%s' SUCCESS\n", step.StepID, absPath)
			} else {
				storeStepResult(db, step.StepID, map[string]interface{}{"result":"failure","message":err.Error()})
				stepLogger.Printf("Step %d: file_exists '%s' FAILURE: %s\n", step.StepID, absPath, err.Error())
			}
		}
	}
}

// CopyStep copies a step to a new task with the given ID
func CopyStep(stepID, toTaskID int) error {
	pgURL, err := getPgURLFromEnv()
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return err
	}
	defer db.Close()

	// Start a transaction
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("error starting transaction: %w", err)
	}
	defer tx.Rollback()

	// 1. Verify the target task exists
	var targetTaskExists bool
	err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1)", toTaskID).Scan(&targetTaskExists)
	if err != nil {
		return fmt.Errorf("error checking target task: %w", err)
	}
	if !targetTaskExists {
		return fmt.Errorf("target task with ID %d does not exist", toTaskID)
	}

	// 2. Get the source step data
	var title, status, settings string
	err = tx.QueryRow(
		"SELECT title, status, settings FROM steps WHERE id = $1",
		stepID,
	).Scan(&title, &status, &settings)

	if err == sql.ErrNoRows {
		return fmt.Errorf("source step with ID %d does not exist", stepID)
	}
	if err != nil {
		return fmt.Errorf("error fetching source step: %w", err)
	}

	// 3. Create the new step in the target task with the same status as source
	_, err = tx.Exec(
		`INSERT INTO steps (task_id, title, settings, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, now(), now())`,
		toTaskID, title, settings, status,
	)

	if err != nil {
		return fmt.Errorf("error creating new step: %w", err)
	}

	// 4. Commit the transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("error committing transaction: %w", err)
	}

	return nil
}

// storeStepResult stores the execution result of a step
func storeStepResult(db *sql.DB, stepID int, result map[string]interface{}) {
	resJson, _ := json.Marshal(result)
	_, err := db.Exec(`UPDATE steps SET results = $1::jsonb, updated_at = now() WHERE id = $2`, string(resJson), stepID)
	if err != nil {
		stepLogger.Println("Failed to update results for step", stepID, ":", err)
	}
}
