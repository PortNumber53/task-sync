package internal

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

// DockerRunConfig represents the configuration for a docker run step
type DockerRunConfig struct {
	DockerRun struct {
		DependsOn []struct {
			ID int `json:"id"`
		} `json:"depends_on"`
		ImageTag            string            `json:"image_tag"`
		ImageID             string            `json:"image_id,omitempty"`
		Container           string            `json:"container,omitempty"`
		DockerRunParameters []string          `json:"docker_run_parameters,omitempty"`
		Command             []string          `json:"command,omitempty"`
		Env                 map[string]string `json:"env,omitempty"`
		Ports               map[string]string `json:"ports,omitempty"`
		Volumes             map[string]string `json:"volumes,omitempty"`
		WorkingDir          string            `json:"working_dir,omitempty"`
		AutoRemove          bool              `json:"auto_remove,omitempty"`
	} `json:"docker_run"`
}

// processDockerRunSteps processes all docker_run steps that are ready to be executed
func processDockerRunSteps(db *sql.DB) {
	query := `
		SELECT s.id, s.task_id, s.settings, s.results, t.local_path, s.title
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.status = 'active'
		AND t.status = 'active'
		AND s.settings::text LIKE '%docker_run%'
		AND (
		    -- Steps that haven't been run yet or failed
		    s.results IS NULL
		    OR s.results = 'null'::jsonb
		    OR s.results->>'result' IS NULL
		    OR s.results->>'result' != 'success'
		    OR jsonb_typeof(s.results) = 'null'
		    -- Or steps with a container that might not be running anymore
		    OR (
		        s.results->>'result' = 'success'
		        AND s.results->>'container_name' IS NOT NULL
		        AND s.results->>'container_name' != ''
		    )
		)
	`

	rows, err := db.Query(query)
	if err != nil {
		stepLogger.Println("Docker run query error:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var step stepExec
		var resultsStr sql.NullString
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &resultsStr, &step.LocalPath, &step.Title); err != nil {
			stepLogger.Printf("Step %d: error scanning row: %v\n", step.StepID, err)
			continue
		}
		stepLogger.Printf("Found step %d (%s) to process\n", step.StepID, step.Title)

		// Parse the settings
		var config DockerRunConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			stepLogger.Printf("Step %d: error parsing settings: %v\n", step.StepID, err)
			storeStepResult(db, step.StepID, map[string]interface{}{
				"result":  "error",
				"message": fmt.Sprintf("Error parsing settings: %v", err),
			})
			continue
		}

		// Check dependencies
		if len(config.DockerRun.DependsOn) > 0 {
			depsCompleted, err := checkDependencies(db, step.StepID, config.DockerRun.DependsOn)
			if err != nil {
				stepLogger.Printf("Step %d: error checking dependencies: %v\n", step.StepID, err)
				continue
			}
			if !depsCompleted {
				stepLogger.Printf("Step %d: waiting for dependencies to complete\n", step.StepID)
				continue
			}

			// Verify the image matches the dependency
			if err := verifyDependencyImage(db, config, step.StepID); err != nil {
				stepLogger.Printf("Step %d: dependency verification failed: %v\n", step.StepID, err)
				storeStepResult(db, step.StepID, map[string]interface{}{
					"result":  "error",
					"message": fmt.Sprintf("Dependency verification failed: %v", err),
				})
				continue
			}
		}

		// Log step information
		stepLogger.Printf("Step %d (%s): Processing Docker run step\n", step.StepID, step.Title)
		if step.LocalPath != "" {
			stepLogger.Printf("Step %d: Using working directory: %s\n", step.StepID, step.LocalPath)
		}

		// Log the image being used
		imageRef := ""
		if config.DockerRun.ImageTag != "" {
			imageRef = config.DockerRun.ImageTag
		} else if config.DockerRun.ImageID != "" {
			imageRef = fmt.Sprintf("image ID: %s", config.DockerRun.ImageID)
		}
		if imageRef != "" {
			stepLogger.Printf("Step %d: Using image: %s\n", step.StepID, imageRef)
		}

		// Generate a unique container name if not specified
		if config.DockerRun.Container == "" {
			// Use format: task-<step_id>-<random_suffix>
			randBytes := make([]byte, 4)
			if _, err := rand.Read(randBytes); err != nil {
				stepLogger.Printf("Step %d: Warning: failed to generate random bytes: %v\n", step.StepID, err)
				// Fallback to timestamp if random generation fails
				config.DockerRun.Container = fmt.Sprintf("task-%d-%d", step.StepID, time.Now().UnixNano())
			} else {
				config.DockerRun.Container = fmt.Sprintf("task-%d-%s", step.StepID, hex.EncodeToString(randBytes))
			}
			stepLogger.Printf("Step %d: Using generated container name: %s\n", step.StepID, config.DockerRun.Container)
		}

		// Check if we have a container name from previous runs
		var containerName string
		if resultsStr.Valid && resultsStr.String != "" && resultsStr.String != "null" {
			var results map[string]interface{}
			err := json.Unmarshal([]byte(resultsStr.String), &results)
			if err == nil {
				if name, ok := results["container_name"].(string); ok && name != "" {
					// Check if the existing container is still running
					isRunning, _, err := isContainerRunning(name)
					if err == nil && isRunning {
						stepLogger.Printf("Step %d: Container %s is already running\n", step.StepID, name)
						continue
					}
					// Container exists but not running, we'll proceed to start a new one
					containerName = name
				}
			}
		}

		// If we have a container name from previous runs, clean it up first
		if containerName != "" {
			cmd := exec.Command("docker", "rm", "-f", containerName)
			if err := cmd.Run(); err != nil {
				stepLogger.Printf("Step %d: Warning: failed to remove existing container %s: %v\n",
					step.StepID, containerName, err)
			}
		}

		// Run the container
		containerID, containerHash, err := runDockerContainer(config, step.LocalPath, stepLogger)
		if err != nil {
			errMsg := fmt.Sprintf("Step %d: error running container: %v\n", step.StepID, err)
			stepLogger.Print(errMsg)
			storeStepResult(db, step.StepID, map[string]interface{}{
				"result":  "error",
				"message": errMsg,
			})
			continue
		}

		stepLogger.Printf("Step %d: Successfully started container: %s\n", step.StepID, containerID)

		// Update the step settings with the container hash
		if config.DockerRun.ImageID == "" {
			config.DockerRun.ImageID = containerHash

			// Save the updated settings back to the database
			updatedSettings, err := json.Marshal(config)
			if err == nil {
				_, err = db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(updatedSettings), step.StepID)
				if err != nil {
					stepLogger.Printf("Step %d: failed to update step settings: %v\n", step.StepID, err)
				}
			} else {
				stepLogger.Printf("Step %d: failed to marshal updated settings: %v\n", step.StepID, err)
			}
		}

		// Verify the container is still running
		isRunning, statusMsg, err := isContainerRunning(containerID)
		if err != nil {
			// If we can't check the status, log the error but continue
			stepLogger.Printf("Step %d: error checking container status: %v\n", step.StepID, err)
		} else if !isRunning {
			// If container is not running, mark the step as failed
			errMsg := fmt.Sprintf("Container is not running: %s", statusMsg)
			stepLogger.Printf("Step %d: %s\n", step.StepID, errMsg)
			storeStepResult(db, step.StepID, map[string]interface{}{
				"result":  "error",
				"message": errMsg,
			})
			continue
		}

		// Store the results with the actual container name from Docker
		containerName = config.DockerRun.Container
		// Verify the container name in Docker matches what we expect
		inspectCmd := exec.Command("docker", "inspect", "-f", "{{.Name}}", containerID)
		nameBytes, err := inspectCmd.CombinedOutput()
		if err == nil {
			// Remove leading slash from the container name
			containerName = strings.TrimPrefix(strings.TrimSpace(string(nameBytes)), "/")
			stepLogger.Printf("Step %d: Container name in Docker: %s\n", step.StepID, containerName)
		}

		result := map[string]interface{}{
			"result":         "success",
			"container_id":   containerID,
			"container_name": containerName, // Use the actual container name from Docker
			"container_hash": containerHash,
			"image_hash":     containerHash,
		}

		storeStepResult(db, step.StepID, result)
		stepLogger.Printf("Step %d: container %s is running successfully with hash %s\n", step.StepID, containerID, containerHash)
	}
}

