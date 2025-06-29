package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// processFileExistsSteps checks for the existence of files specified in step settings.
// It queries active steps that have a 'file_exists' key in their settings.
// The value of 'file_exists' is expected to be an object with a 'files' key,
// which contains a map of file paths to be checked.
// The step succeeds if all specified paths exist. On success, it updates the 'files'
// map with the last modified timestamp of each file.
// It stores the result ('success' or 'failure' with messages) back into the step's results.
func processFileExistsSteps(db *sql.DB) {
	query := `SELECT s.id, s.task_id, s.settings, t.local_path FROM steps s JOIN tasks t ON s.task_id = t.id WHERE t.status = 'active' AND t.local_path IS NOT NULL AND t.local_path <> '' AND s.settings ? 'file_exists'`

	rows, err := db.Query(query)
	if err != nil {
		stepLogger.Println("File exists query error:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var step stepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.LocalPath); err != nil {
			stepLogger.Println("Row scan error:", err)
			continue
		}

		var settings map[string]interface{}
		if err := json.Unmarshal([]byte(step.Settings), &settings); err != nil {
			errMsg := "invalid settings json"
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg})
			stepLogger.Printf("Step %d: %s\n", step.StepID, errMsg)
			continue
		}

		filePathsValue, ok := settings["file_exists"]
		if !ok {
			stepLogger.Printf("Step %d: 'file_exists' key missing in settings\n", step.StepID)
			continue
		}

		feConfig, ok := filePathsValue.(map[string]interface{})
		if !ok {
			errMsg := "invalid type for 'file_exists'; expected an object"
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg})
			stepLogger.Printf("Step %d: %s\n", step.StepID, errMsg)
			continue
		}

		filesMapValue, ok := feConfig["files"]
		if !ok {
			errMsg := "'files' key missing in 'file_exists' settings"
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg})
			stepLogger.Printf("Step %d: %s\n", step.StepID, errMsg)
			continue
		}

		filesMap, ok := filesMapValue.(map[string]interface{})
		if !ok {
			errMsg := "invalid type for 'files'; expected a map of file paths"
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg})
			stepLogger.Printf("Step %d: %s\n", step.StepID, errMsg)
			continue
		}

		if len(filesMap) == 0 {
			errMsg := "'file_exists.files' is present but contains no paths"
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg})
			stepLogger.Printf("Step %d: %s\n", step.StepID, errMsg)
			continue
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
				StoreStepResult(db, step.StepID, map[string]interface{}{
					"result":  "success",
					"message": fmt.Sprintf("All files found, but failed to marshal updated settings: %v", err),
					"files":   updatedFiles,
				})
				stepLogger.Printf("Step %d: Failed to marshal updated settings: %v\n", step.StepID, err)
				continue
			}

			_, err = db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(newSettingsJSON), step.StepID)
			if err != nil {
				stepLogger.Printf("Step %d: Failed to update step settings with timestamps: %v\n", step.StepID, err)
			}

			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "success", "files": updatedFiles})
			stepLogger.Printf("Step %d: file_exists check SUCCESS for all paths\n", step.StepID)
		} else {
			fullErrMsg := strings.Join(errorMessages, "; ")
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": fullErrMsg})
			stepLogger.Printf("Step %d: file_exists check FAILURE: %s\n", step.StepID, fullErrMsg)
		}
	}
}
