package dynamic_lab

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strconv"

	"github.com/PortNumber53/task-sync/pkg/models"
)

var dynamicLabRun = Run

func processDynamicLabSteps(db *sql.DB) error {
	steps, err := models.GetStepsByType(db, "dynamic_lab")
	if err != nil {
		return fmt.Errorf("failed to get dynamic_lab steps: %w", err)
	}

	for _, step := range steps {
		var config models.DynamicLabConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			log.Printf("Error parsing settings for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": fmt.Sprintf("Error parsing settings: %v", err)}
			resultsJSON, _ := json.Marshal(results)
			db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID)
			continue
		}

		var files []string
		var migrated bool

		// Check for old format (files is a map) and migrate if necessary
		if fileMap, ok := config.DynamicLab.Files.(map[string]interface{}); ok {
			log.Printf("Step %d: Migrating 'files' from map to files/hashes format", step.StepID)
			migrated = true

			// Extract files from map keys
			files = make([]string, 0, len(fileMap))
			for k := range fileMap {
				files = append(files, k)
			}

			// Overwrite the Files field in the config with the new slice format
			config.DynamicLab.Files = files

			// Re-serialize the entire config to update the step's settings in the database later
			updatedSettings, err := json.Marshal(config)
			if err != nil {
				return fmt.Errorf("failed to re-marshal migrated settings for step %d: %w", step.StepID, err)
			}
			step.Settings = string(updatedSettings) // Update the in-memory step settings

		} else if fileSlice, ok := config.DynamicLab.Files.([]interface{}); ok {
			// New format, but as []interface{}. Convert to []string.
			for _, v := range fileSlice {
				if fileStr, ok := v.(string); ok {
					files = append(files, fileStr)
				}
			}
		} else if fileSlice, ok := config.DynamicLab.Files.([]string); ok {
			// New format, already []string.
			files = fileSlice
		} else if config.DynamicLab.Files != nil {
			// Handle case where it might be an unexpected type
			err := fmt.Errorf("files field for step %d is of an unexpected type: %T", step.StepID, config.DynamicLab.Files)
			log.Print(err.Error())
			results := map[string]interface{}{"result": "error", "error": err.Error()}
			resultsJSON, _ := json.Marshal(results)
			db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID)
			continue
		}

		newHashes, changed, err := dynamicLabRun(step.LocalPath, files, config.DynamicLab.Hashes)
		if err != nil {
			log.Printf("Error running dynamic_lab for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": err.Error()}
			resultsJSON, _ := json.Marshal(results)
			db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID)
			continue
		}

		if migrated {
			changed = true
		}

		// Find container_id by traversing the dependency graph
		var containerID string
		var runStepDependencyID int

		queue := make([]int, 0)
		// First, check direct dependencies
		if config.DynamicLab.DependsOn != nil {
			for _, dep := range config.DynamicLab.DependsOn {
				queue = append(queue, dep.ID)
			}
		}

		visited := make(map[int]bool)
		for _, id := range queue {
			visited[id] = true
		}

		for len(queue) > 0 {
			currentStepID := queue[0]
			queue = queue[1:]

			var rawResults sql.NullString
			err := db.QueryRow("SELECT results FROM steps WHERE id = $1", currentStepID).Scan(&rawResults)
			if err != nil {
				log.Printf("Step %d: Error getting results for step %d: %v", step.StepID, currentStepID, err)
				continue
			}
			if rawResults.Valid {
				var results map[string]interface{}
				if err := json.Unmarshal([]byte(rawResults.String), &results); err == nil {
					if cID, ok := results["container_id"].(string); ok && cID != "" {
						containerID = cID
						runStepDependencyID = currentStepID
						log.Printf("Found container_id '%s' from step %d", containerID, runStepDependencyID)
						break
					}
				}
			}

			var settingsStr string
			err = db.QueryRow("SELECT settings FROM steps WHERE id = $1", currentStepID).Scan(&settingsStr)
			if err != nil {
				log.Printf("Step %d: Error getting settings for step %d: %v", step.StepID, currentStepID, err)
				continue
			}

			var topLevel map[string]json.RawMessage
			if err := json.Unmarshal([]byte(settingsStr), &topLevel); err == nil {
				for _, rawMessage := range topLevel {
					var holder models.DependencyHolder
					if err := json.Unmarshal(rawMessage, &holder); err == nil {
						for _, dep := range holder.DependsOn {
							if !visited[dep.ID] {
								visited[dep.ID] = true
								queue = append(queue, dep.ID)
							}
						}
					}
				}
			}
		}

		if containerID == "" {
			log.Printf("Step %d: Could not find container_id in dependency graph. Searching all steps in task %d.", step.StepID, step.TaskID)

			query := `SELECT id, results FROM steps WHERE task_id = $1 AND settings ? 'docker_run' ORDER BY id DESC`
			rows, err := db.Query(query, step.TaskID)
			if err != nil {
				log.Printf("Error querying for docker_run steps in task %d: %v", step.TaskID, err)
			} else {
				defer rows.Close()
				for rows.Next() {
					var depStepID int
					var rawResults sql.NullString
					if err := rows.Scan(&depStepID, &rawResults); err != nil {
						log.Printf("Error scanning docker_run step: %v", err)
						continue
					}

					if rawResults.Valid {
						var results map[string]interface{}
						if err := json.Unmarshal([]byte(rawResults.String), &results); err == nil {
							if cID, ok := results["container_id"].(string); ok && cID != "" {
								containerID = cID
								runStepDependencyID = depStepID
								log.Printf("Found container_id '%s' via task-wide search from step %d.", containerID, runStepDependencyID)
								break
							}
						}
					}
				}
			}
		}

		if !changed {
			log.Printf("No file changes for dynamic_lab step %d.", step.StepID)
			continue
		}

		log.Printf("File changes detected for dynamic_lab step %d. Re-generating steps.", step.StepID)

		if err := models.DeleteGeneratedSteps(db, step.StepID); err != nil {
			log.Printf("Error deleting generated steps for step %d: %v", step.StepID, err)
			continue
		}

		rubricFile := config.DynamicLab.RubricFile
		if rubricFile == "" {
			log.Printf("dynamic_lab step %d settings does not specify a 'rubric_file'", step.StepID)
			continue
		}

		criteria, newRubricHash, _, err := rubricParserImpl.RunRubric(step.LocalPath, config.DynamicLab.RubricFile, "") // Pass empty hash to force re-parse
		if err != nil {
			log.Printf("Error running dynamic_rubric for step %d: %v", step.StepID, err)
			results := map[string]interface{}{"result": "error", "error": err.Error()}
			resultsJSON, _ := json.Marshal(results)
			if _, err := db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID); err != nil {
				log.Printf("Failed to update step %d completion with error results: %v", step.StepID, err)
			}
			continue
		}

		if containerID != "" && !config.DynamicLab.Environment.Docker {
			log.Printf("Step %d: Found container_id, ensuring environment is set to docker.", step.StepID)
			config.DynamicLab.Environment.Docker = true
			changed = true
		}

		if containerID == "" {
			log.Printf("Step %d: Could not find a container_id from any dependencies. Skipping generation.", step.StepID)
			results := map[string]interface{}{"result": "success", "info": "Could not find a container_id from any dependencies. Skipping generation."}
			resultsJSON, _ := json.Marshal(results)
			db.Exec("UPDATE steps SET results = $1, updated_at = NOW() WHERE id = $2", string(resultsJSON), step.StepID)
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

			if _, err := models.CreateStep(db, strconv.Itoa(step.TaskID), crit.Title, settings); err != nil {
				log.Printf("Error creating step for criterion '%s' from step %d: %v", crit.Title, step.StepID, err)
			}
		}

		config.DynamicLab.Hashes = newHashes
		if config.DynamicLab.Hashes == nil {
			config.DynamicLab.Hashes = make(map[string]string)
		}
		config.DynamicLab.Hashes[rubricFile] = newRubricHash
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
