package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// processDockerShellSteps processes docker shell steps for active tasks.
func processDockerShellSteps(db *sql.DB) {
	query := `SELECT s.id, s.task_id, s.settings
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.status = 'active'
		AND t.status = 'active'
		AND s.settings::text LIKE '%docker_shell%'`

	rows, err := db.Query(query)
	if err != nil {
		stepLogger.Println("Docker shell query error:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var stepID, taskID int
		var settings string
		if err := rows.Scan(&stepID, &taskID, &settings); err != nil {
			stepLogger.Println("Row scan error:", err)
			continue
		}

		var config DockerShellConfig
		if err := json.Unmarshal([]byte(settings), &config); err != nil {
			StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": "invalid docker shell config"})
			stepLogger.Printf("Step %d: invalid docker shell config: %v\n", stepID, err)
			continue
		}

		ok, err := checkDependencies(db, stepID, config.DockerShell.DependsOn)
		if err != nil {
			stepLogger.Printf("Step %d: error checking dependencies: %v\n", stepID, err)
			continue
		}
		if !ok {
			stepLogger.Printf("Step %d: waiting for dependencies to complete\n", stepID)
			continue
		}

		// Check if the container is running
		containerID := config.DockerShell.Docker.ContainerID
		inspectCmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", containerID)
		output, err := inspectCmd.CombinedOutput()
		if err != nil {
			msg := fmt.Sprintf("failed to inspect container %s: %v. Output: %s", containerID, err, string(output))
			StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": msg})
			stepLogger.Printf("Step %d: %s\n", stepID, msg)
			db.Exec("UPDATE steps SET status = 'error', updated_at = NOW() WHERE id = $1", stepID)
			continue
		}

		if !strings.Contains(string(output), "true") {
			msg := fmt.Sprintf("container %s is not running", containerID)
			StoreStepResult(db, stepID, map[string]interface{}{"result": "failure", "message": msg})
			stepLogger.Printf("Step %d: %s\n", stepID, msg)
			db.Exec("UPDATE steps SET status = 'error', updated_at = NOW() WHERE id = $1", stepID)
			continue
		}

		// Container is running, execute commands
		var results []map[string]string
		var commandErrors []string

		for _, cmdMap := range config.DockerShell.Command {
			for label, command := range cmdMap {
				stepLogger.Printf("Step %d: executing command for label '%s': %s\n", stepID, label, command)
				// Note: Using `sh -c` to handle more complex commands properly
				execCmd := exec.Command("docker", "exec", containerID, "sh", "-c", command)
				cmdOutput, err := execCmd.CombinedOutput()

				if err != nil {
					errorMsg := fmt.Sprintf("failed to execute command '%s': %v. Output: %s", command, err, string(cmdOutput))
					stepLogger.Printf("Step %d: %s\n", stepID, errorMsg)
					commandErrors = append(commandErrors, errorMsg)
					// Store partial failure if needed, or just collect errors
					results = append(results, map[string]string{
						"label":  label,
						"output": "",
						"error":  errorMsg,
					})
				} else {
					outputStr := strings.TrimSpace(string(cmdOutput))
					stepLogger.Printf("Step %d: command for label '%s' succeeded. Output: %s\n", stepID, label, outputStr)
					results = append(results, map[string]string{
						"label":  label,
						"output": outputStr,
						"error":  "",
					})
				}
			}
		}

		if len(commandErrors) > 0 {
			// Some or all commands failed
			StoreStepResult(db, stepID, map[string]interface{}{
				"result":  "failure",
				"message": "one or more shell commands failed",
				"outputs": results,
			})
			db.Exec("UPDATE steps SET status = 'error', updated_at = NOW() WHERE id = $1", stepID)
		} else {
			// All commands succeeded
			StoreStepResult(db, stepID, map[string]interface{}{
				"result":  "success",
				"message": "all shell commands executed successfully",
				"outputs": results,
			})
			db.Exec("UPDATE steps SET status = 'success', updated_at = NOW() WHERE id = $1", stepID)
		}
	}
}
