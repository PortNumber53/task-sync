package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"github.com/PortNumber53/task-sync/pkg/models"
	"strconv"
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
	stepLogger.Printf("Debug: RubricSetConfig AssignContainers: %+v", config.AssignContainers)

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

	// Get or assign containers for solution files
	containers, err := getTaskContainers(db, stepExec.TaskID, stepLogger)
	if err != nil {
		stepLogger.Printf("Warning: failed to get task containers: %v", err)
	}

	// If no containers are assigned, try to auto-assign from available containers
	if len(config.AssignContainers) == 0 && len(containers) > 0 {
		assignments := assignContainersToSolutions(config, containers, stepLogger)
		config.AssignContainers = assignments
		stepLogger.Printf("Debug: Assignments after assignContainersToSolutions: %+v", assignments)
		
		// Update the step settings with the new container assignments
		if len(config.AssignContainers) > 0 {
			wrapped := map[string]interface{}{ "rubric_set": config }
			if updated, err := json.Marshal(wrapped); err == nil {
				stepLogger.Printf("Updating step with auto-assigned containers")
				_, _ = db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(updated), stepExec.StepID)
			}
		}
	}

	if len(config.AssignContainers) == 0 {
		return fmt.Errorf("no containers available for assignment and none explicitly assigned")
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

		// Reconcile each criterion: ensure only one step exists and is correct
		for _, crit := range criteria {
			criterionID := crit.Title
			steps, exists := existingStepsByCriterion[criterionID]
			assignments := make([]models.SolutionAssignment, 0, len(config.AssignContainers))
			for patch, container := range config.AssignContainers {
				assignments = append(assignments, models.SolutionAssignment{Patch: patch, Container: container})
			}
			newRubricShellConfig := models.RubricShellConfig{
				Command:     crit.HeldOutTest,
				CriterionID: crit.Title,
				Counter:     crit.Counter,
				Score:       crit.Score,
				Required:    crit.Required,
				Rerun:       force,
				DependsOn:   []models.Dependency{{ID: stepExec.StepID}},
				GeneratedBy: fmt.Sprintf("%d", stepExec.StepID),
				Assignments: assignments,
				Files:       config.Files,  // Inherit Files map from RubricSetConfig
			}
			wrappedSettings := map[string]models.RubricShellConfig{"rubric_shell": newRubricShellConfig}
			childSettingsJSON, err := json.Marshal(wrappedSettings)
			if err != nil {
				return fmt.Errorf("marshal error: %w", err)
			}
			if !exists || len(steps) == 0 {
				// No step exists, create a new one
				stepID, err := models.CreateStep(db, strconv.Itoa(stepExec.TaskID), criterionID, string(childSettingsJSON))
				if err != nil {
					return fmt.Errorf("create error: %w", err)
				}
				stepLogger.Printf("Created step %d for criterion %s", stepID, criterionID)
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
				// Now update the kept step if config differs
				var existingConfig models.RubricShellConfig
				if err := json.Unmarshal([]byte(keepStep.Settings), &existingConfig); err == nil {
					if !reflect.DeepEqual(existingConfig, newRubricShellConfig) {
						_, err = db.Exec("UPDATE steps SET settings = $1, title = $2, updated_at = NOW() WHERE id = $3", string(childSettingsJSON), criterionID, keepStep.ID)
						if err != nil {
							return fmt.Errorf("update error: %w", err)
						}
						stepLogger.Printf("Updated step %d for criterion %s", keepStep.ID, criterionID)
					}
				} else {
					// Handle unmarshal error by updating anyway (should not happen after pre-pass)
					_, err = db.Exec("UPDATE steps SET settings = $1, title = $2, updated_at = NOW() WHERE id = $3", string(childSettingsJSON), criterionID, keepStep.ID)
					if err != nil {
						return fmt.Errorf("update error due to unmarshal: %w", err)
					}
					stepLogger.Printf("Updated step %d for criterion %s due to unmarshal error", keepStep.ID, criterionID)
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
		return nil
	} else {
		markdownFilePath := filepath.Join(stepExec.BasePath, config.File)
		criteria, err := models.ParseRubric(markdownFilePath)
		if err != nil {
			return fmt.Errorf("failed to parse rubric markdown: %w", err)
		}
		stepLogger.Printf("DEBUG: Parsing rubric from file: %s", markdownFilePath)
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

		// Reconcile each criterion: ensure only one step exists and is correct
		for _, crit := range criteria {
			criterionID := crit.Title
			steps, exists := existingStepsByCriterion[criterionID]
			assignments := make([]models.SolutionAssignment, 0, len(config.AssignContainers))
			for patch, container := range config.AssignContainers {
				assignments = append(assignments, models.SolutionAssignment{Patch: patch, Container: container})
			}
			newRubricShellConfig := models.RubricShellConfig{
				Command:     crit.HeldOutTest,
				CriterionID: crit.Title,
				Counter:     crit.Counter,
				Score:       crit.Score,
				Required:    crit.Required,
				Rerun:       force,
				DependsOn:   []models.Dependency{{ID: stepExec.StepID}},
				GeneratedBy: fmt.Sprintf("%d", stepExec.StepID),
				Assignments: assignments,
				Files:       config.Files,  // Inherit Files map from RubricSetConfig
			}
			wrappedSettings := map[string]models.RubricShellConfig{"rubric_shell": newRubricShellConfig}
			childSettingsJSON, err := json.Marshal(wrappedSettings)
			if err != nil {
				return fmt.Errorf("marshal error: %w", err)
			}
			if !exists || len(steps) == 0 {
				// No step exists, create a new one
				stepID, err := models.CreateStep(db, strconv.Itoa(stepExec.TaskID), criterionID, string(childSettingsJSON))
				if err != nil {
					return fmt.Errorf("create error: %w", err)
				}
				stepLogger.Printf("Created step %d for criterion %s", stepID, criterionID)
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
				// Now update the kept step if config differs
				var existingConfig models.RubricShellConfig
				if err := json.Unmarshal([]byte(keepStep.Settings), &existingConfig); err == nil {
					if !reflect.DeepEqual(existingConfig, newRubricShellConfig) {
						_, err = db.Exec("UPDATE steps SET settings = $1, title = $2, updated_at = NOW() WHERE id = $3", string(childSettingsJSON), criterionID, keepStep.ID)
						if err != nil {
							return fmt.Errorf("update error: %w", err)
						}
						stepLogger.Printf("Updated step %d for criterion %s", keepStep.ID, criterionID)
					}
				} else {
					// Handle unmarshal error by updating anyway (should not happen after pre-pass)
					_, err = db.Exec("UPDATE steps SET settings = $1, title = $2, updated_at = NOW() WHERE id = $3", string(childSettingsJSON), criterionID, keepStep.ID)
					if err != nil {
						return fmt.Errorf("update error due to unmarshal: %w", err)
					}
					stepLogger.Printf("Updated step %d for criterion %s due to unmarshal error", keepStep.ID, criterionID)
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
		return nil
	}
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
