package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// processDockerRunSteps processes docker run steps for active tasks
func processDockerRunSteps(db *sql.DB) {
	query := `SELECT s.id, s.task_id, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.status = 'active'
		AND t.status = 'active'
		AND t.local_path IS NOT NULL
		AND t.local_path <> ''
		AND s.settings::text LIKE '%docker_run%'`

	rows, err := db.Query(query)
	if err != nil {
		stepLogger.Println("Docker run query error:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var step stepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.LocalPath); err != nil {
			stepLogger.Println("Row scan error:", err)
			continue
		}

		var config DockerRunConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "invalid docker run config"})
			stepLogger.Printf("Step %d: invalid docker run config: %v\n", step.StepID, err)
			continue
		}

		ok, err := checkDependencies(db, step.StepID, config.DockerRun.DependsOn)
		if err != nil {
			stepLogger.Printf("Step %d: error checking dependencies: %v\n", step.StepID, err)
			continue
		}
		if !ok {
			stepLogger.Printf("Step %d: waiting for dependencies to complete\n", step.StepID)
			continue
		}

		var imageIDToUse string
		var buildStepImageID string

		for _, dep := range config.DockerRun.DependsOn {
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

		if buildStepImageID != "" {
			imageIDToUse = buildStepImageID
			stepLogger.Printf("Step %d: Prioritizing image_id '%s' from build dependency.\n", step.StepID, imageIDToUse)
		} else {
			stepLogger.Printf("Step %d: No build dependency with a valid image_id found. Falling back to inspecting tag '%s'.\n", step.StepID, config.DockerRun.ImageTag)
			currentImageID, err := getDockerImageID(config.DockerRun.ImageTag)
			if err != nil {
				stepLogger.Printf("Step %d: error getting current image ID by tag: %v\n", step.StepID, err)
				continue
			}
			if currentImageID == "" {
				StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "no image found with tag: " + config.DockerRun.ImageTag})
				stepLogger.Printf("Step %d: no image found with tag %s\n", step.StepID, config.DockerRun.ImageTag)
				_, errDb := db.Exec("UPDATE steps SET status = 'error', updated_at = NOW() WHERE id = $1", step.StepID)
				if errDb != nil {
					stepLogger.Printf("Step %d: Failed to update status to error after image not found: %v\n", step.StepID, errDb)
				}
				continue
			}
			imageIDToUse = currentImageID
		}

		if config.DockerRun.ImageID != imageIDToUse {
			stepLogger.Printf("Step %d: Stored image_id ('%s') is outdated. Updating to '%s' and skipping this run.\n", step.StepID, config.DockerRun.ImageID, imageIDToUse)
			config.DockerRun.ImageID = imageIDToUse
			updatedSettings, _ := json.Marshal(config)
			_, err := db.Exec(`UPDATE steps SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettings), step.StepID)
			if err != nil {
				stepLogger.Printf("Step %d: Failed to update settings with new image_id: %v\n", step.StepID, err)
			}
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "pending", "message": "Image ID updated from dependency, will run next cycle."})
			continue
		}

		if config.DockerRun.ContainerID != "" {
			inspectCmd := exec.Command("docker", "inspect", config.DockerRun.ContainerID)
			output, err := inspectCmd.CombinedOutput()
			if err == nil {
				var inspectResult []struct {
					Config struct {
						Image string `json:"Image"`
					}
					State struct {
						Running bool `json:"Running"`
					} `json:"State"`
				}
				if err := json.Unmarshal(output, &inspectResult); err == nil && len(inspectResult) > 0 {
					if inspectResult[0].State.Running && inspectResult[0].Config.Image == imageIDToUse {
						stepLogger.Printf("Step %d: Container %s (%s) is already running with the correct image %s. Ensuring DB state is consistent.\n", step.StepID, config.DockerRun.ContainerName, config.DockerRun.ContainerID, imageIDToUse)

						updatedSettingsJSON, marshalErr := json.Marshal(config)
						if marshalErr != nil {
							stepLogger.Printf("Step %d: Failed to marshal settings for already running container: %v\n", step.StepID, marshalErr)
							StoreStepResult(db, step.StepID, map[string]interface{}{
								"result":         "success", // Container is running
								"message":        fmt.Sprintf("Container %s (%s) confirmed running, but failed to marshal current settings to DB: %v", config.DockerRun.ContainerName, config.DockerRun.ContainerID, marshalErr),
								"container_id":   config.DockerRun.ContainerID,
								"container_name": config.DockerRun.ContainerName,
							})
							// Mark step as error because its settings in DB might be inconsistent
							_, dbErr := db.Exec("UPDATE steps SET status = 'error', updated_at = NOW() WHERE id = $1", step.StepID)
							if dbErr != nil {
								stepLogger.Printf("Step %d: Also failed to update status to error after marshal error for running container: %v\n", step.StepID, dbErr)
							}
						} else {
							StoreStepResult(db, step.StepID, map[string]interface{}{
								"result":         "success",
								"message":        "Container already running and DB state confirmed.",
								"container_id":   config.DockerRun.ContainerID,
								"container_name": config.DockerRun.ContainerName,
								"image_id_used":  imageIDToUse,
							})
							// Update step settings (even if unchanged, for updated_at) and status to 'complete'
							_, dbErr := db.Exec("UPDATE steps SET settings = $1, status = 'complete', updated_at = NOW() WHERE id = $2", string(updatedSettingsJSON), step.StepID)
							if dbErr != nil {
								stepLogger.Printf("Step %d: Failed to update step settings/status to complete for already running container: %v\n", step.StepID, dbErr)
								// Result already stored, but this is an inconsistency.
							}
						}
						continue
					} else if !inspectResult[0].State.Running {
						stepLogger.Printf("Step %d: Container %s (%s) found but not running. Will attempt to start a new one.\n", step.StepID, config.DockerRun.ContainerName, config.DockerRun.ContainerID)
					} else if inspectResult[0].Config.Image != imageIDToUse {
						stepLogger.Printf("Step %d: Container %s (%s) running with wrong image (%s vs %s). Will attempt to start a new one.\n", step.StepID, config.DockerRun.ContainerName, config.DockerRun.ContainerID, inspectResult[0].Config.Image, imageIDToUse)
						// Consider stopping/removing the old one if desired
					}
				}
			} else {
				stepLogger.Printf("Step %d: Failed to inspect container %s. It might have been removed. Will attempt to start a new one. Error: %v\n", step.StepID, config.DockerRun.ContainerID, err)
			}
			// If container not running correctly or inspect failed, clear old ID/Name to launch a new one
			config.DockerRun.ContainerID = ""
			config.DockerRun.ContainerName = ""
		}

		dockerRunParams := config.DockerRun.DockerRunParameters

		if len(dockerRunParams) == 0 {
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "docker_run_parameters are not specified or invalid"})
			stepLogger.Printf("Step %d: docker_run_parameters are not specified or invalid in command object\n", step.StepID)
			continue
		}

		foundImageTagPlaceholder := false
		for i, part := range dockerRunParams {
			if part == "<<IMAGETAG>>" {
				dockerRunParams[i] = imageIDToUse
				foundImageTagPlaceholder = true
			}
		}

		if !foundImageTagPlaceholder {
			stepLogger.Printf("Step %d: <<IMAGETAG>> placeholder not found in docker_run_parameters. The image '%s' may not be used correctly.", step.StepID, imageIDToUse)
		}

		processedDockerRunParams := []string{}
		for _, param := range dockerRunParams {
			processedDockerRunParams = append(processedDockerRunParams, strings.Fields(param)...)
		}

		hasKeepAliveCmd := false
		for _, param := range dockerRunParams {
			if (strings.Contains(param, "while true") && strings.Contains(param, "sleep")) ||
				strings.Contains(param, "sleep infinity") ||
				strings.Contains(param, "tail -f") {
				hasKeepAliveCmd = true
				break
			}
		}

		if !hasKeepAliveCmd {
			keepAliveArgs := []string{"-c", "while true; do sleep 30; done"}
			processedDockerRunParams = append(processedDockerRunParams, keepAliveArgs...)
		}

		randomSuffix, err := GenerateRandomString(4)
		if err != nil {
			stepLogger.Printf("Step %d: Failed to generate random suffix for container name: %v. Using fixed suffix.\n", step.StepID, err)
			randomSuffix = "xxxx" // Fallback suffix
		}
		containerName := fmt.Sprintf("tasksync_step%d_%s", step.StepID, randomSuffix)

		detachedParams := append([]string{"-d", "--name", containerName}, processedDockerRunParams...)

		cmdArgs := append([]string{"run"}, detachedParams...)
		cmd := exec.Command("docker", cmdArgs...)
		output, err := cmd.CombinedOutput()
		newContainerID := strings.TrimSpace(string(output))

		if err != nil {
			stepLogger.Printf("Step %d: command 'docker run %v' failed: %v\nOutput: %s\n", step.StepID, detachedParams, err, newContainerID)
			StoreStepResult(db, step.StepID, map[string]interface{}{
				"result":  "failure",
				"message": fmt.Sprintf("docker run command failed: %v. Output: %s", err, newContainerID),
			})
			_, updateErr := db.Exec("UPDATE steps SET status = 'error', updated_at = NOW() WHERE id = $1", step.StepID)
			if updateErr != nil {
				stepLogger.Printf("Step %d: Failed to update step status to error after docker run failure: %v\n", step.StepID, updateErr)
			}
			} else {
				stepLogger.Printf("Step %d: command 'docker run %v' succeeded. Container ID: %s, Name: %s\n", step.StepID, detachedParams, newContainerID, containerName)
				config.DockerRun.ContainerID = newContainerID
				config.DockerRun.ContainerName = containerName
				config.DockerRun.ImageID = imageIDToUse // Ensure the used ImageID is saved
				
				newSettingsJSON, marshalErr := json.Marshal(config)
				if marshalErr != nil {
					stepLogger.Printf("Step %d: Failed to marshal updated settings after successful run: %v\n", step.StepID, marshalErr)
					// Even if marshal fails, the container ran. Store success for run, but error for step state.
					StoreStepResult(db, step.StepID, map[string]interface{}{
						"result":         "success", // Container ran
						"message":        fmt.Sprintf("Container %s (%s) started, but failed to marshal updated settings: %v", containerName, newContainerID, marshalErr),
						"container_id":   newContainerID,
						"container_name": containerName,
					})
					// Mark step as error because its settings are inconsistent
					_, updateErr := db.Exec("UPDATE steps SET status = 'error', updated_at = NOW() WHERE id = $1", step.StepID)
					if updateErr != nil {
						stepLogger.Printf("Step %d: Failed to update step status to error after marshal failure: %v\n", step.StepID, updateErr)
					}
				} else {
					// Store success result
					StoreStepResult(db, step.StepID, map[string]interface{}{
						"result":         "success",
						"message":        "container started in detached mode and step updated",
						"container_id":   newContainerID,
						"container_name": containerName,
					})
					// Update step settings and status to 'success'
					_, updateErr := db.Exec("UPDATE steps SET settings = $1, status = 'success', updated_at = NOW() WHERE id = $2", string(newSettingsJSON), step.StepID)
					if updateErr != nil {
						stepLogger.Printf("Step %d: Failed to update step settings/status to success: %v\n", step.StepID, updateErr)
						// If DB update fails, the step is in an inconsistent state.
						// The result was already stored, but we should log this problem.
					}
				}
			}
		}
	}
