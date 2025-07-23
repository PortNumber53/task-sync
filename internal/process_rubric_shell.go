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
	"sync"

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

	// Helper to run a command and log its execution and output.
	runCmd := func(cmd *exec.Cmd, description string, logOutput bool) (string, error) {
		stepLogger.Printf("Executing: %s", cmd.String())
		output, err := cmd.CombinedOutput()
		if err != nil {
			stepLogger.Printf("Error %s: %v\nOutput:\n%s", description, err, string(output))
			return string(output), err
		}
		if logOutput {
			stepLogger.Printf("Success: %s\nOutput:\n%s", description, string(output))
		}
		return string(output), nil
	}

	var (
		wg        sync.WaitGroup
		resultsMu sync.Mutex
	)
	// Create a slice of solution patches to have a deterministic order and for debugging
	solutionPatches := make([]string, 0, len(taskSettings.AssignContainers))
	for sp := range taskSettings.AssignContainers {
		solutionPatches = append(solutionPatches, sp)
	}

	for i, solutionPatch := range solutionPatches {
		if i > 0 { // Process only the first solution for debugging
			stepLogger.Printf("DEBUG: Skipping solution patch %s due to debug limiter", solutionPatch)
			continue
		}
		containerName := taskSettings.AssignContainers[solutionPatch]
		wg.Add(1)
		go func(solutionPatch, containerName string) {
			defer wg.Done()
			stepLogger.Printf("--- Processing solution '%s' in container '%s' ---", solutionPatch, containerName)

			// Ensure a single cleanup block before patch application
			cleanupCmds := [][]string{
				{"docker", "exec", containerName, "git", "apply", "-R", "--ignore-whitespace", "/app/held_out_tests.patch"},
				{"docker", "exec", containerName, "git", "reset", "--hard", "HEAD"},
				{"docker", "exec", containerName, "git", "clean", "-fdx"},
			}
			for _, c := range cleanupCmds {
				cmd := exec.Command(c[0], c[1:]...)
				// We can ignore errors here as the repo might not have the patch applied
				runCmd(cmd, "cleanup repo", false)
			}

			result := make(map[string]interface{})
			var overallErrorBuilder strings.Builder

			// Apply pre_patch.patch if it exists
			if prePatchPath, ok := config.Files["pre_patch.patch"]; ok {
				fullPrePatchPath := filepath.Join("/home/grimlock/go/task-sync/", prePatchPath)
				tmpPrePatchPath := "/tmp/pre_patch.patch"
				cmd := exec.Command("docker", "cp", fullPrePatchPath, fmt.Sprintf("%s:%s", containerName, tmpPrePatchPath))
				if _, err := runCmd(cmd, "copy pre_patch.patch", false); err != nil {
					overallErrorBuilder.WriteString(fmt.Sprintf("copy pre_patch.patch failed: %v; ", err))
				} else {
					cmd = exec.Command("docker", "exec", containerName, "bash", "-c", "bash "+tmpPrePatchPath)
					if _, err := runCmd(cmd, "execute pre_patch.patch", true); err != nil {
						overallErrorBuilder.WriteString(fmt.Sprintf("execute pre_patch.patch failed: %v; ", err))
					}
				}
			}

			// *** DIAGNOSTIC COMMAND ***
			stepLogger.Println("--- DIAGNOSTIC: Listing files after pre_patch.patch ---")
			cmd := exec.Command("docker", "exec", containerName, "ls", "-lR", "/app/")
			runCmd(cmd, "diagnostic list files", true)

			// 2. Apply solution patch
			solutionPatchPath, ok := config.Files[solutionPatch]
			if !ok {
				stepLogger.Printf("Solution patch '%s' not found in triggers.files", solutionPatch)
				return
			}
			fullSolutionPatchPath := filepath.Join("/home/grimlock/go/task-sync/", solutionPatchPath)
			containerPatchPath := "/app/" + solutionPatch
			cmd = exec.Command("docker", "cp", fullSolutionPatchPath, fmt.Sprintf("%s:%s", containerName, containerPatchPath))
			if _, err := runCmd(cmd, "copy solution patch", false); err != nil {
				overallErrorBuilder.WriteString(fmt.Sprintf("copy solution patch failed: %v; ", err))
			} else {
				cmd = exec.Command("docker", "exec", containerName, "git", "apply", containerPatchPath)
				if _, err := runCmd(cmd, "apply solution patch", true); err != nil {
					overallErrorBuilder.WriteString(fmt.Sprintf("apply solution patch failed: %v; ", err))
				}
			}

			// 3. Apply held-out tests patch
			heldOutTestsPatchPath, ok := config.Files["held_out_tests.patch"]
			if !ok {
				stepLogger.Println("held_out_tests.patch not found in triggers.files")
				return
			}
			fullHeldOutTestsPatchPath := filepath.Join("/home/grimlock/go/task-sync/", heldOutTestsPatchPath)
			containerHeldOutTestsPatchPath := "/app/held_out_tests.patch"
			cmd = exec.Command("docker", "cp", fullHeldOutTestsPatchPath, fmt.Sprintf("%s:%s", containerName, containerHeldOutTestsPatchPath))
			if _, err := runCmd(cmd, fmt.Sprintf("copy held-out test patch for %s", containerName), false); err != nil {
				overallErrorBuilder.WriteString(fmt.Sprintf("copy held-out tests patch failed: %v; ", err))
			} else {
				cmd = exec.Command("docker", "exec", containerName, "git", "apply", containerHeldOutTestsPatchPath)
				if _, err := runCmd(cmd, "apply held-out test patch", true); err != nil {
					overallErrorBuilder.WriteString(fmt.Sprintf("apply held-out tests patch failed: %v; ", err))
				}
			}

			// 4. Run the test command
			stepLogger.Printf("[DEBUG] Preparing to run rubric command for container '%s': config.Command=\"%s\"", containerName, config.Command)
			fullCmd := escapeSingleQuotes(config.Command)
			cmd = exec.Command("docker", "exec", containerName, "bash", "-c", fullCmd)
			stepLogger.Printf("[DEBUG] Executing rubric command: %s", cmd.String())
			output, err := cmd.CombinedOutput()
			result["command_output"] = string(output)
			if err != nil {
				stepLogger.Printf("Error executing rubric command: %v\nOutput: %s", err, string(output))
				overallErrorBuilder.WriteString(fmt.Sprintf("rubric command failed: %v", err))
				result["emoji"] = "❌"
			} else if strings.Contains(string(output), passMarker) {
				stepLogger.Printf("Rubric command PASSED for container %s", containerName)
				result["emoji"] = "✅"
			} else {
				stepLogger.Printf("Rubric command FAILED for container %s (pass/fail marker not found)", containerName)
				result["emoji"] = "❌"
			}

			// Set overall error string
			if overallErrorBuilder.Len() > 0 {
				result["error"] = strings.TrimSuffix(overallErrorBuilder.String(), "; ")
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

	var numTotal int
	var numSuccess int
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
