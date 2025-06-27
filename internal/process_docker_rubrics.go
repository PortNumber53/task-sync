package internal

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

		var config DockerRubricsConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "invalid docker rubrics config"})
			stepLogger.Printf("Step %d: invalid docker rubrics config: %v\n", step.StepID, err)
			continue
		}

		ok, err := checkDependencies(db, step.StepID, config.DockerRubrics.DependsOn)
		if err != nil {
			stepLogger.Printf("Step %d: error checking dependencies: %v\n", step.StepID, err)
			continue
		}
		if !ok {
			stepLogger.Printf("Step %d: waiting for dependencies to complete\n", step.StepID)
			continue
		}

		// --- Start of new Image ID logic ---
		var imageIDToUse string
		var buildStepImageID string

		// 1. Try to get image_id from a docker_build dependency
		for _, dep := range config.DockerRubrics.DependsOn {
			depInfo, err := GetStepInfo(db, dep.ID)
			if err != nil {
				stepLogger.Printf("Step %d: Error getting info for dependency step %d: %v\n", step.StepID, dep.ID, err)
				continue
			}
			if depSettings, ok := depInfo.Settings["docker_build"].(map[string]interface{}); ok {
				if id, ok := depSettings["image_id"].(string); ok && id != "" && id != "sha256:" {
					buildStepImageID = id
					stepLogger.Printf("Step %d: Found image_id '%s' from build dependency step %d\n", step.StepID, buildStepImageID, dep.ID)
					break
				}
			}
		}

		// 2. Decide which image ID to use
		if buildStepImageID != "" {
			imageIDToUse = buildStepImageID
			stepLogger.Printf("Step %d: Prioritizing image_id '%s' from build dependency.\n", step.StepID, imageIDToUse)
		} else {
			stepLogger.Printf("Step %d: No build dependency with a valid image_id found. Falling back to inspecting tag '%s'.\n", step.StepID, config.DockerRubrics.ImageTag)
			currentImageID, err := getDockerImageID(config.DockerRubrics.ImageTag)
			if err != nil {
				stepLogger.Printf("Step %d: error getting current image ID by tag: %v\n", step.StepID, err)
				continue
			}
			if currentImageID == "" {
				stepLogger.Printf("Step %d: no image found with tag %s\n", step.StepID, config.DockerRubrics.ImageTag)
				continue
			}
			imageIDToUse = currentImageID
		}

		// 3. Check if the determined image ID is different from the one in settings. If so, update and skip.
		if config.DockerRubrics.ImageID != imageIDToUse {
			stepLogger.Printf("Step %d: Stored image_id ('%s') is outdated. Updating to '%s' and skipping this run.\n", step.StepID, config.DockerRubrics.ImageID, imageIDToUse)
			config.DockerRubrics.ImageID = imageIDToUse
			updatedSettings, _ := json.Marshal(config)
			_, err := db.Exec(`UPDATE steps SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettings), step.StepID)
			if err != nil {
				stepLogger.Printf("Step %d: Failed to update settings with new image_id: %v\n", step.StepID, err)
			}
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "pending", "message": "Image ID updated from dependency, will run next cycle."})
			continue // Skip to next step, will run correctly on next execution cycle
		}
		// --- End of new Image ID logic ---

		// Check if files have changed
		shouldRun := false
		for _, file := range config.DockerRubrics.Files {
			filePath := filepath.Join(step.LocalPath, file)

			if _, err := os.Stat(filePath); os.IsNotExist(err) {
				stepLogger.Printf("Step %d: file not found: %s\n", step.StepID, filePath)
				if !strings.HasSuffix(file, "TASK_DATA.md") {
					StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "required file not found: " + file})
					continue
				}
				shouldRun = true
				continue
			}

			currentHash, err := calculateFileHash(filePath)
			if err != nil {
				stepLogger.Printf("Step %d: error calculating hash for %s: %v\n", step.StepID, file, err)
				continue
			}

			storedHash, hasHash := config.DockerRubrics.Hashes[file]
			if !hasHash || storedHash != currentHash {
				if config.DockerRubrics.Hashes == nil {
					config.DockerRubrics.Hashes = make(map[string]string)
				}
				config.DockerRubrics.Hashes[file] = currentHash
				shouldRun = true
			}
		}

		if !shouldRun {
			stepLogger.Printf("Step %d: no file changes detected, skipping rubrics evaluation.\n", step.StepID)
			// We can't just succeed here, because the container might not be running.
			// The logic to check for a running container with the correct image hash should be here.
			// For now, we assume if no files changed and ID is set, it's fine.
			continue
		}

		// Process the TASK_DATA.md file and determine step outcome
		var requiredCommandFailed bool
		var finalMessage, finalOutput string

	CommandProcessingLoop:
		for _, file := range config.DockerRubrics.Files {
			if strings.HasSuffix(file, "TASK_DATA.md") {
				filePath := filepath.Join(step.LocalPath, file)
				content, err := os.ReadFile(filePath)
				if err != nil {
					stepLogger.Printf("Step %d: error reading file %s: %v\n", step.StepID, file, err)
					continue
				}

				lines := strings.Split(string(content), "\n")
				for i := 0; i < len(lines); i++ {
					line := strings.TrimSpace(lines[i])
					if line == "" {
						continue
					}

					parts := strings.Fields(line)
					if len(parts) < 2 {
						continue
					}

					if _, err := strconv.Atoi(parts[0]); err != nil {
						continue
					}

					required := len(parts) > 1 && parts[1] == "[x]"
					command := strings.TrimSpace(strings.TrimPrefix(line, parts[0]))
					if required {
						command = strings.TrimSpace(strings.TrimPrefix(command, "[x]"))
					} else {
						command = strings.TrimSpace(strings.TrimPrefix(command, "[ ]"))
					}

					cmd := execCommand("docker", "run", "--rm", imageIDToUse, "sh", "-c", command)
					output, err := cmd.CombinedOutput()

					if err != nil {
						stepLogger.Printf("Step %d: command '%s' failed: %v\nOutput: %s\n", step.StepID, command, err, string(output))
						if required {
							requiredCommandFailed = true
							finalMessage = "required command failed: " + command
							finalOutput = string(output)
							break CommandProcessingLoop
						}
					} else {
						stepLogger.Printf("Step %d: command '%s' succeeded\nOutput: %s\n", step.StepID, command, string(output))
					}
				}
			}
		}

		if requiredCommandFailed {
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": finalMessage, "output": finalOutput})
		} else {
			updatedSettings, err := json.Marshal(config)
			if err != nil {
				StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "failed to marshal settings on success"})
				stepLogger.Printf("Step %d: Failed to marshal settings on success: %v\n", step.StepID, err)
				continue
			}
			_, err = db.Exec(`UPDATE steps SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettings), step.StepID)
			if err != nil {
				StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "failed to update settings on success"})
				stepLogger.Printf("Step %d: Failed to update settings on success: %v\n", step.StepID, err)
			} else {
				StoreStepResult(db, step.StepID, map[string]interface{}{"result": "success", "message": "All rubrics passed."})
			}
		}

	}
}
