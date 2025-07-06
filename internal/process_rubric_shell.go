package internal

import (
	"crypto/sha256"
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
// It fetches the latest container assignments from the task settings, then for each assigned container,
// it applies the relevant patches and runs the test command, capturing the results.
func ProcessRubricShellStep(db *sql.DB, stepExec *models.StepExec, stepLogger *log.Logger) error {
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

	allResults := make(map[string]map[string]string)
	var finalErr error

	// Helper to run a command and log its execution and output.
	runCmd := func(cmd *exec.Cmd, description string, logOutput bool) (string, error) {
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

	// For hashing (import is already at the top of the file)

	for solutionPatch, containerName := range taskSettings.AssignContainers {
		stepLogger.Printf("--- Processing solution '%s' in container '%s' ---", solutionPatch, containerName)
		result := make(map[string]string)
		var currentRunError error

		// Fetch container_id and image_id for this container (stub: use containerName as id, could be improved)
		containerID := containerName
		imageID := ""
		// TODO: Optionally fetch real imageID if available (for now, leave as empty string or fetch if possible)

		// Compute hash of tracked fields
		hashInput := fmt.Sprintf("command:%s|counter:%d|criterion_id:%s|required:%v|score:%f|container_id:%s|image_id:%s",
			config.Command, config.Counter, config.CriterionID, config.Required, config.Score, containerID, imageID)
		hashVal := fmt.Sprintf("%x", sha256.Sum256([]byte(hashInput)))

		// Check last_run for this solution/container
		if prevHash, ok := config.LastRun[solutionPatch]; ok && prevHash == hashVal {
			stepLogger.Printf("No changes detected for solution '%s' in container '%s'. Skipping execution.", solutionPatch, containerName)
			allResults[solutionPatch] = map[string]string{"skipped": "true", "reason": "last_run hash matched"}
			continue
		}

		// 1. Fully clean the repo status inside the container
		cleanupCmds := [][]string{
			{"docker", "exec", containerName, "git", "checkout", "--", "."},
			{"docker", "exec", containerName, "git", "clean", "-fd"},
			{"docker", "exec", containerName, "git", "reset", "--hard"},
		}
		for _, args := range cleanupCmds {
			cmd := exec.Command(args[0], args[1:]...)
			if output, err := runCmd(cmd, "cleanup repo", false); err != nil {
				currentRunError = err
				result["error"] = err.Error()
				result["output"] = output
				break
			}
		}

		// 2. Apply solution patch if specified
		if currentRunError == nil && solutionPatch != "" {
			solutionPatchPath := filepath.Join(stepExec.LocalPath, solutionPatch)
			destSolutionPath := filepath.Join("/app", solutionPatch)
			cmdCpSolution := exec.Command("docker", "cp", solutionPatchPath, fmt.Sprintf("%s:%s", containerName, destSolutionPath))
			if output, err := runCmd(cmdCpSolution, "copy solution patch", false); err != nil {
				currentRunError = err
				result["error"] = err.Error()
				result["output"] = output
			} else {
				cmdApplySolution := exec.Command("docker", "exec", containerName, "git", "apply", destSolutionPath)
				if output, err := runCmd(cmdApplySolution, "apply solution patch", false); err != nil {
					currentRunError = err
					result["error"] = err.Error()
					result["output"] = output
				}
			}
		}

		// 3. Apply held-out test patch
		if currentRunError == nil {
			heldOutTestPatch := "held_out_tests.patch" // TODO: This should be configurable
			heldOutTestPatchPath := filepath.Join(stepExec.LocalPath, heldOutTestPatch)
			destTestPath := filepath.Join("/app", heldOutTestPatch)
			cmdCpTest := exec.Command("docker", "cp", heldOutTestPatchPath, fmt.Sprintf("%s:%s", containerName, destTestPath))
			if output, err := runCmd(cmdCpTest, "copy held-out test patch", false); err != nil {
				currentRunError = err
				result["error"] = err.Error()
				result["output"] = output
			} else {
				cmdApplyTest := exec.Command("docker", "exec", containerName, "git", "apply", destTestPath)
				if output, err := runCmd(cmdApplyTest, "apply held-out test patch", false); err != nil {
					currentRunError = err
					result["error"] = err.Error()
					result["output"] = output
				}
			}
		}

		// 4. Run the rubric command
		if currentRunError == nil {
			var commandOutputBuilder strings.Builder
			commandLine := fmt.Sprintf("docker exec %s %s", containerName, config.Command)
			cmdRun := exec.Command("sh", "-c", commandLine)
			output, err := runCmd(cmdRun, "run rubric command", true)
			commandOutputBuilder.WriteString(output)

			result["output"] = commandOutputBuilder.String()
			if err != nil {
				currentRunError = err
				result["error"] = err.Error()
			}
		}

		// Update last_run for this solution/container
		config.LastRun[solutionPatch] = hashVal

		allResults[solutionPatch] = result
		if currentRunError != nil && finalErr == nil {
			finalErr = currentRunError // Capture the first error
		}
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

	stepLogger.Printf("Rubric shell step finished for criterion ID %s. Overall status: %v", config.CriterionID, finalErr)
	return finalErr
}
