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

	"github.com/PortNumber53/task-sync/pkg/models"
)

func processDockerPullSteps(db *sql.DB, stepID int) {
	var query string
	var rows *sql.Rows
	var err error

	if stepID != 0 {
		query = `SELECT s.id, s.task_id, s.settings, t.base_path FROM steps s JOIN tasks t ON s.task_id = t.id WHERE s.id = $1 AND s.settings ? 'docker_pull'`
		rows, err = db.Query(query, stepID)
	} else {
		query = `SELECT s.id, s.task_id, s.settings, t.base_path FROM steps s JOIN tasks t ON s.task_id = t.id WHERE t.status = 'active' AND s.settings ? 'docker_pull'`
		rows, err = db.Query(query)
	}

	if err != nil {
		models.StepLogger.Printf("Error querying for docker_pull steps: %v\n", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var step models.StepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.BasePath); err != nil {
			models.StepLogger.Printf("Error scanning docker_pull step: %v\n", err)
			continue
		}

		var config models.DockerPullConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			errMsg := fmt.Sprintf("Error unmarshalling docker_pull settings for step %d: %v", step.StepID, err)
			models.StepLogger.Println(errMsg)
			if errStore := models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg}); errStore != nil {
				models.StepLogger.Printf("Failed to store error result for step %d: %v\n", step.StepID, errStore)
			}
			continue
		}

		// Debug log for original image_id from step settings
		models.StepLogger.Printf("Step %d: Original image_id from step settings: '%s'\n", step.StepID, config.ImageID)

		// Fetch and use image details from task settings
		var imageIDToUse, imageTagToUse string
		var taskSettingsJSON sql.NullString
		err = db.QueryRow(`SELECT settings FROM tasks WHERE id = $1`, step.TaskID).Scan(&taskSettingsJSON)
		if err != nil && err != sql.ErrNoRows {
			models.StepLogger.Printf("Step %d: failed to get task settings: %v\n", step.StepID, err)
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "Error retrieving task settings"})
			continue
		} else if taskSettingsJSON.Valid {
			var taskSettings map[string]interface{}
			if err := json.Unmarshal([]byte(taskSettingsJSON.String), &taskSettings); err == nil {
				if dockerInfo, ok := taskSettings["docker"].(map[string]interface{}); ok {
					if tag, ok := dockerInfo["image_tag"].(string); ok && tag != "" {
						imageTagToUse = tag
					} else {
						models.StepLogger.Printf("Step %d: image_tag not found in task settings\n", step.StepID)
					}
					if id, ok := dockerInfo["image_hash"].(string); ok && id != "" {
						imageIDToUse = id
					}
				}
			}
		}
		if imageTagToUse == "" {
			models.StepLogger.Printf("Step %d: No image_tag found in task settings\n", step.StepID)
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "Missing image_tag in task settings"})
			continue
		}
		// Override config with task settings values
		config.ImageTag = imageTagToUse
		config.ImageID = imageIDToUse // Set if available, or keep as is if not present
		models.StepLogger.Printf("Step %d: Overridden image_id to '%s' from task settings\n", step.StepID, config.ImageID)

		// Check dependencies
		depsMet, err := models.CheckDependencies(db, &step)
		if err != nil {
			models.StepLogger.Printf("Error checking dependencies for step %d: %v\n", step.StepID, err)
			// Optionally, store this as a failure or keep step active for retry
			continue
		}
		if !depsMet {
			models.StepLogger.Printf("Step %d: Dependencies not met for docker_pull.\n", step.StepID)
			continue // Skip this step until dependencies are met
		}

		// Check PreventRunBefore
		if config.PreventRunBefore != "" {
			preventTime, err := time.Parse(time.RFC3339, config.PreventRunBefore)
			if err != nil {
				models.StepLogger.Printf("Step %d: Error parsing PreventRunBefore timestamp '%s': %v. Proceeding with pull.\n", step.StepID, config.PreventRunBefore, err)
			} else {
				if time.Now().Before(preventTime) {
					models.StepLogger.Printf("Step %d: Skipping docker_pull for image '%s' due to PreventRunBefore setting. Will run after %s.\n", step.StepID, config.ImageTag, preventTime.Format(time.RFC1123))
					continue // Skip this step execution
				}
			}
		}

		if err := executeDockerPull(&config, step.StepID, db); err != nil {
			errMsg := fmt.Sprintf("Error executing docker_pull for step %d: %v", step.StepID, err)
			models.StepLogger.Println(errMsg)
			if errStore := models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg}); errStore != nil {
				models.StepLogger.Printf("Failed to store error result for step %d: %v\n", step.StepID, errStore)
			}
		} else {
			// Success: Only update allowed fields in step.settings (e.g., PreventRunBefore).
			// Never persist image_id or image_tag to step.settings. Only docker_build may write image_id to task.settings.
			// image_tag is user-supplied and must never be written by any step type.

			// Create a copy of config with image_id and image_tag omitted before persisting.
			persistConfig := config
			persistConfig.ImageID = ""
			persistConfig.ImageTag = ""

			updatedSettingsBytes, marshalErr := json.Marshal(persistConfig)
			if marshalErr != nil {
				errMsg := fmt.Sprintf("Error marshalling updated docker_pull settings for step %d: %v", step.StepID, marshalErr)
				models.StepLogger.Println(errMsg)
				if errStore := models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": errMsg}); errStore != nil {
					models.StepLogger.Printf("Failed to store marshalling error result for step %d: %v\n", step.StepID, errStore)
				}
				continue
			}

			_, updateErr := db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(updatedSettingsBytes), step.StepID)
			if updateErr != nil {
				models.StepLogger.Printf("Error saving updated settings for step %d: %v\n", step.StepID, updateErr)
			}

			// Update PreventRunBefore for the next run
			persistConfig.PreventRunBefore = time.Now().Add(6 * time.Hour).Format(time.RFC3339)
			updatedSettingsBytesWithPrevent, marshalErrWithPrevent := json.Marshal(persistConfig)
			if marshalErrWithPrevent != nil {
				models.StepLogger.Printf("Step %d: Error marshalling updated PreventRunBefore: %v\n", step.StepID, marshalErrWithPrevent)
			} else {
				_, updateErrWithPrevent := db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(updatedSettingsBytesWithPrevent), step.StepID)
				if updateErrWithPrevent != nil {
					models.StepLogger.Printf("Step %d: Error saving updated PreventRunBefore: %v\n", step.StepID, updateErrWithPrevent)
				}
			}

			if errStore := models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "success", "image_id": config.ImageID, "prevent_run_before_next": persistConfig.PreventRunBefore}); errStore != nil {
				models.StepLogger.Printf("Failed to store success result for step %d: %v\n", step.StepID, errStore)
			}
			models.StepLogger.Printf("Step %d: docker_pull for image '%s' SUCCESS. Image ID: %s\n", step.StepID, config.ImageTag, config.ImageID)
		}
	}
}

