package internal

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
	"time"
)

// CreateStep inserts a new step for a task. taskRef can be the task id or name. Settings must be a valid JSON string.
func CreateStep(taskRef, title, settings string) error {
	pgURL, err := GetPgURLFromEnv()
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return err
	}
	defer db.Close()

	// Try to parse settings as JSON
	var js interface{}
	if err := json.Unmarshal([]byte(settings), &js); err != nil {
		return fmt.Errorf("settings must be valid JSON: %w", err)
	}

	// Find task_id
	var taskID int
	if id, err := strconv.Atoi(taskRef); err == nil {
		err = db.QueryRow("SELECT id FROM tasks WHERE id = $1", id).Scan(&taskID)
		if err != nil {
			return fmt.Errorf("no task found with id %d", id)
		}
	} else {
		err = db.QueryRow("SELECT id FROM tasks WHERE name = $1", taskRef).Scan(&taskID)
		if err != nil {
			return fmt.Errorf("no task found with name '%s'", taskRef)
		}
	}

	_, err = db.Exec(`INSERT INTO steps (task_id, title, status, settings, created_at, updated_at) VALUES ($1, $2, 'new', $3::jsonb, now(), now())`, taskID, title, settings)
	return err
}

// ActivateStep sets the status of a step to 'active'
func ActivateStep(stepID int) error {
	pgURL, err := GetPgURLFromEnv()
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return err
	}
	defer db.Close()

	result, err := db.Exec(`UPDATE steps SET status = 'active', updated_at = NOW() WHERE id = $1`, stepID)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return fmt.Errorf("no step found with ID %d", stepID)
	}
	return nil
}

// ListSteps prints all steps in the DB. If full is true, prints settings column too.
func ListSteps(full bool) error {
	pgURL, err := GetPgURLFromEnv()
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return err
	}
	defer db.Close()
	var rows *sql.Rows
	if full {
		rows, err = db.Query(`SELECT id, task_id, title, status, settings, created_at, updated_at FROM steps ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		fmt.Printf("%-4s %-7s %-20s %-10s %-30s %-25s %-25s\n", "ID", "TaskID", "Title", "Status", "Settings", "Created At", "Updated At")
		for rows.Next() {
			var id, taskID int
			var title, status, settings, createdAt, updatedAt string
			if err := rows.Scan(&id, &taskID, &title, &status, &settings, &createdAt, &updatedAt); err != nil {
				return err
			}
			fmt.Printf("%-4d %-7d %-20s %-10s %-30s %-25s %-25s\n", id, taskID, title, status, settings, createdAt, updatedAt)
		}
	} else {
		rows, err = db.Query(`SELECT id, task_id, title, status, created_at, updated_at FROM steps ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		fmt.Printf("%-4s %-7s %-20s %-10s %-25s %-25s\n", "ID", "TaskID", "Title", "Status", "Created At", "Updated At")
		for rows.Next() {
			var id, taskID int
			var title, status, createdAt, updatedAt string
			if err := rows.Scan(&id, &taskID, &title, &status, &createdAt, &updatedAt); err != nil {
				return err
			}
			fmt.Printf("%-4d %-7d %-20s %-10s %-25s %-25s\n", id, taskID, title, status, createdAt, updatedAt)
		}
	}
	return nil
}

// DockerBuildConfig represents the configuration for a docker build step
type DockerBuildConfig struct {
	DockerBuild struct {
		DependsOn []struct {
			ID int `json:"id"`
		} `json:"depends_on"`
		Files    []string          `json:"files"`
		Hashes   map[string]string `json:"hashes"`
		Shell    []string          `json:"shell"`
		ImageID  string            `json:"image_id"`
		ImageTag string            `json:"image_tag"`
	} `json:"docker_build"`
}

// DockerRubricsConfig represents the configuration for a docker rubrics step
type DockerRubricsConfig struct {
	DockerRubrics struct {
		DependsOn []struct {
			ID int `json:"id"`
		} `json:"depends_on"`
		Files    []string          `json:"files"`
		Hashes   map[string]string `json:"hashes"`
		ImageID  string            `json:"image_id"`
		ImageTag string            `json:"image_tag"`
	} `json:"docker_rubrics"`
}

