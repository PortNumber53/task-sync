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

	// TEMPORARY CLEANUP: Remove legacy/misspelled fields from settings
	// - Remove 'results' (migrated to dedicated results column)
	// - Remove 'assingments' (misspelling) and 'assignments' to avoid step-level overrides
	var rawSettings map[string]interface{}
	if err := json.Unmarshal([]byte(se.Settings), &rawSettings); err == nil {
		if rubricShellRaw, ok := rawSettings["rubric_shell"].(map[string]interface{}); ok {
			cleaned := false
			if _, hasResults := rubricShellRaw["results"]; hasResults {
				logger.Printf("[CLEANUP] Removing legacy 'results' field from settings for step %d", se.StepID)
				delete(rubricShellRaw, "results")
				cleaned = true
			}
			if _, hasAssingments := rubricShellRaw["assingments"]; hasAssingments {
				logger.Printf("[CLEANUP] Removing misspelled 'assingments' field from settings for step %d", se.StepID)
				delete(rubricShellRaw, "assingments")
				cleaned = true
			}
			if _, hasAssignments := rubricShellRaw["assignments"]; hasAssignments {
				logger.Printf("[CLEANUP] Removing step-level 'assignments' field from settings for step %d", se.StepID)
				delete(rubricShellRaw, "assignments")
				cleaned = true
			}
			if cleaned {
				cleanedSettings, mErr := json.Marshal(rawSettings)
				if mErr == nil {
					if _, uErr := db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(cleanedSettings), se.StepID); uErr != nil {
						logger.Printf("[WARN] Failed to update cleaned settings for step %d: %v", se.StepID, uErr)
					} else {
						logger.Printf("[CLEANUP] Persisted sanitized settings for step %d", se.StepID)
						se.Settings = string(cleanedSettings)
					}
				} else {
					logger.Printf("[WARN] Failed to marshal cleaned settings for step %d: %v", se.StepID, mErr)
				}
			}
		}
	}

	// Treat rerun:true as equivalent to --force for skip logic
	effectiveForce := force || rsConfig.Rerun
	logger.Printf("[TRACE] Step %d effectiveForce=%v (force=%v, rerun=%v)", se.StepID, effectiveForce, force, rsConfig.Rerun)

	// Fetch parent task settings
	taskSettings, err := models.GetTaskSettings(db, se.TaskID)
	if err != nil {
		return fmt.Errorf("failed to fetch parent task settings: %w", err)
	}
	// --- Rubric set hash gating logic (JSON settings based) ---
	// Compare task.settings.rubric_set[criterion_id] against step.settings.rubric_shell.hash_last_run
	rubricSetHash := ""
	if taskSettings != nil && taskSettings.RubricSet != nil {
		rubricSetHash = taskSettings.RubricSet[rsConfig.CriterionID]
	}
	logger.Printf("DEBUG: Step %d skip check: rubricSetHash=%q, hashLastRun=%q, effectiveForce=%v", se.StepID, rubricSetHash, rsConfig.HashLastRun, effectiveForce)
	if rubricSetHash != "" && rsConfig.HashLastRun == rubricSetHash && !effectiveForce {
		logger.Printf("Rubric_shell step %d for criterion '%s' is up-to-date (rubric_set hash: %s); skipping execution.", se.StepID, rsConfig.CriterionID, rubricSetHash)
		return nil
	}
	logger.Printf("Rubric_shell step %d for criterion '%s' will execute: rubric_set hash now '%s', last run '%s'", se.StepID, rsConfig.CriterionID, rubricSetHash, rsConfig.HashLastRun)

	// Build assignments strictly from task.settings.containers_map (ignore any step-level assignments)
	rsConfig.Assignments = nil
	if taskSettings != nil && taskSettings.ContainersMap != nil {
		// Only include solutionN patches that actually exist in rsConfig.Files
		for i := 1; i <= 4; i++ {
			patch := fmt.Sprintf("solution%d.patch", i)
			if _, ok := rsConfig.Files[patch]; !ok {
				continue
			}
			key := fmt.Sprintf("solution%d", i)
			if c, ok := taskSettings.ContainersMap[key]; ok && c.ContainerName != "" {
				rsConfig.Assignments = append(rsConfig.Assignments, models.RubricShellAssignment{
					Patch:     patch,
					Container: c.ContainerName,
				})
			}
		}
		// Add golden path if golden.patch key exists in files (hash may be empty from rubric_set hashing step)
		if _, ok := rsConfig.Files["golden.patch"]; ok {
			if c, ok := taskSettings.ContainersMap["golden"]; ok && c.ContainerName != "" {
				rsConfig.Assignments = append(rsConfig.Assignments, models.RubricShellAssignment{
					Patch:     "golden.patch",
					Container: c.ContainerName,
				})
			}
		}
		// Add original path (no patch application), always if container is available
		if c, ok := taskSettings.ContainersMap["original"]; ok && c.ContainerName != "" {
			rsConfig.Assignments = append(rsConfig.Assignments, models.RubricShellAssignment{
				Patch:     "original",
				Container: c.ContainerName,
			})
		}
	}
	if len(rsConfig.Assignments) == 0 {
		logger.Printf("ERROR: No container assignments could be derived from task.settings.containers_map for step %d", se.StepID)
		return fmt.Errorf("no container assignments from task settings")
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

		mode := "solution"
		if assignment.Patch == "golden.patch" {
			mode = "golden"
		} else if assignment.Patch == "original" {
			mode = "original"
		}

		patchFile := patchFileMap[assignment.Container]
		if mode != "original" {
			if patchFile == "" {
				logger.Printf("ERROR: No patch file found in rsConfig.Files for container '%s' (assignment patch '%s') in step %d. Skipping.", assignment.Container, assignment.Patch, se.StepID)
				results[assignment.Patch] = "Error: No patch file found"
				continue
			}
		}

		if mode == "original" {
			logger.Printf("Processing ORIGINAL baseline in container %s for criterion %s", assignment.Container, rsConfig.CriterionID)
			output, err := runOriginalSequence(se.BasePath, rsConfig, assignment.Container, rsConfig.Command, rsConfig.Rerun, logger)
			if err != nil {
				logger.Printf("ERROR: Test sequence failed for ORIGINAL baseline: %v", err)
				results["original"] = fmt.Sprintf("Error: %v\nOutput:\n%s", err, output)
			} else {
				status := "Unknown"
				if strings.Contains(output, cfg.PassMarker) {
					status = "Pass"
				} else if strings.Contains(output, cfg.FailMarker) {
					status = "Fail"
				} else {
					status = "Success"
				}
				results["original"] = fmt.Sprintf("%s\nOutput: %s", status, output)
				logger.Printf("Test %s for ORIGINAL baseline: %s\nOutput: %s", status, status, output)
			}
			continue
		}

		logger.Printf("Processing solution patch %s (resolved file: %s) in container %s for criterion %s", assignment.Patch, patchFile, assignment.Container, rsConfig.CriterionID)

		// Perform the test sequence: reset git, apply solution/golden patch, apply held-out tests patch, run command
		output, err := runTestSequence(se.BasePath, rsConfig, assignment.Container, patchFile, rsConfig.Command, rsConfig.Rerun, logger)
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

	// Store results only in the dedicated results column (not in settings)
	// Remove any 'container_N' keys from results
	for k := range results {
		if strings.HasPrefix(k, "container_") {
			delete(results, k)
		}
	}
	
	// Store results in the dedicated results column
	logger.Printf("[TRACE] Persisting results for step %d in results column", se.StepID)
	resultsIface := make(map[string]interface{}, len(results))
	for k, v := range results {
		resultsIface[k] = v
	}
	if err := models.StoreStepResult(db, se.StepID, resultsIface); err != nil {
		logger.Printf("[ERROR] Failed to persist results in results column for step %d: %v", se.StepID, err)
		return fmt.Errorf("failed to store step results: %w", err)
	} else {
		logger.Printf("[TRACE] Successfully persisted results in results column for step %d", se.StepID)
	}

	logger.Printf("Completed processing for criterion %s with %d assignments", rsConfig.CriterionID, len(rsConfig.Assignments))

	// Persist updated hash_last_run (and reset rerun if it was set)
	if rubricSetHash != "" {
		rsConfig.HashLastRun = rubricSetHash
	}
	if rsConfig.Rerun {
		rsConfig.Rerun = false
	}
	persistSettings, err := json.Marshal(map[string]models.RubricShellConfig{"rubric_shell": rsConfig})
	if err != nil {
		logger.Printf("Failed to marshal settings for persistence on step %d: %v", se.StepID, err)
	} else {
		_, err := db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(persistSettings), se.StepID)
		if err != nil {
			logger.Printf("Failed to update step settings on step %d: %v", se.StepID, err)
		} else {
			logger.Printf("[TRACE] Persisted hash_last_run and settings for rubric_shell step %d.", se.StepID)
		}
	}
	return nil
}

