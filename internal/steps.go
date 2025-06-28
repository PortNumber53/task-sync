package internal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/yourusername/task-sync/internal/tasks/dynamic_lab"
)

// CreateStep inserts a new step for a task and returns the new step's ID.
func CreateStep(db *sql.DB, taskRef, title, settings string) (int, error) {
	var js interface{}
	if err := json.Unmarshal([]byte(settings), &js); err != nil {
		return 0, fmt.Errorf("settings must be valid JSON: %w", err)
	}

	var taskID int
	if id, err := strconv.Atoi(taskRef); err == nil {
		err = db.QueryRow("SELECT id FROM tasks WHERE id = $1", id).Scan(&taskID)
		if err != nil {
			return 0, fmt.Errorf("no task found with id %d", id)
		}
	} else {
		err = db.QueryRow("SELECT id FROM tasks WHERE name = $1", taskRef).Scan(&taskID)
		if err != nil {
			return 0, fmt.Errorf("no task found with name '%s'", taskRef)
		}
	}

	var newStepID int
	err := db.QueryRow(`INSERT INTO steps (task_id, title, settings, created_at, updated_at) VALUES ($1, $2, $3::jsonb, now(), now()) RETURNING id`, taskID, title, settings).Scan(&newStepID)
	if err != nil {
		return 0, err
	}
	return newStepID, nil
}



// ListSteps prints all steps in the DB. If full is true, prints settings column too.
func ListSteps(db *sql.DB, full bool) error {
	var rows *sql.Rows
	var err error
	if full {
		rows, err = db.Query(`SELECT id, task_id, title, settings, created_at, updated_at FROM steps ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		fmt.Printf("%-4s %-7s %-20s %-30s %-25s %-25s\n", "ID", "TaskID", "Title", "Settings", "Created At", "Updated At")
		for rows.Next() {
			var id, taskID int
			var title, settings, createdAt, updatedAt string
			if err := rows.Scan(&id, &taskID, &title, &settings, &createdAt, &updatedAt); err != nil {
				return err
			}
			fmt.Printf("%-4d %-7d %-20s %-30s %-25s %-25s\n", id, taskID, title, settings, createdAt, updatedAt)
		}
	} else {
		rows, err = db.Query(`SELECT id, task_id, title, created_at, updated_at FROM steps ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		fmt.Printf("%-4s %-7s %-20s %-25s %-25s\n", "ID", "TaskID", "Title", "Created At", "Updated At")
		for rows.Next() {
			var id, taskID int
			var title, createdAt, updatedAt string
			if err := rows.Scan(&id, &taskID, &title, &createdAt, &updatedAt); err != nil {
				return err
			}
			fmt.Printf("%-4d %-7d %-20s %-25s %-25s\n", id, taskID, title, createdAt, updatedAt)
		}
	}
	return nil
}

// DockerBuild defines the structure for docker build specific settings.
type DockerBuild struct {
	DependsOn []struct {
		ID int `json:"id"`
	} `json:"depends_on"`
	Files    []string          `json:"files"`
	Hashes   map[string]string `json:"hashes"`
	Params   []string          `json:"params"`
	ImageID  string            `json:"image_id"`
	ImageTag string            `json:"image_tag"`
}

// DockerBuildConfig represents the configuration for a docker build step
type DockerBuildConfig struct {
	DockerBuild DockerBuild `json:"docker_build"`
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
		DependsOn []struct {
			ID int `json:"id"`
		} `json:"depends_on"`
		Parameters    []string `json:"parameters"`
		ImageID       string   `json:"image_id"`
		ImageTag      string   `json:"image_tag"`
		ContainerID   string   `json:"container_id,omitempty"`
		ContainerName string   `json:"container_name,omitempty"`
		ContainerHash string   `json:"container_hash,omitempty"`
	} `json:"docker_run"`
}

// DockerShellConfig represents the configuration for a docker shell step
type DockerShellConfig struct {
	DockerShell struct {
		Command   []map[string]string `json:"command"`
		DependsOn []struct {
			ID int `json:"id"`
		} `json:"depends_on"`
		Docker struct {
			ContainerID   string `json:"container_id"`
			ContainerName string `json:"container_name"`
			ImageID       string `json:"image_id"`
			ImageTag      string `json:"image_tag"`
		} `json:"docker"`
	} `json:"docker_shell"`
}

