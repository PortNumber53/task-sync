package internal

import (
	"database/sql"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// processRubricsImportSteps processes rubrics_import steps for active tasks.
func processRubricsImportSteps(db *sql.DB, stepID int) error {
	var query string
	var rows *sql.Rows
	var err error

	if stepID != 0 {
		query = `SELECT s.id, s.task_id, s.settings, t.base_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.id = $1 AND s.settings ? 'rubrics_import'`
		rows, err = db.Query(query, stepID)
	} else {
		query = `SELECT s.id, s.task_id, s.settings, t.base_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE t.status = 'active'
		AND t.base_path IS NOT NULL
		AND t.base_path <> ''
		AND s.settings ? 'rubrics_import'`
		rows, err = db.Query(query)
	}

	if err != nil {
		models.StepLogger.Println("Rubrics import query error:", err)
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var step models.StepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.BasePath); err != nil {
			models.StepLogger.Println("Row scan error:", err)
			continue
		}

		if err := ProcessRubricsImportStep(db, &step, models.StepLogger); err != nil {
			models.StepLogger.Printf("Step %d: error processing rubrics_import step: %v\n", step.StepID, err)
		}
	}
	return nil
}

// ProcessRubricsImportStep processes a single rubrics_import step.
func ProcessRubricsImportStep(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
	// Sanitize step settings to remove deprecated MHTML keys before processing
	original := []byte(se.Settings)
	sanitized := models.SanitizeRawJSONRemoveMHTML(original)
	if !bytes.Equal(original, sanitized) {
		if _, err := db.Exec("UPDATE steps SET settings = $1 WHERE id = $2", string(sanitized), se.StepID); err != nil {
			models.StepLogger.Printf("Step %d: failed to sanitize step settings: %v\n", se.StepID, err)
		} else {
			se.Settings = string(sanitized)
		}
	}

	var settingsMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(se.Settings), &settingsMap); err != nil {
		models.StoreStepResult(db, se.StepID, map[string]interface{}{"result": "failure", "message": "invalid step config"})
		models.StepLogger.Printf("Step %d: invalid step config: %v\n", se.StepID, err)
		return nil
	}

	var config models.RubricsImportConfig
	if rubricsImportJSON, ok := settingsMap["rubrics_import"]; ok {
		if err := json.Unmarshal(rubricsImportJSON, &config); err != nil {
			models.StoreStepResult(db, se.StepID, map[string]interface{}{"result": "failure", "message": fmt.Sprintf("invalid rubrics_import config: %v", err)})
			models.StepLogger.Printf("Step %d: invalid rubrics_import config: %v\n", se.StepID, err)
			return nil
		}
	} else {
		models.StoreStepResult(db, se.StepID, map[string]interface{}{"result": "failure", "message": "rubrics_import not found in settings"})
		models.StepLogger.Printf("Step %d: rubrics_import not found in step settings\n", se.StepID)
		return nil
	}

	// Add logging to inspect config values
	models.StepLogger.Printf("DEBUG: Step %d: config: %+v\n", se.StepID, config)
	models.StepLogger.Printf("DEBUG: Step %d: config.MDFile: %s\n", se.StepID, config.MDFile)
	models.StepLogger.Printf("DEBUG: Step %d: config.JSONFile: %s\n", se.StepID, config.JSONFile)

	ok, err := models.CheckDependencies(db, se)
	if err != nil {
		models.StepLogger.Printf("Step %d: error checking dependencies: %v\n", se.StepID, err)
		return nil
	}
	if !ok {
		models.StepLogger.Printf("Step %d: waiting for dependencies to complete\n", se.StepID)
		return nil
	}

	// Decide whether we should run based on force or file hash changes
	shouldRun := config.Force
	filesToCheck := config.Triggers.Files
	if shouldRun {
		models.StepLogger.Printf("Step %d: force=true, bypassing hash checks and running.\n", se.StepID)
	}
	if !shouldRun {
		// File change trigger logic
		if len(filesToCheck) > 0 {
			filesChanged, err := models.CheckFileHashChanges(se.BasePath, filesToCheck, models.StepLogger)
			if err != nil {
				msg := fmt.Sprintf("error checking file hash changes: %v", err)
				models.StoreStepResult(db, se.StepID, map[string]interface{}{"result": "failure", "message": msg})
				models.StepLogger.Printf("Step %d: %s\n", se.StepID, msg)
				return nil
			}
			if !filesChanged {
				msg := "skipped: no relevant file changes detected"
				models.StoreStepResult(db, se.StepID, map[string]interface{}{"result": "skipped", "message": msg})
				models.StepLogger.Printf("Step %d: %s\n", se.StepID, msg)
				return nil
			}
			shouldRun = true
		}
	}
	if !shouldRun {
		return nil
	}

	// After successful import, update file hashes and persist to step settings if triggers.files is set
	if len(filesToCheck) > 0 {
		for filePath := range filesToCheck {
			fullPath := filepath.Join(se.BasePath, filePath)
			hash, err := models.GetSHA256(fullPath)
			if err != nil {
				models.StepLogger.Printf("Step %d: Warning: could not compute hash for %s: %v\n", se.StepID, filePath, err)
				continue
			}
			filesToCheck[filePath] = hash
		}
		// Persist updated hashes to step settings, but NEVER persist a transient force flag
		persistConfig := config
		persistConfig.Force = false
		settingsMap["rubrics_import"], _ = json.Marshal(persistConfig)
		jsonPath := filepath.Join(se.BasePath, config.JSONFile)
		criteria, err := models.ParseRubric(jsonPath)
		if err != nil {
			models.StoreStepResult(db, se.StepID, map[string]interface{}{"result": "failure", "message": fmt.Sprintf("failed to parse JSON rubric: %v", err)})
			return nil
		}
		models.StepLogger.Printf("DEBUG: Parsed JSON criteria for step %d: %+v", se.StepID, criteria)
		        // --- Begin rubric hash logic ---
        rubricHashes := make(map[string]string)
        for _, crit := range criteria {
            // For import, use the stable criterion hash; rubric_set may later append command/counter
            hash := models.CalcRubricCriterionHash(crit.Score, crit.Rubric, crit.Required, crit.HeldOutTest)
            rubricHashes[crit.Title] = hash
        }
        // Fetch, update, and persist task settings: use rubric_set as the single source of truth
        ts, err := models.GetTaskSettings(db, se.TaskID)
        if err != nil {
            models.StepLogger.Printf("Step %d: failed to fetch task settings: %v", se.StepID, err)
        } else {
            ts.RubricSet = rubricHashes
            // Clear legacy field to avoid duplication
            if ts.Rubrics != nil {
                ts.Rubrics = nil
            }
            err = models.UpdateTaskSettings(db, se.TaskID, ts)
            if err != nil {
                models.StepLogger.Printf("Step %d: failed to update task settings with rubric_set hashes: %v", se.StepID, err)
            }
        }
        // --- End rubric hash logic ---
	}
	// After computing hashes, persist updated triggers.files back to step settings (if present)
	if len(filesToCheck) > 0 {
		for filePath := range filesToCheck {
			fullPath := filepath.Join(se.BasePath, filePath)
			hash, err := models.GetSHA256(fullPath)
			if err != nil {
				models.StepLogger.Printf("Step %d: Warning: could not compute hash for %s: %v\n", se.StepID, filePath, err)
				continue
			}
			filesToCheck[filePath] = hash
		}
		// Persist updated hashes to step settings (sanitize deprecated keys before write) and NEVER persist transient force
		persistConfig := config
		persistConfig.Force = false
		settingsMap["rubrics_import"], _ = json.Marshal(persistConfig)
		updatedSettings, _ := json.Marshal(settingsMap)
		updatedSettings = models.SanitizeRawJSONRemoveMHTML(updatedSettings)
		if _, err := db.Exec("UPDATE steps SET settings = $1 WHERE id = $2", string(updatedSettings), se.StepID); err != nil {
			models.StepLogger.Printf("Step %d: Failed to persist updated file hashes to step settings: %v\n", se.StepID, err)
		} else {
			models.StepLogger.Printf("Step %d: Updated file hashes in step settings after import.", se.StepID)
		}
	}
	if config.JSONFile != "" {
		models.StoreStepResult(db, se.StepID, map[string]interface{}{"result": "success", "message": "JSON rubric processed successfully"})
	} else if config.MDFile != "" {
		// Optional: handle MD-only path if needed in future
		models.StoreStepResult(db, se.StepID, map[string]interface{}{"result": "success", "message": "Markdown rubric acknowledged (no-op)"})
	} else {
		models.StoreStepResult(db, se.StepID, map[string]interface{}{"result": "failure", "message": "No rubric file specified"})
	}
	return nil
}
