package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strconv"

	"github.com/PortNumber53/task-sync/internal/tasks/dynamic_lab"
)

func processDynamicLabSteps(db *sql.DB) error {
	steps, err := getStepsByType(db, "dynamic_lab")
	if err != nil {
		return fmt.Errorf("failed to get dynamic_lab steps: %w", err)
	}

	for _, step := range steps {
		var config DynamicLabConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			log.Printf("Error parsing settings for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": fmt.Sprintf("Error parsing settings: %v", err)}
			resultsJSON, _ := json.Marshal(results)
			db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID)
			continue
		}

		newHashes, changed, err := dynamic_lab.Run(step.LocalPath, config.DynamicLab.Files)
		if err != nil {
			log.Printf("Error running dynamic_lab for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": err.Error()}
			resultsJSON, _ := json.Marshal(results)
			db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID)
			continue
		}

		// Find container_id and its step ID from dependencies
		var containerID string
		var runStepDependencyID int
		for _, dep := range config.DynamicLab.DependsOn {
			var rawResults sql.NullString
			err := db.QueryRow("SELECT results FROM steps WHERE id = $1", dep.ID).Scan(&rawResults)
			if err != nil {
				log.Printf("Step %d: Error getting info for dependency step %d: %v", step.StepID, dep.ID, err)
				continue
			}

			if rawResults.Valid {
				var results map[string]interface{}
				if err := json.Unmarshal([]byte(rawResults.String), &results); err != nil {
					log.Printf("Error unmarshalling results for dependency step %d: %v", dep.ID, err)
					continue
				}

				if cID, ok := results["container_id"].(string); ok {
					containerID = cID
					runStepDependencyID = dep.ID // Capture the ID of the dependency that provides the container
					log.Printf("Found container_id '%s' from dependency step %d", containerID, runStepDependencyID)
					break // Found it, no need to check other dependencies
				}
			}
		}

		if !changed {
			log.Printf("No file changes for dynamic_lab step %d.", step.StepID)
			continue
		}

		log.Printf("File changes detected for dynamic_lab step %d. Re-generating steps.", step.StepID)

		if err := deleteGeneratedSteps(db, step.StepID, runStepDependencyID); err != nil {
			log.Printf("Error deleting generated steps for step %d: %v", step.StepID, err)
			continue
		}

		rubricFile := config.DynamicLab.RubricFile
		if rubricFile == "" {
			log.Printf("dynamic_lab step %d settings does not specify a 'rubric_file'", step.StepID)
			continue
		}

		criteria, newRubricHash, _, err := dynamic_lab.RunRubric(step.LocalPath, rubricFile, "") // Pass empty hash to force re-parse
		if err != nil {
			log.Printf("Error running dynamic_rubric for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": err.Error()}
			resultsJSON, _ := json.Marshal(results)
			if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
				log.Printf("Failed to update step %d completion with error results: %v", step.StepID, err)
			}
			continue
		}



		if containerID == "" {
			log.Printf("Step %d: Could not find a container_id from any dependencies. Skipping generation.", step.StepID)
			results := map[string]interface{}{"result": "success", "info": "Could not find a container_id from any dependencies. Skipping generation."}
			resultsJSON, _ := json.Marshal(results)
			if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
				log.Printf("Error updating step %d results: %v", step.StepID, err)
			}
			continue
		}

		for _, crit := range criteria {
			var settings string
			if config.DynamicLab.Environment.Docker {
				settings = fmt.Sprintf(`{
					"docker_shell": {
						"command": [{"run": "%s"}],
						"depends_on": [{"id": %d}],
						"rubric_details": {
							"score": %d,
							"required": %t,
							"description": "%s"
						}
					}
				}`, crit.HeldOutTest, runStepDependencyID, crit.Score, crit.Required, crit.Rubric)
			} else {
				log.Printf("Step %d: Skipping criterion '%s' because environment is not docker.", step.StepID, crit.Title)
				continue
			}

			if _, err := CreateStep(db, strconv.Itoa(step.TaskID), crit.Title, settings); err != nil {
				log.Printf("Error creating step for criterion '%s' from step %d: %v", crit.Title, step.StepID, err)
			}
		}

		config.DynamicLab.Files = newHashes
		config.DynamicLab.Files[rubricFile] = newRubricHash
		updatedSettings, err := json.Marshal(config)
		if err != nil {
			log.Printf("Error marshalling updated settings for step %d: %v", step.StepID, err)
			continue
		}
		if _, err := db.Exec(`UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2`, string(updatedSettings), step.StepID); err != nil {
			log.Printf("Error updating settings for step %d: %v", step.StepID, err)
			continue
		}

		results := map[string]interface{}{"result": "success"}
		resultsJSON, _ := json.Marshal(results)
		if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
			log.Printf("Error updating step %d results to success: %v", step.StepID, err)
		}
	}

	return nil
}
