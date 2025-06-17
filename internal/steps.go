package internal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// CreateStep inserts a new step for a task. taskRef can be the task id or name. Settings must be a valid JSON string.
func CreateStep(db *sql.DB, taskRef, title, settings string) error {
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

	_, err := db.Exec(`INSERT INTO steps (task_id, title, status, settings, created_at, updated_at) VALUES ($1, $2, 'new', $3::jsonb, now(), now())`, taskID, title, settings)
	return err
}

// ActivateStep sets the status of a step to 'active'
func ActivateStep(db *sql.DB, stepID int) error {
	result, err := db.Exec(`UPDATE steps SET status = 'active', updated_at = NOW() WHERE id = $1`, stepID)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return fmt.Errorf("no step found with ID %d", stepID)
	}
	return nil
}

// ListSteps prints all steps in the DB. If full is true, prints settings column too.
func ListSteps(db *sql.DB, full bool) error {
	var rows *sql.Rows
	var err error
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

// DockerBuildConfig represents the configuration for a docker build step
type DockerBuildConfig struct {
	DockerBuild struct {
		DependsOn []struct {
			ID int `json:"id"`
		} `json:"depends_on"`
		Files    []string          `json:"files"`
		Hashes   map[string]string `json:"hashes"`
		Shell    []string          `json:"shell"`
		ImageID  string            `json:"image_id"`
		ImageTag string            `json:"image_tag"`
	} `json:"docker_build"`
}

// DockerRubricsConfig represents the configuration for a docker rubrics step
type DockerRubricsConfig struct {
	DockerRubrics struct {
		DependsOn []struct {
			ID int `json:"id"`
		} `json:"depends_on"`
		Files    []string          `json:"files"`
		Hashes   map[string]string `json:"hashes"`
		ImageID  string            `json:"image_id"`
		ImageTag string            `json:"image_tag"`
	} `json:"docker_rubrics"`
}

// calculateFileHash calculates the SHA256 hash of a file
func calculateFileHash(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// checkDependencies verifies if all dependent steps have completed successfully
func checkDependencies(db *sql.DB, stepID int, dependsOn []struct {
	ID int `json:"id"`
}) (bool, error) {
	if len(dependsOn) == 0 {
		return true, nil
	}

	// Log the dependencies we're checking
	depIDs := make([]int, len(dependsOn))
	for i, dep := range dependsOn {
		depIDs[i] = dep.ID
	}
	stepLogger.Printf("Step %d: checking dependencies: %v\n", stepID, depIDs)

	placeholders := make([]string, len(dependsOn))
	args := make([]interface{}, len(dependsOn)+1)
	args[0] = stepID

	for i, dep := range dependsOn {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args[i+1] = dep.ID
	}

	// First, let's check the status of each dependency directly
	for _, dep := range dependsOn {
		var status string
		var results sql.NullString
		err := db.QueryRow("SELECT status, results FROM steps WHERE id = $1", dep.ID).Scan(&status, &results)
		if err != nil {
			stepLogger.Printf("Step %d: error checking status of dependency %d: %v\n", stepID, dep.ID, err)
			continue
		}
		stepLogger.Printf("Step %d: dependency %d - status: %s, results: %v\n", stepID, dep.ID, status, results.String)
	}

	// We need to find if there are any dependencies that are NOT successful
	// A dependency is successful if:
	// 1. status is 'success' OR
	// 2. results->>'result' is 'success'
	query := fmt.Sprintf(`
		SELECT NOT EXISTS (
			SELECT 1 FROM steps
			WHERE id IN (%s)
			AND id != $1
			AND status != 'success'
			AND (results IS NULL OR results->>'result' IS NULL OR results->>'result' != 'success')
		)`,
		strings.Join(placeholders, ","))

	stepLogger.Printf("Step %d: running dependency check query: %s with args %v\n", stepID, query, args)

	var allDepsCompleted bool
	err := db.QueryRow(query, args...).Scan(&allDepsCompleted)
	stepLogger.Printf("Step %d: dependency check result: %v, error: %v\n", stepID, allDepsCompleted, err)

	return allDepsCompleted, err
}



type stepExec struct {
	StepID    int
	TaskID    int
	Title     string
	Settings  string
	LocalPath string
}

type StepInfo struct {
	ID         int                    `json:"id"`
	TaskID     int                    `json:"task_id"`
	Title      string                 `json:"title"`
	Status     string                 `json:"status"`
	Settings   map[string]interface{} `json:"settings"`
	Results    map[string]interface{} `json:"results,omitempty"`
	CreatedAt  time.Time              `json:"created_at"`
	UpdatedAt  time.Time              `json:"updated_at"`
	RawResults *string                `json:"-"` // Raw JSON string from the database
}

var (
	processFileExistsStepsFunc    = processFileExistsSteps
	processDockerBuildStepsFunc   = processDockerBuildSteps
	processDockerRubricsStepsFunc = processDockerRubricsSteps
)

func executePendingSteps(db *sql.DB) error {
	// Process steps in order of dependencies
	processFileExistsStepsFunc(db)    // First, check file existence
	processDockerBuildStepsFunc(db)   // Then build Docker images
	processDockerRubricsStepsFunc(db) // Finally, run Docker rubrics
	return nil
}


// CopyStep copies a step to a new task with the given ID
// GetStepInfo retrieves detailed information about a specific step by ID
func GetStepInfo(db *sql.DB, stepID int) (*StepInfo, error) {
	var info StepInfo
	var settingsStr, resultsStr sql.NullString
	var createdAt, updatedAt time.Time

	err := db.QueryRow(`
		SELECT
			s.id, s.task_id, s.title, s.status,
			s.settings::text, s.results::text,
			s.created_at, s.updated_at
		FROM steps s
		WHERE s.id = $1
	`, stepID).Scan(
		&info.ID, &info.TaskID, &info.Title, &info.Status,
		&settingsStr, &resultsStr,
		&createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no step found with ID %d", stepID)
	}
	if err != nil {
		return nil, err
	}

	// Only parse settings if they exist and are not null
	if settingsStr.Valid && settingsStr.String != "" && settingsStr.String != "null" {
		info.Settings = make(map[string]interface{})
		decoder := json.NewDecoder(strings.NewReader(settingsStr.String))
		decoder.UseNumber()
		if err := decoder.Decode(&info.Settings); err != nil {
			return nil, fmt.Errorf("error parsing settings: %w", err)
		}
	}

	// Store raw results
	if resultsStr.Valid {
		info.RawResults = &resultsStr.String
	}

	// Only parse results if they exist and are not null
	if resultsStr.Valid && resultsStr.String != "" && resultsStr.String != "null" {
		info.Results = make(map[string]interface{})
		decoder := json.NewDecoder(strings.NewReader(resultsStr.String))
		decoder.UseNumber()
		if err := decoder.Decode(&info.Results); err != nil {
			return nil, fmt.Errorf("error parsing results: %w", err)
		}
	}

	info.CreatedAt = createdAt
	info.UpdatedAt = updatedAt

	return &info, nil
}

func CopyStep(db *sql.DB, stepID, toTaskID int) error {

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

// ClearStepResults clears the results for a step
func ClearStepResults(db *sql.DB, stepID int) error {
	result, err := db.Exec(
		"UPDATE steps SET results = NULL, updated_at = NOW() WHERE id = $1",
		stepID,
	)
	if err != nil {
		return fmt.Errorf("error clearing step results: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("error checking affected rows: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("no step found with ID %d", stepID)
	}
	return nil
}