// calculateFileHash calculates the SHA256 hash of a file
func calculateFileHash(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// checkDependencies verifies if all dependent steps have completed successfully
func checkDependencies(db *sql.DB, stepID int, dependsOn []struct {
	ID int `json:"id"`
}) (bool, error) {
	if len(dependsOn) == 0 {
		return true, nil
	}

	// Log the dependencies we're checking
	depIDs := make([]int, len(dependsOn))
	for i, dep := range dependsOn {
		depIDs[i] = dep.ID
	}
	stepLogger.Printf("Step %d: checking dependencies: %v\n", stepID, depIDs)

	placeholders := make([]string, len(dependsOn))
	args := make([]interface{}, len(dependsOn)+1)
	args[0] = stepID

	for i, dep := range dependsOn {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args[i+1] = dep.ID
	}

	// First, let's check the status of each dependency directly
	for _, dep := range dependsOn {
		var status string
		var results sql.NullString
		err := db.QueryRow("SELECT status, results FROM steps WHERE id = $1", dep.ID).Scan(&status, &results)
		if err != nil {
			stepLogger.Printf("Step %d: error checking status of dependency %d: %v\n", stepID, dep.ID, err)
			continue
		}
		stepLogger.Printf("Step %d: dependency %d - status: %s, results: %v\n", stepID, dep.ID, status, results.String)
	}

	// We need to find if there are any dependencies that are NOT successful
	// A dependency is successful if:
	// 1. status is 'success' OR
	// 2. results->>'result' is 'success'
	query := fmt.Sprintf(`
		SELECT NOT EXISTS (
			SELECT 1 FROM steps
			WHERE id IN (%s)
			AND id != $1
			AND status != 'success'
			AND (results IS NULL OR results->>'result' IS NULL OR results->>'result' != 'success')
		)`,
		strings.Join(placeholders, ","))

	stepLogger.Printf("Step %d: running dependency check query: %s with args %v\n", stepID, query, args)

	var allDepsCompleted bool
	err := db.QueryRow(query, args...).Scan(&allDepsCompleted)
	stepLogger.Printf("Step %d: dependency check result: %v, error: %v\n", stepID, allDepsCompleted, err)

	return allDepsCompleted, err
}

// executeDockerBuild executes the docker build command and captures the image ID
func executeDockerBuild(workDir string, config DockerBuildConfig, stepID int, db *sql.DB) error {
	// Replace image tag placeholder in shell command
	cmdParts := make([]string, len(config.DockerBuild.Shell))
	for i, part := range config.DockerBuild.Shell {
		cmdParts[i] = strings.ReplaceAll(part, "%%IMAGE_TAG%%", config.DockerBuild.ImageTag)
	}

	// Create buffers to capture output
	var stdoutBuf, stderrBuf bytes.Buffer

	// Execute the command
	cmd := exec.Command(cmdParts[0], cmdParts[1:]...)
	cmd.Dir = workDir
	
	// Create a multi-writer that writes to both the buffer and stdout/stderr
	stdoutWriters := []io.Writer{&stdoutBuf, os.Stdout}
	stderrWriters := []io.Writer{&stderrBuf, os.Stderr}
	
	cmd.Stdout = io.MultiWriter(stdoutWriters...)
	cmd.Stderr = io.MultiWriter(stderrWriters...)

	stepLogger.Printf("Step %d: Executing docker build: %v\n", stepID, strings.Join(cmdParts, " "))
	err := cmd.Run()
	
	// Always log the full output for debugging
	stdoutOutput := stdoutBuf.String()
	stderrOutput := stderrBuf.String()
	
	if len(stdoutOutput) > 0 {
		stepLogger.Printf("Step %d: Docker build stdout:\n%s\n", stepID, stdoutOutput)
	}
	if len(stderrOutput) > 0 {
		stepLogger.Printf("Step %d: Docker build stderr:\n%s\n", stepID, stderrOutput)
	}

	if err != nil {
		// Include both stdout and stderr in the error message
		return fmt.Errorf("docker build failed: %v\nStdout:\n%s\nStderr:\n%s", 
			err, stdoutOutput, stderrOutput)
	}

	// Get the image ID
	imageID, err := getDockerImageID(config.DockerBuild.ImageTag)
	if err != nil {
		return fmt.Errorf("failed to get image ID: %w", err)
	}

	// Update the config with the new image ID
	config.DockerBuild.ImageID = imageID

	// Update the step settings with the new config
	updatedSettings, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal updated settings: %w", err)
	}

	// Update the step in the database
	_, err = db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(updatedSettings), stepID)
	if err != nil {
		return fmt.Errorf("failed to update step settings: %w", err)
	}

	return nil
}

