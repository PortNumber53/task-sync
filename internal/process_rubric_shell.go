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
// It iterates through a set of solution patches, applies each one in its assigned container,
// runs the held-out test command, and captures the results.
func ProcessRubricShellStep(db *sql.DB, stepExec *models.StepExec, stepLogger *log.Logger) error {
	var wrappedSettings struct {
		RubricShell models.RubricShellConfig `json:"rubric_shell"`
	}

	if err := json.Unmarshal([]byte(stepExec.Settings), &wrappedSettings); err != nil {
		return fmt.Errorf("failed to unmarshal rubric_shell settings: %w", err)
	}
	config := wrappedSettings.RubricShell

	if len(config.AssignContainers) == 0 {
		return fmt.Errorf("no assigned containers for rubric_shell step %d", stepExec.StepID)
	}

	// TODO: This should be passed in from the dynamic_rubric step.
	heldOutTestPatch := "held_out_tests.patch"
	allResults := make(map[string]map[string]string)
	var overallErr error

	for solutionPatch, containerName := range config.AssignContainers {
		stepLogger.Printf("--- Processing solution '%s' in container '%s' ---", solutionPatch, containerName)
		solutionResult := make(map[string]string)

		// Helper to run a command and log/store results
		runCmd := func(cmd *exec.Cmd, description string) (string, error) {
			stepLogger.Printf("Executing: %s", cmd.String())
			output, err := cmd.CombinedOutput()
			if err != nil {
				stepLogger.Printf("Error %s: %v, Output: %s", description, err, string(output))
				return string(output), fmt.Errorf("failed to %s: %w", description, err)
			}
			stepLogger.Printf("Success: %s", description)
			return string(output), nil
		}

		// 1. Reset the repo status inside the container
		cmdReset := exec.Command("docker", "exec", containerName, "git", "reset", "--hard")
		if output, err := runCmd(cmdReset, "reset repo"); err != nil {
			solutionResult["error"] = err.Error()
			solutionResult["output"] = output
			allResults[solutionPatch] = solutionResult
			overallErr = err // Store first error
			continue        // Move to next solution
		}

		// 2. Apply solution patch
		solutionPatchPath := filepath.Join(stepExec.LocalPath, solutionPatch)
		destSolutionPath := filepath.Join("/app", solutionPatch)
		cmdCpSolution := exec.Command("docker", "cp", solutionPatchPath, fmt.Sprintf("%s:%s", containerName, destSolutionPath))
		if output, err := runCmd(cmdCpSolution, "copy solution patch"); err != nil {
			solutionResult["error"] = err.Error()
			solutionResult["output"] = output
			allResults[solutionPatch] = solutionResult
			if overallErr == nil {
				overallErr = err
			}
			continue
		}

		cmdApplySolution := exec.Command("docker", "exec", containerName, "git", "apply", destSolutionPath)
		if output, err := runCmd(cmdApplySolution, "apply solution patch"); err != nil {
			solutionResult["error"] = err.Error()
			solutionResult["output"] = output
			allResults[solutionPatch] = solutionResult
			if overallErr == nil {
				overallErr = err
			}
			continue
		}

		// 3. Apply held-out test patch
		heldOutTestPatchPath := filepath.Join(stepExec.LocalPath, heldOutTestPatch)
		destTestPath := filepath.Join("/app", heldOutTestPatch)
		cmdCpTest := exec.Command("docker", "cp", heldOutTestPatchPath, fmt.Sprintf("%s:%s", containerName, destTestPath))
		if output, err := runCmd(cmdCpTest, "copy held-out test patch"); err != nil {
			solutionResult["error"] = err.Error()
			solutionResult["output"] = output
			allResults[solutionPatch] = solutionResult
			if overallErr == nil {
				overallErr = err
			}
			continue
		}

		cmdApplyTest := exec.Command("docker", "exec", containerName, "git", "apply", destTestPath)
		if output, err := runCmd(cmdApplyTest, "apply held-out test patch"); err != nil {
			solutionResult["error"] = err.Error()
			solutionResult["output"] = output
			allResults[solutionPatch] = solutionResult
			if overallErr == nil {
				overallErr = err
			}
			continue
		}

		// 4. Run the rubric command
		commandLine := fmt.Sprintf("docker exec -w /app/ansible %s %s", containerName, config.Command)
		cmdRun := exec.Command("sh", "-c", commandLine)
		output, err := runCmd(cmdRun, "run rubric command")

		solutionResult["output"] = output
		if err != nil {
			solutionResult["error"] = err.Error()
			if overallErr == nil {
				overallErr = err
			}
		}
		allResults[solutionPatch] = solutionResult
	}

	// 5. Marshal and save the aggregated results
	stepLogger.Printf("--- Aggregated Results ---")
	resultsBytes, jsonErr := json.MarshalIndent(allResults, "", "  ")
	if jsonErr != nil {
		stepLogger.Printf("Failed to marshal results: %v", jsonErr)
		return jsonErr
	}

	stepLogger.Printf("Results:\n%s", string(resultsBytes))

	_, errUpdate := db.Exec("UPDATE steps SET results = $1 WHERE id = $2", string(resultsBytes), stepExec.StepID)
	if errUpdate != nil {
		stepLogger.Printf("Failed to update step results: %v", errUpdate)
		return errUpdate
	}

	stepLogger.Printf("Rubric shell step executed for criterion ID %s", config.CriterionID)
	return overallErr // Return the first error encountered, or nil if all successful
}
