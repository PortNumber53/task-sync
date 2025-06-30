package dynamic_lab

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strconv"

	"github.com/PortNumber53/task-sync/pkg/models"
)

func processDynamicRubricSteps(db *sql.DB) error {
	log.Println("Processing dynamic rubric steps...")
	dynamicRubricSteps, err := models.GetStepsByType(db, "dynamic_rubric")
	if err != nil {
		return fmt.Errorf("failed to get active dynamic_rubric steps: %w", err)
	}
	log.Printf("Found %d dynamic_rubric steps to process.", len(dynamicRubricSteps))

	for i, step := range dynamicRubricSteps {
		log.Printf("Processing step %d/%d: ID %d", i+1, len(dynamicRubricSteps), step.StepID)

		var config models.DynamicRubricConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			log.Printf("Error unmarshalling settings for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": fmt.Sprintf("Error parsing settings: %v", err)}
			resultsJSON, _ := json.Marshal(results)
			db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID)
			continue
		}

		var overallChanged bool

		// 1. Check hashes of files in the 'files' map
		if config.DynamicRubric.Files != nil {
			if config.DynamicRubric.Hashes == nil {
				config.DynamicRubric.Hashes = make(map[string]string)
			}
			for file := range config.DynamicRubric.Files {
				filePath := filepath.Join(step.LocalPath, file)
				newHash, err := models.GetSHA256(filePath)
				if err != nil {
					log.Printf("Error hashing file %s for step %d: %v", file, step.StepID, err)
					continue
				}

				storedHash, ok := config.DynamicRubric.Hashes[file]
				if !ok || storedHash != newHash {
					log.Printf("File %s changed for step %d. Old hash: %s, New hash: %s", file, step.StepID, storedHash, newHash)
					overallChanged = true
					config.DynamicRubric.Hashes[file] = newHash
				}
			}
		}

		// 2. Check the rubric file itself and parse criteria
		if len(config.DynamicRubric.Rubrics) == 0 {
			return fmt.Errorf("no rubric files specified in step %d", step.StepID)
		}
		rubricFile := config.DynamicRubric.Rubrics[0] // Use the first rubric file
		criteria, newRubricHash, rubricChanged, err := models.RunRubric(step.LocalPath, rubricFile, config.DynamicRubric.Hash)
		if err != nil {
			log.Printf("Error running dynamic_rubric for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": err.Error()}
			resultsJSON, _ := json.Marshal(results)
			if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
				log.Printf("Failed to update step %d completion with error results: %v", step.StepID, err)
			}
			continue
		}
		log.Printf("Step %d: RunRubric completed. Rubric file changed: %t", step.StepID, rubricChanged)

		if rubricChanged {
			config.DynamicRubric.Hash = newRubricHash
			overallChanged = true
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
					runStepDependencyID = dep.ID
					break
				}

				// Also check for 'containers' from docker_pool step
				if containers, ok := results["containers"].([]interface{}); ok && len(containers) > 0 {
					if firstContainer, ok := containers[0].(map[string]interface{}); ok {
						if cID, ok := firstContainer["container_id"].(string); ok {
							containerID = cID
							runStepDependencyID = dep.ID
							break
						}
					}
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

		stepsExist, err := models.GeneratedStepsExist(db, step.StepID)
		if err != nil {
			return fmt.Errorf("failed to check for generated steps for parent step %d: %w", step.StepID, err)
		}

		if overallChanged || !stepsExist {
			if !stepsExist {
				log.Printf("No generated steps found for step %d. Generating new steps.", step.StepID)
			} else {
				log.Printf("Rubric or associated files changed for step %d. Updating generated steps.", step.StepID)
			}

			if err := models.DeleteGeneratedSteps(db, step.StepID); err != nil {
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
							"image_id": "%s",
							"image_tag": "%s",
							"rubric_details": {
								"score": %d,
								"required": %t,
								"description": "%s"
							}
						}
					}`, crit.HeldOutTest, runStepDependencyID, step.StepID, config.DynamicRubric.Environment.ImageID, config.DynamicRubric.Environment.ImageTag, crit.Score, crit.Required, crit.Rubric)
				} else {
					log.Printf("Step %d: Skipping criterion '%s' because environment is not docker.", step.StepID, crit.Title)
					continue
				}

				if _, err := models.CreateStep(db, strconv.Itoa(step.TaskID), crit.Title, settings); err != nil {
					log.Printf("Error creating step for criterion '%s' from step %d: %v", crit.Title, step.StepID, err)
				}
			}

			// Persist the updated hashes and rubric hash
			updatedSettings, err := json.Marshal(config)
			if err != nil {
				log.Printf("Error marshalling updated settings for step %d: %v", step.StepID, err)
				continue
			}
			if _, err := db.Exec(`UPDATE steps SET settings = $1, results = '{"result": "success"}', updated_at = NOW() WHERE id = $2`, string(updatedSettings), step.StepID); err != nil {
				log.Printf("Error updating settings for step %d: %v", step.StepID, err)
				continue
			}
		} else {
			log.Printf("Step %d: No changes detected and generated steps exist. Skipping.", step.StepID)
			// Also ensure result is success if we are skipping
			results := map[string]interface{}{"result": "success"}
			resultsJSON, _ := json.Marshal(results)
			if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
				log.Printf("Error updating step %d results to success: %v", step.StepID, err)
			}
		}
	}
	log.Println("--- Finished processing dynamic_rubric steps ---")
	return nil
}
