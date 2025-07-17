package internal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
		var stepExec models.StepExec
		if err := rows.Scan(&stepExec.StepID, &stepExec.TaskID, &stepExec.Title, &stepExec.Settings, &stepExec.LocalPath); err != nil {
			logger.Printf("failed to scan rubric_shell step: %v", err)
			continue
		}

		// Create a logger for this specific step instance.
		stepLogger := log.New(os.Stdout, fmt.Sprintf("STEP %d [rubric_shell]: ", stepExec.StepID), log.Ldate|log.Ltime|log.Lshortfile)

		// Call the original processor for the individual step.
		if err := ProcessRubricShellStep(db, &stepExec, stepLogger, false); err != nil {
			logger.Printf("failed to process rubric_shell step %d: %v", stepExec.StepID, err)
			// Continue processing other steps even if one fails.
		}
	}

	return nil
}

// ProcessRubricShellStep handles the execution of a rubric_shell step.
// It fetches the latest container assignments from the task settings, then for each assigned container,
// it applies the relevant patches and runs the test command, capturing the results.
func ProcessRubricShellStep(db *sql.DB, stepExec *models.StepExec, stepLogger *log.Logger, force bool) error {
	// Defensive: Check parent task status before running
	var status string
	err := db.QueryRow("SELECT status FROM tasks WHERE id = $1", stepExec.TaskID).Scan(&status)
	if err != nil {
		return fmt.Errorf("failed to fetch parent task status for step %d: %w", stepExec.StepID, err)
	}
	if status != "active" {
		stepLogger.Printf("Skipping execution because parent task %d status is not active (status=\"%s\")", stepExec.TaskID, status)
		return nil
	}
	cfg, _ := LoadConfig()
	passMarker := cfg.PassMarker
	failMarker := cfg.FailMarker
	if passMarker == "" {
		passMarker = "#__PASS__#"
	}
	if failMarker == "" {
		failMarker = "#__FAIL__#"
	}
	var wrappedSettings struct {
		RubricShell models.RubricShellConfig `json:"rubric_shell"`
	}
	if err := json.Unmarshal([]byte(stepExec.Settings), &wrappedSettings); err != nil {
		return fmt.Errorf("failed to unmarshal rubric_shell settings: %w", err)
	}
	config := wrappedSettings.RubricShell

	// Fetch the latest container assignments from the task settings
	taskSettings, err := models.GetTaskSettings(db, stepExec.TaskID)
	if err != nil {
		return fmt.Errorf("failed to get task settings: %w", err)
	}

	if len(taskSettings.AssignContainers) == 0 {
		stepLogger.Println("No containers assigned in task settings. Nothing to do.")
		return nil
	}

	// Load existing results from DB if present
	allResults := make(map[string]map[string]interface{})
	var prevResultsStr sql.NullString
	err = db.QueryRow("SELECT results FROM steps WHERE id = $1", stepExec.StepID).Scan(&prevResultsStr)
	if err != nil && err != sql.ErrNoRows {
		stepLogger.Printf("Warning: failed to fetch previous results: %v", err)
	} else if prevResultsStr.Valid && prevResultsStr.String != "" {
		err = json.Unmarshal([]byte(prevResultsStr.String), &allResults)
		if err != nil {
			stepLogger.Printf("Warning: failed to unmarshal previous results: %v", err)
			allResults = make(map[string]map[string]interface{})
		}
	}

	var numSuccess, numTotal int

	// Helper to run a command and log its execution and output.
	runCmd := func(cmd *exec.Cmd, description string, logOutput bool) (string, error) {
		stepLogger.Printf("DEBUG: Executing test command for criterion %s: %s", config.CriterionID, cmd.String())
		stepLogger.Printf("Executing: %s", cmd.String())
		output, err := cmd.CombinedOutput()
		outputStr := string(output)
		if err != nil {
			stepLogger.Printf("Error %s: %v\nOutput:\n%s", description, err, outputStr)
			return outputStr, fmt.Errorf("failed to %s: %w", description, err)
		}
		if logOutput {
			stepLogger.Printf("Success: %s\nOutput:\n%s", description, outputStr)
		} else {
			stepLogger.Printf("Success: %s", description)
		}
		return outputStr, nil
	}

	// Prepare last_run map in config if not present
	if config.LastRun == nil {
		config.LastRun = make(map[string]string)
	}

	// --- File hash change detection logic (copied from rubric_set) ---
	// Find parent rubric_set step and get its files map
	parentRubricSet, err := models.GetRubricSetFromDependencies(db, stepExec.StepID, stepLogger)
	if err != nil {
		stepLogger.Printf("Warning: could not find parent rubric_set: %v", err)
	}
	filesChanged := false
	if parentRubricSet != nil && parentRubricSet.Files != nil {
		if config.Files == nil {
			config.Files = make(map[string]string)
		}
		for fileName := range parentRubricSet.Files {
			filePath := fileName
			if !filepath.IsAbs(filePath) {
				filePath = filepath.Join(stepExec.LocalPath, filePath)
			}
			stepLogger.Printf("DEBUG: fileName=%s filePath=%s", fileName, filePath)
			info, err := os.Stat(filePath)
			fileIsException := fileName == "TASK_DATA.md" || fileName == "rubrics.mhtml"
			if err != nil {
				stepLogger.Printf("Warning: could not stat %s: %v", filePath, err)
				if config.Files[fileName] != "" && !fileIsException {
					filesChanged = true
				}
				config.Files[fileName] = ""
				continue
			}
			if info.IsDir() {
				stepLogger.Printf("Skipping directory: %s", filePath)
				if config.Files[fileName] != "" && !fileIsException {
					filesChanged = true
				}
				config.Files[fileName] = ""
				continue
			}
			hash, err := models.GetSHA256(filePath)
			if err != nil {
				stepLogger.Printf("Warning: could not compute hash for %s: %v", filePath, err)
				if config.Files[fileName] != "" && !fileIsException {
					filesChanged = true
				}
				config.Files[fileName] = ""
				continue
			}
			if old, ok := config.Files[fileName]; (!ok || old != hash) && !fileIsException {
				filesChanged = true
			}
			config.Files[fileName] = hash
		}
	}
	// --- End file hash change detection ---

	// If LastRun is nil or missing entries for any solution, trigger re-run
	if config.LastRun == nil {
		filesChanged = true
	} else {
		for solutionPatch := range taskSettings.AssignContainers {
			if _, ok := config.LastRun[solutionPatch]; !ok {
				filesChanged = true
				break
			}
		}
	}

	// If not force, and filesChanged is false, skip execution
	if !force && !filesChanged {
		stepLogger.Printf("No changes detected for solution(s). Skipping execution.")
		return nil
	}

	var (
		wg        sync.WaitGroup
		resultsMu sync.Mutex
	)
	for solutionPatch, containerName := range taskSettings.AssignContainers {
		wg.Add(1)
		go func(solutionPatch, containerName string) {
			defer wg.Done()
			stepLogger.Printf("--- Processing solution '%s' in container '%s' ---", solutionPatch, containerName)
			result := make(map[string]interface{})
			var overallErrorBuilder strings.Builder

			// Compute hash of rubric_shell fields + container image_tag/image_hash
			imageTag := config.ImageTag
			imageHash := config.ImageID // If you have ImageHash, use that; else use ImageID as fallback
			hashInput := fmt.Sprintf("command:%s|counter:%s|criterion_id:%s|required:%v|score:%d|image_tag:%s|image_hash:%s",
				config.Command, config.Counter, config.CriterionID, config.Required, config.Score, imageTag, imageHash)
			hashVal := fmt.Sprintf("%x", sha256.Sum256([]byte(hashInput)))

			// Check last_run for this solution/container and file hashes
			shouldSkip := false
			if !force {
				resultsMu.Lock()
				if prevHash, ok := config.LastRun[solutionPatch]; ok && prevHash == hashVal && !filesChanged {
					shouldSkip = true
				}
				resultsMu.Unlock()
			}
			if shouldSkip {
				stepLogger.Printf("No changes detected for solution '%s' in container '%s'. Skipping execution.", solutionPatch, containerName)
				// Preserve previous results if present; do NOT update last_run_at, skipped, or reason
				resultsMu.Lock()
				if ar, ok := allResults[solutionPatch]; ok && ar != nil {
					allResults[solutionPatch] = ar
				}
				resultsMu.Unlock()
				return
			}

			// 1. Fully clean the repo status inside the container
			cleanupCmds := [][]string{
				{"docker", "exec", containerName, "git", "checkout", "--", "."},
				{"docker", "exec", containerName, "git", "clean", "-fd"},
				{"docker", "exec", containerName, "git", "reset", "--hard"},
			}
			var cleanupOutputBuilder strings.Builder
			for _, args := range cleanupCmds {
				cmd := exec.Command(args[0], args[1:]...)
				output, err := runCmd(cmd, "cleanup repo", false)
				cleanupOutputBuilder.WriteString(output + "\n")
				if err != nil {
					overallErrorBuilder.WriteString(fmt.Sprintf("cleanup error: %v; ", err))
					break // Stop cleanup on first error
				}
			}
			result["cleanup_output"] = cleanupOutputBuilder.String()

			// 1b. Execute pre_patch.patch as a shell script in the container if present and non-empty
			prePatchFile := "pre_patch.patch"
			prePatchPath := filepath.Join(stepExec.LocalPath, prePatchFile)
			info, err := os.Stat(prePatchPath)
			if err == nil && !info.IsDir() && info.Size() > 0 {
				destPrePatch := "/tmp/pre_patch.patch"
				cmdCpPrePatch := exec.Command("docker", "cp", prePatchPath, fmt.Sprintf("%s:%s", containerName, destPrePatch))
				output, err := runCmd(cmdCpPrePatch, "copy pre_patch.patch", false)
				if err != nil {
					overallErrorBuilder.WriteString(fmt.Sprintf("pre_patch copy error: %v; ", err))
					result["pre_patch_error"] = err.Error()
					result["pre_patch_output"] = output
				} else {
					cmdExecPrePatch := exec.Command("docker", "exec", containerName, "bash", "-c", "bash /tmp/pre_patch.patch")
					output, err := runCmd(cmdExecPrePatch, "execute pre_patch.patch", true)
					result["pre_patch_output"] = output
					if err != nil {
						overallErrorBuilder.WriteString(fmt.Sprintf("pre_patch exec error: %v; ", err))
						result["pre_patch_error"] = err.Error()
					}
				}
			}

			// 2. Apply solution patch if specified
			if solutionPatch != "" {
				solutionPatchPath := filepath.Join(stepExec.LocalPath, solutionPatch)
				destSolutionPath := filepath.Join("/app", solutionPatch)
				cmdCpSolution := exec.Command("docker", "cp", solutionPatchPath, fmt.Sprintf("%s:%s", containerName, destSolutionPath))
				output, err := runCmd(cmdCpSolution, "copy solution patch", false)
				if err != nil {
					overallErrorBuilder.WriteString(fmt.Sprintf("solution_patch copy error: %v; ", err))
					result["solution_patch_error"] = err.Error()
					result["solution_patch_output"] = output
				} else {
					cmdApplySolution := exec.Command("docker", "exec", containerName, "git", "apply", destSolutionPath)
					output, err := runCmd(cmdApplySolution, "apply solution patch", false)
					result["solution_patch_output"] = output
					if err != nil {
						overallErrorBuilder.WriteString(fmt.Sprintf("solution_patch apply error: %v; ", err))
						result["solution_patch_error"] = err.Error()
					}
				}
			}

			// 3. Apply held-out test patch
			heldOutTestPatch := "held_out_tests.patch" // TODO: This should be configurable
			heldOutTestPatchPath := filepath.Join(stepExec.LocalPath, heldOutTestPatch)
			destTestPath := filepath.Join("/app", heldOutTestPatch)
			cmdCpTest := exec.Command("docker", "cp", heldOutTestPatchPath, fmt.Sprintf("%s:%s", containerName, destTestPath))
			output, err := runCmd(cmdCpTest, "copy held-out test patch", false)
			if err != nil {
				overallErrorBuilder.WriteString(fmt.Sprintf("held_out_patch copy error: %v; ", err))
				result["held_out_patch_error"] = err.Error()
				result["held_out_patch_output"] = output
			} else {
				cmdApplyTest := exec.Command("docker", "exec", containerName, "git", "apply", destTestPath)
				output, err := runCmd(cmdApplyTest, "apply held-out test patch", false)
				result["held_out_patch_output"] = output
				if err != nil {
					overallErrorBuilder.WriteString(fmt.Sprintf("held_out_patch apply error: %v; ", err))
					result["held_out_patch_error"] = err.Error()
				}
			}

			// 4. Run the rubric command - always run, even if patches failed
			stepLogger.Printf("[DEBUG] Preparing to run rubric command for container '%s': config.Command=\"%s\"", containerName, config.Command)
			if config.Command == "" {
				stepLogger.Printf("[ERROR] Rubric command is empty for container '%s'.", containerName)
				result["command_error"] = "Command was empty"
			} else {
				commandLine := fmt.Sprintf("docker exec %s bash -c '%s'", containerName, escapeSingleQuotes(config.Command))
				stepLogger.Printf("[DEBUG] Executing rubric command: %s", commandLine)

				// Timeout logic
				timeoutSeconds := cfg.TimeoutSeconds
				if timeoutSeconds <= 0 {
					timeoutSeconds = 120 // default 2 min
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
				defer cancel()
				cmdRun := exec.CommandContext(ctx, "bash", "-c", commandLine)

				output, err := cmdRun.CombinedOutput()
				outputStr := string(output)
				result["command_output"] = outputStr

				// Emoji logic
				var emoji string
				// Check for TIMEOUT_MARKER in output
				const TIMEOUT_MARKER = "#__TIMEOUT__#"
				if idx := strings.Index(outputStr, TIMEOUT_MARKER); idx != -1 {
					emoji = "⌚"
					result["command_error"] = "Timed out"
					overallErrorBuilder.WriteString("command error: Timed out via marker; ")
					// Optionally extract the time after the marker
					timeInfo := strings.TrimSpace(outputStr[idx+len(TIMEOUT_MARKER):])
					if timeInfo != "" {
						// If the time is present, store it
						result["timeout_time"] = timeInfo
					}
				} else if ctx.Err() == context.DeadlineExceeded {
					emoji = "⌚"
					result["command_error"] = "Timed out"
					overallErrorBuilder.WriteString("command error: Timed out via context; ")
				} else if strings.Contains(outputStr, passMarker) {
					emoji = "✅"
				} else if strings.Contains(outputStr, failMarker) {
					emoji = "❌"
				} else if strings.Contains(strings.ToLower(outputStr), "no such file") || strings.Contains(strings.ToLower(outputStr), "not found") {
					emoji = "💀"
					result["command_error"] = "File not found"
					overallErrorBuilder.WriteString("command error: File not found; ")
				} else {
					emoji = "❓"
				}
				result["emoji"] = emoji

				if err != nil && ctx.Err() != context.DeadlineExceeded {
					result["command_error"] = err.Error()
					overallErrorBuilder.WriteString(fmt.Sprintf("command error: %v; ", err))
				}
			}

			// Set overall error string
			if overallErrorBuilder.Len() > 0 {
				result["error"] = strings.TrimSuffix(overallErrorBuilder.String(), "; ")
			}

			// Update last_run for this solution/container
			resultsMu.Lock()
			config.LastRun[solutionPatch] = hashVal
			resultsMu.Unlock()

			// Add last_run_at and set skipped to false (or remove if present)
			result["last_run_at"] = time.Now().Format(time.RFC3339)
			if _, ok := result["skipped"].(bool); ok {
				delete(result, "skipped")
			}
			resultsMu.Lock()
			allResults[solutionPatch] = result
			resultsMu.Unlock()
		}(solutionPatch, containerName)
	}
	wg.Wait()

	// Insert into rubric_shell_output_history (1 row per criterion/run, with 4 solution outputs)
	solutionOutputs := make([]string, 4)
	solutionNames := []string{"solution1.patch", "solution2.patch", "solution3.patch", "solution4.patch"}
	exceptions := make([]string, 0)
	for i, sol := range solutionNames {
		if res, ok := allResults[sol]; ok {
			if out, ok := res["command_output"].(string); ok {
				solutionOutputs[i] = out
			}
			if errStr, ok := res["error"].(string); ok && errStr != "" {
				exceptions = append(exceptions, fmt.Sprintf("%s: %s", sol, errStr))
			}
		} else {
			solutionOutputs[i] = ""
		}
	}
	moduleExplanation := ""
	// If you want to extract module_explanation from results, add logic here
	exceptionStr := strings.Join(exceptions, "; ")
	errHist := models.InsertRubricShellOutputHistory(
		db,
		config.CriterionID, // rubric_shell_uuid
		config.CriterionID, // criterion (can use same as uuid or extract from config)
		config.Required,
		float64(config.Score),
		config.Command,
		solutionOutputs[0],
		solutionOutputs[1],
		solutionOutputs[2],
		solutionOutputs[3],
		moduleExplanation,
		exceptionStr,
	)
	if errHist != nil {
		stepLogger.Printf("Failed to insert rubric_shell_output_history: %v", errHist)
	}

	// 5. Marshal and save all results
	resultsBytes, jsonErr := json.MarshalIndent(allResults, "", "  ")
	if jsonErr != nil {
		stepLogger.Printf("Failed to marshal results: %v", jsonErr)
		return jsonErr
	}

	// Persist updated settings with last_run
	wrappedSettings.RubricShell = config
	settingsBytes, err := json.MarshalIndent(wrappedSettings, "", "  ")
	if err != nil {
		stepLogger.Printf("Failed to marshal updated settings: %v", err)
		return err
	}
	_, errUpdate := db.Exec("UPDATE steps SET results = $1, settings = $2 WHERE id = $3", string(resultsBytes), string(settingsBytes), stepExec.StepID)
	if errUpdate != nil {
		stepLogger.Printf("Failed to update step results/settings: %v", errUpdate)
		return errUpdate
	}

	// --- WebSocket update logic ---
	// Send only the latest result for this rubric shell step/criterion
	latestPayload := map[string]interface{}{
		"step_id":      stepExec.StepID,
		"criterion_id": config.CriterionID,
		"result":       allResults, // You may want to filter to just the latest solutionPatch if desired
	}
	payloadBytes, err := json.Marshal(latestPayload)
	if err != nil {
		stepLogger.Printf("Failed to marshal websocket update payload: %v", err)
	} else {
		if wsErr := InsertWebsocketUpdate(db, "rubric_shell", &stepExec.TaskID, &stepExec.StepID, string(payloadBytes)); wsErr != nil {
			stepLogger.Printf("Failed to insert websocket update: %v", wsErr)
		}
	}
	// --- end WebSocket update logic ---

	numTotal = len(allResults)
	numSuccess = 0
	for _, result := range allResults {
		if emoji, ok := result["emoji"].(string); ok && emoji == "✅" {
			numSuccess++
		}
	}
	percent := 0
	if numTotal > 0 {
		percent = numSuccess * 100 / numTotal
	}
	stepLogger.Printf("Rubric shell step finished for criterion ID %s. SUCCESS: %d/%d (%d%%)", config.CriterionID, numSuccess, numTotal, percent)
	return nil
}

// escapeSingleQuotes escapes single quotes for safe use inside bash -c '<cmd>'
func escapeSingleQuotes(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
