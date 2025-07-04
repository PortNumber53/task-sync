package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// processAllFileExistsSteps is the cron-style runner for all active file_exists steps.
func processAllFileExistsSteps(db *sql.DB, logger *log.Logger) error {
	query := `SELECT s.id, s.task_id, s.settings, t.local_path FROM steps s JOIN tasks t ON s.task_id = t.id WHERE t.status = 'active' AND t.local_path IS NOT NULL AND t.local_path <> '' AND s.settings ? 'file_exists'`
	rows, err := db.Query(query)
	if err != nil {
		logger.Println("File exists query error:", err)
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var step models.StepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.LocalPath); err != nil {
			logger.Println("Row scan error:", err)
			continue // Continue to next step
		}
		// Use a step-specific logger for clarity
		stepLogger := log.New(os.Stdout, fmt.Sprintf("STEP %d [file_exists]: ", step.StepID), log.Ldate|log.Ltime|log.Lshortfile)
		if err := ProcessFileExistsStep(db, &step, stepLogger); err != nil {
			stepLogger.Printf("Error processing step: %v", err)
		}
	}
	return nil
}

// ProcessFileExistsStep processes a single file_exists step.
func ProcessFileExistsStep(db *sql.DB, step *models.StepExec, logger *log.Logger) error {
	var settings map[string]interface{}
	if err := json.Unmarshal([]byte(step.Settings), &settings); err != nil {
		errMsg := "invalid settings json"
		models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg})
		logger.Printf("Step %d: %s\n", step.StepID, errMsg)
		return fmt.Errorf("%s for step %d", errMsg, step.StepID)
	}

	filePathsValue, ok := settings["file_exists"]
	if !ok {
		// This case should ideally not be hit if called from a dispatcher that has confirmed the step type.
		logger.Printf("Step %d: 'file_exists' key missing in settings\n", step.StepID)
		return nil
	}

	feConfig, ok := filePathsValue.(map[string]interface{})
	if !ok {
		errMsg := "invalid type for 'file_exists'; expected an object"
		models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg})
		logger.Printf("Step %d: %s\n", step.StepID, errMsg)
		return fmt.Errorf("%s for step %d", errMsg, step.StepID)
	}

	filesMapValue, ok := feConfig["files"]
	if !ok {
		errMsg := "'files' key missing in 'file_exists' settings"
		models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg})
		logger.Printf("Step %d: %s\n", step.StepID, errMsg)
		return fmt.Errorf("%s for step %d", errMsg, step.StepID)
	}

	filesMap, ok := filesMapValue.(map[string]interface{})
	if !ok {
		errMsg := "invalid type for 'files'; expected a map of file paths"
		models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg})
		logger.Printf("Step %d: %s\n", step.StepID, errMsg)
		return fmt.Errorf("%s for step %d", errMsg, step.StepID)
	}

	if len(filesMap) == 0 {
		errMsg := "'file_exists.files' is present but contains no paths"
		models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg})
		logger.Printf("Step %d: %s\n", step.StepID, errMsg)
		return fmt.Errorf("%s for step %d", errMsg, step.StepID)
	}

	var errorMessages []string
	updatedFiles := make(map[string]interface{})

	for path := range filesMap {
		absPath := filepath.Join(step.LocalPath, path)
		fileInfo, err := os.Stat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				errorMessages = append(errorMessages, fmt.Sprintf("file not found: %s", path))
			} else {
				errorMessages = append(errorMessages, fmt.Sprintf("error checking file '%s': %v", path, err))
			}
		} else {
			updatedFiles[path] = fileInfo.ModTime().Format(time.RFC3339)
		}
	}

	if len(errorMessages) == 0 {
		feConfig["files"] = updatedFiles
		settings["file_exists"] = feConfig
		newSettingsJSON, err := json.Marshal(settings)
		if err != nil {
			models.StoreStepResult(db, step.StepID, map[string]interface{}{
				"result":  "success",
				"message": fmt.Sprintf("All files found, but failed to marshal updated settings: %v", err),
				"files":   updatedFiles,
			})
			logger.Printf("Step %d: Failed to marshal updated settings: %v\n", step.StepID, err)
			return err
		}

		_, err = db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(newSettingsJSON), step.StepID)
		if err != nil {
			logger.Printf("Step %d: Failed to update step settings with timestamps: %v\n", step.StepID, err)
			// Not returning error here as the core logic succeeded.
		}

		models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "success", "files": updatedFiles})
		logger.Printf("Step %d: file_exists check SUCCESS for all paths\n", step.StepID)
	} else {
		fullErrMsg := strings.Join(errorMessages, "; ")
		models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": fullErrMsg})
		logger.Printf("Step %d: file_exists check FAILURE: %s\n", step.StepID, fullErrMsg)
	}
	return nil
}
