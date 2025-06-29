package internal

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// processDockerBuildSteps processes docker build steps for active tasks
func processDockerBuildSteps(db *sql.DB) {
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
		var step stepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.LocalPath); err != nil {
			stepLogger.Println("Row scan error:", err)
			continue
		}

		var config DockerBuildConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "invalid docker build config"})
			stepLogger.Printf("Step %d: invalid docker build config: %v\n", step.StepID, err)
			continue
		}

		ok, err := checkDependencies(db, step.StepID, stepLogger)
		if err != nil {
			stepLogger.Printf("Step %d: error checking dependencies: %v\n", step.StepID, err)
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "error checking dependencies"})
			continue
		}
		if !ok {
			stepLogger.Printf("Step %d: waiting for dependencies to complete\n", step.StepID)
			continue
		}

		shouldBuild := false
		if config.DockerBuild.Files == nil {
			config.DockerBuild.Files = make(map[string]string)
		}

		for file, storedModTimeStr := range config.DockerBuild.Files {
			filePath := filepath.Join(step.LocalPath, file)
			fileInfo, err := os.Stat(filePath)
			if err != nil {
				stepLogger.Printf("Step %d: error checking file %s: %v. Assuming change.\n", step.StepID, file, err)
				shouldBuild = true
				config.DockerBuild.Files[file] = "" // Mark as needing update
				break
			}

			currentModTime := fileInfo.ModTime().Format(time.RFC3339)
			if storedModTimeStr != currentModTime {
				shouldBuild = true
				config.DockerBuild.Files[file] = currentModTime
			}
		}

		if !shouldBuild && config.DockerBuild.ImageID != "" {
			stepLogger.Printf("Step %d: no changes detected, skipping build for image %s\n", step.StepID, config.DockerBuild.ImageID)
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "success", "message": "No changes detected, build skipped"})
			continue
		}

		if err := executeDockerBuild(step.LocalPath, &config, step.StepID, db); err != nil {
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": err.Error()})
			stepLogger.Printf("Step %d: docker build failed: %v\n", step.StepID, err)
			continue
		}

		updatedSettings, jsonErr := json.Marshal(config)
		if jsonErr != nil {
			stepLogger.Printf("Step %d: failed to marshal updated settings: %v\n", step.StepID, jsonErr)
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "failed to marshal updated settings after build"})
			continue
		}
		_, execErr := db.Exec(`UPDATE steps SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettings), step.StepID)
		if execErr != nil {
			stepLogger.Printf("Step %d: failed to update settings in DB: %v\n", step.StepID, execErr)
			StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "failed to update settings in DB after build"})
			continue
		}

		StoreStepResult(db, step.StepID, map[string]interface{}{"result": "success"})
		stepLogger.Printf("Step %d: docker build completed successfully, ImageID: %s\n", step.StepID, config.DockerBuild.ImageID)
	}
}
