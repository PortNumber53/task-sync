package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strconv"

	"github.com/PortNumber53/task-sync/internal/tasks/dynamic_lab"
)

func processDynamicRubricSteps(db *sql.DB) error {
	log.Println("Processing dynamic rubric steps...")
	dynamicRubricSteps, err := getStepsByType(db, "dynamic_rubric")
	if err != nil {
		return fmt.Errorf("failed to get active dynamic_rubric steps: %w", err)
	}
	log.Printf("Found %d dynamic_rubric steps to process.", len(dynamicRubricSteps))

	for i, step := range dynamicRubricSteps {
		log.Printf("Processing step %d/%d: ID %d", i+1, len(dynamicRubricSteps), step.StepID)

		var config DynamicRubricConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			log.Printf("Error unmarshalling settings for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": fmt.Sprintf("Error parsing settings: %v", err)}
			resultsJSON, _ := json.Marshal(results)
			db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID)
			continue
		}

		// Find container_id and its step ID from dependencies
		var containerID string
		var runStepDependencyID int
		for _, dep := range config.DynamicRubric.DependsOn {
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

		if containerID == "" {
			log.Printf("Step %d: Could not find a container_id from any dependencies. Skipping generation.", step.StepID)
			results := map[string]interface{}{"result": "success", "info": "Could not find a container_id from any dependencies. Skipping generation."}
			resultsJSON, _ := json.Marshal(results)
			if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
				log.Printf("Error updating step %d results: %v", step.StepID, err)
			}
			continue
		}

		log.Printf("Step %d: Running RunRubric", step.StepID)
				criteria, newHash, changed, err := dynamic_lab.RunRubric(step.LocalPath, config.DynamicRubric.File, config.DynamicRubric.Hash)
		if err != nil {
			log.Printf("Error running dynamic_rubric for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": err.Error()}
			resultsJSON, _ := json.Marshal(results)
			if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
				log.Printf("Failed to update step %d completion with error results: %v", step.StepID, err)
			}
			continue
		}
		log.Printf("Step %d: RunRubric completed. Changed: %t", step.StepID, changed)

		if changed {
			log.Printf("Rubric file changed for step %d. Updating generated steps.", step.StepID)

			if err := deleteGeneratedSteps(db, step.StepID, runStepDependencyID); err != nil {
				log.Printf("Error deleting generated steps for step %d: %v", step.StepID, err)
				continue
			}

			for _, crit := range criteria {
				var settings string
				if config.DynamicRubric.Environment.Docker {
					settings = fmt.Sprintf(`{
						"docker_shell": {
							"command": [{"run": "%s"}],
							"depends_on": [{"id": %d}],
							"generated_by": %d,
							"rubric_details": {
								"score": %d,
								"required": %t,
								"description": "%s"
							}
						}
					}`, crit.HeldOutTest, runStepDependencyID, step.StepID, crit.Score, crit.Required, crit.Rubric)
				} else {
					log.Printf("Step %d: Skipping criterion '%s' because environment is not docker.", step.StepID, crit.Title)
					continue
				}

				if _, err := CreateStep(db, strconv.Itoa(step.TaskID), crit.Title, settings); err != nil {
					log.Printf("Error creating step for criterion '%s' from step %d: %v", crit.Title, step.StepID, err)
				}
			}

			config.DynamicRubric.Hash = newHash
			updatedSettings, err := json.Marshal(config)
			if err != nil {
				log.Printf("Error marshalling updated settings for step %d: %v", step.StepID, err)
				continue
			}
			if _, err := db.Exec(`UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2`, string(updatedSettings), step.StepID); err != nil {
				log.Printf("Error updating settings for step %d: %v", step.StepID, err)
				continue
			}
		}

		results := map[string]interface{}{"result": "success"}
		resultsJSON, _ := json.Marshal(results)
		if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
			log.Printf("Error updating step %d results to success: %v", step.StepID, err)
		}
	}
	log.Println("--- Finished processing dynamic_rubric steps ---")
	return nil
}
