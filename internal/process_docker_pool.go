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
func processDockerPoolSteps(db *sql.DB, stepID int) error {
	var query string
	var rows *sql.Rows
	var err error

	if stepID != 0 {
		query = `SELECT s.id, s.task_id, s.settings, t.base_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.id = $1 AND s.settings ? 'docker_pool'`
		rows, err = db.Query(query, stepID)
	} else {
		query = `SELECT s.id, s.task_id, s.settings, t.base_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE t.status = 'active'
		AND t.base_path IS NOT NULL
		AND t.base_path <> ''
		AND s.settings ? 'docker_pool'`
		rows, err = db.Query(query)
	}

	if err != nil {
		models.StepLogger.Println("Docker pool query error:", err)
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var step models.StepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.BasePath); err != nil {
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

		// Read image_tag directly from task.settings.docker.image_tag
		taskSettings, err := models.GetTaskSettings(db, step.TaskID)
		if err != nil {
			models.StepLogger.Printf("Step %d: CRITICAL: Could not load task settings for docker_pool. Error: %v\n", step.StepID, err)
			return err
		}
		imageTag := taskSettings.Docker.ImageTag
		if imageTag == "" {
			models.StepLogger.Printf("Step %d: CRITICAL: No image_tag found in task settings.\n", step.StepID)
			return fmt.Errorf("no image_tag found in task settings")
		}
		models.StepLogger.Printf("Step %d: Using image_tag '%s' from task settings\n", step.StepID, imageTag)

		// Logic for handling a pool of containers
		numContainers := config.PoolSize
		if numContainers <= 0 {
			numContainers = 1 // Default to one container if not specified
		}

		runningContainers := []models.ContainerInfo{}
		for _, container := range config.Containers {
			inspectCmd := exec.Command("docker", "inspect", container.ContainerID)
			output, err := inspectCmd.CombinedOutput()
			if err == nil {
				var inspectResult []struct {
					Config struct {
						Image string `json:"Image"`
					} `json:"Config"`
					State struct {
						Running bool `json:"Running"`
					} `json:"State"`
				}
				if err := json.Unmarshal(output, &inspectResult); err == nil && len(inspectResult) > 0 {
					if inspectResult[0].State.Running && inspectResult[0].Config.Image == imageTag {
						runningContainers = append(runningContainers, container)
					} else if inspectResult[0].State.Running && inspectResult[0].Config.Image != imageTag {
						// Stop and remove outdated container
						exec.Command("docker", "stop", container.ContainerID).Run()
						exec.Command("docker", "rm", container.ContainerID).Run()
					}
				}
			}
		}

		if len(runningContainers) < numContainers {
			// Start missing or new containers with updated image
			dockerRunParams := config.Parameters
			processedDockerRunParams := []string{}
			for _, param := range dockerRunParams {
				replacedParam := strings.Replace(param, "%%IMAGETAG%%", imageTag, -1)
				if replacedParam != "--rm" {
					processedDockerRunParams = append(processedDockerRunParams, strings.Fields(replacedParam)...)
				}
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

			for i := len(runningContainers); i < numContainers; i++ {
				randomSuffix, _ := models.GenerateRandomString(4)
				containerName := fmt.Sprintf("tasksync_step%d_%s_%d", step.StepID, randomSuffix, i)
				// Ensure processedDockerRunParams does NOT contain the image tag
				// Append imageTag ONCE, after all Docker options, before entrypoint/command args
				cmdArgs := append([]string{"run", "-d", "--name", containerName}, processedDockerRunParams...)
				// cmdArgs = append(cmdArgs, imageTag)
				// If you have entrypoint/command args, append them here (e.g., --login, -c ...)
				// Example: cmdArgs = append(cmdArgs, "--login", "-c", "while true; do sleep 30; done")
				models.StepLogger.Printf("Constructed docker command: docker %s\n", strings.Join(cmdArgs, " "))
				cmd := exec.Command("docker", cmdArgs...)
				output, err := cmd.CombinedOutput()
				models.StepLogger.Printf("Step %d: docker run output: %s", step.StepID, string(output))
				if err == nil {
					newContainerID := strings.TrimSpace(string(output))
					runningContainers = append(runningContainers, models.ContainerInfo{ContainerID: newContainerID, ContainerName: containerName})
				} else {
					models.StepLogger.Printf("Step %d: failed to start container: %v. Output: %s\n", step.StepID, err, string(output))
				}
			}
		}

		config.Containers = runningContainers
		updatedConfig := map[string]interface{}{
			"docker_pool": config,
		}
		newSettingsJSON, _ := json.Marshal(updatedConfig)

		// --- NEW: Update task.settings.containers with the correct mapping ---
		taskSettings, err = models.GetTaskSettings(db, step.TaskID)
		if err == nil {
			taskSettings.Containers = runningContainers
			if err := models.UpdateTaskSettings(db, step.TaskID, taskSettings); err != nil {
				models.StepLogger.Printf("Step %d: Failed to update containers in task settings: %v\n", step.StepID, err)
			} else {
				models.StepLogger.Printf("Step %d: Updated task settings with containers: %+v\n", step.StepID, runningContainers)
			}
		}
		//-----------------------------------------------------------

		models.StoreStepResult(db, step.StepID, map[string]interface{}{
			"result":     "success",
			"message":    fmt.Sprintf("%d/%d containers running.", len(runningContainers), numContainers),
			"containers": runningContainers,
		})
		db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(newSettingsJSON), step.StepID)
	}
	return nil
}
