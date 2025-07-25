package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
		  AND t.status = 'active'
	`
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query for rubric_shell steps: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var step models.Step
		if err := rows.Scan(&step.ID, &step.TaskID, &step.Title, &step.Settings, &step.LocalPath); err != nil {
			logger.Printf("failed to scan rubric_shell step: %v", err)
			continue
		}

		// Call the original processor for the individual step.
		if err := ProcessRubricShellStep(db, step, logger); err != nil {
			logger.Printf("failed to process rubric_shell step %d: %v", step.ID, err)
			// Continue processing other steps even if one fails.
		}
	}

	return nil
}

// ProcessRubricShellStep handles the execution of a rubric_shell step.
func ProcessRubricShellStep(db *sql.DB, step models.Step, logger *log.Logger) error {
	// Defensive: Check parent task status before running
	var status string
	err := db.QueryRow("SELECT status FROM tasks WHERE id = $1", step.TaskID).Scan(&status)
	if err != nil {
		return fmt.Errorf("failed to fetch parent task status for step %d: %w", step.ID, err)
	}
	if status != "active" {
		logger.Printf("Skipping execution because parent task %d status is not active (status=\"%s\")", step.TaskID, status)
		return nil
	}

	// Unmarshal the rubric shell config and handle multiple assignments
	var config struct {
		RubricShell models.RubricShellConfig `json:"rubric_shell"`
	}
	if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
		return fmt.Errorf("failed to unmarshal settings: %w", err)
	}
	rsConfig := config.RubricShell

	// Debug logging for assignment unmarshaling and count
	logger.Printf("Debug: Unmarshaled RubricShellConfig for step %d with %d assignments", step.ID, len(rsConfig.Assignments))

	// After unmarshaling rsConfig, log the Files map for debugging
	logger.Printf("Debug: rsConfig.Files contents: %v", rsConfig.Files)

	// Add a warning log if rsConfig.Files is empty
	if len(rsConfig.Files) == 0 {
		logger.Printf("Warning: rsConfig.Files is empty for step %d", step.ID)
	}

	// Initialize results storage, e.g., a map to hold results per solution
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	results := make(map[string]string) // Reset to string map for compatibility

	// Iterate over each assignment and run the test sequence
	for _, assignment := range rsConfig.Assignments {
		// Log the start of processing for this assignment
		logger.Printf("Processing solution patch %s in container %s for criterion %s", assignment.Patch, assignment.Container, rsConfig.CriterionID)

		// Perform the test sequence: reset git, apply solution patch, apply held-out tests patch, run command
		output, err := runTestSequence(step.LocalPath, rsConfig, assignment.Container, assignment.Patch, rsConfig.Command, logger)
		status := "Unknown"
		if strings.Contains(output, cfg.PassMarker) {
			status = "Pass"
		} else if strings.Contains(output, cfg.FailMarker) {
			status = "Fail"
		} else if err != nil {
			status = "Error"
		} else {
			status = "Success"
		}
		results[assignment.Patch] = fmt.Sprintf("%s\nOutput: %s", status, output)
		logger.Printf("Test %s for patch %s: %s\nOutput: %s", status, assignment.Patch, status, output)
	}

	// Store aggregated results back in the step, e.g., serialize results map to JSON and update step settings
	updatedConfig := rsConfig
	updatedConfig.Results = results // Add a Results field to RubricShellConfig if not already present; otherwise, use a suitable storage mechanism
	updatedSettings, err := json.Marshal(map[string]models.RubricShellConfig{"rubric_shell": updatedConfig})
	if err != nil {
		return fmt.Errorf("failed to marshal updated settings: %w", err)
	}
	if err := models.UpdateStep(db, step.ID, step.Title, string(updatedSettings)); err != nil {
		return fmt.Errorf("failed to update step with results: %w", err)
	}

	logger.Printf("Completed processing for criterion %s with %d assignments", rsConfig.CriterionID, len(rsConfig.Assignments))
	return nil
}

// Helper function to run the test sequence (adapt based on existing code)
func runTestSequence(localPath string, rsConfig models.RubricShellConfig, container string, patch string, command string, logger *log.Logger) (string, error) {
	// Ensure a single cleanup block before patch application
	cleanupCmds := [][]string{
		{"docker", "exec", container, "git", "apply", "-R", "--ignore-whitespace", "/app/held_out_tests.patch"},
		{"docker", "exec", container, "git", "reset", "--hard", "HEAD"},
		{"docker", "exec", container, "git", "clean", "-fdx"},
	}
	for _, c := range cleanupCmds {
		cmd := exec.Command(c[0], c[1:]...)
		// We can ignore errors here as the repo might not have the patch applied
		runCmd(cmd, "cleanup repo", false, logger)
	}

	// Apply pre_patch.patch if it exists
	if _, ok := rsConfig.Files["pre_patch.patch"]; ok {
		fullPrePatchPath := filepath.Join(localPath, "pre_patch.patch")  // Use file name key
		tmpPrePatchPath := "/tmp/pre_patch.patch"
		cmd := exec.Command("docker", "cp", fullPrePatchPath, fmt.Sprintf("%s:%s", container, tmpPrePatchPath))
		if _, err := runCmd(cmd, "copy pre_patch.patch", false, logger); err != nil {
			return "", fmt.Errorf("copy pre_patch.patch failed: %w", err)
		} else {
			cmd = exec.Command("docker", "exec", container, "bash", "-c", "bash "+tmpPrePatchPath)
			if _, err := runCmd(cmd, "execute pre_patch.patch", true, logger); err != nil {
				return "", fmt.Errorf("execute pre_patch.patch failed: %w", err)
			}
		}
	}

	// Apply solution patch
	if _, ok := rsConfig.Files[patch]; ok {
		fullSolutionPatchPath := filepath.Join(localPath, patch)
		containerPatchPath := "/app/" + patch
		cmd := exec.Command("docker", "cp", fullSolutionPatchPath, fmt.Sprintf("%s:%s", container, containerPatchPath))
		if _, err := runCmd(cmd, "copy solution patch", false, logger); err != nil {
			return "", fmt.Errorf("copy solution patch failed: %w", err)
		} else {
			cmd = exec.Command("docker", "exec", container, "git", "apply", containerPatchPath)
			if _, err := runCmd(cmd, "apply solution patch", true, logger); err != nil {
				return "", fmt.Errorf("apply solution patch failed: %w", err)
			}
		}
	} else {
		logger.Printf("Solution patch '%s' not found in rsConfig.Files", patch)
		return "", fmt.Errorf("solution patch '%s' not found in rsConfig.Files", patch)
	}

	// Apply held-out tests patch
	if _, ok := rsConfig.Files["held_out_tests.patch"]; ok {
		fullHeldOutTestsPath := filepath.Join(localPath, "held_out_tests.patch")
		if _, err := os.Stat(fullHeldOutTestsPath); os.IsNotExist(err) {
			return "", fmt.Errorf("held_out_tests.patch does not exist at %s", fullHeldOutTestsPath)
		} else if err != nil {
			return "", fmt.Errorf("error checking held_out_tests.patch: %w", err)
		}
		logger.Printf("Confirmed held_out_tests.patch exists at %s", fullHeldOutTestsPath)
		containerHeldOutTestsPatchPath := "/app/held_out_tests.patch"
		cmd := exec.Command("docker", "cp", fullHeldOutTestsPath, fmt.Sprintf("%s:%s", container, containerHeldOutTestsPatchPath))
		if _, err := runCmd(cmd, "copy held-out test patch", false, logger); err != nil {
			return "", fmt.Errorf("copy held-out tests patch failed: %w", err)
		} else {
			cmd = exec.Command("docker", "exec", container, "git", "apply", containerHeldOutTestsPatchPath)
			if _, err := runCmd(cmd, "apply held-out test patch", true, logger); err != nil {
				return "", fmt.Errorf("apply held-out tests patch failed: %w", err)
			}
		}
	} else {
		logger.Println("held_out_tests.patch not found in rsConfig.Files")
		return "", fmt.Errorf("held_out_tests.patch not found in rsConfig.Files")
	}

	// Run the test command
	fullCmd := escapeSingleQuotes(command)
	logger.Printf("Debug: Executing rubric command: %s", fullCmd)  // Added logging for command
	cmd := exec.Command("docker", "exec", container, "bash", "-c", fullCmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Command error details: %s failed with error %v and output %s", cmd, err, string(output))
		logger.Printf("Command failed, attempting to cat the file: cat /app/ansible/lib/ansible/plugins/strategy/free.py")
		debugCmd := exec.Command("docker", "exec", container, "cat", "/app/ansible/lib/ansible/plugins/strategy/free.py")
		debugOutput, debugErr := debugCmd.CombinedOutput()
		if debugErr != nil {
			logger.Printf("Failed to cat file: %v, output: %s", debugErr, string(debugOutput))
		} else {
			logger.Printf("File content: %s", string(debugOutput))
		}
		return string(output), err
	}
	return string(output), nil
}

// Helper to run a command and log its execution and output.
func runCmd(cmd *exec.Cmd, description string, logOutput bool, logger *log.Logger) (string, error) {
	logger.Printf("Executing: %s", cmd.String())
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Error %s: %v\nOutput:\n%s", description, err, string(output))
		return string(output), err
	}
	if logOutput {
		logger.Printf("Success: %s\nOutput:\n%s", description, string(output))
	}
	return string(output), nil
}

// escapeSingleQuotes escapes single quotes for safe use inside bash -c '<cmd>'
func escapeSingleQuotes(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
