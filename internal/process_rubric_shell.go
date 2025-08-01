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
func processAllRubricShellSteps(db *sql.DB, logger *log.Logger, force bool) error {
	// Query for all steps of type 'rubric_shell'.
	query := `
		SELECT s.id, s.task_id, s.title, s.settings, t.base_path
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
		if err := rows.Scan(&step.ID, &step.TaskID, &step.Title, &step.Settings, &step.BasePath); err != nil {
			logger.Printf("failed to scan rubric_shell step: %v", err)
			continue
		}

		// Call the original processor for the individual step.
		if err := ProcessRubricShellStep(db, &models.StepExec{StepID: step.ID, TaskID: step.TaskID, Title: step.Title, Settings: step.Settings, BasePath: step.BasePath}, logger, force); err != nil {
			logger.Printf("failed to process rubric_shell step %d: %v", step.ID, err)
			// Continue processing other steps even if one fails.
		}
	}

	return nil
}

// ProcessRubricShellStep handles the execution of a rubric_shell step.
func ProcessRubricShellStep(db *sql.DB, se *models.StepExec, logger *log.Logger, force bool) error {
	// Defensive: Check parent task status before running
	var status string
	err := db.QueryRow("SELECT status FROM tasks WHERE id = $1", se.TaskID).Scan(&status)
	if err != nil {
		return fmt.Errorf("failed to fetch parent task status for step %d: %w", se.StepID, err)
	}
	if status != "active" {
		logger.Printf("Skipping execution because parent task %d status is not active (status=\"%s\")", se.TaskID, status)
		return nil
	}

	// Always reload the most up-to-date settings for this step from the DB
var freshSettings string
err = db.QueryRow("SELECT settings FROM steps WHERE id = $1", se.StepID).Scan(&freshSettings)
if err != nil {
	return fmt.Errorf("failed to reload latest settings for step %d: %w", se.StepID, err)
}
logger.Printf("[TRACE] Reloaded latest settings for step %d from DB", se.StepID)
se.Settings = freshSettings

// Unmarshal the rubric shell config and handle multiple assignments
	// This struct matches the JSON structure: { "rubric_shell": { ... } }
	type stepSettings struct {
		RubricShell models.RubricShellConfig `json:"rubric_shell"`
	}
	var settings stepSettings
	logger.Printf("DEBUG: Step %d raw settings: %s", se.StepID, se.Settings)
	if err := json.Unmarshal([]byte(se.Settings), &settings); err != nil {
		return fmt.Errorf("failed to unmarshal settings: %w", err)
	}
	rsConfig := settings.RubricShell
	logger.Printf("[DEEPDEBUG] After unmarshal: step %d rsConfig.Rerun=%v, full struct=%+v", se.StepID, rsConfig.Rerun, rsConfig)

	// Treat rerun:true as equivalent to --force for skip logic
	effectiveForce := force || rsConfig.Rerun
	logger.Printf("[TRACE] Step %d effectiveForce=%v (force=%v, rerun=%v)", se.StepID, effectiveForce, force, rsConfig.Rerun)

	// Fetch parent task settings
	taskSettings, err := models.GetTaskSettings(db, se.TaskID)
	if err != nil {
		return fmt.Errorf("failed to fetch parent task settings: %w", err)
	}
	// --- Rubric hash gating logic (atomic) ---
	storedHash := ""
	if taskSettings != nil && taskSettings.Rubrics != nil {
		storedHash = taskSettings.Rubrics[rsConfig.CriterionID]
	}
	currentHash := models.CalcRubricCriterionHash(rsConfig.Score, rsConfig.Rubric, rsConfig.Required, rsConfig.Command)
	logger.Printf("DEBUG: Step %d skip check: storedHash=%q, currentHash=%q, effectiveForce=%v", se.StepID, storedHash, currentHash, effectiveForce)
	if storedHash == currentHash && !effectiveForce {
		logger.Printf("Rubric_shell step %d for criterion '%s' is up-to-date (hash: %s); skipping all git and rubric commands.", se.StepID, rsConfig.CriterionID, currentHash)
		// Defensive: do NOT touch or reset results on skip. Exit immediately.
		return nil
	} else {
		logger.Printf("Rubric_shell step %d for criterion '%s' will execute: stored hash '%s', current hash '%s'", se.StepID, rsConfig.CriterionID, storedHash, currentHash)
	}
	// Overwrite rsConfig.Assignments with the resolved assignments
	assignmentMap, err := models.GetAssignedContainersForStep(se.Settings, taskSettings, logger)
	if err != nil {
		logger.Printf("ERROR: Could not determine container assignments for step %d: %v", se.StepID, err)
		return err
	}
	rsConfig.Assignments = nil
	for patch, cinfo := range assignmentMap {
		rsConfig.Assignments = append(rsConfig.Assignments, models.RubricShellAssignment{
			Patch:     patch,
			Container: cinfo.ContainerName,
		})
	}

	// Debug logging for assignment unmarshaling and count
	logger.Printf("Debug: Unmarshaled RubricShellConfig for step %d with %d assignments", se.StepID, len(rsConfig.Assignments))

	// After unmarshaling rsConfig, log the Files map for debugging
	logger.Printf("Debug: rsConfig.Files contents: %v", rsConfig.Files)

	// Add a warning log if rsConfig.Files is empty
	if len(rsConfig.Files) == 0 {
		logger.Printf("Warning: rsConfig.Files is empty for step %d", se.StepID)
	}

	// Add debug log for base_path
	logger.Printf("Debug: Using base_path '%s' for step %d", se.BasePath, se.StepID)

	// Initialize results storage, e.g., a map to hold results per solution
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	results := make(map[string]string) // Reset to string map for compatibility

	// Use utility to resolve patch file for each container
	patchFileMap := models.GetPatchFileForContainerAssignments(rsConfig.Assignments, rsConfig.Files)

	// Iterate over each assignment and run the test sequence
	for _, assignment := range rsConfig.Assignments {
		containerRunning := false
		containerNeedsRecreation := false
		cmdInspect := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", assignment.Container)
		inspectOutput, err := cmdInspect.Output()
		if err == nil && strings.TrimSpace(string(inspectOutput)) == "true" {
			containerRunning = true
		} else if err == nil {
			// Container exists but is not running (likely exited)
			logger.Printf("INFO: Container '%s' is not running. Attempting to start...", assignment.Container)
			cmdStart := exec.Command("docker", "start", assignment.Container)
			startOut, startErr := cmdStart.CombinedOutput()
			if startErr == nil {
				logger.Printf("INFO: Successfully started container '%s'.", assignment.Container)
				containerRunning = true
			} else {
				logger.Printf("ERROR: Failed to start container '%s': %v\nOutput: %s", assignment.Container, startErr, string(startOut))
				// Cleanup: remove the container
				logger.Printf("CLEANUP: Removing container '%s' due to failed restart.", assignment.Container)
				cmdRm := exec.Command("docker", "rm", assignment.Container)
				rmOut, rmErr := cmdRm.CombinedOutput()
				if rmErr != nil {
					logger.Printf("ERROR: Failed to remove container '%s': %v\nOutput: %s", assignment.Container, rmErr, string(rmOut))
				} else {
					logger.Printf("INFO: Successfully removed container '%s'.", assignment.Container)
					containerNeedsRecreation = true
				}
			}
		} else {
			// Inspect failed (container likely does not exist)
			logger.Printf("ERROR: Container '%s' does not exist or cannot be inspected. Will attempt to recreate.", assignment.Container)
			containerNeedsRecreation = true
		}
		if containerNeedsRecreation {
			// Gather image and volume info from taskSettings
			imageTag := ""
			if taskSettings != nil {
				imageTag = taskSettings.Docker.ImageTag
			}
			if imageTag == "" {
				logger.Printf("ERROR: Cannot recreate container '%s': missing image_tag in task settings.", assignment.Container)
				continue
			}
			appFolder := "/app"
			if taskSettings != nil && taskSettings.AppFolder != "" {
				appFolder = taskSettings.AppFolder
			}
			volumeName := ""
			if taskSettings != nil {
				volumeName = taskSettings.VolumeName
			}
			if volumeName == "" {
				logger.Printf("ERROR: Cannot recreate container '%s': missing volume_name in task settings.", assignment.Container)
				continue
			}
			// Run docker to create the container
			logger.Printf("INFO: Recreating container '%s' with image '%s', volume '%s', app folder '%s'...", assignment.Container, imageTag, volumeName, appFolder)
			cmdRun := exec.Command(
				"docker", "run", "-d", "--name", assignment.Container,
				"-v", volumeName+":"+appFolder,
				imageTag,
				"/bin/bash", "--login", "-c", "sleep infinity",
			)
			runOut, runErr := cmdRun.CombinedOutput()
			if runErr != nil {
				logger.Printf("ERROR: Failed to recreate container '%s': %v\nOutput: %s", assignment.Container, runErr, string(runOut))
				continue
			}
			logger.Printf("INFO: Successfully recreated container '%s'.", assignment.Container)
			// Verify container is running
			cmdInspect2 := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", assignment.Container)
			inspectOutput2, err2 := cmdInspect2.CombinedOutput()
			if err2 == nil && strings.TrimSpace(string(inspectOutput2)) == "true" {
				containerRunning = true
			} else {
				logger.Printf("ERROR: Container '%s' is still not running after recreation. Skipping.", assignment.Container)
				continue
			}
		}
		if !containerRunning {
			logger.Printf("ERROR: Container '%s' is not running after attempted restart/recreation. Skipping rubric shell execution for this assignment.", assignment.Container)
			continue
		}

		if assignment.Container == "" {
			logger.Printf("ERROR: No container assigned for solution patch '%s' in step %d. Skipping this assignment.", assignment.Patch, se.StepID)
			results[assignment.Patch] = "Error: No container assigned"
			continue
		}

		patchFile := patchFileMap[assignment.Container]
		if patchFile == "" {
			logger.Printf("ERROR: No patch file found in rsConfig.Files for container '%s' (assignment patch '%s') in step %d. Skipping.", assignment.Container, assignment.Patch, se.StepID)
			results[assignment.Patch] = "Error: No patch file found"
			continue
		}

		logger.Printf("Processing solution patch %s (resolved file: %s) in container %s for criterion %s", assignment.Patch, patchFile, assignment.Container, rsConfig.CriterionID)

		// Perform the test sequence: reset git, apply solution patch, apply held-out tests patch, run command
		output, err := runTestSequence(se.BasePath, rsConfig, assignment.Container, patchFile, rsConfig.Command, logger)
		if err != nil {
			logger.Printf("ERROR: Test sequence failed for patch %s: %v", patchFile, err)
			results[patchFile] = fmt.Sprintf("Error: %v\nOutput:\n%s", err, output)
		} else {
			status := "Unknown"
			if strings.Contains(output, cfg.PassMarker) {
				status = "Pass"
			} else if strings.Contains(output, cfg.FailMarker) {
				status = "Fail"
			} else {
				status = "Success"
			}
			results[patchFile] = fmt.Sprintf("%s\nOutput: %s", status, output)
			logger.Printf("Test %s for patch %s: %s\nOutput: %s", status, assignment.Patch, status, output)
		}
	}

	// Store aggregated results back in the step, e.g., serialize results map to JSON and update step settings
	updatedConfig := rsConfig
	// Remove any 'container_N' keys from results
	for k := range results {
		if strings.HasPrefix(k, "container_") {
			delete(results, k)
		}
	}
	updatedConfig.Results = results // Only patch file keys remain
	logger.Printf("[TRACE] Persisting results for step %d: rerun=%v, settings=%s", se.StepID, updatedConfig.Rerun, func() string { bs, _ := json.Marshal(map[string]models.RubricShellConfig{"rubric_shell": updatedConfig}); return string(bs) }())
	updatedSettings, err := json.Marshal(map[string]models.RubricShellConfig{"rubric_shell": updatedConfig})
	if err != nil {
		return fmt.Errorf("failed to marshal updated settings: %w", err)
	}
	if err := models.UpdateStep(db, se.StepID, se.Title, string(updatedSettings)); err != nil {
		return fmt.Errorf("failed to update step with results: %w", err)
	}
	logger.Printf("[TRACE] Also persisting results for step %d in results column", se.StepID)
	resultsIface := make(map[string]interface{}, len(results))
	for k, v := range results {
		resultsIface[k] = v
	}
	if err := models.StoreStepResult(db, se.StepID, resultsIface); err != nil {
		logger.Printf("[ERROR] Failed to persist results in results column for step %d: %v", se.StepID, err)
	} else {
		logger.Printf("[TRACE] Successfully persisted results in results column for step %d", se.StepID)
	}

	logger.Printf("Completed processing for criterion %s with %d assignments", rsConfig.CriterionID, len(rsConfig.Assignments))

	// If rerun was true, reset it to false and persist
	if rsConfig.Rerun {
		rsConfig.Rerun = false
		persistSettings, err := json.Marshal(map[string]models.RubricShellConfig{"rubric_shell": rsConfig})
		if err != nil {
			logger.Printf("Failed to marshal settings for rerun reset on step %d: %v", se.StepID, err)
		} else {
			_, err := db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(persistSettings), se.StepID)
			if err != nil {
				logger.Printf("Failed to update step settings to reset rerun on step %d: %v", se.StepID, err)
			} else {
				logger.Printf("[TRACE] Reset rerun flag to false for rubric_shell step %d. Persisting settings: %s", se.StepID, string(persistSettings))
			}
		}
	}
	return nil
}

// Helper function to run the test sequence (adapt based on existing code)
func runTestSequence(basePath string, rsConfig models.RubricShellConfig, container string, patch string, command string, logger *log.Logger) (string, error) {
	// Add debug log for base_path
	logger.Printf("Debug: runTestSequence base_path '%s' for patch %s", basePath, patch)

	// Ensure a single cleanup block before patch application
	cleanupCmds := [][]string{
		{"docker", "exec", container, "git", "apply", "-R", "--ignore-whitespace", "/app/held_out_tests.patch"},
		{"docker", "exec", container, "git", "reset", "--hard", "HEAD"},
		{"docker", "exec", container, "git", "checkout", "--", "."},
		{"docker", "exec", container, "git", "clean", "-fdx"},
	}
	for _, c := range cleanupCmds {
		cmd := exec.Command(c[0], c[1:]...)
		_ = cmd.Run()
	}

	// Apply pre_patch.patch if it exists
	if _, ok := rsConfig.Files["pre_patch.patch"]; ok {
		tmpPrePatchPath := "/tmp/pre_patch.patch"
		cmd := exec.Command("docker", "cp", filepath.Join(basePath, "pre_patch.patch"), fmt.Sprintf("%s:%s", container, tmpPrePatchPath))
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("copy pre_patch.patch failed: %w", err)
		} else {
			cmd = exec.Command("docker", "exec", container, "bash", "-c", escapeSingleQuotes("bash "+tmpPrePatchPath))
			if err := cmd.Run(); err != nil {
				return "", fmt.Errorf("execute pre_patch.patch failed: %w", err)
			}
		}
	}

	// Apply solution patch
	if _, ok := rsConfig.Files[patch]; ok {
		containerPatchPath := "/app/" + patch
		cmd := exec.Command("docker", "cp", filepath.Join(basePath, patch), fmt.Sprintf("%s:%s", container, containerPatchPath))
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("copy solution patch failed: %w", err)
		} else {
			applyPatchCmd := exec.Command("docker", "exec", container, "git", "apply", containerPatchPath)
			if err := applyPatchCmd.Run(); err != nil {
				logger.Printf("ERROR: Patch apply failed for criterion %s: %v", rsConfig.CriterionID, err)
				return "", fmt.Errorf("patch apply failed: %w", err)
			}
		}
	} else {
		logger.Printf("Solution patch '%s' not found in rsConfig.Files", patch)
		return "", fmt.Errorf("solution patch '%s' not found in rsConfig.Files", patch)
	}

	// Apply held-out tests patch
	if _, ok := rsConfig.Files["held_out_tests.patch"]; ok {
		fullHeldOutTestsPath := filepath.Join(basePath, "held_out_tests.patch")
		if _, err := os.Stat(fullHeldOutTestsPath); os.IsNotExist(err) {
			return "", fmt.Errorf("held_out_tests.patch does not exist at %s", fullHeldOutTestsPath)
		} else if err != nil {
			return "", fmt.Errorf("error checking held_out_tests.patch: %w", err)
		}
		logger.Printf("Confirmed held_out_tests.patch exists at %s", fullHeldOutTestsPath)
		containerHeldOutTestsPatchPath := "/app/held_out_tests.patch"
		cmd := exec.Command("docker", "cp", fullHeldOutTestsPath, fmt.Sprintf("%s:%s", container, containerHeldOutTestsPatchPath))
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("copy held-out tests patch failed: %w", err)
		} else {
			cmd = exec.Command("docker", "exec", container, "git", "apply", containerHeldOutTestsPatchPath)
			if err := cmd.Run(); err != nil {
				logger.Printf("ERROR: Patch apply failed for criterion %s: %v", rsConfig.CriterionID, err)
				return "", fmt.Errorf("patch apply failed: %w", err)
			}
		}
	} else {
		logger.Println("held_out_tests.patch not found in rsConfig.Files")
		return "", fmt.Errorf("held_out_tests.patch not found in rsConfig.Files")
	}

	// Run the test command
	// Create a temporary script file to hold the command.
	scriptFile, err := os.CreateTemp("", "rubric-script-*.sh")
	if err != nil {
		return "", fmt.Errorf("failed to create temp script file: %w", err)
	}
	defer os.Remove(scriptFile.Name()) // Ensure cleanup

	// Write the command to the script file with a shebang.
	scriptContent := fmt.Sprintf("#!/bin/bash\n%s", command)
	if _, err := scriptFile.WriteString(scriptContent); err != nil {
		return "", fmt.Errorf("failed to write to temp script file: %w", err)
	}
	scriptFile.Close()

	// Make the script executable.
	if err := os.Chmod(scriptFile.Name(), 0755); err != nil {
		return "", fmt.Errorf("failed to make script executable: %w", err)
	}

	// Copy the script to the container.
	containerScriptPath := "/tmp/run_rubric.sh"
	copyCmd := exec.Command("docker", "cp", scriptFile.Name(), fmt.Sprintf("%s:%s", container, containerScriptPath))
	if err := copyCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to copy script to container: %w", err)
	}

	// Execute the script inside the container.
	cmd := exec.Command("docker", "exec", container, "/bin/bash", containerScriptPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Error running rubric script: %v\nOutput:\n%s", err, string(output))
		return string(output), err
	}
	return string(output), nil
}

// escapeSingleQuotes escapes single quotes for safe use inside bash -c '<cmd>'
func escapeSingleQuotes(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
