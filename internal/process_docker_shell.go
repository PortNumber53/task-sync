package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// processDockerShellSteps processes docker shell steps for active tasks.
func processDockerShellSteps(db *sql.DB) {
	query := `SELECT s.id, s.task_id, s.settings FROM steps s JOIN tasks t ON s.task_id = t.id WHERE t.status = 'active' AND s.settings::text LIKE '%docker_shell%'`

	rows, err := db.Query(query)
	if err != nil {
		stepLogger.Println("Docker shell query error:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var stepID, taskID int
		var settings string
		if err := rows.Scan(&stepID, &taskID, &settings); err != nil {
			stepLogger.Println("Row scan error:", err)
			continue
		}

		var config DockerShellConfig
		if err := json.Unmarshal([]byte(settings), &config); err != nil {
			StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": "invalid docker shell config"})
			stepLogger.Printf("Step %d: invalid docker shell config: %v\n", stepID, err)
			continue
		}

		ok, err := checkDependencies(db, stepID, stepLogger)
		if err != nil {
			stepLogger.Printf("Step %d: error checking dependencies: %v\n", stepID, err)
			continue
		}
		if !ok {
			stepLogger.Printf("Step %d: waiting for dependencies to complete\n", stepID)
			continue
		}

		targetImageTag := config.DockerShell.Docker.ImageTag
		expectedImageHash := config.DockerShell.Docker.ImageID

		if targetImageTag == "" || expectedImageHash == "" {
			msg := "docker_shell settings must include both an image_tag and an image_id"
			StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": msg})
			stepLogger.Printf("Step %d: %s\n", stepID, msg)
			continue
		}

		containerID, actualImageHash, err := findContainerByImageTag(targetImageTag)
		if err != nil {
			msg := fmt.Sprintf("failed to find running container for image %s: %v", targetImageTag, err)
			StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": msg})
			stepLogger.Printf("Step %d: %s\n", stepID, msg)
			continue
		}

		if !strings.HasPrefix(actualImageHash, expectedImageHash) {
			msg := fmt.Sprintf("image hash mismatch for %s. Expected prefix '%s', got '%s'", targetImageTag, expectedImageHash, actualImageHash)
			StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": msg})
			stepLogger.Printf("Step %d: %s\n", stepID, msg)
			continue
		}

		// Container is found and verified, execute commands
		var results []map[string]interface{}
		var commandErrors []string

		for _, cmdMap := range config.DockerShell.Command {
			for label, command := range cmdMap {
				stepLogger.Printf("Step %d: executing command for label '%s': %s\n", stepID, label, command)
				execCmd := execCommand("docker", "exec", containerID, "sh", "-c", command)
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
			StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": "one or more shell commands failed", "outputs": results})
		} else {
			StoreStepResult(db, stepID, map[string]interface{}{"result": "success", "message": "all shell commands executed successfully", "outputs": results})
		}
	}
}

// findContainerByImageTag searches for a running container with the given image tag.
// It returns the container ID and the full image hash (ImageID) of the container.
func findContainerByImageTag(imageTag string) (string, string, error) {
	// Find container IDs using the image tag
	cmd := execCommand("docker", "ps", "--filter", fmt.Sprintf("ancestor=%s", imageTag), "--format", "{{.ID}}")
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
	inspectCmd := execCommand("docker", "inspect", "-f", "{{.Image}}", containerID)
	imageHashOutput, err := inspectCmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("failed to inspect container %s to get image hash: %w, output: %s", containerID, err, string(imageHashOutput))
	}

	imageHash := strings.TrimSpace(string(imageHashOutput))
	return containerID, imageHash, nil
}