// verifyDependencyImage checks if the image from the dependency matches the expected image
func verifyDependencyImage(db *sql.DB, config DockerRunConfig, stepID int) error {
	if len(config.DockerRun.DependsOn) == 0 {
		return nil
	}

	// Get the first dependency (we'll use the first one for image verification)
	depID := config.DockerRun.DependsOn[0].ID

	// Get the dependent step's results
	var resultsStr sql.NullString
	err := db.QueryRow(`SELECT results FROM steps WHERE id = $1`, depID).Scan(&resultsStr)
	if err != nil {
		return fmt.Errorf("error getting dependent step %d: %w", depID, err)
	}

	if !resultsStr.Valid {
		return fmt.Errorf("dependent step %d has no results", depID)
	}

	var results map[string]interface{}
	if err := json.Unmarshal([]byte(resultsStr.String), &results); err != nil {
		return fmt.Errorf("error parsing dependent step results: %w", err)
	}

	// Check if the dependent step has image information
	if imageID, ok := results["image_id"].(string); ok && imageID != "" {
		if config.DockerRun.ImageID != "" && config.DockerRun.ImageID != imageID {
			return fmt.Errorf("image ID mismatch: expected %s, got %s", config.DockerRun.ImageID, imageID)
		}
	}

	if imageTag, ok := results["image_tag"].(string); ok && imageTag != "" {
		if config.DockerRun.ImageTag != "" && config.DockerRun.ImageTag != imageTag {
			return fmt.Errorf("image tag mismatch: expected %s, got %s", config.DockerRun.ImageTag, imageTag)
		}
	}

	return nil
}