// Helper function to run the test sequence (adapt based on existing code)
func runTestSequence(basePath string, rsConfig models.RubricShellConfig, container string, patch string, command string, rerun bool, logger *log.Logger) (string, error) {
	// Add debug log for base_path
	logger.Printf("Debug: runTestSequence base_path '%s' for patch %s", basePath, patch)

	// Step 1: Ensure clean git state in the container
	// Execute git cleanup commands in proper order
	// When rerun=true, be more aggressive with cleanup
	cleanupCmds := [][]string{
		{"docker", "exec", container, "git", "clean", "-fdx"},
		{"docker", "exec", container, "git", "reset", "--hard", "HEAD"},
		{"docker", "exec", container, "git", "checkout", "--", "."},
	}
	
	// When rerun=true, add additional cleanup steps
	if rerun {
		logger.Printf("[RERUN] Performing enhanced git cleanup for forced rerun in container %s", container)
		// Add more aggressive cleanup commands for rerun
		additionalCleanup := [][]string{
			{"docker", "exec", container, "git", "stash", "clear"},
			{"docker", "exec", container, "find", "/app", "-name", "*.orig", "-delete"},
			{"docker", "exec", container, "find", "/app", "-name", "*.rej", "-delete"},
		}
		cleanupCmds = append(cleanupCmds, additionalCleanup...)
	} else {
		logger.Printf("[NORMAL] Performing standard git cleanup in container %s", container)
	}
	
	for i, c := range cleanupCmds {
		cmd := exec.Command(c[0], c[1:]...)
		if err := cmd.Run(); err != nil {
			if rerun {
				// For rerun, log cleanup failures as warnings but continue
				logger.Printf("Warning: enhanced cleanup command %d failed during rerun: %v", i+1, err)
			} else {
				logger.Printf("Warning: cleanup command %d failed: %v", i+1, err)
			}
			// Continue with other cleanup commands even if one fails
		}
	}

	// Step 2: Apply PREPATCH (if it exists) - run as script
	if _, ok := rsConfig.Files["pre_patch.patch"]; ok {
		tmpPrePatchPath := "/tmp/pre_patch.patch"
		cmd := exec.Command("docker", "cp", filepath.Join(basePath, "pre_patch.patch"), fmt.Sprintf("%s:%s", container, tmpPrePatchPath))
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("copy pre_patch.patch failed: %w", err)
		}
		cmd = exec.Command("docker", "exec", container, "bash", "-c", escapeSingleQuotes("bash "+tmpPrePatchPath))
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("execute pre_patch.patch failed: %w", err)
		}
		logger.Printf("Applied pre_patch.patch in container %s", container)
	}

	// Step 3: Apply solution/golden patch using git apply
	if _, ok := rsConfig.Files[patch]; ok {
		// Special-case: if golden.patch exists but is empty, skip applying and continue
		if patch == "golden.patch" {
			fullPath := filepath.Join(basePath, patch)
			if fi, err := os.Stat(fullPath); err == nil && fi.Size() == 0 {
				logger.Printf("Golden patch is empty; skipping apply in container %s", container)
			} else {
				containerPatchPath := "/app/" + patch
				cmd := exec.Command("docker", "cp", filepath.Join(basePath, patch), fmt.Sprintf("%s:%s", container, containerPatchPath))
				if err := cmd.Run(); err != nil {
					// For golden, continue even if we fail to copy/apply the patch
					logger.Printf("WARNING: Copy golden patch failed (continuing without it): %v", err)
				} else {
					applyPatchCmd := exec.Command("docker", "exec", container, "git", "apply", containerPatchPath)
					if err := applyPatchCmd.Run(); err != nil {
						logger.Printf("WARNING: Golden patch apply failed for criterion %s (continuing without it): %v", rsConfig.CriterionID, err)
					} else {
						logger.Printf("Applied solution patch %s in container %s", patch, container)
					}
				}
			}
		} else {
			containerPatchPath := "/app/" + patch
			cmd := exec.Command("docker", "cp", filepath.Join(basePath, patch), fmt.Sprintf("%s:%s", container, containerPatchPath))
			if err := cmd.Run(); err != nil {
				return "", fmt.Errorf("copy solution patch %s failed: %w", patch, err)
			}
			applyPatchCmd := exec.Command("docker", "exec", container, "git", "apply", containerPatchPath)
			if err := applyPatchCmd.Run(); err != nil {
				logger.Printf("ERROR: Solution patch %s apply failed for criterion %s: %v", patch, rsConfig.CriterionID, err)
				return "", fmt.Errorf("solution patch %s apply failed: %w", patch, err)
			}
			logger.Printf("Applied solution patch %s in container %s", patch, container)
		}
	} else {
		logger.Printf("Solution patch '%s' not found in rsConfig.Files", patch)
		return "", fmt.Errorf("solution patch '%s' not found in rsConfig.Files", patch)
	}

	// Step 4: Apply held-out tests patch using git apply
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
		}
		cmd = exec.Command("docker", "exec", container, "git", "apply", containerHeldOutTestsPatchPath)
		if err := cmd.Run(); err != nil {
			logger.Printf("ERROR: Held-out tests patch apply failed for criterion %s: %v", rsConfig.CriterionID, err)
			return "", fmt.Errorf("held-out tests patch apply failed: %w", err)
		}
		logger.Printf("Applied held_out_tests.patch in container %s", container)
	} else {
		logger.Println("held_out_tests.patch not found in rsConfig.Files")
		return "", fmt.Errorf("held_out_tests.patch not found in rsConfig.Files")
	}

	// Step 5: Run the rubric test command and capture output
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