// getDockerImageID retrieves the image ID for a given tag
func getDockerImageID(tag string) (string, error) {
	cmd := exec.Command("docker", "inspect", "-f", "{{.Id}}", tag)
	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return "", err
	}

	return strings.TrimSpace(out.String()), nil
}

type stepExec struct {
	StepID    int
	TaskID    int
	Title     string
	Settings  string
	LocalPath string
}

type StepInfo struct {
	ID         int                    `json:"id"`
	TaskID     int                    `json:"task_id"`
	Title      string                 `json:"title"`
	Status     string                 `json:"status"`
	Settings   map[string]interface{} `json:"settings"`
	Results    map[string]interface{} `json:"results,omitempty"`
	CreatedAt  time.Time              `json:"created_at"`
	UpdatedAt  time.Time              `json:"updated_at"`
	RawResults *string                `json:"-"` // Raw JSON string from the database
}

func executePendingSteps() error {
	pgURL, err := GetPgURLFromEnv()
	if err != nil {
		stepLogger.Println("DB config error:", err)
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		stepLogger.Println("DB open error:", err)
		return err
	}
	defer db.Close()

	// Process steps in order of dependencies
	processFileExistsSteps(db)     // First, check file existence
	processDockerBuildSteps(db)    // Then build Docker images
	processDockerRunSteps(db)      // Then run Docker containers
	processDockerRubricsSteps(db)  // Finally, run Docker rubrics
	return nil
}

func processFileExistsSteps(db *sql.DB) {
	query := `SELECT s.id, s.task_id, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.status = 'active'
		AND t.status = 'active'
		AND t.local_path IS NOT NULL
		AND t.local_path <> ''
		AND s.settings::text LIKE '%file_exists%'`

	rows, err := db.Query(query)
	if err != nil {
		stepLogger.Println("File exists query error:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var step stepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.LocalPath); err != nil {
			stepLogger.Println("Row scan error:", err)
			continue
		}

		var settings map[string]interface{}
		if err := json.Unmarshal([]byte(step.Settings), &settings); err != nil {
			storeStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "invalid settings json"})
			stepLogger.Printf("Step %d: invalid settings json\n", step.StepID)
			continue
		}

		filePath, ok := settings["file_exists"].(string)
		if !ok {
			continue
		}

		absPath := filepath.Join(step.LocalPath, filePath)
		if _, err := os.Stat(absPath); err == nil {
			storeStepResult(db, step.StepID, map[string]interface{}{"result": "success"})
			stepLogger.Printf("Step %d: file_exists '%s' SUCCESS\n", step.StepID, absPath)
		} else {
			storeStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": err.Error()})
			stepLogger.Printf("Step %d: file_exists '%s' FAILURE: %s\n", step.StepID, absPath, err.Error())
		}
	}
}

