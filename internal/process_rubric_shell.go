package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// processAllRubricShellSteps finds and executes all rubric_shell steps.
func processAllRubricShellSteps(db *sql.DB, logger *log.Logger) error {
	// Query for all steps of type 'rubric_shell'.
	query := `
		SELECT s.id, s.task_id, s.title, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.settings ? 'rubric_shell'
	`
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query for rubric_shell steps: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var stepExec models.StepExec
		if err := rows.Scan(&stepExec.StepID, &stepExec.TaskID, &stepExec.Title, &stepExec.Settings, &stepExec.LocalPath); err != nil {
			logger.Printf("failed to scan rubric_shell step: %v", err)
			continue
		}

		// Create a logger for this specific step instance.
		stepLogger := log.New(os.Stdout, fmt.Sprintf("STEP %d [rubric_shell]: ", stepExec.StepID), log.Ldate|log.Ltime|log.Lshortfile)

		// Call the original processor for the individual step.
		if err := ProcessRubricShellStep(db, &stepExec, stepLogger); err != nil {
			logger.Printf("failed to process rubric_shell step %d: %v", stepExec.StepID, err)
			// Continue processing other steps even if one fails.
		}
	}

	return nil
}

// ProcessRubricShellStep handles the execution of a rubric_shell step.
// It runs the held-out test command inside a Docker container and captures output and errors.
func ProcessRubricShellStep(db *sql.DB, stepExec *models.StepExec, stepLogger *log.Logger) error {
	var wrappedSettings struct {
		RubricShell models.RubricShellConfig `json:"rubric_shell"`
	}

	// Unmarshal the step settings
	if err := json.Unmarshal([]byte(stepExec.Settings), &wrappedSettings); err != nil {
		return fmt.Errorf("failed to unmarshal rubric_shell settings: %w", err)
	}
	config := wrappedSettings.RubricShell

	// Find a container from a docker_pool in the dependency tree
	containerName, err := models.GetContainerName(db, stepExec.StepID, stepLogger)
	if err != nil {
		return fmt.Errorf("failed to find a container: %w", err)
	}

	if containerName == "" {
		return fmt.Errorf("no container found in docker_pool for rubric_shell step %d", stepExec.StepID)
	}

	// Get the rubric_set configuration from dependencies
	rubricSetConfig, err := models.GetRubricSetFromDependencies(db, stepExec.StepID, stepLogger)
	if err != nil {
		return fmt.Errorf("failed to get rubric_set from dependencies: %w", err)
	}

	// If a rubric_set is found, clean the repo and apply patches
	if rubricSetConfig != nil {
		stepLogger.Println("Found rubric_set config, preparing to clean repo and apply patches.")

		// Helper function to execute shell commands
		executeCommand := func(command, description string) error {
			stepLogger.Printf("Executing command for '%s': %s", description, command)
			cmd := exec.Command("sh", "-c", command)
			output, err := cmd.CombinedOutput()
			if err != nil {
				stepLogger.Printf("Error %s: %v, Output: %s", description, err, output)
				return fmt.Errorf("failed to %s: %w. Output: %s", description, err, output)
			}
			stepLogger.Printf("Successfully executed: %s. Output: %s", description, output)
			return nil
		}

		// Clean the git repository
		cleanCmd := fmt.Sprintf(`docker exec -w /app/ansible %s sh -c "pwd && git reset --hard HEAD && git clean -fdx"`, containerName)
		if err := executeCommand(cleanCmd, "clean git repo"); err != nil {
			return err
		}

		// Apply patches
		patchFiles := []string{rubricSetConfig.Solution1, rubricSetConfig.HeldOutTest}
		for _, patchFile := range patchFiles {
			if patchFile == "" {
				continue
			}
			localPatchPath := filepath.Join(stepExec.LocalPath, patchFile)
			if _, err := os.Stat(localPatchPath); os.IsNotExist(err) {
				stepLogger.Printf("Patch file not found, skipping: %s", localPatchPath)
				continue
			}

			// Copy patch to container
			dockerCpCmd := fmt.Sprintf("docker cp %s %s:/tmp/%s", localPatchPath, containerName, patchFile)
			if err := executeCommand(dockerCpCmd, fmt.Sprintf("copy patch %s", patchFile)); err != nil {
				return err
			}

			// Apply patch in container
			gitApplyCmd := fmt.Sprintf("docker exec -w /app/ansible %s git apply /tmp/%s", containerName, patchFile)
			if err := executeCommand(gitApplyCmd, fmt.Sprintf("apply patch %s", patchFile)); err != nil {
				return err
			}
		}
	} else {
		stepLogger.Println("No rubric_set config found, skipping patch application.")
	}

	// Construct the Docker exec command
	commandLine := fmt.Sprintf("docker exec -w /app/ansible %s %s", containerName, config.Command)
	stepLogger.Printf("Executing command: %s\n", commandLine)

	// Run the command using os/exec to capture output and errors
	cmd := exec.Command("sh", "-c", commandLine)
	cmd.Dir = stepExec.LocalPath // Set working directory if needed
	output, err := cmd.CombinedOutput()
	if err != nil {
		stepLogger.Printf("Error executing Docker command: %v, Output: %s\n", err, output)
		// Update step result with error and output
		errorResult := map[string]string{
			"error":  err.Error(),
			"output": string(output),
		}
		jsonResult, jsonErr := json.Marshal(errorResult)
		if jsonErr != nil {
			stepLogger.Printf("Failed to marshal error result: %v\n", jsonErr)
			return err // Return original error
		}
		_, errUpdate := db.Exec("UPDATE steps SET results = $1 WHERE id = $2", jsonResult, stepExec.StepID)
		if errUpdate != nil {
			stepLogger.Printf("Failed to update step result with error: %v\n", errUpdate)
		}
		return err
	}

	// Update step result with successful output
	stepLogger.Printf("Command output: %s\n", output)
	successResult := map[string]string{"output": string(output)}
	jsonResult, jsonErr := json.Marshal(successResult)
	if jsonErr != nil {
		stepLogger.Printf("Failed to marshal success result: %v\n", jsonErr)
		return jsonErr
	}
	_, err = db.Exec("UPDATE steps SET results = $1 WHERE id = $2", jsonResult, stepExec.StepID)
	if err != nil {
		stepLogger.Printf("Failed to update step result: %v\n", err)
		return err
	}

	stepLogger.Printf("Rubric shell step executed successfully for criterion ID %s\n", config.CriterionID)
	return nil
}
