package internal

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// rubricRunMode controls which assignments are included by ProcessRubricShellStep
// "" (empty): default behavior for task run -> solutions only
// "golden-only": only golden container logic
var rubricRunMode string

// setRubricRunMode temporarily sets rubricRunMode and returns a restore func
func setRubricRunMode(mode string) func() {
	prev := rubricRunMode
	rubricRunMode = mode
	return func() { rubricRunMode = prev }
}

// SetRubricRunModeForCLI exposes rubricRunMode setter for CLI commands and returns
// a restore function to revert to the previous mode when done.
func SetRubricRunModeForCLI(mode string) func() {
	return setRubricRunMode(mode)
}

// processAllRubricShellSteps finds and executes all rubric_shell steps.
func processAllRubricShellSteps(db *sql.DB, logger *log.Logger, force bool, golden bool) error {
	// Query for all steps of type 'rubric_shell'.
	query := `
		SELECT s.id, s.task_id, s.title, s.settings, COALESCE(t.local_path, '') AS base_path
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
		if err := ProcessRubricShellStep(db, &models.StepExec{StepID: step.ID, TaskID: step.TaskID, Title: step.Title, Settings: step.Settings, BasePath: step.BasePath}, logger, force, golden); err != nil {
			logger.Printf("failed to process rubric_shell step %d: %v", step.ID, err)
			// Continue processing other steps even if one fails.
		}
	}

	return nil
}

// ProcessRubricShellStep handles the execution of a rubric_shell step.
func ProcessRubricShellStep(db *sql.DB, se *models.StepExec, logger *log.Logger, force bool, golden bool) error {
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
		// Debug: show available containers_map entries
		logger.Printf("[TRACE] task.settings.containers_map keys: %v", func() []string {
			keys := make([]string, 0, len(taskSettings.ContainersMap))
			for k := range taskSettings.ContainersMap {
				keys = append(keys, k)
			}
			return keys
		}())
		// Solutions are included by default except in special modes.
		// Exclude when rubricRunMode is "golden-only" or "original-only".
		if rubricRunMode != "golden-only" && rubricRunMode != "original-only" {
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
		}

		// Golden path: include when (a) global golden flag set OR (b) golden-only mode.
		// Do NOT require golden.patch to exist here; golden mode may run without it.
		if golden || rubricRunMode == "golden-only" {
			goldenName := ""
			if c, ok := taskSettings.ContainersMap["golden"]; ok && c.ContainerName != "" {
				goldenName = c.ContainerName
			} else {
				// Fallback: infer golden container name
				infer := models.GenerateDVContainerNameForBase(se.TaskID, "golden")
				running, _ := models.CheckContainerExists(infer)
				if running {
					goldenName = infer
					logger.Printf("[GOLDEN] containers_map missing golden; inferred running container '%s'", infer)
				} else {
					logger.Printf("[GOLDEN] No golden container found in containers_map and inference '%s' not running", infer)
				}
			}
			if goldenName != "" {
				rsConfig.Assignments = append(rsConfig.Assignments, models.RubricShellAssignment{
					Patch:     "golden.patch",
					Container: goldenName,
				})
			}
		}

		// Original baseline inclusion rules:
		// - When rubricRunMode == "original-only": always include Original if present
		// - Else: include only for task run with --golden (and not golden-only mode)
		if rubricRunMode == "original-only" {
			if c, ok := taskSettings.ContainersMap["original"]; ok && c.ContainerName != "" {
				rsConfig.Assignments = append(rsConfig.Assignments, models.RubricShellAssignment{
					Patch:     "original",
					Container: c.ContainerName,
				})
			}
		} else if rubricRunMode != "golden-only" && golden {
			if c, ok := taskSettings.ContainersMap["original"]; ok && c.ContainerName != "" {
				rsConfig.Assignments = append(rsConfig.Assignments, models.RubricShellAssignment{
					Patch:     "original",
					Container: c.ContainerName,
				})
			}
		}
	}
	// If golden not enabled, ensure any accidental golden assignments are filtered out
	if !golden && rubricRunMode != "golden-only" && len(rsConfig.Assignments) > 0 {
		filtered := rsConfig.Assignments[:0]
		for _, a := range rsConfig.Assignments {
			if a.Patch == "golden.patch" {
				continue
			}
			filtered = append(filtered, a)
		}
		rsConfig.Assignments = filtered
	}

	// In original-only mode, keep only the Original assignment
	if rubricRunMode == "original-only" && len(rsConfig.Assignments) > 0 {
		filtered := rsConfig.Assignments[:0]
		for _, a := range rsConfig.Assignments {
			if a.Patch == "original" {
				filtered = append(filtered, a)
			}
		}
		rsConfig.Assignments = filtered
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

	// Determine app folder for running git commands inside the container
	appFolder := "/app"
	if taskSettings != nil && taskSettings.AppFolder != "" {
		appFolder = taskSettings.AppFolder
	}

	// Use utility to resolve patch file for each container
	patchFileMap := models.GetPatchFileForContainerAssignments(rsConfig.Assignments, rsConfig.Files)

	// Iterate over each assignment and run the test sequence in parallel
	var wg sync.WaitGroup
	var resultsMu sync.Mutex
	for _, asg := range rsConfig.Assignments {
		assignment := asg // capture loop variable
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Only read container assignment and state; do NOT start/stop/recreate here.
			cmdInspect := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", assignment.Container)
			inspectOutput, err := cmdInspect.Output()
			if err != nil {
				logger.Printf("ERROR: Cannot inspect container '%s': %v", assignment.Container, err)
				resultsMu.Lock()
				results[assignment.Patch] = "Error: Cannot inspect container"
				resultsMu.Unlock()
				return
			}
			if strings.TrimSpace(string(inspectOutput)) != "true" {
				logger.Printf("ERROR: Container '%s' is not running. rubric_shell will not manage lifecycle; skipping.", assignment.Container)
				resultsMu.Lock()
				results[assignment.Patch] = "Error: Container not running"
				resultsMu.Unlock()
				return
			}

			if assignment.Container == "" {
				logger.Printf("ERROR: No container assigned for solution patch '%s' in step %d. Skipping this assignment.", assignment.Patch, se.StepID)
				resultsMu.Lock()
				results[assignment.Patch] = "Error: No container assigned"
				resultsMu.Unlock()
				return
			}

			mode := "solution"
			if assignment.Patch == "golden.patch" {
				mode = "golden"
			} else if assignment.Patch == "original" {
				mode = "original"
			}

			patchFile := patchFileMap[assignment.Container]
			if mode == "solution" {
				if patchFile == "" {
					logger.Printf("ERROR: No patch file found in rsConfig.Files for container '%s' (assignment patch '%s') in step %d. Skipping.", assignment.Container, assignment.Patch, se.StepID)
					resultsMu.Lock()
					results[assignment.Patch] = "Error: No patch file found"
					resultsMu.Unlock()
					return
				}
			}

			if mode == "original" {
				logger.Printf("Processing ORIGINAL baseline in container %s for criterion %s", assignment.Container, rsConfig.CriterionID)
				output, err := runOriginalSequence(se.BasePath, appFolder, rsConfig, assignment.Container, rsConfig.Command, rsConfig.Rerun, logger)
				if err != nil {
					logger.Printf("ERROR: Test sequence failed for ORIGINAL baseline: %v", err)
					resultsMu.Lock()
					results["original"] = fmt.Sprintf("Error: %v\nOutput:\n%s", err, output)
					resultsMu.Unlock()
				} else {
					status := "Unknown"
					if strings.Contains(output, cfg.PassMarker) {
						status = "Pass"
					} else if strings.Contains(output, cfg.FailMarker) {
						status = "Fail"
					} else {
						status = "Success"
					}
					resultsMu.Lock()
					results["original"] = fmt.Sprintf("%s\nOutput: %s", status, output)
					resultsMu.Unlock()
					logger.Printf("Test %s for ORIGINAL baseline: %s\nOutput: %s", status, status, output)
				}
				return
			}

			logger.Printf("Processing solution patch %s (resolved file: %s) in container %s for criterion %s", assignment.Patch, patchFile, assignment.Container, rsConfig.CriterionID)

			// Perform the test sequence: reset git, apply solution/golden patch, apply held-out tests patch, run command
			output, err := runTestSequence(se.BasePath, appFolder, rsConfig, assignment.Container, assignment.Patch, rsConfig.Command, rsConfig.Rerun, logger)
			if err != nil {
				logger.Printf("ERROR: Test sequence failed for patch %s: %v", assignment.Patch, err)
				// Use stable key: 'golden' for golden runs, else the patch filename
				resultKey := assignment.Patch
				if assignment.Patch == "golden.patch" {
					resultKey = "golden"
				}
				resultsMu.Lock()
				results[resultKey] = fmt.Sprintf("Error: %v\nOutput:\n%s", err, output)
				resultsMu.Unlock()
			} else {
				status := "Unknown"
				if strings.Contains(output, cfg.PassMarker) {
					status = "Pass"
				} else if strings.Contains(output, cfg.FailMarker) {
					status = "Fail"
				} else {
					status = "Success"
				}
				// Use stable key: 'golden' for golden runs, else the patch filename
				resultKey := assignment.Patch
				if assignment.Patch == "golden.patch" {
					resultKey = "golden"
				}
				resultsMu.Lock()
				results[resultKey] = fmt.Sprintf("%s\nOutput: %s", status, output)
				resultsMu.Unlock()
				logger.Printf("Test %s for patch %s: %s\nOutput: %s", status, assignment.Patch, status, output)
			}

			// If this was the GOLDEN container run and a held_out_test_clean_up command is configured
			// execute it now to clean up held-out test changes. Do not alter any other cleanup logic.
			if assignment.Patch == "golden.patch" && taskSettings != nil && taskSettings.HeldOutTestCleanUp != "" {
				logger.Printf("[GOLDEN] Executing held_out_test_clean_up command in container %s", assignment.Container)
				// Run cleanup from <appFolder>/ansible to match rubric execution context
				workDir := filepath.Join(appFolder, "ansible")
				cleanupCmd := exec.Command(
					"docker", "exec", "-w", workDir, assignment.Container,
					"bash", "-c", taskSettings.HeldOutTestCleanUp,
				)
				if out, cerr := cleanupCmd.CombinedOutput(); cerr != nil {
					logger.Printf("[GOLDEN] WARNING: held_out_test_clean_up failed: %v\nOutput: %s", cerr, string(out))
				} else {
					logger.Printf("[GOLDEN] held_out_test_clean_up completed successfully")
				}
			}
		}()
	}
	wg.Wait()

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

func runTestSequence(basePath string, appFolder string, rsConfig models.RubricShellConfig, container string, patch string, command string, rerun bool, logger *log.Logger) (string, error) {
	// Add debug log for base_path
	logger.Printf("Debug: runTestSequence base_path '%s' for patch %s", basePath, patch)

	// Step 1: Ensure clean git state in the container
	// Golden mode: revert ONLY files/folders touched by held_out_tests.patch
	if patch == "golden.patch" {
		// Compute touched paths from held_out_tests.patch
		touched, perr := parsePatchTouchedPaths(basePath, "held_out_tests.patch")
		if perr != nil {
			logger.Printf("[GOLDEN] WARNING: failed to parse held_out_tests.patch for selective cleanup: %v (skipping cleanup)", perr)
		} else if len(touched) == 0 {
			logger.Printf("[GOLDEN] No paths parsed from held_out_tests.patch; skipping cleanup")
		} else {
			// Build a space-separated, single-quoted path list
			quoted := make([]string, 0, len(touched))
			for _, p := range touched {
				// Prevent leading ./ duplication; ensure relative paths
				p = strings.TrimPrefix(p, "./")
				if p == "" {
					continue
				}
				quoted = append(quoted, "'"+strings.ReplaceAll(p, "'", "'\\''")+"'")
			}
			if len(quoted) > 0 {
				argList := strings.Join(quoted, " ")
				cmds := []string{
					"sync && if [ -e .git/index.lock ]; then rm -f .git/index.lock; fi",
					"sync && git checkout -- " + argList,
					"sync && git reset --hard HEAD -- " + argList,
					// Use -d (directories) only where applicable; git clean supports pathspecs
					"sync && git clean -fd -- " + argList,
				}
				// Also remove .orig/.rej only under touched paths
				for _, p := range quoted {
					cmds = append(cmds, "sync && find "+p+" -name '*.orig' -delete || true")
					cmds = append(cmds, "sync && find "+p+" -name '*.rej' -delete || true")
				}
				logger.Printf("[GOLDEN] Performing selective cleanup for paths from held_out_tests.patch in container %s: %v", container, touched)
				for i, sc := range cmds {
					logger.Printf("Debug: selective cleanup %d: %s", i+1, sc)
					cmd := exec.Command("docker", "exec", "-w", appFolder, container, "sh", "-c", sc)
					if out, err := cmd.CombinedOutput(); err != nil {
						logger.Printf("Warning: selective cleanup step %d failed: %v\nOutput: %s", i+1, err, string(out))
					}
				}
			}
		}
	} else {
		// Solutions and others: keep existing broader cleanup
		cleanupCmds := [][]string{
			{"docker", "exec", "-w", appFolder, container, "sh", "-c", "sync && git checkout -- ."},
			{"docker", "exec", "-w", appFolder, container, "sh", "-c", "sync && git clean -fdx"},
			{"docker", "exec", "-w", appFolder, container, "sh", "-c", "sync && git reset --hard HEAD"},
			{"docker", "exec", "-w", appFolder, container, "sh", "-c", "sync && git checkout -- ."},
			{"docker", "exec", "-w", appFolder, container, "sh", "-c", "sync && git clean -fdx"},
			{"docker", "exec", "-w", appFolder, container, "sh", "-c", "sync && git stash clear"},
			{"docker", "exec", "-w", appFolder, container, "sh", "-c", "sync && find '" + appFolder + "' -name '*.orig' -delete"},
			{"docker", "exec", "-w", appFolder, container, "sh", "-c", "sync && find '" + appFolder + "' -name '*.rej' -delete"},
		}

		logger.Printf("[NORMAL] Performing git cleanup in container %s", container)

		for i, c := range cleanupCmds {
			// Guard: remove a stale .git/index.lock if present before each cleanup step
			guardCmdSlice := []string{"docker", "exec", "-w", appFolder, container, "sh", "-c", "if [ -e .git/index.lock ]; then echo '[guard] removing .git/index.lock'; rm -f .git/index.lock; fi"}
			logger.Printf("Debug: runTestSequence guard before cleanup %d: %v", i+1, guardCmdSlice)
			guardCmd := exec.Command(guardCmdSlice[0], guardCmdSlice[1:]...)
			if gout, gerr := guardCmd.CombinedOutput(); gerr != nil {
				logger.Printf("Warning: guard before cleanup %d failed: %v\nOutput: %s", i+1, gerr, string(gout))
				// Continue regardless; attempt the cleanup command anyway
			}

			logger.Printf("Debug: runTestSequence cleanup command %d: %v", i+1, c)
			cmd := exec.Command(c[0], c[1:]...)
			if out, err := cmd.CombinedOutput(); err != nil {
				logger.Printf("Warning: cleanup command %d failed: %v\nOutput: %s", i+1, err, string(out))
				// Continue with other cleanup commands even if one fails
			}
		}
	}

	// Step 2: Apply PREPATCH (if it exists) - run as script
	if _, ok := rsConfig.Files["pre_patch.patch"]; ok {
		tmpPrePatchPath := "/tmp/pre_patch.patch"
		cpOut, cpErr := exec.Command("docker", "cp", filepath.Join(basePath, "pre_patch.patch"), fmt.Sprintf("%s:%s", container, tmpPrePatchPath)).CombinedOutput()
		if cpErr != nil {
			return string(cpOut), fmt.Errorf("copy pre_patch.patch failed: %w", cpErr)
		}
		// Ensure executable and run with working directory set to appFolder
		cmdStr := "chmod +x " + tmpPrePatchPath + " && " + tmpPrePatchPath
		execOut, execErr := exec.Command("docker", "exec", "-w", appFolder, container, "bash", "-lc", cmdStr).CombinedOutput()
		if execErr != nil {
			return string(execOut), fmt.Errorf("execute pre_patch script failed: %w", execErr)
		}
		logger.Printf("Executed pre_patch script in container %s", container)
	}

	// Step 3: Apply solution patch using git apply (skip golden by design)
	if patch == "golden.patch" {
		logger.Printf("[GOLDEN] Skipping golden.patch application by design in container %s", container)
	} else {
		if _, ok := rsConfig.Files[patch]; ok {
			containerPatchPath := "/tmp/" + patch
			cpOut, cpErr := exec.Command("docker", "cp", filepath.Join(basePath, patch), fmt.Sprintf("%s:%s", container, containerPatchPath)).CombinedOutput()
			if cpErr != nil {
				return string(cpOut), fmt.Errorf("copy solution patch %s failed: %w", patch, cpErr)
			}
			applyOut, applyErr := exec.Command("docker", "exec", "-w", appFolder, container, "git", "apply", containerPatchPath).CombinedOutput()
			if applyErr != nil {
				logger.Printf("ERROR: Solution patch %s apply failed for criterion %s: %v\nOutput: %s", patch, rsConfig.CriterionID, applyErr, string(applyOut))
				return string(applyOut), fmt.Errorf("solution patch %s apply failed: %w", patch, applyErr)
			}
			logger.Printf("Applied solution patch %s in container %s", patch, container)
		} else {
			logger.Printf("Solution patch '%s' not found in rsConfig.Files", patch)
			return "", fmt.Errorf("solution patch '%s' not found in rsConfig.Files", patch)
		}
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
		containerHeldOutTestsPatchPath := filepath.Join(appFolder, "held_out_tests.patch")
		cpOut, cpErr := exec.Command("docker", "cp", fullHeldOutTestsPath, fmt.Sprintf("%s:%s", container, containerHeldOutTestsPatchPath)).CombinedOutput()
		if cpErr != nil {
			return string(cpOut), fmt.Errorf("copy held-out tests patch failed: %w", cpErr)
		}
		applyOut, applyErr := exec.Command("docker", "exec", "-w", appFolder, container, "git", "apply", containerHeldOutTestsPatchPath).CombinedOutput()
		if applyErr != nil {
			logger.Printf("ERROR: Held-out tests patch apply failed for criterion %s: %v\nOutput: %s", rsConfig.CriterionID, applyErr, string(applyOut))
			return string(applyOut), fmt.Errorf("held-out tests patch apply failed: %w", applyErr)
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

	// Execute the script directly; working dir is set via docker exec -w
	execSnippet := containerScriptPath
	logger.Printf("Executing rubric script: %s", execSnippet)
	cmd := exec.Command("docker", "exec", "-w", appFolder, container, "bash", "-lc", execSnippet)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Error running rubric script: %v\nOutput:\n%s", err, string(output))
		return string(output), err
	}
	return string(output), nil
}

// runOriginalSequence runs the rubric on the unmodified ORIGINAL container state.
// It performs git cleanup, applies only held_out_tests.patch, and runs the command.
func runOriginalSequence(basePath string, appFolder string, rsConfig models.RubricShellConfig, container string, command string, rerun bool, logger *log.Logger) (string, error) {
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
	// Execute the script directly; working dir is set via docker exec -w
	execSnippet := containerScriptPath
	logger.Printf("Executing rubric script (ORIGINAL): %s", execSnippet)
	cmd := exec.Command("docker", "exec", "-w", appFolder, container, "bash", "-lc", execSnippet)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Error running rubric script (ORIGINAL): %v\nOutput:\n%s", err, string(output))
		return string(output), err
	}
	return string(output), nil
}

// parsePatchTouchedPaths reads a unified diff patch file at basePath/patchFileName and
// returns a de-duplicated list of file or directory paths that are touched by the patch.
func parsePatchTouchedPaths(basePath string, patchFileName string) ([]string, error) {
	patchPath := filepath.Join(basePath, patchFileName)
	f, err := os.Open(patchPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	seen := make(map[string]struct{})
	addPath := func(p string) {
		if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
			p = p[2:]
		}
		p = strings.TrimSpace(p)
		if p == "" || p == "/dev/null" {
			return
		}
		seen[p] = struct{}{}
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "diff --git ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				addPath(parts[2])
				addPath(parts[3])
			}
			continue
		}
		if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				addPath(fields[1])
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	return paths, nil
}
