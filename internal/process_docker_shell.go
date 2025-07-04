package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// processDockerShellSteps processes docker shell steps for active tasks.
func processDockerShellSteps(db *sql.DB, targetStepID int) {
	var query string
	var rows *sql.Rows
	var err error

	if targetStepID != 0 {
		query = `SELECT s.id, s.task_id, s.settings FROM steps s JOIN tasks t ON s.task_id = t.id WHERE s.id = $1 AND s.settings ? 'docker_shell'`
		rows, err = db.Query(query, targetStepID)
	} else {
		query = `SELECT s.id, s.task_id, s.settings FROM steps s JOIN tasks t ON s.task_id = t.id WHERE t.status = 'active' AND s.settings ? 'docker_shell'`
		rows, err = db.Query(query)
	}

	if err != nil {
		models.StepLogger.Println("Docker shell query error:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var stepID, taskID int
		var settings string
		if err := rows.Scan(&stepID, &taskID, &settings); err != nil {
			models.StepLogger.Println("Row scan error:", err)
			continue
		}

		var configHolder models.StepConfigHolder
		if err := json.Unmarshal([]byte(settings), &configHolder); err != nil {
			models.StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": "invalid step config"})
			models.StepLogger.Printf("Step %d: invalid step config: %v\n", stepID, err)
			continue
		}

		config := configHolder.DockerShell
		if config == nil {
			models.StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": "docker_shell config not found"})
			models.StepLogger.Printf("Step %d: docker_shell config not found in step settings\n", stepID)
			continue
		}

		ok, err := models.CheckDependencies(db, &models.StepExec{StepID: stepID})
		if err != nil {
			models.StepLogger.Printf("Step %d: error checking dependencies: %v\n", stepID, err)
			continue
		}
		if !ok {
			models.StepLogger.Printf("Step %d: waiting for dependencies to complete\n", stepID)
			continue
		}

		// Remove dependency loop and directly use task settings for the current step
		imageHash, imageTag, err := models.FindImageDetailsRecursive(db, stepID, models.StepLogger)
		if err != nil {
			models.StepLogger.Printf("Step %d: Error finding image details: %v\n", stepID, err)
			models.StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": "Error retrieving image details"})
			continue
		}
		if imageHash == "" || imageTag == "" {
			models.StepLogger.Printf("Step %d: No image details found in task settings\n", stepID)
			models.StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": "Missing image details in task settings"})
			continue
		}
		// Use imageHash and imageTag directly for the step
		models.StepLogger.Printf("Step %d: Using image_hash '%s' and image_tag '%s' from task settings\n", stepID, imageHash, imageTag)

		targetImageTag := imageTag
		expectedImageHash := imageHash

		if targetImageTag == "" || expectedImageHash == "" {
			msg := "docker_shell settings must include both an image_tag and an image_id"
			models.StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": msg})
			models.StepLogger.Printf("Step %d: %s\n", stepID, msg)
			continue
		}

		containerID, actualImageHash, err := findContainerByImageTag(targetImageTag)
		if err != nil {
			msg := fmt.Sprintf("failed to find running container for image %s: %v", targetImageTag, err)
			models.StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": msg})
			models.StepLogger.Printf("Step %d: %s\n", stepID, msg)
			continue
		}

		expectedTrimmed := strings.TrimPrefix(strings.TrimSpace(expectedImageHash), "sha256:")
		actualTrimmed := strings.TrimPrefix(strings.TrimSpace(actualImageHash), "sha256:")
		models.StepLogger.Printf("Step %d: Debugging hash comparison for %s - Expected (trimmed): '%s', Actual (trimmed): '%s'\n", stepID, targetImageTag, expectedTrimmed, actualTrimmed)
		if expectedTrimmed != actualTrimmed {
			msg := fmt.Sprintf("image hash mismatch for %s. Expected '%s', got '%s' after trimming", targetImageTag, expectedTrimmed, actualTrimmed)
			models.StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": msg})
			models.StepLogger.Printf("Step %d: %s\n", stepID, msg)
			continue
		}

		// Container is found and verified, execute commands
		var results []map[string]interface{}
		var commandErrors []string

		for _, cmdMap := range config.Command {
			for label, command := range cmdMap {
				models.StepLogger.Printf("Step %d: executing command for label '%s': %s\n", stepID, label, command)
				execCmd := exec.Command("docker", "exec", containerID, "sh", "-c", command)
				cmdOutput, err := execCmd.CombinedOutput()

				if err != nil {
					errorMsg := fmt.Sprintf("failed to execute command '%s': %v. Output: %s", command, err, string(cmdOutput))
					commandErrors = append(commandErrors, errorMsg)
					results = append(results, map[string]interface{}{"label": label, "output": "", "error": errorMsg})
				} else {
					outputStr := strings.TrimSpace(string(cmdOutput))
					results = append(results, map[string]interface{}{"label": label, "output": outputStr, "error": ""})
				}
			}
		}

		if len(commandErrors) > 0 {
			models.StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": "one or more shell commands failed", "outputs": results})
		} else {
			models.StoreStepResult(db, stepID, map[string]interface{}{"result": "success", "message": "all shell commands executed successfully", "outputs": results})
		}
	}
}

func findContainerByImageTag(imageTag string) (string, string, error) {
	// Find container IDs using the image tag
	cmd := exec.Command("docker", "ps", "--filter", fmt.Sprintf("ancestor=%s", imageTag), "--format", "{{.ID}}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("failed to list containers for image tag %s: %w, output: %s", imageTag, err, string(output))
	}

	containerIDs := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(containerIDs) == 0 || containerIDs[0] == "" {
		return "", "", fmt.Errorf("no running container found for image tag %s", imageTag)
	}

	// Use the first container found
	containerID := containerIDs[0]

	// Inspect the container to get its image hash
	inspectCmd := exec.Command("docker", "inspect", "-f", "{{.Image}}", containerID)
	imageHashOutput, err := inspectCmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("failed to inspect container %s to get image hash: %w, output: %s", containerID, err, string(imageHashOutput))
	}

	imageHash := strings.TrimSpace(string(imageHashOutput))
	return containerID, imageHash, nil
}
