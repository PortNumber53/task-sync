package internal

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
)

// processDockerBuildSteps processes docker build steps for active tasks
func processDockerBuildSteps(db *sql.DB) {
	query := `SELECT s.id, s.task_id, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.status = 'active'
		AND t.status = 'active'
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
		var step stepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.LocalPath); err != nil {
			stepLogger.Println("Row scan error:", err)
			continue
		}

		// Parse the docker build config
		var config DockerBuildConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "invalid docker build config"}); errStore != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
			}
			stepLogger.Printf("Step %d: invalid docker build config: %v\n", step.StepID, err)
			continue
		}

		// Check if dependencies are met
		ok, err := checkDependencies(db, step.StepID, config.DockerBuild.DependsOn)
		if err != nil {
			stepLogger.Printf("Step %d: error checking dependencies: %v\n", step.StepID, err)
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "error checking dependencies"}); errStore != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
			}
			continue
		}
		if !ok {
			stepLogger.Printf("Step %d: waiting for dependencies to complete\n", step.StepID)
			// StoreStepResult(db, step.StepID, map[string]interface{}{"result": "pending", "message": "waiting for dependencies"}) // Optional: update status to pending
			continue
		}

		// Check if files have changed
		shouldBuild := false
		if config.DockerBuild.Hashes == nil { // Ensure Hashes map is initialized
			config.DockerBuild.Hashes = make(map[string]string)
		}
		for _, file := range config.DockerBuild.Files {
			filePath := filepath.Join(step.LocalPath, file)
			currentHash, err := calculateFileHash(filePath)
			if err != nil {
				stepLogger.Printf("Step %d: error calculating hash for %s: %v. Assuming change.\n", step.StepID, file, err)
				shouldBuild = true
				config.DockerBuild.Hashes[file] = "" // Mark as needing update due to error
				break
			}

			storedHash, exists := config.DockerBuild.Hashes[file]
			if !exists || storedHash != currentHash {
				shouldBuild = true
				config.DockerBuild.Hashes[file] = currentHash
			}
		}

		// If no changes and we already have an image ID, skip the build
		if !shouldBuild && config.DockerBuild.ImageID != "" {
			stepLogger.Printf("Step %d: no changes detected, skipping build for image %s\n", step.StepID, config.DockerBuild.ImageID)
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "success", "message": "No changes detected, build skipped"}); errStore != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
			}
			continue
		}

		// Execute the docker build
		// executeDockerBuild should update config.DockerBuild.ImageID on success
		if err := executeDockerBuild(step.LocalPath, &config, step.StepID, db); err != nil {
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": err.Error()}); errStore != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
			}
			stepLogger.Printf("Step %d: docker build failed: %v\n", step.StepID, err)
			continue
		}

		// Persist updated settings (including new hashes and ImageID)
		updatedSettings, jsonErr := json.Marshal(config)
		if jsonErr != nil {
			stepLogger.Printf("Step %d: failed to marshal updated settings: %v\n", step.StepID, jsonErr)
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "failed to marshal updated settings after build"}); errStore != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
			}
			continue
		}
		_, execErr := db.Exec(`UPDATE steps SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettings), step.StepID)
		if execErr != nil {
			stepLogger.Printf("Step %d: failed to update settings in DB: %v\n", step.StepID, execErr)
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "failed to update settings in DB after build"}); errStore != nil {
				stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
			}
			continue
		}

		if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "success"}); errStore != nil {
			stepLogger.Println("Failed to update results for step", step.StepID, ":", errStore)
		}
		stepLogger.Printf("Step %d: docker build completed successfully, ImageID: %s\n", step.StepID, config.DockerBuild.ImageID)
	}
}