func processDockerRubricsSteps(db *sql.DB) {
	query := `SELECT s.id, s.task_id, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.status = 'active'
		AND t.status = 'active'
		AND t.local_path IS NOT NULL
		AND t.local_path <> ''
		AND s.settings::text LIKE '%docker_rubrics%'`

	rows, err := db.Query(query)
	if err != nil {
		stepLogger.Println("Docker rubrics query error:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var step stepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.LocalPath); err != nil {
			stepLogger.Println("Row scan error:", err)
			continue
		}

		// Parse the docker rubrics config
		var config DockerRubricsConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			storeStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "invalid docker rubrics config"})
			stepLogger.Printf("Step %d: invalid docker rubrics config: %v\n", step.StepID, err)
			continue
		}

		// Check if dependencies are met
		ok, err := checkDependencies(db, step.StepID, config.DockerRubrics.DependsOn)
		if err != nil {
			stepLogger.Printf("Step %d: error checking dependencies: %v\n", step.StepID, err)
			continue
		}
		if !ok {
			stepLogger.Printf("Step %d: waiting for dependencies to complete\n", step.StepID)
			continue
		}

		// Get current image ID for the tag
		currentImageID, err := getDockerImageID(config.DockerRubrics.ImageTag)
		if err != nil {
			stepLogger.Printf("Step %d: error getting current image ID: %v\n", step.StepID, err)
			continue
		}

		// If image ID is empty, we can't proceed
		if currentImageID == "" {
			stepLogger.Printf("Step %d: no image found with tag %s\n", step.StepID, config.DockerRubrics.ImageTag)
			continue
		}

		// If image ID is different from stored one, update and skip this run
		if config.DockerRubrics.ImageID != "" && config.DockerRubrics.ImageID != currentImageID {
			stepLogger.Printf("Step %d: image ID changed, updating and skipping this run\n", step.StepID)
			config.DockerRubrics.ImageID = currentImageID
			// Update the step with new image ID
			updatedSettings, _ := json.Marshal(config)
			db.Exec(`UPDATE steps SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettings), step.StepID)
			continue
		}

		// Check if files have changed
		shouldRun := false
		for _, file := range config.DockerRubrics.Files {
			filePath := filepath.Join(step.LocalPath, file)

			// Check if file exists first
			if _, err := os.Stat(filePath); os.IsNotExist(err) {
				stepLogger.Printf("Step %d: file not found: %s\n", step.StepID, filePath)
				// If it's TASK_DATA.md, we should still try to proceed as it might be created later
				if !strings.HasSuffix(file, "TASK_DATA.md") {
					storeStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": fmt.Sprintf("required file not found: %s", file)})
					continue
				}
				shouldRun = true // Mark to run if TASK_DATA.md is missing (it will be created)
				continue
			}

			currentHash, err := calculateFileHash(filePath)
			if err != nil {
				stepLogger.Printf("Step %d: error calculating hash for %s: %v\n", step.StepID, file, err)
				continue
			}

			storedHash, hasHash := config.DockerRubrics.Hashes[file]
			if !hasHash || storedHash != currentHash {
				// Update the hash
				if config.DockerRubrics.Hashes == nil {
					config.DockerRubrics.Hashes = make(map[string]string)
				}
				config.DockerRubrics.Hashes[file] = currentHash
				shouldRun = true
			}
		}

		// If no changes and we already have an image ID, skip
		if !shouldRun && config.DockerRubrics.ImageID != "" {
			stepLogger.Printf("Step %d: no changes detected, skipping\n", step.StepID)
			continue
		}

		// Process the TASK_DATA.md file
		for _, file := range config.DockerRubrics.Files {
			if strings.HasSuffix(file, "TASK_DATA.md") {
				filePath := filepath.Join(step.LocalPath, file)
				content, err := os.ReadFile(filePath)
				if err != nil {
					stepLogger.Printf("Step %d: error reading file %s: %v\n", step.StepID, file, err)
					continue
				}

				// Parse the TASK_DATA.md content
				lines := strings.Split(string(content), "\n")
				for i := 0; i < len(lines); i++ {
					line := strings.TrimSpace(lines[i])
					if line == "" {
						continue
					}

					// Parse the score and required flag
					parts := strings.Fields(line)
					if len(parts) < 2 {
						continue
					}

					_, err = strconv.Atoi(parts[0])
					if err != nil {
						continue
					}

					// Check if the command is required
					required := false
					if len(parts) > 1 && parts[1] == "[x]" {
						required = true
					}

					// Get the command (rest of the line after score and [x])
					command := strings.TrimSpace(strings.TrimPrefix(line, parts[0]))
					if required {
						command = strings.TrimSpace(strings.TrimPrefix(command, "[x]"))
					} else {
						command = strings.TrimSpace(strings.TrimPrefix(command, "[ ]"))
					}

					// Execute the command in the container
					cmd := exec.Command("docker", "run", "--rm", config.DockerRubrics.ImageTag, "sh", "-c", command)
					output, err := cmd.CombinedOutput()
					if err != nil {
						stepLogger.Printf("Step %d: command '%s' failed: %v\nOutput: %s\n", step.StepID, command, err, string(output))
						if required {
							storeStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": fmt.Sprintf("required command failed: %s", command), "output": string(output)})
							continue
						}
					} else {
						stepLogger.Printf("Step %d: command '%s' succeeded\nOutput: %s\n", step.StepID, command, string(output))
					}
				}
			}
		}

		// Update the step with new hashes and image ID
		config.DockerRubrics.ImageID = currentImageID
		updatedSettings, _ := json.Marshal(config)
		db.Exec(`UPDATE steps SET settings = $1, updated_at = now() WHERE id = $2`, string(updatedSettings), step.StepID)
		storeStepResult(db, step.StepID, map[string]interface{}{"result": "success"})
	}
}

func processDockerBuildSteps(db *sql.DB) {
	query := `SELECT s.id, s.task_id, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.status = 'active'
		AND t.status = 'active'
		AND t.local_path IS NOT NULL
		AND t.local_path <> ''
		AND s.settings::text LIKE '%docker_build%'`

	rows, err := db.Query(query)
	if err != nil {
		stepLogger.Println("Docker build query error:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var step stepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.LocalPath); err != nil {
			stepLogger.Println("Row scan error:", err)
			continue
		}

		// Parse the docker build config
		var config DockerBuildConfig
		if err := json.Unmarshal([]byte(step.Settings), &config); err != nil {
			storeStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "invalid docker build config"})
			stepLogger.Printf("Step %d: invalid docker build config: %v\n", step.StepID, err)
			continue
		}

		// Check if dependencies are met
		ok, err := checkDependencies(db, step.StepID, config.DockerBuild.DependsOn)
		if err != nil {
			stepLogger.Printf("Step %d: error checking dependencies: %v\n", step.StepID, err)
			continue
		}
		if !ok {
			stepLogger.Printf("Step %d: waiting for dependencies to complete\n", step.StepID)
			continue
		}

		// Check if files have changed
		shouldBuild := false
		for _, file := range config.DockerBuild.Files {
			filePath := filepath.Join(step.LocalPath, file)
			currentHash, err := calculateFileHash(filePath)
			if err != nil {
				stepLogger.Printf("Step %d: error calculating hash for %s: %v\n", step.StepID, file, err)
				continue
			}

			storedHash, exists := config.DockerBuild.Hashes[file]
			if !exists || storedHash != currentHash {
				shouldBuild = true
				config.DockerBuild.Hashes[file] = currentHash
			}
		}

		// If no changes and we already have an image ID, skip the build
		if !shouldBuild && config.DockerBuild.ImageID != "" {
			stepLogger.Printf("Step %d: no changes detected, skipping build\n", step.StepID)
			continue
		}

		// Execute the docker build
		if err := executeDockerBuild(step.LocalPath, config, step.StepID, db); err != nil {
			storeStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": err.Error()})
			stepLogger.Printf("Step %d: docker build failed: %v\n", step.StepID, err)
			continue
		}

		// Mark step as successful
		storeStepResult(db, step.StepID, map[string]interface{}{"result": "success"})
		stepLogger.Printf("Step %d: docker build completed successfully\n", step.StepID)
	}
}

// CopyStep copies a step to a new task with the given ID
// GetStepInfo retrieves detailed information about a specific step by ID
func GetStepInfo(stepID int) (*StepInfo, error) {
	pgURL, err := GetPgURLFromEnv()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var info StepInfo
	var settingsStr, resultsStr sql.NullString
	var createdAt, updatedAt time.Time

	err = db.QueryRow(`
		SELECT 
			s.id, s.task_id, s.title, s.status, 
			s.settings::text, s.results::text,
			s.created_at, s.updated_at
		FROM steps s
		WHERE s.id = $1
	`, stepID).Scan(
		&info.ID, &info.TaskID, &info.Title, &info.Status,
		&settingsStr, &resultsStr,
		&createdAt, &updatedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no step found with ID %d", stepID)
		}
		return nil, fmt.Errorf("error fetching step: %w", err)
	}

	// Store raw results
	if resultsStr.Valid {
		info.RawResults = &resultsStr.String
	}

	// Parse settings JSON if exists
	if settingsStr.Valid && settingsStr.String != "" {
		decoder := json.NewDecoder(strings.NewReader(settingsStr.String))
		decoder.UseNumber()
		if err := decoder.Decode(&info.Settings); err != nil {
			return nil, fmt.Errorf("error parsing settings: %w", err)
		}
	} else {
		info.Settings = make(map[string]interface{})
	}

	// Only parse results if they exist and are not null
	if resultsStr.Valid && resultsStr.String != "" && resultsStr.String != "null" {
		info.Results = make(map[string]interface{})
		decoder := json.NewDecoder(strings.NewReader(resultsStr.String))
		decoder.UseNumber()
		if err := decoder.Decode(&info.Results); err != nil {
			return nil, fmt.Errorf("error parsing results: %w", err)
		}
	}

	info.CreatedAt = createdAt
	info.UpdatedAt = updatedAt

	return &info, nil
}

func CopyStep(stepID, toTaskID int) error {
	pgURL, err := GetPgURLFromEnv()
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return err
	}
	defer db.Close()

	// Start a transaction
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("error starting transaction: %w", err)
	}
	defer tx.Rollback()

	// 1. Verify the target task exists
	var targetTaskExists bool
	err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1)", toTaskID).Scan(&targetTaskExists)
	if err != nil {
		return fmt.Errorf("error checking target task: %w", err)
	}
	if !targetTaskExists {
		return fmt.Errorf("target task with ID %d does not exist", toTaskID)
	}

	// 2. Get the source step data
	var title, status, settings string
	err = tx.QueryRow(
		"SELECT title, status, settings FROM steps WHERE id = $1",
		stepID,
	).Scan(&title, &status, &settings)

	if err == sql.ErrNoRows {
		return fmt.Errorf("source step with ID %d does not exist", stepID)
	}
	if err != nil {
		return fmt.Errorf("error fetching source step: %w", err)
	}

	// 3. Create the new step in the target task with the same status as source
	_, err = tx.Exec(
		`INSERT INTO steps (task_id, title, settings, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, now(), now())`,
		toTaskID, title, settings, status,
	)

	if err != nil {
		return fmt.Errorf("error creating new step: %w", err)
	}

	// 4. Commit the transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("error committing transaction: %w", err)
	}

	return nil
}

// ClearStepResults clears the results for a step
func ClearStepResults(db *sql.DB, stepID int) error {
	_, err := db.Exec(
		"UPDATE steps SET results = NULL, updated_at = NOW() WHERE id = $1",
		stepID,
	)
	if err != nil {
		return fmt.Errorf("error clearing step results: %w", err)
	}
	return nil
}

// EditStepSettings updates a specific field in the step's settings using dot notation
// For example: EditStepSettings(db, 1, "docker_run.image_tag", "new-image:latest")
// Special case: if path is "results", it will update the results column directly
func EditStepSettings(db *sql.DB, stepID int, path string, value interface{}) error {
	// Special case for updating results directly
	if path == "results" {
		if value == nil {
			return ClearStepResults(db, stepID)
		}
		// Convert value to JSON string if it's not already
		resultsJSON, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("error marshaling results: %w", err)
		}
		_, err = db.Exec(
			"UPDATE steps SET results = $1::jsonb, updated_at = NOW() WHERE id = $2",
			string(resultsJSON),
			stepID,
		)
		return err
	}
	// Get current settings
	var settingsJSON []byte
	err := db.QueryRow("SELECT settings FROM steps WHERE id = $1", stepID).Scan(&settingsJSON)
	if err != nil {
		return fmt.Errorf("error getting step settings: %w", err)
	}

	// Parse settings into a map
	var settings map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(settingsJSON))
	decoder.UseNumber() // Preserve number types
	if err := decoder.Decode(&settings); err != nil {
		return fmt.Errorf("error parsing settings: %w", err)
	}

	// Split the path by dots to traverse the JSON structure
	parts := strings.Split(path, ".")
	current := settings

	// Navigate to the parent of the target field
	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]
		if next, ok := current[part].(map[string]interface{}); ok {
			current = next
		} else {
			// Create nested maps for non-existent paths
			current[part] = make(map[string]interface{})
			current = current[part].(map[string]interface{})
		}
	}

	// Set the value at the final path component
	current[parts[len(parts)-1]] = value

	// Convert back to JSON without HTML escaping
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "") // No indentation to match existing format
	if err := encoder.Encode(settings); err != nil {
		return fmt.Errorf("error marshaling updated settings: %w", err)
	}

	// Update the database
	_, err = db.Exec(
		"UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2",
		strings.TrimSpace(buf.String()),
		stepID,
	)
	if err != nil {
		return fmt.Errorf("error updating step: %w", err)
	}

	return nil
}

// storeStepResult stores the execution result of a step
func storeStepResult(db *sql.DB, stepID int, result map[string]interface{}) {
	resJson, _ := json.Marshal(result)
	_, err := db.Exec(`UPDATE steps SET results = $1::jsonb, updated_at = now() WHERE id = $2`, string(resJson), stepID)
	if err != nil {
		stepLogger.Println("Failed to update results for step", stepID, ":", err)
	}
}