// DynamicLabConfig represents the configuration for a dynamic_lab step
type DynamicLabConfig struct {
	DynamicLab struct {
		Files       map[string]string `json:"files"`
		RubricFile  string            `json:"rubric_file"`
		DependsOn   []struct {
			ID int `json:"id"`
		} `json:"depends_on,omitempty"`
		Environment DynamicRubricEnvironment `json:"environment,omitempty"`
	} `json:"dynamic_lab"`
}

// DockerPullConfig represents the configuration for a docker pull step
type DockerPullConfig struct {
	DockerPull struct {
		DependsOn []struct {
			ID int `json:"id"`
		} `json:"depends_on,omitempty"`
		ImageTag         string `json:"image_tag"`
		ImageID          string `json:"image_id,omitempty"`           // Optional: for verification after pull
		PreventRunBefore string `json:"prevent_run_before,omitempty"` // RFC3339 timestamp
	} `json:"docker_pull"`
}

// DynamicRubricEnvironment defines the execution environment for generated steps.
type DynamicRubricEnvironment struct {
	Docker   bool   `json:"docker"`
	ImageTag string `json:"image_tag"`
	ImageID  string `json:"image_id"`
}

// DynamicRubricConfig represents the configuration for a dynamic_rubric step
type DynamicRubricConfig struct {
	DynamicRubric struct {
		File      string `json:"file"`
		Hash      string `json:"hash,omitempty"`
		DependsOn []struct {
			ID int `json:"id"`
		} `json:"depends_on,omitempty"`
		Environment DynamicRubricEnvironment `json:"environment,omitempty"`
	} `json:"dynamic_rubric"`
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
func checkDependencies(db *sql.DB, stepID int, stepLogger *log.Logger) (bool, error) {
	var dependsOnJSON string
	// Correctly extract the top-level 'depends_on' key
	err := db.QueryRow(`
		SELECT COALESCE(
			(SELECT value FROM jsonb_each(settings) WHERE key = 'depends_on'),
			'[]'::jsonb
		)::text
		FROM steps WHERE id = $1
	`, stepID).Scan(&dependsOnJSON)

	if err != nil {
		if err == sql.ErrNoRows {
			return true, nil // Step not found, no dependencies to check.
		}
		return false, fmt.Errorf("could not retrieve dependencies for step %d: %w", stepID, err)
	}

	var dependsOn []struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal([]byte(dependsOnJSON), &dependsOn); err != nil {
		// It's possible the settings don't have a top-level depends_on, but a nested one.
		// This is a fallback to a more general, but less efficient, parsing.
		var settingsMap map[string]json.RawMessage
		if err2 := json.Unmarshal([]byte(dependsOnJSON), &settingsMap); err2 != nil {
			return false, fmt.Errorf("could not parse dependencies for step %d: %w", stepID, err)
		}
		if val, ok := settingsMap["depends_on"]; ok {
			if err3 := json.Unmarshal(val, &dependsOn); err3 != nil {
				return false, fmt.Errorf("could not parse nested dependencies for step %d: %w", stepID, err3)
			}
		}
	}

	if len(dependsOn) == 0 {
		return true, nil
	}

	depIDs := make([]int, len(dependsOn))
	for i, dep := range dependsOn {
		depIDs[i] = dep.ID
	}

	query := `
		SELECT NOT EXISTS (
			SELECT 1
			FROM steps s
			WHERE s.id = ANY($1::int[])
			AND (s.results->>'result' IS NULL OR s.results->>'result' != 'success')
		)`

	stepLogger.Printf("Step %d: running dependency check query: %s with args %v\n", stepID, query, depIDs)

	var allDepsCompleted bool
	err = db.QueryRow(query, pq.Array(depIDs)).Scan(&allDepsCompleted)
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
	Settings   map[string]interface{} `json:"settings"`
	Results    map[string]interface{} `json:"results"`
	CreatedAt  time.Time              `json:"created_at"`
	UpdatedAt  time.Time              `json:"updated_at"`

}

// ProcessSteps is the main entry point for processing all pending steps.
func ProcessSteps(db *sql.DB) error {
	stepProcessors := map[string]func(*sql.DB) error{
		"dynamic_lab":    processDynamicLabSteps,
		"docker_pull":    func(db *sql.DB) error { processDockerPullSteps(db); return nil },
		"docker_build":   func(db *sql.DB) error { processDockerBuildSteps(db); return nil },
		"docker_run":     func(db *sql.DB) error { processDockerRunSteps(db); return nil },
		"docker_shell":   func(db *sql.DB) error { processDockerShellSteps(db); return nil },
		"docker_rubrics": func(db *sql.DB) error { processDockerRubricsSteps(db); return nil },
		"file_exists":    func(db *sql.DB) error { processFileExistsSteps(db); return nil },
	}

	return executePendingSteps(db, stepProcessors)
}

func executePendingSteps(db *sql.DB, stepProcessors map[string]func(*sql.DB) error) error {
	// Process dynamic rubrics first to generate other steps
	if err := processDynamicRubricSteps(db); err != nil {
		log.Printf("Error processing dynamic_rubric steps: %v", err)
	}

	// Iterate over the map and call each function
	for stepType, processorFunc := range stepProcessors {
		if err := processorFunc(db); err != nil {
			log.Printf("Error processing %s steps: %v", stepType, err)
			// Decide if you want to continue or return on error
		}
	}

	// Wait for all goroutines to complete
	// This is a simplified approach; a sync.WaitGroup would be more robust
	// For now, assuming steps complete or timeout reasonably quickly
	time.Sleep(5 * time.Second)

	return nil
}

// GetStepInfo retrieves detailed information about a specific step by ID
func GetStepInfo(db *sql.DB, stepID int) (*StepInfo, error) {
	var info StepInfo
	var settingsJSON, resultsJSON sql.NullString


	err := db.QueryRow(`
		SELECT s.id, s.task_id, s.title, s.settings::text, s.results::text, s.created_at, s.updated_at
		FROM steps s
		WHERE s.id = $1
	`, stepID).Scan(
		&info.ID, &info.TaskID, &info.Title,
		&settingsJSON, &resultsJSON, &info.CreatedAt, &info.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no step found with ID %d", stepID)
	}
	if err != nil {
		return nil, err
	}

	// Only parse settings if they exist and are not null
	if settingsJSON.Valid && settingsJSON.String != "" && settingsJSON.String != "null" {
		info.Settings = make(map[string]interface{})
		decoder := json.NewDecoder(strings.NewReader(settingsJSON.String))
		decoder.UseNumber()
		if err := decoder.Decode(&info.Settings); err != nil {
			return nil, fmt.Errorf("error parsing settings: %w", err)
		}
	}

	// Only parse results if they exist and are not null
	if resultsJSON.Valid && resultsJSON.String != "" && resultsJSON.String != "null" {
		info.Results = make(map[string]interface{})
		decoder := json.NewDecoder(strings.NewReader(resultsJSON.String))
		decoder.UseNumber()
		if err := decoder.Decode(&info.Results); err != nil {
			return nil, fmt.Errorf("error parsing results: %w", err)
		}
	}

	return &info, nil
}

func CopyStep(db *sql.DB, fromStepID, toTaskID int) (int, error) {
	// Start a transaction
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("error starting transaction: %w", err)
	}
	defer tx.Rollback()

	// 1. Verify the target task exists
	var targetTaskExists bool
	err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1)", toTaskID).Scan(&targetTaskExists)
	if err != nil {
		return 0, fmt.Errorf("error checking target task: %w", err)
	}
	if !targetTaskExists {
		return 0, fmt.Errorf("target task with ID %d does not exist", toTaskID)
	}

	// 1. Get the source step's data
	var title, settings string
	err = tx.QueryRow(
		"SELECT title, settings FROM steps WHERE id = $1",
		fromStepID,
	).Scan(&title, &settings)
	if err != nil {
		return 0, fmt.Errorf("reading source step %d failed: %w", fromStepID, err)
	}

	// 2. (Future) Transform settings if needed, e.g., if it contains references
	// to other steps in the original task. For now, we do a direct copy.

	// 3. Create the new step in the target task
	var newStepID int
	err = tx.QueryRow(
		`INSERT INTO steps (task_id, title, settings, created_at, updated_at)
		 VALUES ($1, $2, $3::jsonb, now(), now())
		 RETURNING id`,
		toTaskID, title, settings,
	).Scan(&newStepID)
	if err != nil {
		return 0, fmt.Errorf("creating new step in task %d failed: %w", toTaskID, err)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("error committing transaction: %w", err)
	}

	return newStepID, nil
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
	validFields := map[string]bool{
		"title": true,
	}

	// If the key is a direct field on the 'steps' table
	if _, ok := validFields[keyToSet]; ok {
		if keyToSet != "title" { // Ensure it's one of the explicitly handled direct fields
			return fmt.Errorf("invalid field to update: %s", keyToSet)
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
			if valueToSet == "" {
				// Special case: empty string means remove the key.
				keys := strings.Split(keyToSet, ".")
				currentMap := stepInfo.Settings
				for i, key := range keys {
					if i == len(keys)-1 {
						delete(currentMap, key)
						break
					}
					next, ok := currentMap[key]
					if !ok {
						break
					}
					nextMap, ok := next.(map[string]interface{})
					if !ok {
						// Path is not a map, can't continue.
						break
					}
					currentMap = nextMap
				}
			} else {
				if errSet := setNestedValue(stepInfo.Settings, keyToSet, valueToSet); errSet != nil {
					return fmt.Errorf("failed to set nested key '%s' in settings for step %d: %w", keyToSet, stepID, errSet)
				}
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

func processDynamicLabSteps(db *sql.DB) error {
	steps, err := getStepsByType(db, "dynamic_lab")
	if err != nil {
		return fmt.Errorf("failed to get dynamic_lab steps: %w", err)
	}

	for _, step := range steps {
		var config DynamicLabConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			log.Printf("Error parsing settings for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": fmt.Sprintf("Error parsing settings: %v", err)}
			resultsJSON, _ := json.Marshal(results)
			db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID)
			continue
		}

		newHashes, changed, err := dynamic_lab.Run(step.LocalPath, config.DynamicLab.Files)
		if err != nil {
			log.Printf("Error running dynamic_lab for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": err.Error()}
			resultsJSON, _ := json.Marshal(results)
			db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID)
			continue
		}

		if !changed {
			log.Printf("No file changes for dynamic_lab step %d.", step.StepID)
			continue
		}

		log.Printf("File changes detected for dynamic_lab step %d. Re-generating steps.", step.StepID)

		if err := deleteGeneratedSteps(db, step.StepID); err != nil {
			log.Printf("Error deleting generated steps for step %d: %v", step.StepID, err)
			continue
		}

		rubricFile := config.DynamicLab.RubricFile
		if rubricFile == "" {
			log.Printf("dynamic_lab step %d settings does not specify a 'rubric_file'", step.StepID)
			continue
		}

		criteria, newRubricHash, _, err := dynamic_lab.RunRubric(step.LocalPath, rubricFile, "") // Pass empty hash to force re-parse
		if err != nil {
			log.Printf("Error running dynamic_rubric for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": err.Error()}
			resultsJSON, _ := json.Marshal(results)
			if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
				log.Printf("Failed to update step %d completion with error results: %v", step.StepID, err)
			}
			continue
		}

		var containerID string
		var runStepDependencyID int
		for _, dep := range config.DynamicLab.DependsOn {
			var rawResults sql.NullString
			err := db.QueryRow("SELECT results FROM steps WHERE id = $1", dep.ID).Scan(&rawResults)
			if err != nil {
				log.Printf("Step %d: Error getting info for dependency step %d: %v", step.StepID, dep.ID, err)
				continue
			}

			if rawResults.Valid {
				var results map[string]interface{}
				if err := json.Unmarshal([]byte(rawResults.String), &results); err != nil {
					log.Printf("Error unmarshalling results for dependency step %d: %v", dep.ID, err)
					continue
				}

				if cID, ok := results["container_id"].(string); ok {
					containerID = cID
					runStepDependencyID = dep.ID
					log.Printf("Found container_id '%s' from dependency step %d", containerID, runStepDependencyID)
					break
				}
			}
		}

		if containerID == "" {
			log.Printf("Step %d: Could not find a container_id from any dependencies. Skipping generation.", step.StepID)
			results := map[string]interface{}{"result": "success", "info": "Could not find a container_id from any dependencies. Skipping generation."}
			resultsJSON, _ := json.Marshal(results)
			if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
				log.Printf("Error updating step %d results: %v", step.StepID, err)
			}
			continue
		}

		for _, crit := range criteria {
			var settings string
			if config.DynamicLab.Environment.Docker {
				settings = fmt.Sprintf(`{
					"docker_shell": {
						"command": [{"run": "%s"}],
						"depends_on": [{"id": %d}],
						"rubric_details": {
							"score": %d,
							"required": %t,
							"description": "%s"
						}
					}
				}`, crit.HeldOutTest, runStepDependencyID, crit.Score, crit.Required, crit.Rubric)
			} else {
				log.Printf("Step %d: Skipping criterion '%s' because environment is not docker.", step.StepID, crit.Title)
				continue
			}

			if _, err := CreateStep(db, strconv.Itoa(step.TaskID), crit.Title, settings); err != nil {
				log.Printf("Error creating step for criterion '%s' from step %d: %v", crit.Title, step.StepID, err)
			}
		}

		config.DynamicLab.Files = newHashes
		config.DynamicLab.Files[rubricFile] = newRubricHash
		updatedSettings, err := json.Marshal(config)
		if err != nil {
			log.Printf("Error marshalling updated settings for step %d: %v", step.StepID, err)
			continue
		}
		if _, err := db.Exec(`UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2`, string(updatedSettings), step.StepID); err != nil {
			log.Printf("Error updating settings for step %d: %v", step.StepID, err)
			continue
		}

		results := map[string]interface{}{"result": "success"}
		resultsJSON, _ := json.Marshal(results)
		if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
			log.Printf("Error updating step %d results to success: %v", step.StepID, err)
		}
	}

	return nil
}

