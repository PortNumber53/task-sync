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

		// We always manage exactly 6 containers mapped as:
		// original, golden, solution1..solution4
		desiredKeys := []string{"original", "golden", "solution1", "solution2", "solution3", "solution4"}

		// Load any existing containers from task.settings (new location) for continuity
		runningContainers := map[string]models.ContainerInfo{}

		// First, validate any existing taskSettings.ContainersMap entries and keep running ones with matching image
		if taskSettings.ContainersMap != nil {
			for key, container := range taskSettings.ContainersMap {
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
							runningContainers[key] = container
						} else if inspectResult[0].State.Running && inspectResult[0].Config.Image != imageTag {
							// Stop and remove outdated container
							exec.Command("docker", "stop", container.ContainerID).Run()
							exec.Command("docker", "rm", container.ContainerID).Run()
						}
					}
				}
			}
		}

		// Backward compatibility: also consider step.settings.docker_pool.containers if any
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
						// Place into the first empty desired key slot
						for _, key := range desiredKeys {
							if _, exists := runningContainers[key]; !exists {
								runningContainers[key] = container
								break
							}
						}
					} else if inspectResult[0].State.Running && inspectResult[0].Config.Image != imageTag {
						// Stop and remove outdated container
						exec.Command("docker", "stop", container.ContainerID).Run()
						exec.Command("docker", "rm", container.ContainerID).Run()
					}
				}
			}
		}

		// Determine docker run parameters: read from task.settings.docker_run_parameters if present, else fallback to step config then persist to task
		dockerRunParams := taskSettings.DockerRunParameters
		if len(dockerRunParams) == 0 && len(config.Parameters) > 0 {
			dockerRunParams = config.Parameters
		}

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

		// Start missing containers to satisfy all desired keys
		for _, key := range desiredKeys {
			if _, exists := runningContainers[key]; exists {
				continue
			}
			randomSuffix, _ := models.GenerateRandomString(4)
			containerName := fmt.Sprintf("tasksync_%d_%s_%s", step.TaskID, randomSuffix, key)
			cmdArgs := append([]string{"run", "-d", "--name", containerName}, processedDockerRunParams...)
			models.StepLogger.Printf("Constructed docker command: docker %s\n", strings.Join(cmdArgs, " "))
			cmd := exec.Command("docker", cmdArgs...)
			output, err := cmd.CombinedOutput()
			models.StepLogger.Printf("Step %d: docker run output: %s", step.StepID, string(output))
			if err == nil {
				newContainerID := strings.TrimSpace(string(output))
				runningContainers[key] = models.ContainerInfo{ContainerID: newContainerID, ContainerName: containerName}
			} else {
				models.StepLogger.Printf("Step %d: failed to start container for key %s: %v. Output: %s\n", step.StepID, key, err, string(output))
			}
		}

		// Write new state:
		// 1) Update task.settings with containers map and docker_run_parameters
		taskSettings.ContainersMap = make(map[string]models.ContainerInfo)
		for _, key := range desiredKeys {
			if c, ok := runningContainers[key]; ok {
				taskSettings.ContainersMap[key] = c
			}
		}
		if len(taskSettings.DockerRunParameters) == 0 && len(dockerRunParams) > 0 {
			taskSettings.DockerRunParameters = dockerRunParams
		}
		if err := models.UpdateTaskSettings(db, step.TaskID, taskSettings); err != nil {
			models.StepLogger.Printf("Step %d: Failed to update containers map/params in task settings: %v\n", step.StepID, err)
		} else {
			models.StepLogger.Printf("Step %d: Updated task settings with containers map and docker_run_parameters\n", step.StepID)
		}

		// 2) Minimize step.settings.docker_pool (remove containers/parameters to avoid duplication)
		config.Containers = nil
		config.Parameters = nil
		// Keep depends_on and keep_forever, source_step_id, etc.
		updatedConfig := map[string]interface{}{
			"docker_pool": config,
		}
		newSettingsJSON, _ := json.Marshal(updatedConfig)

		models.StoreStepResult(db, step.StepID, map[string]interface{}{
			"result":  "success",
			"message": fmt.Sprintf("Started/validated containers: %d", len(taskSettings.ContainersMap)),
		})
		db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(newSettingsJSON), step.StepID)
	}
	return nil
}
