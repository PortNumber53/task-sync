package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"

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

		// Ensure ImageTag is set. If not provided, generate a default.
		if config.ImageTag == "" {
			config.ImageTag = fmt.Sprintf("task-sync-step-%d", step.StepID)
			stepLogger.Printf("Step %d: ImageTag not specified, defaulting to '%s'\n", step.StepID, config.ImageTag)
		}

		// Check if any files have changed
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

		buildSkipped := false
		if !filesChanged && config.ImageID != "" {
			stepLogger.Printf("Step %d: docker build skipped, no file changes and image already built. ImageID: %s, ImageTag: %s\n", step.StepID, config.ImageID, config.ImageTag)
			buildSkipped = true
		}

		if !buildSkipped {
			// Log the build start
			stepLogger.Printf("Step %d: building image %s:%s\n", step.StepID, config.ImageID, config.ImageTag)

			// Execute the build
			if err := executeDockerBuild(step.LocalPath, &config, step.StepID, db); err != nil {
				stepLogger.Printf("Step %d: docker build failed: %v\n", step.StepID, err)
				continue
			}
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

		updatedConfig := map[string]interface{}{
			"docker_build": config,
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

		stepLogger.Printf("Step %d: docker build completed successfully, ImageID: %s\n", step.StepID, config.ImageID)
	}
}