func processDynamicRubricSteps(db *sql.DB) error {
	log.Println("Processing dynamic rubric steps...")
	dynamicRubricSteps, err := getStepsByType(db, "dynamic_rubric")
	if err != nil {
		return fmt.Errorf("failed to get active dynamic_rubric steps: %w", err)
	}
	log.Printf("Found %d dynamic_rubric steps to process.", len(dynamicRubricSteps))

	for i, step := range dynamicRubricSteps {
		log.Printf("Processing step %d/%d: ID %d", i+1, len(dynamicRubricSteps), step.StepID)

		var config DynamicRubricConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			log.Printf("Error unmarshalling settings for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": fmt.Sprintf("Error parsing settings: %v", err)}
			resultsJSON, _ := json.Marshal(results)
			db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID)
			continue
		}

		// Find container_id and its step ID from dependencies
		var containerID string
		var runStepDependencyID int
		for _, dep := range config.DynamicRubric.DependsOn {
			var rawResults sql.NullString
			err := db.QueryRow("SELECT results FROM steps WHERE id = $1", dep.ID).Scan(&rawResults)
			if err != nil {
				log.Printf("Step %d: Error getting info for dependency step %d: %v", step.StepID, dep.ID, err)
				continue
			}

			if rawResults.Valid {
				var results map[string]interface{}
				if err := json.Unmarshal([]byte(rawResults.String), &results); err != nil {
					log.Printf("Error unmarshalling results for dependency step %d: %v", dep.ID, err)
					continue
				}

				if cID, ok := results["container_id"].(string); ok {
					containerID = cID
					runStepDependencyID = dep.ID // Capture the ID of the dependency that provides the container
					log.Printf("Found container_id '%s' from dependency step %d", containerID, runStepDependencyID)
					break // Found it, no need to check other dependencies
				}
			}
		}

		if containerID == "" {
			log.Printf("Step %d: Could not find a container_id from any dependencies. Skipping generation.", step.StepID)
			results := map[string]interface{}{"result": "success", "info": "Could not find a container_id from any dependencies. Skipping generation."}
			resultsJSON, _ := json.Marshal(results)
			if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
				log.Printf("Error updating step %d results: %v", step.StepID, err)
			}
			continue
		}

		log.Printf("Step %d: Running RunRubric", step.StepID)
		criteria, newHash, changed, err := dynamic_lab.RunRubric(step.LocalPath, config.DynamicRubric.File, config.DynamicRubric.Hash)
		if err != nil {
			log.Printf("Error running dynamic_rubric for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": err.Error()}
			resultsJSON, _ := json.Marshal(results)
			if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
				log.Printf("Failed to update step %d completion with error results: %v", step.StepID, err)
			}
			continue
		}
		log.Printf("Step %d: RunRubric completed. Changed: %t", step.StepID, changed)

		if changed {
			log.Printf("Rubric file changed for step %d. Updating generated steps.", step.StepID)

			if err := deleteGeneratedSteps(db, step.StepID); err != nil {
				log.Printf("Error deleting generated steps for step %d: %v", step.StepID, err)
				continue
			}

			for _, crit := range criteria {
				var settings string
				if config.DynamicRubric.Environment.Docker {
					settings = fmt.Sprintf(`{
						"docker_shell": {
							"command": [{"run": "%s"}],
							"depends_on": [{"id": %d}],
							"rubric_details": {
								"score": %d,
								"required": %t,
								"description": "%s"
							}
						}
					}`, crit.HeldOutTest, runStepDependencyID, crit.Score, crit.Required, crit.Rubric)
				} else {
					log.Printf("Step %d: Skipping criterion '%s' because environment is not docker.", step.StepID, crit.Title)
					continue
				}

				if _, err := CreateStep(db, strconv.Itoa(step.TaskID), crit.Title, settings); err != nil {
					log.Printf("Error creating step for criterion '%s' from step %d: %v", crit.Title, step.StepID, err)
				}
			}

			config.DynamicRubric.Hash = newHash
			updatedSettings, err := json.Marshal(config)
			if err != nil {
				log.Printf("Error marshalling updated settings for step %d: %v", step.StepID, err)
				continue
			}
			if _, err := db.Exec(`UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2`, string(updatedSettings), step.StepID); err != nil {
				log.Printf("Error updating settings for step %d: %v", step.StepID, err)
				continue
			}
		}

		results := map[string]interface{}{"result": "success"}
		resultsJSON, _ := json.Marshal(results)
		if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
			log.Printf("Error updating step %d results to success: %v", step.StepID, err)
		}
	}
	log.Println("--- Finished processing dynamic_rubric steps ---")
	return nil
}