func executeDockerPull(config *models.DockerPullConfig, stepID int, db *sql.DB) error {
	if config.ImageTag == "" {
		return fmt.Errorf("image_tag is required for docker_pull")
	}

	cmd := exec.Command("docker", "pull", config.ImageTag)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &stdoutBuf) // Write to both os.Stdout and buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf) // Write to both os.Stderr and buffer

	models.StepLogger.Printf("Step %d: Executing docker pull %s\n", stepID, config.ImageTag)
	err := cmd.Run()

	stdoutOutput := stdoutBuf.String()
	stderrOutput := stderrBuf.String()

	if len(stdoutOutput) > 0 {
		models.StepLogger.Printf("Step %d: Docker pull stdout:\n%s\n", stepID, stdoutOutput)
	}
	if len(stderrOutput) > 0 {
		models.StepLogger.Printf("Step %d: Docker pull stderr:\n%s\n", stepID, stderrOutput)
	}

	if err != nil {
		return fmt.Errorf("docker pull failed for %s: %v. Stderr: %s", config.ImageTag, err, stderrOutput)
	}

	// Get the image ID of the pulled image
	imageID, _, err := models.GetDockerImageID(db, stepID, models.StepLogger)
	if err != nil {
		return fmt.Errorf("failed to get image ID for %s: %v", config.ImageTag, err)
	}

	// If an ImageID was provided in the config, verify it matches the pulled image.
	// Otherwise, populate it with the actual ID.
	if config.ImageID != "" && config.ImageID != imageID {
		return fmt.Errorf("pulled image ID '%s' does not match expected ID '%s' for tag '%s'", imageID, config.ImageID, config.ImageTag)
	}
	config.ImageID = imageID // Store the actual/verified image ID

	models.StepLogger.Printf("Step %d: Successfully pulled image '%s' with ID '%s'\n", stepID, config.ImageTag, imageID)
	return nil
}
