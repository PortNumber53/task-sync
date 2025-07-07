package internal

import (
	"database/sql"
	"encoding/json"
	"log"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// processDockerBuildSteps processes docker build steps for active tasks
func processDockerBuildSteps(db *sql.DB, stepLogger *log.Logger, stepID int) {
	var steps []models.StepExec
	var err error

	if stepID != 0 {
		var step models.StepExec
		query := `
            SELECT s.id, s.task_id, s.title, s.settings, t.local_path
            FROM steps s
            JOIN tasks t ON s.task_id = t.id
            WHERE s.id = $1 AND s.settings ? 'docker_build'
        `
		err = db.QueryRow(query, stepID).Scan(&step.StepID, &step.TaskID, &step.Title, &step.Settings, &step.LocalPath)
		if err != nil {
			if err == sql.ErrNoRows {
				stepLogger.Printf("Step %d not found or not a docker_build step.", stepID)
				return
			}
			stepLogger.Printf("Failed to query specific step %d: %v", stepID, err)
			return
		}
		steps = append(steps, step)
	} else {
		query := `
            SELECT s.id, s.task_id, s.title, s.settings, t.local_path
            FROM steps s
            JOIN tasks t ON s.task_id = t.id
            WHERE t.status = 'active' AND s.settings ? 'docker_build'
            ORDER BY s.id
        `
		rows, err := db.Query(query)
		if err != nil {
			stepLogger.Printf("Failed to query active docker_build steps: %v", err)
			return
		}
		defer rows.Close()

		for rows.Next() {
			var step models.StepExec
			if err := rows.Scan(&step.StepID, &step.TaskID, &step.Title, &step.Settings, &step.LocalPath); err != nil {
				stepLogger.Printf("Failed to scan step: %v", err)
				continue
			}
			steps = append(steps, step)
		}
	}

	for _, step := range steps {
		var config models.DockerBuildConfig
		var settingsMap map[string]json.RawMessage
		if err := json.Unmarshal([]byte(step.Settings), &settingsMap); err != nil {
			stepLogger.Printf("Step %d: failed to unmarshal top-level settings: %v\n", step.StepID, err)
			continue
		}

		dockerBuildJSON, ok := settingsMap["docker_build"]
		if !ok {
			stepLogger.Printf("Step %d: settings missing 'docker_build' key\n", step.StepID)
			continue
		}

		if err := json.Unmarshal(dockerBuildJSON, &config); err != nil {
			stepLogger.Printf("Step %d: failed to unmarshal docker_build settings: %v\n", step.StepID, err)
			continue
		}

		if dockerfile, ok := config.Files["Dockefile"]; ok {
			delete(config.Files, "Dockefile")
			config.Files["Dockerfile"] = dockerfile
			stepLogger.Printf("Step %d: Corrected 'Dockefile' to 'Dockerfile' in files map\n", step.StepID)
		}

		taskSettings, err := models.GetTaskSettings(db, step.TaskID)
		if err != nil {
			stepLogger.Printf("Step %d: Failed to get task settings for task %d: %v. Skipping build.", step.StepID, step.TaskID, err)
			continue
		}

		if taskSettings.Docker.ImageTag == "" {
			stepLogger.Printf("Step %d: CRITICAL: Task settings do not contain an image_tag. Skipping build.", step.StepID)
			continue
		}

		config.ImageTag = taskSettings.Docker.ImageTag
		// config.ImageID will be set after successful build
		stepLogger.Printf("Step %d: Using image tag '%s' from task settings.\n", step.StepID, config.ImageTag)

		// Ensure config.ImageID is loaded from taskSettings
		config.ImageID = taskSettings.Docker.ImageID
		// Log config.ImageID and config.ImageTag after loading config
		stepLogger.Printf("Step %d: Loaded config.ImageID = '%s', config.ImageTag = '%s' before Docker inspect.", step.StepID, config.ImageID, config.ImageTag)

		buildNeeded := false
		cmdInspect := exec.Command("docker", "image", "inspect", config.ImageTag)
		outputInspect, errInspect := cmdInspect.Output()
		if errInspect != nil {
			stepLogger.Printf("Step %d: Docker image inspect failed for tag %s: %v. Will trigger build.", step.StepID, config.ImageTag, errInspect)
			buildNeeded = true
		} else {
			var inspectResult []map[string]interface{}
			if json.Unmarshal(outputInspect, &inspectResult) == nil && len(inspectResult) > 0 {
				id, ok := inspectResult[0]["Id"].(string)
				stepLogger.Printf("Step %d: Docker inspect: image tag = %s, docker id = %s, config.ImageID = %s", step.StepID, config.ImageTag, id, config.ImageID)
				if !ok {
					stepLogger.Printf("Step %d: Could not extract Docker image ID from inspect. Will trigger build.", step.StepID)
					buildNeeded = true
				} else if strings.TrimPrefix(id, "sha256:") != strings.TrimPrefix(config.ImageID, "sha256:") {
					stepLogger.Printf("Step %d: Docker image ID mismatch: docker inspect ID '%s', config.ImageID '%s'. Will trigger build.", step.StepID, id, config.ImageID)
					buildNeeded = true
				} else {
					stepLogger.Printf("Step %d: Docker image ID matches: docker inspect ID '%s', config.ImageID '%s'. No build needed based on image ID.", step.StepID, id, config.ImageID)
				}
			} else {
				stepLogger.Printf("Step %d: Docker inspect: could not parse inspect result for tag %s. Will trigger build.", step.StepID, config.ImageTag)
				buildNeeded = true
			}
		}

		filesChanged := false
		for filePath, oldHash := range config.Files {
			fullPath := filepath.Join(step.LocalPath, filePath)
			currentHash, err := models.GetSHA256(fullPath)
			if err != nil {
				stepLogger.Printf("Step %d: File %s: could not compute hash (error: %v). Marking as changed.", step.StepID, filePath, err)
				filesChanged = true
				break
			}
			if oldHash == "" {
				stepLogger.Printf("Step %d: File %s: stored hash is empty. Marking as changed.", step.StepID, filePath)
				filesChanged = true
				break
			}
			if currentHash != oldHash {
				stepLogger.Printf("Step %d: File %s: hash mismatch. Marking as changed.", step.StepID, filePath)
				filesChanged = true
				break
			}
			stepLogger.Printf("Step %d: File %s: stored hash = %s, current hash = %s", step.StepID, filePath, oldHash, currentHash)
		}
		if filesChanged {
			buildNeeded = true
		}

		if buildNeeded {
			stepLogger.Printf("Step %d: Building image %s:%s\n", step.StepID, config.ImageTag, config.ImageID)
			if err := executeDockerBuild(step.LocalPath, &config, step.StepID, db, stepLogger); err != nil {
				stepLogger.Printf("Step %d: docker build failed: %v\n", step.StepID, err)
				continue
			}

			// After successful build, update ImageID in task settings
			taskSettings.Docker.ImageID = config.ImageID
			if err := models.UpdateTaskSettings(db, step.TaskID, taskSettings); err != nil {
				stepLogger.Printf("Step %d: Failed to update task settings with new image ID: %v\n", step.StepID, err)
				// Continue, as the build was successful, but log the error
			}

			// After successful build, update file hashes and persist to step settings
			for filePath := range config.Files {
				fullPath := filepath.Join(step.LocalPath, filePath)
				hash, err := models.GetSHA256(fullPath)
				if err != nil {
					stepLogger.Printf("Step %d: Warning: could not compute hash for %s: %v\n", step.StepID, filePath, err)
					continue
				}
				config.Files[filePath] = hash
			}
			persistMap := map[string]interface{}{
				"files":      config.Files,
				"depends_on": config.DependsOn,
				"parameters": config.Parameters,
				// add any other allowed fields here
			}
			settingsMap["docker_build"], _ = json.Marshal(persistMap)
			updatedSettings, _ := json.Marshal(settingsMap)
			_, err := db.Exec("UPDATE steps SET settings = $1 WHERE id = $2", string(updatedSettings), step.StepID)
			if err != nil {
				stepLogger.Printf("Step %d: Failed to persist updated file hashes to step settings: %v\n", step.StepID, err)
			} else {
				stepLogger.Printf("Step %d: Updated file hashes only in step settings (never persisting image_id or image_tag).", step.StepID)
			}
		} else {
			stepLogger.Printf("Step %d: Docker image '%s:%s' is ready. Build skipped.\n", step.StepID, config.ImageTag, config.ImageID)
		}
	}
}