func deleteGeneratedSteps(db *sql.DB, parentStepID int) error {
	// Find steps that depend on the parent step
	query := `
		SELECT id FROM steps WHERE settings -> 'docker_shell' -> 'depends_on' @> jsonb_build_array(jsonb_build_object('id', $1::int))
	`
	rows, err := db.Query(query, parentStepID)
	if err != nil {
		return fmt.Errorf("querying for generated steps failed: %w", err)
	}
	defer rows.Close()

	var idsToDelete []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scanning generated step id failed: %w", err)
		}
		idsToDelete = append(idsToDelete, id)
	}

	if len(idsToDelete) > 0 {
		deleteQuery := `DELETE FROM steps WHERE id = ANY($1::int[])`
		_, err := db.Exec(deleteQuery, pq.Array(idsToDelete))
		if err != nil {
			return fmt.Errorf("deleting generated steps failed: %w", err)
		}
		log.Printf("Deleted %d generated steps for parent step %d", len(idsToDelete), parentStepID)
	}

	return nil
}

func getStepsByType(db *sql.DB, stepType string) ([]stepExec, error) {
	query := `
		SELECT s.id, s.task_id, s.title, s.settings, COALESCE(t.local_path, '')
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.settings ? $1`

	rows, err := db.Query(query, stepType)
	if err != nil {
		return nil, fmt.Errorf("querying for steps by type failed: %w", err)
	}
	defer rows.Close()

	var steps []stepExec
	for rows.Next() {
		var step stepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Title, &step.Settings, &step.LocalPath); err != nil {
			return nil, fmt.Errorf("scanning step failed: %w", err)
		}
		steps = append(steps, step)
	}

	return steps, nil
}

