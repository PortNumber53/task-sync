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
	var rerunNeeded bool

	var settings struct {
		RubricSet models.RubricSetConfig `json:"rubric_set"`
	}
	if err := json.Unmarshal([]byte(stepExec.Settings), &settings); err != nil {
		return fmt.Errorf("failed to unmarshal step settings: %w", err)
	}
	config := &settings.RubricSet

	// Hash the main rubric file and store in Files map under the rubric file name
	mainPath := config.File
	mainFileName := filepath.Base(config.File)
	if !filepath.IsAbs(mainPath) {
		mainPath = filepath.Join(stepExec.LocalPath, mainPath)
	}
	stepLogger.Printf("DEBUG: main rubric file fullPath=%s", mainPath)
	mainInfo, err := os.Stat(mainPath)
	if err != nil {
		stepLogger.Printf("Warning: could not stat main rubric file (%s): %v", mainPath, err)
		rerunNeeded = true
	} else if mainInfo.IsDir() {
		stepLogger.Printf("Skipping directory for main rubric file: %s", mainPath)
	} else {
		hash, err := models.GetSHA256(mainPath)
		if err != nil {
			stepLogger.Printf("Warning: could not compute hash for main rubric file (%s): %v", mainPath, err)
			rerunNeeded = true
		} else {
			if old, ok := config.Files[mainFileName]; !ok || old != hash {
				rerunNeeded = true
			}
			config.Files[mainFileName] = hash
		}
	}

	// Hash all files in config.Files (keys are file names)
	for fileName := range config.Files {
		filePath := fileName
		if !filepath.IsAbs(filePath) {
			filePath = filepath.Join(stepExec.LocalPath, filePath)
		}
		stepLogger.Printf("DEBUG: fileName=%s filePath=%s", fileName, filePath)
		info, err := os.Stat(filePath)
		if err != nil {
			stepLogger.Printf("Warning: could not stat %s: %v", filePath, err)
			config.Files[fileName] = ""
			rerunNeeded = true
			continue
		}
		if info.IsDir() {
			stepLogger.Printf("Skipping directory: %s", filePath)
			config.Files[fileName] = ""
			continue
		}
		hash, err := models.GetSHA256(filePath)
		if err != nil {
			stepLogger.Printf("Warning: could not compute hash for %s: %v", filePath, err)
			config.Files[fileName] = ""
			rerunNeeded = true
			continue
		}
		if old, ok := config.Files[fileName]; !ok || old != hash {
			rerunNeeded = true
		}
		config.Files[fileName] = hash
	}

	// If any hash changed, or hashes were missing, update and persist
	if rerunNeeded {
		stepLogger.Printf("File hashes changed or missing; will update and persist hashes.")

		// Persist updated hashes to step settings
		wrapped := map[string]interface{}{ "rubric_set": config }
		if updated, err := json.Marshal(wrapped); err == nil {
			stepLogger.Printf("DEBUG: Marshaled settings: %s", string(updated))
			_, _ = db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(updated), stepExec.StepID)
		} else {
			stepLogger.Printf("ERROR: Failed to marshal settings for step %d: %v", stepExec.StepID, err)
		}
	}

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
	updatedOrCreated := false
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

		// Preserve last_run if present in the existing step
		if existingStep, ok := existingStepsMap[criterion.Title]; ok {
			var existingStepSettings struct {
				RubricShell models.RubricShellConfig `json:"rubric_shell"`
			}
			shouldUpdate := false
			if err := json.Unmarshal([]byte(existingStep.Settings), &existingStepSettings); err == nil {
				// Compare relevant fields
				rsOld := existingStepSettings.RubricShell
				rsNew := newRubricShellSettings
				if rsOld.Command != rsNew.Command ||
					rsOld.CriterionID != rsNew.CriterionID ||
					rsOld.Counter != rsNew.Counter ||
					rsOld.Score != rsNew.Score ||
					rsOld.Required != rsNew.Required ||
					len(rsOld.DependsOn) != len(rsNew.DependsOn) ||
					rsOld.GeneratedBy != rsNew.GeneratedBy {
					shouldUpdate = true
				} else {
					for i := range rsOld.DependsOn {
						if rsOld.DependsOn[i] != rsNew.DependsOn[i] {
							shouldUpdate = true
							break
						}
					}
				}
				if existingStep.Title != title {
					shouldUpdate = true
				}
			} else {
				shouldUpdate = true // Could not unmarshal, safest to update
			}
			if shouldUpdate {
				if err := models.UpdateStep(db, existingStep.ID, title, string(newSettingsBytes)); err != nil {
					stepLogger.Printf("Failed to update step %d for criterion '%s': %v", existingStep.ID, criterion.Title, err)
				} else {
					stepLogger.Printf("Updated step %d for criterion '%s'", existingStep.ID, criterion.Title)
					updatedOrCreated = true
				}
			}
			delete(existingStepsMap, criterion.Title) // Mark as processed
		} else {
			// Step doesn't exist, create it.
			if _, err := models.CreateStep(db, strconv.Itoa(stepExec.TaskID), title, string(newSettingsBytes)); err != nil {
				stepLogger.Printf("Failed to create new step for criterion '%s': %v", criterion.Title, err)
			} else {
				updatedOrCreated = true
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

	// 6. Force re-run: set LastRun=nil for all upserted rubric_shell steps
	generatedSteps, err := models.GetGeneratedSteps(db, stepExec.StepID)
	if err != nil {
		stepLogger.Printf("Failed to fetch generated rubric_shell steps for force re-run: %v", err)
	} else {
		if len(generatedSteps) > 0 {
			if updatedOrCreated {
				for _, step := range generatedSteps {
					var holder models.StepConfigHolder
					if err := json.Unmarshal([]byte(step.Settings), &holder); err == nil && holder.RubricShell != nil {
						holder.RubricShell.LastRun = nil
						updatedSettings, err := json.Marshal(map[string]interface{}{ "rubric_shell": holder.RubricShell })
						if err == nil {
							if err := models.UpdateStepSettings(db, step.ID, string(updatedSettings)); err == nil {
								stepLogger.Printf("Forced re-run: reset LastRun for rubric_shell step %d due to changes", step.ID)
							} else {
								stepLogger.Printf("Failed to update settings for forced re-run of step %d: %v", step.ID, err)
							}
						} else {
							stepLogger.Printf("Failed to marshal settings for forced re-run of step %d: %v", step.ID, err)
						}
					}
				}
			} else {
				stepLogger.Printf("No changes detected, skipping LastRun reset")
			}
		} else {
			stepLogger.Printf("No generated steps to update")
		}
	}

	stepLogger.Println("Successfully reconciled rubric_set step.")
	return nil
}
