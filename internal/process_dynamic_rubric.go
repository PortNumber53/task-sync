package internal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strconv"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// ProcessDynamicRubricStep handles the execution of a single dynamic_rubric step.
func ProcessDynamicRubricStep(db *sql.DB, stepExec *models.StepExec, stepLogger *log.Logger) error {
	stepLogger.Printf("Processing dynamic_rubric step ID %d", stepExec.StepID)

	var config models.DynamicRubricConfig
	if err := json.Unmarshal([]byte(stepExec.Settings), &config); err != nil {
		return fmt.Errorf("failed to unmarshal dynamic_rubric settings for step %d: %w", stepExec.StepID, err)
	}

	// Check if there are containers assigned to solutions
	if len(config.DynamicRubric.AssignContainers) == 0 {
		stepLogger.Printf("Warning: No containers assigned in dynamic_rubric step %d. Nothing to process.", stepExec.StepID)
		return nil
	}

	var overallChanged bool

	// 1. Check hashes of associated files
	if config.DynamicRubric.Files != nil {
		for file := range config.DynamicRubric.Files {
			filePath := filepath.Join(stepExec.BasePath, file)
			newHash, err := models.GetSHA256(filePath)
			if err != nil {
				if errors.Is(err, models.ErrEmptyFile) {
					stepLogger.Printf("Warning: Treating empty file as changed: %s in step %d", filePath, stepExec.StepID)
					newHash = ""
				} else {
					stepLogger.Printf("Error hashing file %s for step %d: %v", file, stepExec.StepID, err)
					continue // Skip this file on other errors
				}
			}

			storedHash, ok := config.DynamicRubric.Files[file]
			if !ok || storedHash != newHash {
				stepLogger.Printf("File %s changed for step %d. Old hash: '%s', New hash: '%s'", file, stepExec.StepID, storedHash, newHash)
				overallChanged = true
			}
			config.DynamicRubric.Files[file] = newHash
		}
	}

	// 2. Check the main rubric file
	if len(config.DynamicRubric.Rubrics) == 0 {
		return fmt.Errorf("no rubric files specified in step %d", stepExec.StepID)
	}
	rubricFile := config.DynamicRubric.Rubrics[0] // Assuming the first one is the main one
	criteria, newRubricHash, rubricChanged, err := models.RunRubric(stepExec.BasePath, rubricFile, config.DynamicRubric.Hash)
	if err != nil {
		return fmt.Errorf("error running dynamic_rubric for step %d: %w", stepExec.StepID, err)
	}
	stepLogger.Printf("Step %d: RunRubric completed. Rubric file changed: %t", stepExec.StepID, rubricChanged)

	if rubricChanged {
		config.DynamicRubric.Hash = newRubricHash
		overallChanged = true
	}

	stepsExist, err := models.GeneratedStepsExist(db, stepExec.StepID)
	if err != nil {
		return fmt.Errorf("failed to check for generated steps for parent step %d: %w", stepExec.StepID, err)
	}

	if overallChanged || !stepsExist {
		if !stepsExist {
			stepLogger.Printf("No generated steps found for step %d. Generating new steps.", stepExec.StepID)
		} else {
			stepLogger.Printf("Rubric or associated files changed for step %d. Deleting old steps and generating new ones.", stepExec.StepID)
		}

		if err := models.DeleteGeneratedSteps(db, stepExec.StepID); err != nil {
			return fmt.Errorf("failed to delete generated steps for step %d: %w", stepExec.StepID, err)
		}
		stepLogger.Printf("Successfully deleted all previously generated steps for parent step %d.", stepExec.StepID)

		dependencyOnParent := models.Dependency{ID: stepExec.StepID}

		// Create new steps for each criterion for each solution-container assignment
		for _, crit := range criteria {
			var assignments []models.RubricShellAssignment
			for solution, container := range config.DynamicRubric.AssignContainers {
				if container == "" {
					stepLogger.Printf("Warning: Skipping solution '%s' for criterion '%s' because no container is assigned.", solution, crit.Title)
					continue
				}
				assignments = append(assignments, models.RubricShellAssignment{
					Patch:     solution,
					Container: container,
				})
			}

			if len(assignments) == 0 {
				stepLogger.Printf("Warning: No valid container assignments found for criterion '%s'. Skipping step creation.", crit.Title)
				continue
			}

			title := fmt.Sprintf("Rubric %s: %s", crit.Counter, crit.Title)
			rubricShellSettings := models.RubricShellConfig{
				Command:     crit.HeldOutTest,
				CriterionID: crit.Title,
				Counter:     crit.Counter,
				Score:       crit.Score,
				Required:    crit.Required,
				Rubric:      crit.Rubric,
				DependsOn:   []models.Dependency{dependencyOnParent},
				GeneratedBy: strconv.Itoa(stepExec.StepID),
				Assignments: assignments,
				Files:       config.DynamicRubric.Files, // Pass down the files map
			}

			wrappedSettings := map[string]models.RubricShellConfig{"rubric_shell": rubricShellSettings}
			settingsBytes, err := json.Marshal(wrappedSettings)
			if err != nil {
				stepLogger.Printf("Error marshalling settings for criterion '%s': %v", crit.Title, err)
				continue
			}

			if _, err := models.CreateStep(db, strconv.Itoa(stepExec.TaskID), title, string(settingsBytes)); err != nil {
				stepLogger.Printf("Error creating step for criterion '%s': %v", crit.Title, err)
			} else {
				stepLogger.Printf("Successfully created step for criterion '%s'", crit.Title)
			}
		}

		updatedSettings, err := json.Marshal(config)
		if err != nil {
			return fmt.Errorf("error marshalling updated settings for step %d: %w", stepExec.StepID, err)
		}
		if _, err := db.Exec(`UPDATE steps SET settings = $1, results = '{"result": "success", "info": "Generated/updated child steps."}', updated_at = NOW() WHERE id = $2`, string(updatedSettings), stepExec.StepID); err != nil {
			return fmt.Errorf("error updating settings for step %d: %w", stepExec.StepID, err)
		}
		stepLogger.Printf("Successfully updated settings for parent step %d.", stepExec.StepID)

	} else {
		stepLogger.Printf("Step %d: No changes detected and generated steps exist. Skipping.", stepExec.StepID)
		results := map[string]interface{}{"result": "success", "info": "No changes detected."}
		resultsJSON, _ := json.Marshal(results)
		if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), stepExec.StepID); err != nil {
			return fmt.Errorf("error updating step %d results to success: %w", stepExec.StepID, err)
		}
	}

	return nil
}
