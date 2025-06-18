package internal

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

func processDockerPullSteps(db *sql.DB) {
	query := `SELECT s.id, s.task_id, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.status = 'active'
		AND t.status = 'active' -- Assuming tasks also need to be active
		AND s.settings ? 'docker_pull'`

	rows, err := db.Query(query)
	if err != nil {
		stepLogger.Printf("Error querying for docker_pull steps: %v\n", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var step stepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.LocalPath); err != nil {
			stepLogger.Printf("Error scanning docker_pull step: %v\n", err)
			continue
		}

		var config DockerPullConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			errMsg := fmt.Sprintf("Error unmarshalling docker_pull settings for step %d: %v", step.StepID, err)
			stepLogger.Println(errMsg)
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg}); errStore != nil {
				stepLogger.Printf("Failed to store error result for step %d: %v\n", step.StepID, errStore)
			}
			continue
		}

		// Check dependencies
		if len(config.DockerPull.DependsOn) > 0 {
			depsMet, err := checkDependencies(db, step.StepID, config.DockerPull.DependsOn)
			if err != nil {
				stepLogger.Printf("Error checking dependencies for step %d: %v\n", step.StepID, err)
				// Optionally, store this as a failure or keep step active for retry
				continue
			}
			if !depsMet {
				stepLogger.Printf("Step %d: Dependencies not met for docker_pull.\n", step.StepID)
				continue // Skip this step until dependencies are met
			}
		}

		// Check PreventRunBefore
		if config.DockerPull.PreventRunBefore != "" {
			preventTime, err := time.Parse(time.RFC3339, config.DockerPull.PreventRunBefore)
			if err != nil {
				stepLogger.Printf("Step %d: Error parsing PreventRunBefore timestamp '%s': %v. Proceeding with pull.\n", step.StepID, config.DockerPull.PreventRunBefore, err)
			} else {
				if time.Now().Before(preventTime) {
					stepLogger.Printf("Step %d: Skipping docker_pull for image '%s' due to PreventRunBefore setting. Will run after %s.\n", step.StepID, config.DockerPull.ImageTag, preventTime.Format(time.RFC1123))
					continue // Skip this step execution
				}
			}
		}

		if err := executeDockerPull(&config, step.StepID, db); err != nil {
			errMsg := fmt.Sprintf("Error executing docker_pull for step %d: %v", step.StepID, err)
			stepLogger.Println(errMsg)
			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg}); errStore != nil {
				stepLogger.Printf("Failed to store error result for step %d: %v\n", step.StepID, errStore)
			}
		} else {
			// Success, store updated config (if ImageID was fetched and not just verified)
			updatedSettingsBytes, marshalErr := json.Marshal(config)
			if marshalErr != nil {
				errMsg := fmt.Sprintf("Error marshalling updated docker_pull settings for step %d: %v", step.StepID, marshalErr)
				stepLogger.Println(errMsg)
				// Store original failure if this happens, or a new failure for marshalling
				if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg}); errStore != nil {
					stepLogger.Printf("Failed to store marshalling error result for step %d: %v\n", step.StepID, errStore)
				}
				continue
			}

			_, updateErr := db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(updatedSettingsBytes), step.StepID)
			if updateErr != nil {
				stepLogger.Printf("Error saving updated settings for step %d: %v\n", step.StepID, updateErr)
				// Decide if this is critical enough to mark the step as failed despite successful pull
			}

			// Update PreventRunBefore for the next run
			config.DockerPull.PreventRunBefore = time.Now().Add(6 * time.Hour).Format(time.RFC3339)
			updatedSettingsBytesWithPrevent, marshalErrWithPrevent := json.Marshal(config)
			if marshalErrWithPrevent != nil {
				stepLogger.Printf("Step %d: Error marshalling updated PreventRunBefore: %v\n", step.StepID, marshalErrWithPrevent)
				// The main result is already stored, this is a secondary update failure
			} else {
				_, updateErrWithPrevent := db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(updatedSettingsBytesWithPrevent), step.StepID)
				if updateErrWithPrevent != nil {
					stepLogger.Printf("Step %d: Error saving updated PreventRunBefore: %v\n", step.StepID, updateErrWithPrevent)
				}
			}

			if errStore := StoreStepResult(db, step.StepID, map[string]interface{}{"result": "success", "image_id": config.DockerPull.ImageID, "prevent_run_before_next": config.DockerPull.PreventRunBefore}); errStore != nil {
				stepLogger.Printf("Failed to store success result for step %d: %v\n", step.StepID, errStore)
			}
			stepLogger.Printf("Step %d: docker_pull for image '%s' SUCCESS. Image ID: %s\n", step.StepID, config.DockerPull.ImageTag, config.DockerPull.ImageID)
		}
	}
}

func executeDockerPull(config *DockerPullConfig, stepID int, db *sql.DB) error {
	if config.DockerPull.ImageTag == "" {
		return fmt.Errorf("image_tag is required for docker_pull")
	}

	cmdArgs := []string{"pull", config.DockerPull.ImageTag}
	cmd := exec.Command("docker", cmdArgs...)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &stdoutBuf) // Write to both os.Stdout and buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf) // Write to both os.Stderr and buffer

	stepLogger.Printf("Step %d: Executing docker pull %s\n", stepID, config.DockerPull.ImageTag)
	err := cmd.Run()

	stdoutOutput := stdoutBuf.String()
	stderrOutput := stderrBuf.String()

	if len(stdoutOutput) > 0 {
		stepLogger.Printf("Step %d: Docker pull stdout:\n%s\n", stepID, stdoutOutput)
	}
	if len(stderrOutput) > 0 {
		stepLogger.Printf("Step %d: Docker pull stderr:\n%s\n", stepID, stderrOutput)
	}

	if err != nil {
		return fmt.Errorf("docker pull failed for %s: %v. Stderr: %s", config.DockerPull.ImageTag, err, stderrOutput)
	}

	// Get the image ID of the pulled image
	actualImageID, err := getDockerImageID(config.DockerPull.ImageTag)
	if err != nil {
		return fmt.Errorf("failed to get image ID for %s after pull: %w", config.DockerPull.ImageTag, err)
	}

	// If an ImageID was provided in the config, verify it matches the pulled image.
	// Otherwise, populate it with the actual ID.
	if config.DockerPull.ImageID != "" && config.DockerPull.ImageID != actualImageID {
		return fmt.Errorf("pulled image ID '%s' does not match expected ID '%s' for tag '%s'", actualImageID, config.DockerPull.ImageID, config.DockerPull.ImageTag)
	}
	config.DockerPull.ImageID = actualImageID // Store the actual/verified image ID

	stepLogger.Printf("Step %d: Successfully pulled image '%s' with ID '%s'\n", stepID, config.DockerPull.ImageTag, actualImageID)
	return nil
}
