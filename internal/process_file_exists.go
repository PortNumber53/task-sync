package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// processFileExistsSteps checks for the existence of files specified in step settings.
// It queries active steps that have a 'file_exists' key in their settings.
// The value of 'file_exists' can be a single path (string) or multiple paths (array of strings).
// The step succeeds only if all specified paths exist relative to the task's local_path.
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
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg}); errStore != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
			}
			stepLogger.Printf("Step %d: %s\n", step.StepID, errMsg)
			continue
		}

		filePathsValue, ok := settings["file_exists"]
		if !ok {
			// This should not happen due to the `?` operator in the query, but check for safety.
			stepLogger.Printf("Step %d: 'file_exists' key missing in settings\n", step.StepID)
			continue
		}

		var pathsToCheck []string
		validSettings := true
		errMsg := ""

		switch v := filePathsValue.(type) {
		case string:
			pathsToCheck = append(pathsToCheck, v)
		case []interface{}:
			for i, item := range v {
				if pathStr, ok := item.(string); ok {
					pathsToCheck = append(pathsToCheck, pathStr)
				} else {
					errMsg = fmt.Sprintf("invalid type for path at index %d; expected string", i)
					validSettings = false
					break
				}
			}
		default:
			errMsg = "invalid type for 'file_exists'; expected string or array of strings"
			validSettings = false
		}

		if !validSettings {
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg}); errStore != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
			}
			stepLogger.Printf("Step %d: %s\n", step.StepID, errMsg)
			continue
		}

		if len(pathsToCheck) == 0 {
			errMsg = "'file_exists' key is present but contains no paths"
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg}); errStore != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
			}
			stepLogger.Printf("Step %d: %s\n", step.StepID, errMsg)
			continue
		}

		var errorMessages []string
		for _, path := range pathsToCheck {
			absPath := filepath.Join(step.LocalPath, path)
			if _, err := os.Stat(absPath); err != nil {
				if os.IsNotExist(err) {
					errorMessages = append(errorMessages, fmt.Sprintf("file not found: %s", path))
				} else {
					errorMessages = append(errorMessages, fmt.Sprintf("error checking file '%s': %v", path, err))
				}
			}
		}

		if len(errorMessages) == 0 {
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "success"}); errStore != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
			}
			stepLogger.Printf("Step %d: file_exists check SUCCESS for all paths\n", step.StepID)
		} else {
			fullErrMsg := strings.Join(errorMessages, "; ")
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": fullErrMsg}); errStore != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
			}
			stepLogger.Printf("Step %d: file_exists check FAILURE: %s\n", step.StepID, fullErrMsg)
		}
	}
}
