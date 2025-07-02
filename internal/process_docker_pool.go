package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// processDockerPoolSteps processes docker pool steps for active tasks
func processDockerPoolSteps(db *sql.DB) error {
	query := `SELECT s.id, s.task_id, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE t.status = 'active'
		AND t.local_path IS NOT NULL
		AND t.local_path <> ''
		AND s.settings ? 'docker_pool'`

	rows, err := db.Query(query)
	if err != nil {
		models.StepLogger.Println("Docker pool query error:", err)
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

		config := configHolder.DockerPool
		if config == nil {
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "docker_pool config not found"})
			models.StepLogger.Printf("Step %d: docker_pool config not found in step settings\n", step.StepID)
			continue
		}

		ok, err := models.CheckDependencies(db, &step)
		if err != nil {
			models.StepLogger.Printf("Step %d: error checking dependencies: %v\n", step.StepID, err)
			continue
		}
		if !ok {
			models.StepLogger.Printf("Step %d: waiting for dependencies to complete\n", step.StepID)
			continue
		}

		var imageIDToUse string
		var buildStepImageID string

		for _, dep := range config.DependsOn {
			depInfo, err := models.GetStepInfo(db, dep.ID)
			if err != nil {
				models.StepLogger.Printf("Step %d: Error getting info for dependency step %d: %v\n", step.StepID, dep.ID, err)
				continue
			}
			var depSettingsMap map[string]interface{}
			if err := json.Unmarshal([]byte(depInfo), &depSettingsMap); err != nil {
				models.StepLogger.Printf("Step %d: Error unmarshalling dependency step %d settings: %v\n", step.StepID, dep.ID, err)
				continue
			}
			if dockerBuildSettings, ok := depSettingsMap["docker_build"].(map[string]interface{}); ok {
				if id, ok := dockerBuildSettings["image_id"].(string); ok && id != "" && id != "sha256:" {
					buildStepImageID = id
					models.StepLogger.Printf("Step %d: Found image_id '%s' from build dependency step %d\n", step.StepID, buildStepImageID, dep.ID)
					break
				}
			}
		}

		if buildStepImageID != "" {
			imageIDToUse = buildStepImageID
			models.StepLogger.Printf("Step %d: Prioritizing image_id '%s' from build dependency.\n", step.StepID, imageIDToUse)
		} else {
			models.StepLogger.Printf("Step %d: No build dependency with a valid image_id found. Falling back to inspecting tag '%s'.\n", step.StepID, config.ImageTag)
			currentImageID, _, err := models.GetDockerImageID(db, step.StepID, models.StepLogger)
			if err != nil {
				models.StepLogger.Printf("Step %d: error getting current image ID by tag: %v\n", step.StepID, err)
				continue
			}
			if currentImageID == "" {
				models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "no image found with tag: " + config.ImageTag})
				models.StepLogger.Printf("Step %d: no image found with tag %s\n", step.StepID, config.ImageTag)
				continue
			}
			imageIDToUse = currentImageID
		}

		if config.ImageID != imageIDToUse {
			models.StepLogger.Printf("Step %d: Stored image_id ('%s') is outdated. Updating to '%s' and skipping this run.\n", step.StepID, config.ImageID, imageIDToUse)
			config.ImageID = imageIDToUse
			configHolder.DockerPool = config // Update the configHolder with the modified config
			updatedSettings, _ := json.Marshal(configHolder) // Marshal the entire configHolder
			_, err := db.Exec(`UPDATE steps SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettings), step.StepID)
			if err != nil {
				models.StepLogger.Printf("Step %d: Failed to update settings with new image_id: %v\n", step.StepID, err)
			}
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "pending", "message": "Image ID updated from dependency, will run next cycle."})
			continue
		}

		// Logic for handling a pool of containers
		numContainers := config.PoolSize
		if numContainers <= 0 {
			numContainers = 1 // Default to one container if not specified
		}

		var runningContainers []models.ContainerInfo
		for _, container := range config.Containers {
			inspectCmd := exec.Command("docker", "inspect", container.ContainerID)
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
						runningContainers = append(runningContainers, container)
					}
				}
			}
		}

		if len(runningContainers) == numContainers {
			models.StepLogger.Printf("Step %d: All %d containers are already running with the correct image.\n", step.StepID, numContainers)
			config.Containers = runningContainers
			// Update the config in the holder to maintain proper structure
			configHolder.DockerPool = config
			updatedSettingsJSON, _ := json.Marshal(configHolder)
			models.StoreStepResult(db, step.StepID, map[string]interface{}{
				"result":     "success",
				"message":    "All containers already running and DB state confirmed.",
				"containers": runningContainers,
			})
			db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(updatedSettingsJSON), step.StepID)
			continue
		}

		// Start missing containers
		for i := len(runningContainers); i < numContainers; i++ {
			dockerRunParams := config.Parameters
			processedDockerRunParams := []string{}
			for _, param := range dockerRunParams {
				replacedParam := strings.Replace(param, "%%IMAGETAG%%", imageIDToUse, -1)
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
					// Add a keep-alive command if one isn't present in the parameters
					keepAliveArgs := []string{"-c", "while true; do sleep 30; done"}
					processedDockerRunParams = append(processedDockerRunParams, keepAliveArgs...)
				}
			}

			randomSuffix, _ := models.GenerateRandomString(4)
			containerName := fmt.Sprintf("tasksync_step%d_%s_%d", step.StepID, randomSuffix, i)

			detachedParams := append([]string{"-d", "--name", containerName}, processedDockerRunParams...)
			cmdArgs := append([]string{"run"}, detachedParams...)
			cmd := exec.Command("docker", cmdArgs...)
			output, err := cmd.CombinedOutput()
			newContainerID := strings.TrimSpace(string(output))

			if err != nil {
				models.StepLogger.Printf("Step %d: command 'docker run %v' failed: %v\nOutput: %s\n", step.StepID, detachedParams, err, newContainerID)
				// Decide on error handling, maybe fail the whole step
			} else {
				models.StepLogger.Printf("Step %d: command 'docker run %v' succeeded. Container ID: %s, Name: %s\n", step.StepID, detachedParams, newContainerID, containerName)
				runningContainers = append(runningContainers, models.ContainerInfo{ContainerID: newContainerID, ContainerName: containerName})
			}
		}

		config.Containers = runningContainers
		config.ImageID = imageIDToUse
		configHolder.DockerPool = config // Update the configHolder with the modified config
		newSettingsJSON, _ := json.Marshal(configHolder) // Marshal the entire configHolder

		models.StoreStepResult(db, step.StepID, map[string]interface{}{
			"result":     "success",
			"message":    fmt.Sprintf("%d/%d containers running.", len(runningContainers), numContainers),
			"containers": runningContainers,
		})
		db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(newSettingsJSON), step.StepID)
	}
	return nil
}
