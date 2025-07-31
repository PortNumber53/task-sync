package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strconv"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// processAllRubricSetSteps finds and executes all rubric_set steps.
func processAllRubricSetSteps(db *sql.DB, logger *log.Logger) error {
	query := `
		SELECT s.id, s.task_id, s.title, s.settings, t.base_path
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
		if err := rows.Scan(&stepExec.StepID, &stepExec.TaskID, &stepExec.Title, &stepExec.Settings, &stepExec.BasePath); err != nil {
			logger.Printf("failed to scan rubric_set step: %v", err)
			continue
		}

		stepLogger := log.New(os.Stdout, fmt.Sprintf("STEP %d [rubric_set]: ", stepExec.StepID), log.Ldate|log.Ltime|log.Lshortfile)

		if err := ProcessRubricSetStep(db, &stepExec, stepLogger, false); err != nil {
			logger.Printf("failed to process rubric_set step %d: %v", stepExec.StepID, err)
		}
	}

	return nil
}

// ProcessRubricSetStep handles the execution of a rubric_set step.
// It parses the main rubric file, updates the task-level settings with container assignments,
// and then creates, updates, or deletes child rubric_shell steps to match the rubric criteria.
func ProcessRubricSetStep(db *sql.DB, stepExec *models.StepExec, stepLogger *log.Logger, force bool) error {
	var rerunNeeded bool

	var settings struct {
		RubricSet models.RubricSetConfig `json:"rubric_set"`
	}
	if err := json.Unmarshal([]byte(stepExec.Settings), &settings); err != nil {
		return fmt.Errorf("failed to unmarshal step settings: %w", err)
	}
	config := &settings.RubricSet

	stepLogger.Printf("Debug: Unmarshaled RubricSetConfig: %+v", config)

	// Hash the main rubric file and store in Files map under the rubric file name
	mainPath := config.File
	mainFileName := filepath.Base(config.File)
	if !filepath.IsAbs(mainPath) {
		mainPath = filepath.Join(stepExec.BasePath, mainPath)
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
			filePath = filepath.Join(stepExec.BasePath, filePath)
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
	stepLogger.Printf("Debug: Updated config.Files after hashing: %v", config.Files)

	// If any hash changed, or hashes were missing, update and persist
	if rerunNeeded {
		stepLogger.Printf("Debug: Rerun needed, updating settings for step %d", stepExec.StepID)
		updatedSettings, err := json.Marshal(settings)
		if err != nil {
			return fmt.Errorf("failed to marshal updated settings: %w", err)
		}
		if _, err := db.Exec(`UPDATE steps SET settings = $1 WHERE id = $2`, string(updatedSettings), stepExec.StepID); err != nil {
			return fmt.Errorf("failed to update step settings in db: %w", err)
		}
	}

	// Prioritize rubrics.json if it exists, otherwise use config.File
	jsonPath := filepath.Join(stepExec.BasePath, "rubrics.json")
	if _, err := os.Stat(jsonPath); err == nil {
		markdownFilePath := jsonPath
		criteria, err := models.ParseRubric(markdownFilePath)
		if err != nil {
			return fmt.Errorf("failed to parse rubric markdown: %w", err)
		}
		stepLogger.Printf("DEBUG: Parsing rubric from JSON file: %s", markdownFilePath)
		for i, crit := range criteria {
			rubricSnippet := crit.Rubric
			if len(rubricSnippet) > 50 {
				rubricSnippet = rubricSnippet[:50]
			}
			stepLogger.Printf("DEBUG: Criterion %d - ID: %s, Rubric snippet: %s", i, crit.Title, rubricSnippet)
		}
		stepLogger.Printf("Found %d criteria in %s", len(criteria), markdownFilePath)

		// 2. Fetch existing generated steps.
		existingSteps, err := models.GetGeneratedSteps(db, stepExec.StepID)
		if err != nil {
			return fmt.Errorf("failed to get existing generated steps: %w", err)
		}
		stepLogger.Printf("Found %d existing generated steps for parent step %d.", len(existingSteps), stepExec.StepID)

		// After fetching existing steps, clean up any with invalid settings
		for _, step := range existingSteps {
			var settings struct {
				RubricShell models.RubricShellConfig `json:"rubric_shell"`
			}
			if err := json.Unmarshal([]byte(step.Settings), &settings); err != nil {
				if err := models.DeleteStep(db, step.ID); err != nil {
					stepLogger.Printf("Delete error for invalid step %d: %v", step.ID, err)
				} else {
					stepLogger.Printf("Deleted invalid step %d due to unmarshal error", step.ID)
				}
			}
		}

		// Now build the map of valid existing steps grouped by CriterionID
		existingStepsByCriterion := make(map[string][]models.Step)
		for _, step := range existingSteps {
			var settings struct {
				RubricShell models.RubricShellConfig `json:"rubric_shell"`
			}
			if err := json.Unmarshal([]byte(step.Settings), &settings); err == nil {
				criterionID := settings.RubricShell.CriterionID
				existingStepsByCriterion[criterionID] = append(existingStepsByCriterion[criterionID], step)
			}
		}

		// Make a set of current CriterionIDs for quick lookup
		currentCriterionIDs := make(map[string]struct{})
		for _, crit := range criteria {
			currentCriterionIDs[crit.Title] = struct{}{}
		}

		// Fetch rubric hashes from task settings
		settingsObj, err := models.GetTaskSettings(db, stepExec.TaskID)
		var rubricHashes map[string]string
		if err == nil && settingsObj != nil && settingsObj.Rubrics != nil {
			rubricHashes = settingsObj.Rubrics
		} else {
			rubricHashes = map[string]string{}
		}

		// Reconcile each criterion: ensure only one step exists and is correct
		for _, crit := range criteria {
			criterionID := crit.Title
			steps, exists := existingStepsByCriterion[criterionID]
			// Calculate current hash for this criterion
			currentHash := models.CalcRubricCriterionHash(crit.Score, crit.Rubric, crit.Required, crit.HeldOutTest)
			storedHash, hashExists := rubricHashes[criterionID]
			shouldUpsert := !hashExists || storedHash != currentHash || force

			newRubricShellConfig := models.RubricShellConfig{
				Command:     crit.HeldOutTest,
				CriterionID: crit.Title,
				Counter:     crit.Counter,
				Score:       crit.Score,
				Required:    crit.Required,
				Rubric:      crit.Rubric,
				Rerun:       force || shouldUpsert, // Always set Rerun true if force is true
				DependsOn:   []models.Dependency{{ID: stepExec.StepID}},
				GeneratedBy: fmt.Sprintf("%d", stepExec.StepID),
				Assignments: []models.RubricShellAssignment{},
				Files:       config.Files, // Inherit Files map from RubricSetConfig
			}
			wrappedSettings := map[string]models.RubricShellConfig{"rubric_shell": newRubricShellConfig}
			childSettingsJSON, err := json.Marshal(wrappedSettings)
			if err != nil {
				return fmt.Errorf("marshal error: %w", err)
			}
			if !exists || len(steps) == 0 {
				// No step exists, create a new one
				stepLogger.Printf("[TRACE] Creating rubric_shell step for criterion %s: rerun=%v, settings=%s", criterionID, newRubricShellConfig.Rerun, string(childSettingsJSON))
				stepID, err := models.CreateStep(db, strconv.Itoa(stepExec.TaskID), criterionID, string(childSettingsJSON))
				if err != nil {
					return fmt.Errorf("create error: %w", err)
				}
				stepLogger.Printf("Created step %d for criterion %s", stepID, criterionID)
				// Immediately update the map so future logic sees only the canonical step
				newStep := models.Step{ID: stepID, Title: criterionID, Settings: string(childSettingsJSON)}
				existingStepsByCriterion[criterionID] = []models.Step{newStep}
			} else {
				// Steps exist, keep the first one and delete duplicates if any
				keepStep := steps[0]
				if len(steps) > 1 {
					for _, dupStep := range steps[1:] {
						if err := models.DeleteStep(db, dupStep.ID); err != nil {
							stepLogger.Printf("Delete error for duplicate step %d: %v", dupStep.ID, err)
						} else {
							stepLogger.Printf("Deleted duplicate step %d for criterion %s", dupStep.ID, criterionID)
						}
					}
				}
				// Now update the kept step if config differs or hash changed
				var existingWrapped map[string]models.RubricShellConfig
				if err := json.Unmarshal([]byte(keepStep.Settings), &existingWrapped); err == nil {
					existingConfig := existingWrapped["rubric_shell"]
					// Preserve results if present in the existing config and not intentionally resetting
					if existingConfig.Results == nil && newRubricShellConfig.Results == nil {
						// Try to load from results column
						var resultsJSON sql.NullString
						err := db.QueryRow("SELECT results FROM steps WHERE id = $1", keepStep.ID).Scan(&resultsJSON)
						if err == nil && resultsJSON.Valid && resultsJSON.String != "" && resultsJSON.String != "null" {
							var resultsCol map[string]interface{}
							if err := json.Unmarshal([]byte(resultsJSON.String), &resultsCol); err == nil && len(resultsCol) > 0 {
								resultsStr := make(map[string]string, len(resultsCol))
								allString := true
								for k, v := range resultsCol {
									strVal, ok := v.(string)
									if ok {
										resultsStr[k] = strVal
									} else {
										allString = false
										stepLogger.Printf("[WARN] Non-string result value for key '%s' in results column for step %d", k, keepStep.ID)
									}
								}
								if allString {
									newRubricShellConfig.Results = resultsStr
									stepLogger.Printf("[TRACE] Merged results from results column for step %d", keepStep.ID)
								} else {
									stepLogger.Printf("[WARN] Skipped merging results column for step %d due to non-string values", keepStep.ID)
								}
							}
						}
					} else if existingConfig.Results != nil && newRubricShellConfig.Results == nil {
						newRubricShellConfig.Results = existingConfig.Results
						stepLogger.Printf("[TRACE] Merged results from settings for step %d", keepStep.ID)
					}
					// Preserve rerun:true if it was set in the existing config
					if existingConfig.Rerun {
						newRubricShellConfig.Rerun = true
					}
					wrappedSettings := map[string]models.RubricShellConfig{"rubric_shell": newRubricShellConfig}
					childSettingsJSON, err := json.Marshal(wrappedSettings)
					if err != nil {
						return fmt.Errorf("marshal error: %w", err)
					}
					if shouldUpsert || !reflect.DeepEqual(existingConfig, newRubricShellConfig) || force {
						stepLogger.Printf("[TRACE] Updating rubric_shell step %d for criterion %s: rerun=%v, settings=%s", keepStep.ID, criterionID, newRubricShellConfig.Rerun, string(childSettingsJSON))
						_, err = db.Exec("UPDATE steps SET settings = $1, title = $2, updated_at = NOW() WHERE id = $3", string(childSettingsJSON), criterionID, keepStep.ID)
						if err != nil {
							return fmt.Errorf("update error: %w", err)
						}
						stepLogger.Printf("Updated step %d for criterion %s", keepStep.ID, criterionID)
						// Update the map so only the canonical step is present
						keepStep.Settings = string(childSettingsJSON)
						existingStepsByCriterion[criterionID] = []models.Step{keepStep}
					}
				} else {
					// Handle unmarshal error by updating anyway (should not happen after pre-pass)
					wrappedSettings := map[string]models.RubricShellConfig{"rubric_shell": newRubricShellConfig}
					childSettingsJSON, err := json.Marshal(wrappedSettings)
					if err != nil {
						return fmt.Errorf("marshal error: %w", err)
					}
					_, err = db.Exec("UPDATE steps SET settings = $1, title = $2, updated_at = NOW() WHERE id = $3", string(childSettingsJSON), criterionID, keepStep.ID)
					if err != nil {
						return fmt.Errorf("update error due to unmarshal: %w", err)
					}
					stepLogger.Printf("Updated step %d for criterion %s due to unmarshal error", keepStep.ID, criterionID)
					// Update the map so only the canonical step is present
					keepStep.Settings = string(childSettingsJSON)
					existingStepsByCriterion[criterionID] = []models.Step{keepStep}
				}
			}
		}

		// Delete steps for CriterionIDs not in current set
		for criterionID, steps := range existingStepsByCriterion {
			if _, ok := currentCriterionIDs[criterionID]; !ok {
				for _, step := range steps {
					if err := models.DeleteStep(db, step.ID); err != nil {
						stepLogger.Printf("Delete error for obsolete step %d: %v", step.ID, err)
					} else {
						stepLogger.Printf("Deleted obsolete step %d for criterion %s", step.ID, criterionID)
					}
				}
			}
		}

		stepLogger.Println("Successfully reconciled rubric_set step.")
	}

	return nil
}

// Helper function to check if slice contains string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// getTaskContainers and assignContainersToSolutions are now in process_rubric_set_helpers.go
