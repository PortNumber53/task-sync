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
// It parses the main rubric file, updates the task-level settings with container assignments,
// and then creates, updates, or deletes child rubric_shell steps to match the rubric criteria.
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

	// 2. Fetch existing generated steps.
	existingSteps, err := models.GetGeneratedSteps(db, stepExec.StepID)
	if err != nil {
		return fmt.Errorf("failed to get existing generated steps: %w", err)
	}
	stepLogger.Printf("Found %d existing generated steps for parent step %d.", len(existingSteps), stepExec.StepID)

	// 3. Create a map of existing steps by criterion ID for efficient lookup.
	existingStepsMap := make(map[string]models.Step)
	for _, step := range existingSteps {
		var stepSettings struct {
			RubricShell models.RubricShellConfig `json:"rubric_shell"`
		}
		if err := json.Unmarshal([]byte(step.Settings), &stepSettings); err == nil {
			existingStepsMap[stepSettings.RubricShell.CriterionID] = step
		}
	}

	// 4. Reconcile steps based on the rubric file.
	for _, criterion := range criteria {
		title := fmt.Sprintf("Rubric %s: %s", criterion.Counter, criterion.Title)

		newRubricShellSettings := models.RubricShellConfig{
			Command:     criterion.HeldOutTest,
			CriterionID: criterion.Title,
			Counter:     criterion.Counter,
			Score:       criterion.Score,
			Required:    criterion.Required,
			DependsOn:   []models.Dependency{{ID: stepExec.StepID}},
			GeneratedBy: strconv.Itoa(stepExec.StepID),
		}

		wrappedSettings := map[string]models.RubricShellConfig{"rubric_shell": newRubricShellSettings}
		newSettingsBytes, _ := json.Marshal(wrappedSettings)

		if existingStep, ok := existingStepsMap[criterion.Title]; ok {
			// Step exists, check if it needs an update.
			if existingStep.Title != title || string(existingStep.Settings) != string(newSettingsBytes) {
				if err := models.UpdateStep(db, existingStep.ID, title, string(newSettingsBytes)); err != nil {
					stepLogger.Printf("Failed to update step %d for criterion '%s': %v", existingStep.ID, criterion.Title, err)
				} else {
					stepLogger.Printf("Updated step %d for criterion '%s'", existingStep.ID, criterion.Title)
				}
			}
			delete(existingStepsMap, criterion.Title) // Mark as processed
		} else {
			// Step doesn't exist, create it.
			if _, err := models.CreateStep(db, strconv.Itoa(stepExec.TaskID), title, string(newSettingsBytes)); err != nil {
				stepLogger.Printf("Failed to create new step for criterion '%s': %v", criterion.Title, err)
			}
		}
	}

	// 5. Delete steps that are no longer in the rubric.
	for _, stepToDelete := range existingStepsMap {
		if err := models.DeleteStep(db, stepToDelete.ID); err != nil {
			stepLogger.Printf("Failed to delete obsolete step %d: %v", stepToDelete.ID, err)
		} else {
			stepLogger.Printf("Deleted obsolete step %d", stepToDelete.ID)
		}
	}

	stepLogger.Println("Successfully reconciled rubric_set step.")
	return nil
}
