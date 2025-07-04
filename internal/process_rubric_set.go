package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// processAllRubricSetSteps finds and executes all rubric_set steps.
func processAllRubricSetSteps(db *sql.DB, logger *log.Logger) error {
	query := `
		SELECT s.id, s.task_id, s.title, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.settings ? 'rubric_set'
	`
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query for rubric_set steps: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var stepExec models.StepExec
		if err := rows.Scan(&stepExec.StepID, &stepExec.TaskID, &stepExec.Title, &stepExec.Settings, &stepExec.LocalPath); err != nil {
			logger.Printf("failed to scan rubric_set step: %v", err)
			continue
		}

		stepLogger := log.New(os.Stdout, fmt.Sprintf("STEP %d [rubric_set]: ", stepExec.StepID), log.Ldate|log.Ltime|log.Lshortfile)

		if err := ProcessRubricSetStep(db, &stepExec, stepLogger); err != nil {
			logger.Printf("failed to process rubric_set step %d: %v", stepExec.StepID, err)
		}
	}

	return nil
}

// ProcessRubricSetStep handles the execution of a rubric_set step.
// It parses the main rubric file and then, for each solution patch assigned in
// assign_containers, it creates, updates, or deletes child rubric_shell steps
// to match the rubric criteria.
func ProcessRubricSetStep(db *sql.DB, stepExec *models.StepExec, stepLogger *log.Logger) error {
	var settings struct {
		RubricSet models.RubricSetConfig `json:"rubric_set"`
	}
	if err := json.Unmarshal([]byte(stepExec.Settings), &settings); err != nil {
		return fmt.Errorf("failed to unmarshal step settings: %w", err)
	}
	config := &settings.RubricSet

	// 1. Parse the main rubric file to get the list of criteria.
	markdownFilePath := filepath.Join(stepExec.LocalPath, config.File)
	criteria, err := models.ParseRubric(markdownFilePath)
	if err != nil {
		return fmt.Errorf("failed to parse rubric markdown: %w", err)
	}
	stepLogger.Printf("Found %d criteria in %s", len(criteria), markdownFilePath)

		// 2. Delete all previously generated steps to ensure a clean slate.
	if err := models.DeleteGeneratedSteps(db, stepExec.StepID); err != nil {
		return fmt.Errorf("failed to delete existing generated steps: %w", err)
	}
	stepLogger.Printf("Successfully deleted all previously generated steps for parent step %d.", stepExec.StepID)

	// 3. Create a new rubric_shell step for each criterion.
	if len(config.AssignContainers) == 0 {
		stepLogger.Println("No containers assigned in 'assign_containers'. Nothing to do.")
		return nil
	}

	for _, criterion := range criteria {
		title := fmt.Sprintf("Rubric %s: %s", criterion.Counter, criterion.Title)

		rubricShellSettings := models.RubricShellConfig{
			Command:          criterion.HeldOutTest,
			CriterionID:      criterion.Title,
			Counter:          criterion.Counter,
			Score:            criterion.Score,
			Required:         criterion.Required,
			DependsOn:        []models.Dependency{{ID: stepExec.StepID}},
			GeneratedBy:      strconv.Itoa(stepExec.StepID),
			AssignContainers: config.AssignContainers,
		}

		// Create the new step.
		wrappedSettings := map[string]models.RubricShellConfig{"rubric_shell": rubricShellSettings}
		settingsBytes, _ := json.Marshal(wrappedSettings)
		newStepID, err := models.CreateStep(db, strconv.Itoa(stepExec.TaskID), title, string(settingsBytes))
		if err != nil {
			stepLogger.Printf("Failed to create new step for criterion %s: %v", criterion.Title, err)
		} else {
			stepLogger.Printf("Created new step %d for criterion %s", newStepID, criterion.Title)
		}
	}

	stepLogger.Println("Successfully processed rubric_set step.")
	return nil
}
