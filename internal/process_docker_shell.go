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
func processDockerShellSteps(db *sql.DB) {
	query := `SELECT s.id, s.task_id, s.settings FROM steps s JOIN tasks t ON s.task_id = t.id WHERE t.status = 'active' AND s.settings::text LIKE '%docker_shell%'`

	rows, err := db.Query(query)
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

		var config models.DockerShellConfig
		if err := json.Unmarshal([]byte(settings), &config); err != nil {
			models.StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": "invalid docker shell config"})
			models.StepLogger.Printf("Step %d: invalid docker shell config: %v\n", stepID, err)
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

		// Inherit image_id and image_tag from dependencies if not specified
		if config.Docker.ImageID == "" || config.Docker.ImageTag == "" {
			var imageID, imageTag string
			// Search through direct dependencies
			for _, dep := range config.DependsOn {
				id, tag, err := findImageDetailsRecursive(db, dep.ID, make(map[int]bool))
				if err != nil {
					models.StepLogger.Printf("Step %d: error searching for image details in dependency %d: %v", stepID, dep.ID, err)
					continue // Try next dependency
				}
				if id != "" {
					imageID = id
					imageTag = tag
					models.StepLogger.Printf("Step %d: Found ImageID '%s' and ImageTag '%s' from dependency step %d\n", stepID, imageID, imageTag, dep.ID)
					break // Found it, stop searching
				}
			}

			if imageID != "" {
				config.Docker.ImageID = imageID
				config.Docker.ImageTag = imageTag
				models.StepLogger.Printf("Step %d: Inherited ImageID '%s' and ImageTag '%s' from dependency chain\n", stepID, imageID, imageTag)
			}
		}

		targetImageTag := config.Docker.ImageTag
		expectedImageHash := config.Docker.ImageID

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

		if !strings.HasPrefix(actualImageHash, expectedImageHash) {
			msg := fmt.Sprintf("image hash mismatch for %s. Expected prefix '%s', got '%s'", targetImageTag, expectedImageHash, actualImageHash)
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

// findImageDetailsRecursive searches for image details (ID and Tag) by recursively checking step dependencies.
// It returns the image ID and image Tag, or an error if a circular dependency is detected or an issue occurs.
func findImageDetailsRecursive(db *sql.DB, stepID int, visited map[int]bool) (string, string, error) {
	models.StepLogger.Printf("Entering findImageDetailsRecursive for step %d\n", stepID)

	if visited[stepID] {
		models.StepLogger.Printf("Circular dependency detected for step %d. Returning.\n", stepID)
		return "", "", fmt.Errorf("circular dependency detected at step %d", stepID)
	}
	visited[stepID] = true

	stepInfoStr, err := models.GetStepInfo(db, stepID)
	if err != nil {
		models.StepLogger.Printf("Failed to get info for step %d: %v\n", stepID, err)
		return "", "", fmt.Errorf("failed to get info for step %d: %w", stepID, err)
	}

	// Unmarshal settings into a StepConfigHolder to check for image details or dependencies
	var holder models.StepConfigHolder
	if err := json.Unmarshal([]byte(stepInfoStr), &holder); err != nil {
		models.StepLogger.Printf("Failed to unmarshal settings for step %d: %v (continuing)\n", stepID, err)

		// Not an error, might just not have relevant settings for image details
		// or dependencies, so we continue to check dependencies recursively.
		// Or it could be a step type that doesn't have image details.
	}

	// Check current step's settings for image details. Only return if we find BOTH id and tag.
	if holder.DockerBuild != nil && holder.DockerBuild.ImageID != "" && holder.DockerBuild.ImageTag != "" {
		models.StepLogger.Printf("Found DockerBuild image details for step %d: %s:%s\n", stepID, holder.DockerBuild.ImageID, holder.DockerBuild.ImageTag)
		return holder.DockerBuild.ImageID, holder.DockerBuild.ImageTag, nil
	}
	if holder.DockerRun != nil && holder.DockerRun.ImageID != "" && holder.DockerRun.ImageTag != "" {
		models.StepLogger.Printf("Found DockerRun image details for step %d: %s:%s\n", stepID, holder.DockerRun.ImageID, holder.DockerRun.ImageTag)
		return holder.DockerRun.ImageID, holder.DockerRun.ImageTag, nil
	}
	if holder.DockerPull != nil && holder.DockerPull.ImageID != "" && holder.DockerPull.ImageTag != "" {
		models.StepLogger.Printf("Found DockerPull image details for step %d: %s:%s\n", stepID, holder.DockerPull.ImageID, holder.DockerPull.ImageTag)
		return holder.DockerPull.ImageID, holder.DockerPull.ImageTag, nil
	}
	if holder.DockerShell != nil && holder.DockerShell.Docker.ImageID != "" && holder.DockerShell.Docker.ImageTag != "" {
		models.StepLogger.Printf("Found DockerShell image details for step %d: %s:%s\n", stepID, holder.DockerShell.Docker.ImageID, holder.DockerShell.Docker.ImageTag)
		return holder.DockerShell.Docker.ImageID, holder.DockerShell.Docker.ImageTag, nil
	}
	if holder.DockerPool != nil && holder.DockerPool.ImageID != "" && holder.DockerPool.ImageTag != "" {
		models.StepLogger.Printf("Found DockerPool image details for step %d: %s:%s\n", stepID, holder.DockerPool.ImageID, holder.DockerPool.ImageTag)
		return holder.DockerPool.ImageID, holder.DockerPool.ImageTag, nil
	}
	if holder.DynamicRubric != nil && holder.DynamicRubric.DynamicRubric.Environment.Docker && holder.DynamicRubric.DynamicRubric.Environment.ImageID != "" && holder.DynamicRubric.DynamicRubric.Environment.ImageTag != "" {
		models.StepLogger.Printf("Found DynamicRubric image details for step %d: %s:%s\n", stepID, holder.DynamicRubric.DynamicRubric.Environment.ImageID, holder.DynamicRubric.DynamicRubric.Environment.ImageTag)
		return holder.DynamicRubric.DynamicRubric.Environment.ImageID, holder.DynamicRubric.DynamicRubric.Environment.ImageTag, nil
	}
	// Removed direct ImageID/ImageTag check for DynamicLab as it does not have an Environment struct
	// DynamicLab steps are expected to get their image from dependencies.
	if holder.DockerRubrics != nil && holder.DockerRubrics.DockerRubrics.ImageID != "" && holder.DockerRubrics.DockerRubrics.ImageTag != "" {
		models.StepLogger.Printf("Found DockerRubrics image details for step %d: %s:%s\n", stepID, holder.DockerRubrics.DockerRubrics.ImageID, holder.DockerRubrics.DockerRubrics.ImageTag)
		return holder.DockerRubrics.DockerRubrics.ImageID, holder.DockerRubrics.DockerRubrics.ImageTag, nil
	}

	// If not found in current step, recurse through this step's dependencies
	var dependencies []models.Dependency
	if holder.DockerBuild != nil {
		dependencies = holder.DockerBuild.DependsOn
	} else if holder.DockerRun != nil {
		dependencies = holder.DockerRun.DependsOn
	} else if holder.DockerPull != nil {
		dependencies = holder.DockerPull.DependsOn
	} else if holder.DockerShell != nil {
		dependencies = holder.DockerShell.DependsOn
	} else if holder.DockerPool != nil {
		dependencies = holder.DockerPool.DependsOn
	} else if holder.DynamicRubric != nil && holder.DynamicRubric.DynamicRubric.DependsOn != nil {
		dependencies = holder.DynamicRubric.DynamicRubric.DependsOn
	} else if holder.DynamicLab != nil && holder.DynamicLab.DynamicLab.DependsOn != nil {
		dependencies = holder.DynamicLab.DynamicLab.DependsOn
	} else if holder.FileExists != nil {
		// FileExists steps don't have image dependencies
		return "", "", nil
	} else if holder.DockerRubrics != nil && holder.DockerRubrics.DockerRubrics.DependsOn != nil {
		dependencies = holder.DockerRubrics.DockerRubrics.DependsOn
	}

	// If no dependencies found or unmarshaling failed for a specific type, return
	if len(dependencies) == 0 {
		models.StepLogger.Printf("No dependencies found for step %d. Returning empty image details.\n", stepID)
		return "", "", nil
	}

	models.StepLogger.Printf("Recursing through dependencies for step %d: %v\n", stepID, dependencies)
	for _, dep := range dependencies {
		models.StepLogger.Printf("Checking dependency step %d for step %d\n", dep.ID, stepID)
		imageID, imageTag, err := findImageDetailsRecursive(db, dep.ID, visited)
		if err != nil {
			models.StepLogger.Printf("Error in sub-dependency branch of step %d (from step %d): %v\n", dep.ID, stepID, err)
			continue
		}
		if imageID != "" && imageTag != "" {
			models.StepLogger.Printf("Found image details in dependency step %d: %s:%s (from step %d). Returning.\n", dep.ID, imageID, imageTag, stepID)
			return imageID, imageTag, nil // Found it!
		}
	}

	models.StepLogger.Printf("Image details not found in step %d or its dependencies. Returning empty.\n", stepID)
	return "", "", nil // Not found in this branch
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
