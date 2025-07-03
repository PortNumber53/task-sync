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
func ProcessRubricSetStep(db *sql.DB, stepExec *models.StepExec, stepLogger *log.Logger) error {
	var settings struct {
		RubricSet models.RubricSetConfig `json:"rubric_set"`
	}

	if err := json.Unmarshal([]byte(stepExec.Settings), &settings); err != nil {
		return fmt.Errorf("failed to unmarshal step settings: %w", err)
	}

	config := &settings.RubricSet
	if config.File == "" {
		return fmt.Errorf("file path is not specified in the rubric_set settings")
	}

	filesToCheck := map[string]string{
		"file":          config.File,
		"held_out_test": config.HeldOutTest,
		"solution_1":    config.Solution1,
		"solution_2":    config.Solution2,
		"solution_3":    config.Solution3,
		"solution_4":    config.Solution4,
	}

	changed := false
	newHashes := make(map[string]string)

	for key, file := range filesToCheck {
		if file == "" {
			continue
		}
		filePath := filepath.Join(stepExec.LocalPath, file)
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			return fmt.Errorf("%s file does not exist at path: %s", key, filePath)
		}

		currentHash, err := models.GetSHA256(filePath)
		if err != nil {
			if err == models.ErrEmptyFile {
				stepLogger.Printf("Warning: %s file is empty, skipping: %s", key, filePath)
				continue // Skip this file
			}
			return fmt.Errorf("failed to calculate hash for %s: %w", filePath, err)
		}

		if oldHash, ok := config.Hashes[key]; !ok || oldHash != currentHash {
			changed = true
		}
		newHashes[key] = currentHash
	}

	if !changed {
		stepLogger.Println("No changes detected in rubric files. Skipping.")
		return nil
	}

	stepLogger.Println("Changes detected in rubric files. Processing steps.")

	markdownFilePath := filepath.Join(stepExec.LocalPath, config.File)
	criteria, err := models.ParseRubric(markdownFilePath)
	if err != nil {
		return fmt.Errorf("failed to parse rubric markdown: %w", err)
	}
	stepLogger.Printf("Found %d criteria in %s", len(criteria), markdownFilePath)

	existingSteps, err := models.GetGeneratedSteps(db, stepExec.StepID)
	if err != nil {
		return fmt.Errorf("failed to get existing generated steps: %w", err)
	}
	stepLogger.Printf("Found %d existing generated steps.", len(existingSteps))

	if len(criteria) == 0 {
		stepLogger.Printf("No criteria found in the rubric file, deleting existing steps.")
		// Delete all existing generated steps
		for _, step := range existingSteps {
			if err := models.DeleteStep(db, step.StepID); err != nil {
				stepLogger.Printf("Failed to delete obsolete step %d: %v", step.StepID, err)
			}
		}
		return nil
	}

	existingStepsByCriterion := make(map[string][]models.StepExec)
	for _, step := range existingSteps {
		var stepSettings models.RubricShellConfig
		var wrapped map[string]json.RawMessage
		if err := json.Unmarshal([]byte(step.Settings), &wrapped); err == nil && wrapped["rubric_shell"] != nil {
			if err := json.Unmarshal(wrapped["rubric_shell"], &stepSettings); err != nil {
				stepLogger.Printf("failed to unmarshal nested rubric_shell settings for step %d, skipping: %v", step.StepID, err)
				continue
			}
		} else {
			stepLogger.Printf("Step %d has malformed settings, skipping: %v", step.StepID, err)
			continue
		}

		if stepSettings.CriterionID != "" {
			existingStepsByCriterion[stepSettings.CriterionID] = append(existingStepsByCriterion[stepSettings.CriterionID], step)
		}
	}

	keptStepIDs := make(map[int]bool)

	for _, criterion := range criteria {
		title := fmt.Sprintf("Rubric %s: %s", criterion.Counter, criterion.Title)
		rubricShellSettings := models.RubricShellConfig{
			Command:     criterion.HeldOutTest,
			CriterionID: criterion.Title,
			Counter:     criterion.Counter,
			Score:       criterion.Score,
			Required:    criterion.Required,
			DependsOn:   []models.Dependency{{ID: stepExec.StepID}},
			GeneratedBy: strconv.Itoa(stepExec.StepID),
		}

		if stepsForCriterion, ok := existingStepsByCriterion[criterion.Title]; ok && len(stepsForCriterion) > 0 {
			stepToUpdate := stepsForCriterion[0]
			keptStepIDs[stepToUpdate.StepID] = true

			var existingSettings models.RubricShellConfig
			var wrapped map[string]json.RawMessage
			if err := json.Unmarshal([]byte(stepToUpdate.Settings), &wrapped); err == nil && wrapped["rubric_shell"] != nil {
				_ = json.Unmarshal(wrapped["rubric_shell"], &existingSettings)
			}

			needsUpdate := stepToUpdate.Title != title ||
				existingSettings.Command != rubricShellSettings.Command ||
				existingSettings.Score != rubricShellSettings.Score ||
				existingSettings.Required != rubricShellSettings.Required

			if needsUpdate {
				wrappedSettings := map[string]models.RubricShellConfig{"rubric_shell": rubricShellSettings}
				settingsBytes, _ := json.Marshal(wrappedSettings)
				if err := models.UpdateStep(db, stepToUpdate.StepID, title, string(settingsBytes)); err != nil {
					stepLogger.Printf("Failed to update step %d: %v", stepToUpdate.StepID, err)
				} else {
					stepLogger.Printf("Updated step %d for criterion %s", stepToUpdate.StepID, criterion.Title)
				}
			}
		} else {
			wrappedSettings := map[string]models.RubricShellConfig{"rubric_shell": rubricShellSettings}
			settingsBytes, _ := json.Marshal(wrappedSettings)
			newStepID, err := models.CreateStep(db, strconv.Itoa(stepExec.TaskID), title, string(settingsBytes))
			if err != nil {
				stepLogger.Printf("Failed to create new step for criterion %s: %v", criterion.Title, err)
			} else {
				stepLogger.Printf("Created new step %d for criterion %s", newStepID, criterion.Title)
			}
		}
	}

	for _, step := range existingSteps {
		if _, isKept := keptStepIDs[step.StepID]; !isKept {
			if err := models.DeleteStep(db, step.StepID); err != nil {
				stepLogger.Printf("Failed to delete obsolete step %d: %v", step.StepID, err)
			}
		}
	}

	// Update the hashes in the settings
	config.Hashes = newHashes
	wrappedSettings := map[string]models.RubricSetConfig{"rubric_set": *config}
	newSettingsBytes, err := json.Marshal(wrappedSettings)
	if err != nil {
		return fmt.Errorf("failed to marshal updated settings: %w", err)
	}

	if err := models.UpdateStep(db, stepExec.StepID, stepExec.Title, string(newSettingsBytes)); err != nil {
		return fmt.Errorf("failed to update rubric_set step with new hashes: %w", err)
	}

	stepLogger.Println("Successfully updated rubric_set step with new hashes.")
	return nil
}
