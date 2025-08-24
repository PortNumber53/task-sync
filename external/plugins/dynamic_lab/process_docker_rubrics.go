package dynamic_lab

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// execCommand is a package-level variable that can be overridden in tests
var execCommand = exec.Command

// getDockerImageID retrieves the full image ID (SHA256 digest) for a given Docker image tag.
func getDockerImageID(tag string) (string, error) {
	if tag == "" {
		return "", fmt.Errorf("empty image tag provided")
	}

	// First, try to get the image ID using docker inspect
	cmd := execCommand("docker", "inspect", "-f", "{{.Id}}", tag)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return strings.TrimSpace(out.String()), nil
	}

	// If that failed and the tag doesn't contain a colon, try appending :latest
	if !strings.Contains(tag, ":") {
		latestTag := tag + ":latest"
		cmd = execCommand("docker", "inspect", "-f", "{{.Id}}", latestTag)
		out.Reset()
		stderr.Reset()
		cmd.Stdout = &out
		cmd.Stderr = &stderr

		if err := cmd.Run(); err == nil {
			return strings.TrimSpace(out.String()), nil
		}
	}

	return "", fmt.Errorf("failed to get image ID for tag %s: %v, stderr: %s", tag, err, stderr.String())
}

// calculateFileHash calculates the SHA256 hash of a file
func calculateFileHash(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", fmt.Errorf("failed to calculate hash: %w", err)
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// processDockerRubricsSteps processes docker rubrics steps for active tasks
func processDockerRubricsSteps(db *sql.DB) {
	query := `SELECT s.id, s.task_id, s.settings, COALESCE(t.local_path, '') AS base_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE t.status = 'active'
		AND t.local_path IS NOT NULL
		AND t.local_path <> ''
		AND s.settings::text LIKE '%docker_rubrics%'`

	rows, err := db.Query(query)
	if err != nil {
		models.StepLogger.Println("Docker rubrics query error:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var step models.StepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.BasePath); err != nil {
			models.StepLogger.Println("Row scan error:", err)
			continue
		}

		var config models.DockerRubricsConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "invalid docker rubrics config"})
			models.StepLogger.Printf("Step %d: invalid docker rubrics config: %v\n", step.StepID, err)
			continue
		}

		ok, err := models.CheckDependencies(db, &step)
		if err != nil {
			models.StepLogger.Printf("Step %d: error checking dependencies: %v\n", step.StepID, err)
			continue
		}
		if !ok {
			models.StepLogger.Printf("Step %d: waiting for dependencies to complete\n", step.StepID)
			continue
		}

		// --- Start of new Image ID logic ---
		var imageIDToUse string
		var buildStepImageID string

		// 1. Try to get image_id from a docker_build dependency
		for _, dep := range config.DockerRubrics.DependsOn {
			depInfo, err := models.GetStepInfo(db, dep.ID)
			if err != nil {
				models.StepLogger.Printf("Step %d: Error getting info for step %d: %v\n", step.StepID, dep.ID, err)
				continue
			}
			var depConfig models.StepConfigHolder
			if err := json.Unmarshal([]byte(depInfo), &depConfig); err != nil {
				models.StepLogger.Printf("Step %d: Error unmarshaling dependency config for step %d: %v\n", step.StepID, dep.ID, err)
				continue
			}
			if depConfig.DockerBuild != nil && depConfig.DockerBuild.ImageID != "" {
				buildStepImageID = depConfig.DockerBuild.ImageID
				models.StepLogger.Printf("Step %d: Found image_id '%s' from build step %d\n", step.StepID, buildStepImageID, dep.ID)
				break
			}
		}

		// 2. Decide which image ID to use
		if buildStepImageID != "" {
			imageIDToUse = buildStepImageID
			models.StepLogger.Printf("Step %d: Prioritizing image_id '%s' from build step.\n", step.StepID, imageIDToUse)
		} else {
			models.StepLogger.Printf("Step %d: No build step with a valid image_id found. Falling back to inspecting tag '%s'.\n", step.StepID, config.DockerRubrics.ImageTag)
			currentImageID, err := getDockerImageID(config.DockerRubrics.ImageTag)
			if err != nil {
				models.StepLogger.Printf("Step %d: error getting current image ID by tag: %v\n", step.StepID, err)
				continue
			}
			if currentImageID == "" {
				models.StepLogger.Printf("Step %d: no image found with tag %s\n", step.StepID, config.DockerRubrics.ImageTag)
				continue
			}
			imageIDToUse = currentImageID
		}

		// 3. Check if the determined image ID is different from the one in settings. If so, update and skip.
		if config.DockerRubrics.ImageID != imageIDToUse {
			models.StepLogger.Printf("Step %d: Stored image_id ('%s') is outdated. Updating to '%s' and skipping this run.\n", step.StepID, config.DockerRubrics.ImageID, imageIDToUse)
			config.DockerRubrics.ImageID = imageIDToUse
			updatedSettings, _ := json.Marshal(config)
			_, err := db.Exec(`UPDATE steps SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettings), step.StepID)
			if err != nil {
				models.StepLogger.Printf("Step %d: Failed to update settings with new image_id: %v\n", step.StepID, err)
			}
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "pending", "message": "Image ID updated from build step, will run next cycle."})
			continue // Skip to next step, will run correctly on next execution cycle
		}
		// --- End of new Image ID logic ---

		// Check if files have changed
		shouldRun := false
		for _, file := range config.DockerRubrics.Files {
			filePath := filepath.Join(step.BasePath, file)

			if _, err := os.Stat(filePath); os.IsNotExist(err) {
				models.StepLogger.Printf("Step %d: file not found: %s\n", step.StepID, filePath)
				if !strings.HasSuffix(file, "TASK_DATA.md") {
					models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "required file not found: " + file})
					continue
				}
				shouldRun = true
				continue
			}

			currentHash, err := calculateFileHash(filePath)
			if err != nil {
				models.StepLogger.Printf("Step %d: error calculating hash for %s: %v\n", step.StepID, file, err)
				continue
			}

			storedHash, hasHash := config.DockerRubrics.Hashes[file]
			if !hasHash || storedHash != currentHash {
				if config.DockerRubrics.Hashes == nil {
					config.DockerRubrics.Hashes = make(map[string]string)
				}
				config.DockerRubrics.Hashes[file] = currentHash
				shouldRun = true
			}
		}

		if !shouldRun {
			models.StepLogger.Printf("Step %d: no file changes detected, skipping rubrics evaluation.\n", step.StepID)
			// We can't just succeed here, because the container might not be running.
			// The logic to check for a running container with the correct image hash should be here.
			// For now, we assume if no files changed and ID is set, it's fine.
			continue
		}

		// Process the TASK_DATA.md file and determine step outcome
		var requiredCommandFailed bool
		var finalMessage, finalOutput string

	CommandProcessingLoop:
		for _, file := range config.DockerRubrics.Files {
			if strings.HasSuffix(file, "TASK_DATA.md") {
				filePath := filepath.Join(step.BasePath, file)
				content, err := os.ReadFile(filePath)
				if err != nil {
					models.StepLogger.Printf("Step %d: error reading file %s: %v\n", step.StepID, file, err)
					continue
				}

				lines := strings.Split(string(content), "\n")
				for i := 0; i < len(lines); i++ {
					line := strings.TrimSpace(lines[i])
					if line == "" {
						continue
					}

					parts := strings.Fields(line)
					if len(parts) < 2 {
						continue
					}

					if _, err := strconv.Atoi(parts[0]); err != nil {
						continue
					}

					required := len(parts) > 1 && parts[1] == "[x]"
					command := strings.TrimSpace(strings.TrimPrefix(line, parts[0]))
					if required {
						command = strings.TrimSpace(strings.TrimPrefix(command, "[x]"))
					} else {
						command = strings.TrimSpace(strings.TrimPrefix(command, "[ ]"))
					}

					cmd := execCommand("docker", "run", "--rm", imageIDToUse, "sh", "-c", command)
					output, err := cmd.CombinedOutput()

					if err != nil {
						models.StepLogger.Printf("Step %d: command '%s' failed: %v\nOutput: %s\n", step.StepID, command, err, string(output))
						if required {
							requiredCommandFailed = true
							finalMessage = "required command failed: " + command
							finalOutput = string(output)
							break CommandProcessingLoop
						}
					} else {
						models.StepLogger.Printf("Step %d: command '%s' succeeded\nOutput: %s\n", step.StepID, command, string(output))
					}
				}
			}
		}

		if requiredCommandFailed {
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": finalMessage, "output": finalOutput})
		} else {
			updatedSettings, err := json.Marshal(config)
			if err != nil {
				models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "failed to marshal settings on success"})
				models.StepLogger.Printf("Step %d: Failed to marshal settings on success: %v\n", step.StepID, err)
				continue
			}
			_, err = db.Exec(`UPDATE steps SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettings), step.StepID)
			if err != nil {
				models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "failed to update settings on success"})
				models.StepLogger.Printf("Step %d: Failed to update settings on success: %v\n", step.StepID, err)
			} else {
				models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "success", "message": "All rubrics passed."})
			}
		}

	}
}
