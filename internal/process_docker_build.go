package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// processDockerBuildSteps processes docker build steps for active tasks
func processDockerBuildSteps(db *sql.DB, stepLogger *log.Logger) {
	query := `SELECT s.id, s.task_id, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE t.status = 'active'
		AND t.local_path IS NOT NULL
		AND t.local_path <> ''
		AND s.settings::text LIKE '%docker_build%'`

	rows, err := db.Query(query)
	if err != nil {
		stepLogger.Println("Docker build query error:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var step StepExec
		var taskSettings map[string]interface{}
		var taskSettingsJSON sql.NullString
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.LocalPath); err != nil {
			stepLogger.Println("Row scan error:", err)
			continue
		}

		// First, unmarshal into a map to extract the docker_build section
		var configMap map[string]json.RawMessage
		if err := json.Unmarshal([]byte(step.Settings), &configMap); err != nil {
			stepLogger.Printf("Step %d: invalid settings format: %v\n", step.StepID, err)
			continue
		}

		// Extract the docker_build section as json.RawMessage
		dockerBuildRaw, ok := configMap["docker_build"]
		if !ok {
			stepLogger.Printf("Step %d: missing docker_build section in settings\n", step.StepID)
			continue
		}

		var config models.DockerBuildConfig
		if err := json.Unmarshal(dockerBuildRaw, &config); err != nil {
			stepLogger.Printf("Step %d: invalid docker build config: %v\n", step.StepID, err)
			continue
		}

		// First, query task settings for image_tag and image_hash before setting defaults
		err = db.QueryRow(`SELECT settings FROM tasks WHERE id = $1`, step.TaskID).Scan(&taskSettingsJSON)
		if err == nil && taskSettingsJSON.Valid {
			stepLogger.Printf("Step %d: Raw task settings JSON: %s\n", step.StepID, taskSettingsJSON.String)
			var taskSettings map[string]interface{}
			if err := json.Unmarshal([]byte(taskSettingsJSON.String), &taskSettings); err == nil {
				stepLogger.Printf("Step %d: Unmarshaled task settings: %v\n", step.StepID, taskSettings)
				if dockerInfo, ok := taskSettings["docker"].(map[string]interface{}); ok {
					stepLogger.Printf("Step %d: Docker info found: %v\n", step.StepID, dockerInfo)
					if tag, ok := dockerInfo["image_tag"].(string); ok && tag != "" && config.ImageTag == "" {
						config.ImageTag = tag
						stepLogger.Printf("Step %d: Set ImageTag from task settings to '%s'\n", step.StepID, config.ImageTag)
					}
					if hash, ok := dockerInfo["image_hash"].(string); ok && hash != "" && config.ImageID == "" {
						config.ImageID = hash
						stepLogger.Printf("Step %d: Set ImageID from task settings to '%s'\n", step.StepID, config.ImageID)
					}
				} else {
					stepLogger.Printf("Step %d: No docker key in task settings\n", step.StepID)
				}
			} else {
				stepLogger.Printf("Step %d: Failed to unmarshal task settings: %v\n", step.StepID, err)
			}
		} else if err != nil && err != sql.ErrNoRows {
			stepLogger.Printf("Step %d: Failed to get task settings: %v\n", step.StepID, err)
		}

		// Set defaults if still not specified, but only if not set in task settings
		if config.ImageTag == "" {
			config.ImageTag = fmt.Sprintf("task-sync-step-%d", step.StepID)
			stepLogger.Printf("Step %d: ImageTag not specified, defaulting to '%s'\n", step.StepID, config.ImageTag)
		}
		if config.ImageID == "" {
			config.ImageID = "unknown" // Temporary placeholder, will be updated after build if needed
			stepLogger.Printf("Step %d: ImageID not specified, setting to 'unknown'\n", step.StepID)
		}

		// Check if image exists and hash matches using docker image inspect
		buildNeeded := false
		cmdInspect := exec.Command("docker", "image", "inspect", config.ImageTag)
		outputInspect, errInspect := cmdInspect.Output()
		found := false
		stepLogger.Printf("Step %d: Debugging hash check for %s - Expected ID: '%s', Inspect Output: %s\n", step.StepID, config.ImageTag, config.ImageID, string(outputInspect))
		if errInspect == nil {
			var inspectResult []map[string]interface{}
			if errUnmarshal := json.Unmarshal(outputInspect, &inspectResult); errUnmarshal == nil {
				for _, img := range inspectResult {
					id, ok := img["Id"].(string)
					if ok {
						trimmedID := strings.TrimPrefix(id, "sha256:")
						stepLogger.Printf("Step %d: Trimmed ID from inspect: '%s', Comparing to expected: '%s'\n", step.StepID, trimmedID, config.ImageID)
						if trimmedID == config.ImageID {
							found = true
							break
						}
					}
				}
			}
		}
		if !found {
			stepLogger.Printf("Step %d: No matching hash found, triggering rebuild\n", step.StepID)
			buildNeeded = true
		} else {
			stepLogger.Printf("Step %d: Hash match found, build skipped\n", step.StepID)
		}

		// Also check if files have changed as before
		filesChanged := false
		for filePath, oldHash := range config.Files {
			fullPath := filepath.Join(step.LocalPath, filePath)
			currentHash, err := models.GetSHA256(fullPath)
			if err != nil {
				stepLogger.Printf("Step %d: failed to get SHA256 for %s: %v\n", step.StepID, fullPath, err)
				filesChanged = true // Treat as changed if we can't read hash
				break
			}
			if currentHash != oldHash {
				filesChanged = true
				break
			}
		}
		if filesChanged {
			buildNeeded = true
			stepLogger.Printf("Step %d: Files have changed, triggering rebuild\n", step.StepID)
		}

		if buildNeeded {
			// Log the build start
			stepLogger.Printf("Step %d: building image %s:%s\n", step.StepID, config.ImageID, config.ImageTag)
			if err := executeDockerBuild(step.LocalPath, &config, step.StepID, db); err != nil {
				stepLogger.Printf("Step %d: docker build failed: %v\n", step.StepID, err)
				continue
			}
			// After successful build, get the new image ID and update task settings image_hash
			cmdInspect = exec.Command("docker", "image", "inspect", config.ImageTag, "--format", "{{.ID}}")
			outputInspect, err = cmdInspect.Output()
			if err != nil {
				stepLogger.Printf("Step %d: Failed to inspect image after build: %v\n", step.StepID, err)
			} else {
				newImageID := strings.TrimPrefix(strings.TrimSpace(string(outputInspect)), "sha256:")
				// Update tasks.settings.image_hash
				_, err = db.Exec(`UPDATE tasks SET settings = jsonb_set(settings, '{docker,image_hash}', to_jsonb($1::text)) WHERE id = $2`, newImageID, step.TaskID)
				if err != nil {
					stepLogger.Printf("Step %d: Failed to update image_hash in task settings: %v\n", step.StepID, err)
				} else {
					stepLogger.Printf("Step %d: Updated image_hash to '%s' in task settings after successful build\n", step.StepID, newImageID)
				}
				config.ImageID = newImageID // Update local config for consistency
			}
		} else {
			stepLogger.Printf("Step %d: Docker image '%s:%s' is ready. Build skipped: true\n", step.StepID, config.ImageTag, config.ImageID)
		}

		// Update the config with the docker_build section and new file hashes
		for filePath := range config.Files {
			fullPath := filepath.Join(step.LocalPath, filePath)
			currentHash, err := models.GetSHA256(fullPath)
			if err != nil {
				stepLogger.Printf("Step %d: failed to get SHA256 for %s after build: %v\n", step.StepID, fullPath, err)
				continue
			}
			config.Files[filePath] = currentHash
		}

		// Create a new config map for the step settings, excluding ImageID and ImageTag
		// as these are now stored in tasks.settings
		stepConfig := models.DockerBuildConfig{
			DependsOn:  config.DependsOn,
			Parameters: config.Parameters,
			Files:      config.Files,
			// ImageID and ImageTag are intentionally omitted here
		}
		updatedConfig := map[string]interface{}{
			"docker_build": stepConfig,
		}

		updatedSettings, jsonErr := json.Marshal(updatedConfig)
		if jsonErr != nil {
			stepLogger.Printf("Step %d: failed to marshal updated settings: %v\n", step.StepID, jsonErr)
			continue
		}

		_, execErr := db.Exec(`UPDATE steps SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettings), step.StepID)
		if execErr != nil {
			stepLogger.Printf("Step %d: failed to update settings in DB: %v\n", step.StepID, execErr)
			continue
		}

		// Update the task's settings with the Docker image information
		// Get current task settings
		err = db.QueryRow(`SELECT settings FROM tasks WHERE id = $1`, step.TaskID).Scan(&taskSettingsJSON)
		if err != nil && err != sql.ErrNoRows {
			stepLogger.Printf("Step %d: failed to get task settings: %v\n", step.StepID, err)
		}

		// Parse existing settings or create new map
		if taskSettingsJSON.Valid && taskSettingsJSON.String != "" {
			if err := json.Unmarshal([]byte(taskSettingsJSON.String), &taskSettings); err != nil {
				stepLogger.Printf("Step %d: failed to unmarshal task settings: %v\n", step.StepID, err)
				taskSettings = make(map[string]interface{})
			}
		} else {
			taskSettings = make(map[string]interface{})
		}

		// Update the docker section in task settings
		dockerInfo := map[string]string{
			"image_tag":  config.ImageTag,
			"image_hash": config.ImageID,
		}
		taskSettings["docker"] = dockerInfo

		// Marshal and update the task settings
		updatedTaskSettings, jsonErr := json.Marshal(taskSettings)
		if jsonErr != nil {
			stepLogger.Printf("Step %d: failed to marshal updated task settings: %v\n", step.StepID, jsonErr)
		} else {
			_, execErr := db.Exec(`UPDATE tasks SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedTaskSettings), step.TaskID)
			if execErr != nil {
				stepLogger.Printf("Step %d: failed to update task settings in DB: %v\n", step.StepID, execErr)
			} else {
				stepLogger.Printf("Step %d: updated task %d settings with Docker image info\n", step.StepID, step.TaskID)
			}
		}
	}
}
