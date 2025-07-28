package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
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

		var settingsMap map[string]json.RawMessage
		if err := json.Unmarshal([]byte(step.Settings), &settingsMap); err != nil {
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "invalid step config"})
			models.StepLogger.Printf("Step %d: invalid step config: %v\n", step.StepID, err)
			continue
		}

		var config models.RubricsImportConfig
		if rubricsImportJSON, ok := settingsMap["rubrics_import"]; ok {
			if err := json.Unmarshal(rubricsImportJSON, &config); err != nil {
				models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": fmt.Sprintf("invalid rubrics_import config: %v", err)})
				models.StepLogger.Printf("Step %d: invalid rubrics_import config: %v\n", step.StepID, err)
				continue
			}
		} else {
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "rubrics_import not found in settings"})
			models.StepLogger.Printf("Step %d: rubrics_import not found in step settings\n", step.StepID)
			continue
		}

		// Add logging to inspect config values
		models.StepLogger.Printf("DEBUG: Step %d: config: %+v\n", step.StepID, config)
		models.StepLogger.Printf("DEBUG: Step %d: config.MHTMLFile: %s\n", step.StepID, config.MHTMLFile)
		models.StepLogger.Printf("DEBUG: Step %d: config.MDFile: %s\n", step.StepID, config.MDFile)
		models.StepLogger.Printf("DEBUG: Step %d: config.JSONFile: %s\n", step.StepID, config.JSONFile)

		ok, err := models.CheckDependencies(db, &step)
		if err != nil {
			models.StepLogger.Printf("Step %d: error checking dependencies: %v\n", step.StepID, err)
			continue
		}
		if !ok {
			models.StepLogger.Printf("Step %d: waiting for dependencies to complete\n", step.StepID)
			continue
		}

		if config.JSONFile != "" {
			jsonPath := filepath.Join(step.BasePath, config.JSONFile)
			criteria, err := models.ParseRubric(jsonPath)
			if err != nil {
				models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": fmt.Sprintf("failed to parse JSON rubric: %v", err)})
				continue
			}
			models.StepLogger.Printf("DEBUG: Parsed JSON criteria for step %d: %+v", step.StepID, criteria)
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "success", "message": "JSON rubric processed successfully"})
		} else if config.MHTMLFile != "" {
			mhtmlFile := filepath.Join(step.BasePath, config.MHTMLFile)
			mdFile := filepath.Join(step.BasePath, config.MDFile)
			models.StepLogger.Printf("Processing rubrics_import step: %s -> %s\n", mhtmlFile, mdFile)
			err = models.ProcessRubricsMHTML(mhtmlFile, mdFile)
			if err != nil {
				models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": fmt.Sprintf("failed to process MHTML file: %v", err)})
				continue
			}
		} else if config.MDFile != "" {
			// Handle MD file logic if needed
		} else {
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "No rubric file specified"})
		}
	}
	return nil
}
