package internal

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"encoding/json"
	"strconv"
	"os/exec"
)

// processDockerRubricsSteps processes docker rubrics steps for active tasks
func processDockerRubricsSteps(db *sql.DB) {
	query := `SELECT s.id, s.task_id, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.status = 'active'
		AND t.status = 'active'
		AND t.local_path IS NOT NULL
		AND t.local_path <> ''
		AND s.settings::text LIKE '%docker_rubrics%'`

	rows, err := db.Query(query)
	if err != nil {
		stepLogger.Println("Docker rubrics query error:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var step stepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.LocalPath); err != nil {
			stepLogger.Println("Row scan error:", err)
			continue
		}

		// Parse the docker rubrics config
		var config DockerRubricsConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			if err := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "invalid docker rubrics config"}); err != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", err)
			}
			stepLogger.Printf("Step %d: invalid docker rubrics config: %v\n", step.StepID, err)
			continue
		}

		// Check if dependencies are met
		ok, err := checkDependencies(db, step.StepID, config.DockerRubrics.DependsOn)
		if err != nil {
			stepLogger.Printf("Step %d: error checking dependencies: %v\n", step.StepID, err)
			continue
		}
		if !ok {
			stepLogger.Printf("Step %d: waiting for dependencies to complete\n", step.StepID)
			continue
		}

		// Get current image ID for the tag
		currentImageID, err := getDockerImageID(config.DockerRubrics.ImageTag)
		if err != nil {
			stepLogger.Printf("Step %d: error getting current image ID: %v\n", step.StepID, err)
			continue
		}

		// If image ID is empty, we can't proceed
		if currentImageID == "" {
			stepLogger.Printf("Step %d: no image found with tag %s\n", step.StepID, config.DockerRubrics.ImageTag)
			continue
		}

		// If image ID is different from stored one, update and skip this run
		if config.DockerRubrics.ImageID != "" && config.DockerRubrics.ImageID != currentImageID {
			stepLogger.Printf("Step %d: image ID changed, updating and skipping this run\n", step.StepID)
			config.DockerRubrics.ImageID = currentImageID
			// Update the step with new image ID
			updatedSettings, _ := json.Marshal(config)
			db.Exec(`UPDATE steps SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettings), step.StepID)
			continue
		}

		// Check if files have changed
		shouldRun := false
		for _, file := range config.DockerRubrics.Files {
			filePath := filepath.Join(step.LocalPath, file)

			// Check if file exists first
			if _, err := os.Stat(filePath); os.IsNotExist(err) {
				stepLogger.Printf("Step %d: file not found: %s\n", step.StepID, filePath)
				// If it's TASK_DATA.md, we should still try to proceed as it might be created later
				if !strings.HasSuffix(file, "TASK_DATA.md") {
					if err := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "required file not found: " + file}); err != nil {
						stepLogger.Println("Failed to update results for step", step.StepID, ":", err)
					}
					continue
				}
				shouldRun = true // Mark to run if TASK_DATA.md is missing (it will be created)
				continue
			}

			currentHash, err := calculateFileHash(filePath)
			if err != nil {
				stepLogger.Printf("Step %d: error calculating hash for %s: %v\n", step.StepID, file, err)
				continue
			}

			storedHash, hasHash := config.DockerRubrics.Hashes[file]
			if !hasHash || storedHash != currentHash {
				// Update the hash
				if config.DockerRubrics.Hashes == nil {
					config.DockerRubrics.Hashes = make(map[string]string)
				}
				config.DockerRubrics.Hashes[file] = currentHash
				shouldRun = true
			}
		}

		// If no changes and we already have an image ID, skip
		if !shouldRun && config.DockerRubrics.ImageID != "" {
			stepLogger.Printf("Step %d: no changes detected, skipping\n", step.StepID)
			continue
		}

		// Process the TASK_DATA.md file
		for _, file := range config.DockerRubrics.Files {
			if strings.HasSuffix(file, "TASK_DATA.md") {
				filePath := filepath.Join(step.LocalPath, file)
				content, err := os.ReadFile(filePath)
				if err != nil {
					stepLogger.Printf("Step %d: error reading file %s: %v\n", step.StepID, file, err)
					continue
				}

				// Parse the TASK_DATA.md content
				lines := strings.Split(string(content), "\n")
				for i := 0; i < len(lines); i++ {
					line := strings.TrimSpace(lines[i])
					if line == "" {
						continue
					}

					// Parse the score and required flag
					parts := strings.Fields(line)
					if len(parts) < 2 {
						continue
					}

					_, err = strconv.Atoi(parts[0])
					if err != nil {
						continue
					}

					// Check if the command is required
					required := false
					if len(parts) > 1 && parts[1] == "[x]" {
						required = true
					}

					// Get the command (rest of the line after score and [x])
					command := strings.TrimSpace(strings.TrimPrefix(line, parts[0]))
					if required {
						command = strings.TrimSpace(strings.TrimPrefix(command, "[x]"))
					} else {
						command = strings.TrimSpace(strings.TrimPrefix(command, "[ ]"))
					}

					// Execute the command in the container
					cmd := exec.Command("docker", "run", "--rm", config.DockerRubrics.ImageTag, "sh", "-c", command)
					output, err := cmd.CombinedOutput()
					if err != nil {
						stepLogger.Printf("Step %d: command '%s' failed: %v\nOutput: %s\n", step.StepID, command, err, string(output))
						if required {
							if err := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "required command failed: " + command, "output": string(output)}); err != nil {
								stepLogger.Println("Failed to update results for step", step.StepID, ":", err)
							}
							continue
						}
					} else {
						stepLogger.Printf("Step %d: command '%s' succeeded\nOutput: %s\n", step.StepID, command, string(output))
					}
				}
			}
		}

		// Update the step with new hashes and image ID
		config.DockerRubrics.ImageID = currentImageID
		updatedSettings, _ := json.Marshal(config)
		db.Exec(`UPDATE steps SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettings), step.StepID)
		if err := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "success"}); err != nil {
			stepLogger.Println("Failed to update results for step", step.StepID, ":", err)
		}
	}
}