// isContainerRunning checks if a container is currently running
func isContainerRunning(containerID string) (bool, string, error) {
	// Run docker inspect to get container status
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.Status}} {{.State.ExitCode}} {{.State.Error}}", containerID)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, "", fmt.Errorf("failed to inspect container %s: %v\nOutput: %s", containerID, err, string(output))
	}

	// Parse the output (format: "running 0 " or "exited 1 error message")
	statusStr := strings.TrimSpace(string(output))
	statusParts := strings.Fields(statusStr)
	if len(statusParts) < 2 {
		return false, "", fmt.Errorf("unexpected container status format: %s", statusStr)
	}

	status := statusParts[0]
	exitCode := statusParts[1]
	var errorMsg string
	if len(statusParts) > 2 {
		errorMsg = strings.Join(statusParts[2:], " ")
	}

	if status == "running" && exitCode == "0" {
		return true, "", nil
	}

	// If we got here, container is not running properly
	return false, fmt.Sprintf("container %s is %s with exit code %s: %s",
		containerID, status, exitCode, errorMsg), nil
}

// containsString checks if a string slice contains a specific string
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// runDockerContainer runs a Docker container with the given configuration and verifies it's running
func runDockerContainer(config DockerRunConfig, workDir string, logger *log.Logger) (string, string, error) {
	// Start with the base docker run command
	args := []string{"run", "-d"} // Always run in detached mode

	// Add docker_run_parameters if they exist
	if len(config.DockerRun.DockerRunParameters) > 0 {
		// Replace %%IMAGETAG%% with the actual image tag if present
		replacer := strings.NewReplacer(
			"%%IMAGETAG%%", config.DockerRun.ImageTag,
		)

		for _, param := range config.DockerRun.DockerRunParameters {
			args = append(args, replacer.Replace(param))
		}
	}

	// Ensure we have an image to run
	var imageRef string
	if config.DockerRun.ImageTag != "" {
		imageRef = config.DockerRun.ImageTag
	} else if config.DockerRun.ImageID != "" {
		imageRef = config.DockerRun.ImageID
	} else {
		return "", "", fmt.Errorf("no image specified (neither image_tag nor image_id is set)")
	}

	// Add container name if specified
	// if config.DockerRun.Container != "" {
	// 	args = append(args, "--name", config.DockerRun.Container)
	// }

	// Add environment variables
	for key, value := range config.DockerRun.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", key, value))
	}

	// Add port mappings
	for hostPort, containerPort := range config.DockerRun.Ports {
		args = append(args, "-p", fmt.Sprintf("%s:%s", hostPort, containerPort))
	}

	// Add volume mounts
	for hostPath, containerPath := range config.DockerRun.Volumes {
		args = append(args, "-v", fmt.Sprintf("%s:%s", hostPath, containerPath))
	}

	// Set working directory
	if config.DockerRun.WorkingDir != "" {
		args = append(args, "-w", config.DockerRun.WorkingDir)
	}

	// Auto-remove container when it exits
	if config.DockerRun.AutoRemove {
		args = append(args, "--rm")
	}

	// Add the image if not already in args
	imageInArgs := false
	for _, arg := range args {
		if arg == imageRef {
			imageInArgs = true
			break
		}
	}
	if !imageInArgs {
		args = append(args, imageRef)
	}

	// Check if we have a bash entrypoint with --login
	// for i := 0; i < len(args); i++ {
	// 	if args[i] == "--entrypoint" && i+1 < len(args) && args[i+1] == "/bin/bash" {
	// 		// Look for --login after the image reference
	// 		for j := i + 2; j < len(args); j++ {
	// 			if args[j] == "--login" {
	// 				// Replace --login with -c "tail -f /dev/null"
	// 				args[j] = "-c"
	// 				if j+1 < len(args) {
	// 					// Remove the next argument if it exists
	// 					args = append(args[:j+1], args[j+2:]...)
	// 				}
	// 				args = append(args, "tail -f /dev/null")
	// 				break
	// 			}
	// 		}
	// 		break
	// 	}
	// }

	// Add the image reference
	// args = append(args, imageRef)

	// Add the command if specified
	if len(config.DockerRun.Command) > 0 {
		args = append(args, config.DockerRun.Command...)
	}

	// Add a keep-alive command to keep the container running
	// args = append(args, "tail", "-f", "/dev/null")

	// Log the full docker run command for debugging
	fullCmd := "docker " + strings.Join(args, " ")
	logger.Printf("Executing: %s\n", fullCmd)

	// Run the container
	cmd := exec.Command("docker", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}

	// Capture both stdout and stderr
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	// Run the command and capture output
	if err := cmd.Run(); err != nil {
		// Include both stdout and stderr in the error for better debugging
		errOutput := fmt.Sprintf("Stdout:\n%s\nStderr:\n%s", stdoutBuf.String(), stderrBuf.String())
		return "", "", fmt.Errorf("failed to start container: %v\n%s", err, errOutput)
	}
	output := stdoutBuf.String()

	containerID := strings.TrimSpace(output)
	logger.Printf("Container started with ID: %s\n", containerID)

	// Get the container's image hash
	inspectCmd := exec.Command("docker", "inspect", "-f", "{{.Image}}", containerID)
	logger.Printf("Executing: %s\n", strings.Join(inspectCmd.Args, " "))

	imageHashBytes, err := inspectCmd.CombinedOutput()
	if err != nil {
		logger.Printf("Warning: failed to get container image hash: %v\nOutput: %s\n", err, string(imageHashBytes))
		return containerID, "", fmt.Errorf("failed to get container image hash: %v", err)
	}

	// Wait a moment to check if the container is still running
	time.Sleep(2 * time.Second)

	// Check container status
	statusCmd := exec.Command("docker", "inspect", "-f", "{{.State.Status}} {{.State.ExitCode}} {{.State.Error}}", containerID)
	var statusOutput []byte
	statusOutput, err = statusCmd.CombinedOutput()
	if err != nil {
		// If we can't check status, assume failure to be safe
		return "", "", fmt.Errorf("failed to check container status: %v\nOutput: %s", err, string(statusOutput))
	}

	// Parse status output (format: "running 0 " or "exited 1 error message")
	statusStr := strings.TrimSpace(string(statusOutput))
	statusParts := strings.Fields(statusStr)
	if len(statusParts) < 2 {
		return "", "", fmt.Errorf("unexpected container status format: %s", statusStr)
	}

	status := statusParts[0]
	exitCode := statusParts[1]
	// Combine remaining parts as error message if any
	var errorMsg string
	if len(statusParts) > 2 {
		errorMsg = strings.Join(statusParts[2:], " ")
	}

	// If container exited with non-zero code or is not running, consider it a failure
	if status != "running" || exitCode != "0" {
		// Clean up the container
		exec.Command("docker", "rm", "-f", containerID).Run()
		return "", "", fmt.Errorf("container %s failed with status %s, exit code %s: %s",
			containerID, status, exitCode, errorMsg)
	}

	imageHash := strings.TrimSpace(string(imageHashBytes))
	return containerID, imageHash, nil
}
