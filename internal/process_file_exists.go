package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// processFileExistsSteps checks for the existence of files specified in step settings.
// It queries active steps that have a 'file_exists' key in their settings,
// checks if the specified file exists at the task's local_path,
// and stores the result ('success' or 'failure') back into the step's results.
func processFileExistsSteps(db *sql.DB) {
	query := `SELECT s.id, s.task_id, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.status = 'active'
		AND t.status = 'active'
		AND t.local_path IS NOT NULL
		AND t.local_path <> ''
		AND s.settings::text LIKE '%"file_exists"%'` // Ensure we are matching the key "file_exists"

	rows, err := db.Query(query)
	if err != nil {
		stepLogger.Println("File exists query error:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var step stepExec // Assumes stepExec is defined in the package (e.g., types.go or common part of steps.go)
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.LocalPath); err != nil {
			stepLogger.Println("Row scan error:", err)
			continue
		}

		var settings map[string]interface{}
		if err := json.Unmarshal([]byte(step.Settings), &settings); err != nil {
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "invalid settings json"}); errStore != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
			}
			stepLogger.Printf("Step %d: invalid settings json\n", step.StepID)
			continue
		}

		filePathSetting, ok := settings["file_exists"].(string)
		if !ok {
			stepLogger.Printf("Step %d: 'file_exists' key missing or not a string in settings for step ID %d\n", step.StepID, step.StepID)
			// Consider if this should be a failure or just a skip
			// For now, skipping as the query might be too broad if "file_exists" can appear elsewhere in settings as non-string
			continue
		}

		absPath := filepath.Join(step.LocalPath, filePathSetting)
		if _, err := os.Stat(absPath); err == nil {
			// File exists
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "success"}); errStore != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
			}
			stepLogger.Printf("Step %d: file_exists '%s' SUCCESS\n", step.StepID, absPath)
		} else {
			// File does not exist or other error with os.Stat
			errMsg := fmt.Sprintf("file not found: %s", filePathSetting)
			if !os.IsNotExist(err) { // If the error is something other than "not exist"
				errMsg = fmt.Sprintf("error checking file '%s': %v", filePathSetting, err)
			}
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg}); errStore != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
			}
			stepLogger.Printf("Step %d: file_exists '%s' FAILURE: %s\n", step.StepID, absPath, errMsg)
		}
	}
}