type StepNode struct {
	ID       int
	Title    string
	TaskID   int
	Children []*StepNode
}

// TreeSteps fetches all steps and prints them as a dependency tree, grouped by task.
func TreeSteps(db *sql.DB) error {
	// 1. Fetch all tasks to get their names
	taskRows, err := db.Query(`SELECT id, name FROM tasks ORDER BY id`)
	if err != nil {
		return fmt.Errorf("querying tasks failed: %w", err)
	}
	defer taskRows.Close()

	taskNames := make(map[int]string)
	var taskIDs []int
	for taskRows.Next() {
		var id int
		var name string
		if err := taskRows.Scan(&id, &name); err != nil {
			return err
		}
		taskNames[id] = name
		taskIDs = append(taskIDs, id)
	}

	// 2. Fetch all steps
	stepRows, err := db.Query(`SELECT id, task_id, title, settings FROM steps ORDER BY id`)
	if err != nil {
		return err
	}
	defer stepRows.Close()

	nodes := make(map[int]*StepNode)
	dependencies := make(map[int][]int)
	taskSteps := make(map[int][]*StepNode)

	for stepRows.Next() {
		var id, taskID int
		var title, settingsStr string
		if err := stepRows.Scan(&id, &taskID, &title, &settingsStr); err != nil {
			return err
		}

		node := &StepNode{ID: id, TaskID: taskID, Title: title}
		nodes[id] = node
		taskSteps[taskID] = append(taskSteps[taskID], node)

		var settings struct {
			DependsOn []struct {
				ID int `json:"id"`
			} `json:"depends_on"`
		}

		var topLevel map[string]json.RawMessage
		if err := json.Unmarshal([]byte(settingsStr), &topLevel); err == nil {
			for _, rawMessage := range topLevel {
				if err := json.Unmarshal(rawMessage, &settings); err == nil {
					if len(settings.DependsOn) > 0 {
						for _, dep := range settings.DependsOn {
							dependencies[id] = append(dependencies[id], dep.ID)
						}
					}
				}
			}
		}
	}

	// 3. Build the dependency tree
	isChild := make(map[int]bool)
	for childID, parentIDs := range dependencies {
		for _, parentID := range parentIDs {
			if parentNode, ok := nodes[parentID]; ok {
				if childNode, ok := nodes[childID]; ok {
					parentNode.Children = append(parentNode.Children, childNode)
					isChild[childID] = true
				}
			}
		}
	}

	// 4. Sort children for each node
	for _, node := range nodes {
		sort.Slice(node.Children, func(i, j int) bool {
			return node.Children[i].ID < node.Children[j].ID
		})
	}

	// 5. Print the tree, grouped by task
	sort.Ints(taskIDs) // Sort tasks by ID for consistent output
	for _, taskID := range taskIDs {
		if steps, ok := taskSteps[taskID]; ok {
			if taskName, ok := taskNames[taskID]; ok {
				fmt.Printf("%d-%s\n", taskID, taskName)

				var rootNodes []*StepNode
				for _, node := range steps {
					if !isChild[node.ID] {
						rootNodes = append(rootNodes, node)
					}
				}

				sort.Slice(rootNodes, func(i, j int) bool {
					return rootNodes[i].ID < rootNodes[j].ID
				})

				printChildren(rootNodes, "")
			}
		}
	}

	return nil
}

func printChildren(nodes []*StepNode, prefix string) {
	for i, node := range nodes {
		connector := "├── "
		newPrefix := prefix + "│   "
		if i == len(nodes)-1 {
			connector = "╰── "
			newPrefix = prefix + "    "
		}
		fmt.Printf("%s%s%d-%s\n", prefix, connector, node.ID, node.Title)
		printChildren(node.Children, newPrefix)
	}
}

// DeleteStep removes a step from the database by its ID.
func DeleteStep(db *sql.DB, stepID int) error {
	result, err := db.Exec("DELETE FROM steps WHERE id = $1", stepID)
	if err != nil {
		return fmt.Errorf("failed to delete step %d: %w", stepID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected for step %d: %w", stepID, err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("no step found with ID %d", stepID)
	}

	return nil
}
