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

// DockerRunConfig represents the configuration for a docker run step
type DockerRunConfig struct {
	DockerRun struct {
		DependsOn           []struct {
			ID int `json:"id"`
		} `json:"depends_on"`
		DockerRunParameters []string `json:"docker_run_parameters"`
		ImageID             string   `json:"image_id"`
		ImageTag            string   `json:"image_tag"`
		ContainerID         string   `json:"container_id,omitempty"`
		ContainerName       string   `json:"container_name,omitempty"`
		ContainerHash       string   `json:"container_hash,omitempty"`
	} `json:"docker_run"`
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
	processDockerRunStepsFunc     = processDockerRunSteps
	processDockerRubricsStepsFunc = processDockerRubricsSteps
)

func executePendingSteps(db *sql.DB) error {
	// Process steps in order of dependencies
	processFileExistsStepsFunc(db)    // First, check file existence
	processDockerBuildStepsFunc(db)   // Then build Docker images
	processDockerRunStepsFunc(db)     // Then run general Docker commands
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

// RemoveStepSettingKey removes a top-level key from the step's settings JSON.
func RemoveStepSettingKey(db *sql.DB, stepID int, keyToRemove string) error {
	stepInfo, err := GetStepInfo(db, stepID)
	if err != nil {
		return fmt.Errorf("failed to get step info for step %d: %w", stepID, err)
	}

	if stepInfo.Settings == nil {
		// Nothing to remove if settings are nil, or key effectively doesn't exist
		// Depending on desired behavior, could return an error or just succeed silently.
		// For now, succeed silently as the key is not present.
		return nil
	}

	// Check if key exists before trying to delete
	if _, ok := stepInfo.Settings[keyToRemove]; !ok {
		// Key not found, consider this a success as the state is as if it were removed.
		// Alternatively, return an error: fmt.Errorf("key '%s' not found in settings for step %d", keyToRemove, stepID)
		return nil 
	}

	// Remove the key
	delete(stepInfo.Settings, keyToRemove)

	// Marshal the updated settings back to JSON
	updatedSettingsBytes, err := json.Marshal(stepInfo.Settings)
	if err != nil {
		return fmt.Errorf("failed to marshal updated settings for step %d: %w", stepID, err)
	}

	// Update the database
	result, err := db.Exec(
		"UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2",
		string(updatedSettingsBytes),
		stepID,
	)
	if err != nil {
		return fmt.Errorf("error updating step settings in database for step %d: %w", stepID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("error checking affected rows for step %d: %w", stepID, err)
	}
	if rowsAffected == 0 {
		// This case should ideally be caught by GetStepInfo, but as a safeguard:
		return fmt.Errorf("no step found with ID %d during update", stepID)
	}

	return nil
}


// setNestedValue sets a value in a nested map based on a dot-separated path.
// It creates intermediate maps if they don't exist.
func setNestedValue(dataMap map[string]interface{}, path string, value interface{}) error {
	parts := strings.Split(path, ".")
	current := dataMap

	for i, part := range parts {
		if i == len(parts)-1 { // Last part, set the value
			current[part] = value
		} else { // Intermediate part, navigate or create map
			if _, ok := current[part]; !ok {
				// Part doesn't exist, create a new map
				current[part] = make(map[string]interface{})
			}
			
			nextMap, ok := current[part].(map[string]interface{})
			if !ok {
				// Part exists but is not a map, cannot traverse
				return fmt.Errorf("cannot set value at path '%s': segment '%s' is not an object", path, part)
			}
			current = nextMap
		}
	}
	return nil
}

// UpdateStepFieldOrSetting updates a direct field of a step or a key within its settings JSON.
// For settings, dot notation (e.g., "docker_run.image_tag") is supported for nested keys.
// It attempts to parse valueToSet as JSON; if it fails, valueToSet is treated as a string.
func UpdateStepFieldOrSetting(db *sql.DB, stepID int, keyToSet string, valueToSet string) error {
	// List of updatable direct columns in the 'steps' table
	validDirectFields := map[string]bool{
		"title":  true,
		"status": true,
	}

	if _, isDirectField := validDirectFields[keyToSet]; isDirectField {
		// Basic safety check, though keys are from a controlled map.
		if keyToSet != "title" && keyToSet != "status" { // Ensure it's one of the explicitly handled direct fields
			return fmt.Errorf("internal error: unhandled direct field for update: %s", keyToSet)
		}

		query := fmt.Sprintf("UPDATE steps SET %s = $1, updated_at = NOW() WHERE id = $2", keyToSet) // keyToSet is safe due to check above
		result, err := db.Exec(query, valueToSet, stepID)
		if err != nil {
			return fmt.Errorf("error updating step field %s: %w", keyToSet, err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("error checking affected rows for step field %s update: %w", keyToSet, err)
		}
		if rowsAffected == 0 {
			return fmt.Errorf("no step found with ID %d, or %s was already set to '%s'", stepID, keyToSet, valueToSet)
		}
		return nil
	} else {
		// Assume keyToSet is for the 'settings' JSON field
		stepInfo, err := GetStepInfo(db, stepID)
		if err != nil {
			return fmt.Errorf("failed to get step info for step %d: %w", stepID, err)
		}

		if stepInfo.Settings == nil {
			stepInfo.Settings = make(map[string]interface{})
		}
		
		var jsonValue interface{}
		// Attempt to unmarshal valueToSet to see if it's a JSON primitive (number, boolean, null) or a pre-formatted JSON object/array.
		err = json.Unmarshal([]byte(valueToSet), &jsonValue)
		if err == nil {
			// It's a valid JSON value (e.g. "123", "true", "null", "{\"a\":1}")
			if errSet := setNestedValue(stepInfo.Settings, keyToSet, jsonValue); errSet != nil {
				return fmt.Errorf("failed to set nested key '%s' in settings for step %d: %w", keyToSet, stepID, errSet)
			}
		} else {
			// Not a valid JSON value on its own, so treat it as a plain string.
			if errSet := setNestedValue(stepInfo.Settings, keyToSet, valueToSet); errSet != nil {
				return fmt.Errorf("failed to set nested key '%s' in settings for step %d: %w", keyToSet, stepID, errSet)
			}
		}

		updatedSettingsBytes, err := json.Marshal(stepInfo.Settings)
		if err != nil {
			return fmt.Errorf("failed to marshal updated settings for step %d: %w", stepID, err)
		}

		result, err := db.Exec(
			"UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2",
			string(updatedSettingsBytes),
			stepID,
		)
		if err != nil {
			return fmt.Errorf("error updating step settings in database for step %d: %w", stepID, err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("error checking affected rows for step ID %d settings update: %w", stepID, err)
		}
		if rowsAffected == 0 {
			return fmt.Errorf("no step found with ID %d during settings update", stepID)
		}
		return nil
	}
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