// runOriginalSequence runs the rubric on the unmodified ORIGINAL container state.
// It performs git cleanup, applies only held_out_tests.patch, and runs the command.
func runOriginalSequence(basePath string, rsConfig models.RubricShellConfig, container string, command string, rerun bool, logger *log.Logger) (string, error) {
    // Step 1: Ensure clean git state in the container
    cleanupCmds := [][]string{
        {"docker", "exec", container, "git", "clean", "-fdx"},
        {"docker", "exec", container, "git", "reset", "--hard", "HEAD"},
        {"docker", "exec", container, "git", "checkout", "--", "."},
    }
    if rerun {
        logger.Printf("[RERUN] Performing enhanced git cleanup for forced rerun in container %s (ORIGINAL)", container)
        additionalCleanup := [][]string{
            {"docker", "exec", container, "git", "stash", "clear"},
            {"docker", "exec", container, "find", "/app", "-name", "*.orig", "-delete"},
            {"docker", "exec", container, "find", "/app", "-name", "*.rej", "-delete"},
        }
        cleanupCmds = append(cleanupCmds, additionalCleanup...)
    } else {
        logger.Printf("[NORMAL] Performing standard git cleanup in container %s (ORIGINAL)", container)
    }
    for i, c := range cleanupCmds {
        cmd := exec.Command(c[0], c[1:]...)
        if err := cmd.Run(); err != nil {
            if rerun {
                logger.Printf("Warning: enhanced cleanup command %d failed during rerun (ORIGINAL): %v", i+1, err)
            } else {
                logger.Printf("Warning: cleanup command %d failed (ORIGINAL): %v", i+1, err)
            }
        }
    }

    // Step 2: Apply held-out tests patch using git apply
    if _, ok := rsConfig.Files["held_out_tests.patch"]; ok {
        fullHeldOutTestsPath := filepath.Join(basePath, "held_out_tests.patch")
        if _, err := os.Stat(fullHeldOutTestsPath); os.IsNotExist(err) {
            return "", fmt.Errorf("held_out_tests.patch does not exist at %s", fullHeldOutTestsPath)
        } else if err != nil {
            return "", fmt.Errorf("error checking held_out_tests.patch: %w", err)
        }
        logger.Printf("Confirmed held_out_tests.patch exists at %s (ORIGINAL)", fullHeldOutTestsPath)
        containerHeldOutTestsPatchPath := "/app/held_out_tests.patch"
        cmd := exec.Command("docker", "cp", fullHeldOutTestsPath, fmt.Sprintf("%s:%s", container, containerHeldOutTestsPatchPath))
        if err := cmd.Run(); err != nil {
            return "", fmt.Errorf("copy held-out tests patch failed (ORIGINAL): %w", err)
        }
        cmd = exec.Command("docker", "exec", container, "git", "apply", containerHeldOutTestsPatchPath)
        if err := cmd.Run(); err != nil {
            logger.Printf("ERROR: Held-out tests patch apply failed for criterion %s (ORIGINAL): %v", rsConfig.CriterionID, err)
            return "", fmt.Errorf("held-out tests patch apply failed (ORIGINAL): %w", err)
        }
        logger.Printf("Applied held_out_tests.patch in container %s (ORIGINAL)", container)
    } else {
        logger.Println("held_out_tests.patch not found in rsConfig.Files (ORIGINAL)")
        return "", fmt.Errorf("held_out_tests.patch not found in rsConfig.Files (ORIGINAL)")
    }

    // Step 3: Run the rubric test command and capture output
    scriptFile, err := os.CreateTemp("", "rubric-script-*.sh")
    if err != nil {
        return "", fmt.Errorf("failed to create temp script file (ORIGINAL): %w", err)
    }
    defer os.Remove(scriptFile.Name())
    scriptContent := fmt.Sprintf("#!/bin/bash\n%s", command)
    if _, err := scriptFile.WriteString(scriptContent); err != nil {
        return "", fmt.Errorf("failed to write to temp script file (ORIGINAL): %w", err)
    }
    scriptFile.Close()
    if err := os.Chmod(scriptFile.Name(), 0755); err != nil {
        return "", fmt.Errorf("failed to make script executable (ORIGINAL): %w", err)
    }
    containerScriptPath := "/tmp/run_rubric.sh"
    copyCmd := exec.Command("docker", "cp", scriptFile.Name(), fmt.Sprintf("%s:%s", container, containerScriptPath))
    if err := copyCmd.Run(); err != nil {
        return "", fmt.Errorf("failed to copy script to container (ORIGINAL): %w", err)
    }
    cmd := exec.Command("docker", "exec", container, "/bin/bash", containerScriptPath)
    output, err := cmd.CombinedOutput()
    if err != nil {
        logger.Printf("Error running rubric script (ORIGINAL): %v\nOutput:\n%s", err, string(output))
        return string(output), err
    }
    return string(output), nil
}
