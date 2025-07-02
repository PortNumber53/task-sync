package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// processDockerRunSteps processes docker run steps for active tasks
func processDockerRunSteps(db *sql.DB) error {
	query := `SELECT s.id, s.task_id, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE t.status = 'active'
		AND t.local_path IS NOT NULL
		AND t.local_path <> ''
		AND s.settings ? 'docker_run'`

	rows, err := db.Query(query)
	if err != nil {
		models.StepLogger.Println("Docker run query error:", err)
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var step models.StepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.LocalPath); err != nil {
			models.StepLogger.Println("Row scan error:", err)
			continue
		}

		var configHolder models.StepConfigHolder
		if err := json.Unmarshal([]byte(step.Settings), &configHolder); err != nil {
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "invalid step config"})
			models.StepLogger.Printf("Step %d: invalid step config: %v\n", step.StepID, err)
			continue
		}

		config := configHolder.DockerRun
		if config == nil {
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "docker_run config not found"})
			models.StepLogger.Printf("Step %d: docker_run config not found in step settings\n", step.StepID)
			continue
		}
		models.StepLogger.Printf("Step %d: Unmarshaled DockerRunConfig Parameters: %+v\n", step.StepID, config.Parameters)

		ok, err := models.CheckDependencies(db, &step)
		if err != nil {
			models.StepLogger.Printf("Step %d: error checking dependencies: %v\n", step.StepID, err)
			continue
		}
		if !ok {
			models.StepLogger.Printf("Step %d: waiting for dependencies to complete\n", step.StepID)
			continue
		}

		imageIDToUse, imageTagToUse, err := models.FindImageDetailsRecursive(db, step.StepID, make(map[int]bool), models.StepLogger)
		if err != nil {
			models.StepLogger.Printf("Step %d: Error finding image details recursively: %v\n", step.StepID, err)
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": fmt.Sprintf("Error finding image details: %v", err)})
			continue
		}

		if imageIDToUse == "" || imageTagToUse == "" {
			models.StepLogger.Printf("Step %d: No valid image_id or image_tag found from dependencies.\n", step.StepID)
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "no valid image_id or image_tag found from dependencies"})
			continue
		}

		// Update config with the found image details
		config.ImageID = imageIDToUse
		config.ImageTag = imageTagToUse

		if config.ImageID != imageIDToUse {
			models.StepLogger.Printf("Step %d: Stored image_id ('%s') is outdated. Updating to '%s' and skipping this run.\n", step.StepID, config.ImageID, imageIDToUse)
			config.ImageID = imageIDToUse
			// Update the config in the holder to maintain proper structure
			configHolder.DockerRun = config
			updatedSettings, _ := json.Marshal(configHolder)
			_, err := db.Exec(`UPDATE steps SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettings), step.StepID)
			if err != nil {
				models.StepLogger.Printf("Step %d: Failed to update settings with new image_id: %v\n", step.StepID, err)
			}
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "pending", "message": "Image ID updated from dependency, will run next cycle."})
			continue
		}

		// First check if there's any running container with the correct image tag
		cmd := exec.Command("docker", "ps", "--filter", fmt.Sprintf("ancestor=%s", imageTagToUse), "--format", "{{.ID}}")
		psOutput, psErr := cmd.CombinedOutput()
		if psErr == nil {
			containerIDs := strings.Split(strings.TrimSpace(string(psOutput)), "\n")
			if len(containerIDs) > 0 && containerIDs[0] != "" {
				// Found at least one running container with the correct image tag
				containerID := containerIDs[0]
				
				// Get the container name
				nameCmd := exec.Command("docker", "inspect", "--format", "{{.Name}}", containerID)
				nameOutput, nameErr := nameCmd.CombinedOutput()
				containerName := "unknown"
				if nameErr == nil {
					containerName = strings.TrimPrefix(strings.TrimSpace(string(nameOutput)), "/")
				}
				
				models.StepLogger.Printf("Step %d: Found existing container %s (%s) running with image %s. Using this container.", 
					step.StepID, containerName, containerID, imageTagToUse)
				
				// Update the config with the found container
				config.ContainerID = containerID
				config.ContainerName = containerName
				config.ImageID = imageIDToUse
				config.ImageTag = imageTagToUse
				
				// Update the config in the holder to maintain proper structure
				configHolder.DockerRun = config
				updatedSettingsJSON, _ := json.Marshal(configHolder)
				
				models.StoreStepResult(db, step.StepID, map[string]interface{}{
					"result":         "success",
					"message":        "Found existing container running with correct image. Using this container.",
					"container_id":   containerID,
					"container_name": containerName,
				})
				
				_, dbErr := db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(updatedSettingsJSON), step.StepID)
				if dbErr != nil {
					models.StepLogger.Printf("Step %d: Failed to update step settings for found container: %v\n", step.StepID, dbErr)
				}
				
				continue
			}
		}
		
		// If we didn't find an existing container, check if the one in our config is still running
		if config.ContainerID != "" {
			inspectCmd := exec.Command("docker", "inspect", config.ContainerID)
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
						models.StepLogger.Printf("Step %d: Container %s (%s) is already running with the correct image %s. Ensuring DB state is consistent.\n", step.StepID, config.ContainerName, config.ContainerID, imageIDToUse)

						updatedSettingsJSON, marshalErr := json.Marshal(config)
						if marshalErr != nil {
							models.StepLogger.Printf("Step %d: Failed to marshal settings for already running container: %v\n", step.StepID, marshalErr)
							models.StoreStepResult(db, step.StepID, map[string]interface{}{
								"result":         "success", // Container is running
								"message":        fmt.Sprintf("Container %s (%s) confirmed running, but failed to marshal current settings to DB: %v", config.ContainerName, config.ContainerID, marshalErr),
								"container_id":   config.ContainerID,
								"container_name": config.ContainerName,
							})
							// Mark step as error because its settings in DB might be inconsistent
							_, dbErr := db.Exec("UPDATE steps SET updated_at = NOW() WHERE id = $1", step.StepID)
							if dbErr != nil {
								models.StepLogger.Printf("Step %d: Also failed to update updated_at after marshal error for running container: %v\n", step.StepID, dbErr)
							}
						} else {
							models.StoreStepResult(db, step.StepID, map[string]interface{}{
								"result":         "success",
								"message":        "Container already running and DB state confirmed.",
								"container_id":   config.ContainerID,
								"container_name": config.ContainerName,
								"image_id_used":  imageIDToUse,
							})
							// Update step settings (even if unchanged, for updated_at)
							_, dbErr := db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(updatedSettingsJSON), step.StepID)
							if dbErr != nil {
								models.StepLogger.Printf("Step %d: Failed to update step settings to complete for already running container: %v\n", step.StepID, dbErr)
							}
						}
						continue
					} else if !inspectResult[0].State.Running {
						models.StepLogger.Printf("Step %d: Container %s (%s) found but not running. Will attempt to start a new one.\n", step.StepID, config.ContainerName, config.ContainerID)
					} else if inspectResult[0].Config.Image != imageIDToUse {
						models.StepLogger.Printf("Step %d: Container %s (%s) running with wrong image (%s vs %s). Will attempt to start a new one.\n", step.StepID, config.ContainerName, config.ContainerID, inspectResult[0].Config.Image, imageIDToUse)
					}
				}
			} else {
				models.StepLogger.Printf("Step %d: Failed to inspect container %s. It might have been removed. Will attempt to start a new one. Error: %v\n", step.StepID, config.ContainerID, err)
			}
			config.ContainerID = ""
			config.ContainerName = ""
		}

		dockerRunParams := config.Parameters
		if len(dockerRunParams) == 0 {
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "docker_run parameters are not specified or invalid"})
			models.StepLogger.Printf("Step %d: docker_run parameters are not specified or invalid in command object\n", step.StepID)
			continue
		}

		imageTagFound := false
		for _, param := range dockerRunParams {
			if strings.Contains(param, "%%IMAGETAG%%") {
				imageTagFound = true
				break
			}
		}

		if !imageTagFound {
			models.StepLogger.Printf("Step %d: '%%IMAGETAG%%' placeholder not found in docker_run parameters. Skipping.", step.StepID)
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "'%%IMAGETAG%%' placeholder not found in docker_run parameters"})
			continue
		}

		processedDockerRunParams := []string{}
		for _, param := range dockerRunParams {
			// If KeepForever is true, and the parameter is "--rm", skip it.
			if config.KeepForever && param == "--rm" {
				continue
			}
			replacedParam := strings.Replace(param, "%%IMAGETAG%%", imageTagToUse, -1)
			processedDockerRunParams = append(processedDockerRunParams, strings.Fields(replacedParam)...)
		}

		if config.KeepForever {
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
		}

		randomSuffix, err := models.GenerateRandomString(4)
		if err != nil {
			models.StepLogger.Printf("Step %d: Failed to generate random suffix for container name: %v. Using fixed suffix.\n", step.StepID, err)
			randomSuffix = "xxxx" // Fallback suffix
		}
		containerName := fmt.Sprintf("tasksync_step%d_%s", step.StepID, randomSuffix)

		detachedParams := append([]string{"-d", "--name", containerName}, processedDockerRunParams...)

		cmdArgs := append([]string{"run"}, detachedParams...)
		runCmd := exec.Command("docker", cmdArgs...)
		runOutput, runErr := runCmd.CombinedOutput()
		newContainerID := strings.TrimSpace(string(runOutput))

		if runErr != nil {
			models.StepLogger.Printf("Step %d: command 'docker run %v' failed: %v\nOutput: %s\n", step.StepID, detachedParams, runErr, newContainerID)
			models.StoreStepResult(db, step.StepID, map[string]interface{}{
				"result":  "failure",
				"message": fmt.Sprintf("docker run command failed: %v. Output: %s", runErr, newContainerID),
			})
		} else {
			models.StepLogger.Printf("Step %d: command 'docker run %v' succeeded. Container ID: %s, Name: %s\n", step.StepID, detachedParams, newContainerID, containerName)
			config.ContainerID = newContainerID
			config.ContainerName = containerName
			config.ImageID = imageIDToUse
			config.ImageTag = imageTagToUse // Ensure the used ImageTag is saved

			// Update the config in the holder to maintain proper structure
			configHolder.DockerRun = config
			newSettingsJSON, marshalErr := json.Marshal(configHolder)
			if marshalErr != nil {
				models.StepLogger.Printf("Step %d: Failed to marshal updated settings after successful run: %v\n", step.StepID, marshalErr)
				models.StoreStepResult(db, step.StepID, map[string]interface{}{
					"result":         "success", // Container ran
					"message":        fmt.Sprintf("Container %s (%s) started, but failed to marshal updated settings: %v", containerName, newContainerID, marshalErr),
					"container_id":   newContainerID,
					"container_name": containerName,
				})
			} else {
				models.StoreStepResult(db, step.StepID, map[string]interface{}{
					"result":         "success",
					"message":        "container started in detached mode and step updated",
					"container_id":   newContainerID,
					"container_name": containerName,
				})
				_, updateErr := db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(newSettingsJSON), step.StepID)
				if updateErr != nil {
					models.StepLogger.Printf("Step %d: Failed to update step settings to success: %v\n", step.StepID, updateErr)
				}
			}
		}
	}
	return nil
}
