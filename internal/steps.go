package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/PortNumber53/task-sync/pkg/models"
)

var CommandFunc func(name string, arg ...string) *exec.Cmd = exec.Command

// stepProcessors maps step types to their respective processor functions with consistent signature using wrappers.
// getStepProcessors returns the step processor map, parameterized by force and golden flags for rubric-related steps.
func getStepProcessors(force bool, golden bool) map[string]func(*sql.DB, *models.StepExec, *log.Logger) error {
	return map[string]func(*sql.DB, *models.StepExec, *log.Logger) error{
		"docker_pull": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
			processDockerPullSteps(db, se.StepID)
			return nil
		},
		"docker_build": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
			processDockerBuildSteps(db, logger, se.StepID)
			return nil
		},
		"docker_run": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
			return processDockerRunSteps(db, se.StepID)
		},
		"docker_pool": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
			return processDockerPoolSteps(db, se.StepID)
		},
		"docker_shell": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
			processDockerShellSteps(db, se.StepID)
			return nil
		},
		"docker_volume_pool":    ProcessDockerVolumePoolStep,
		"docker_extract_volume": ProcessDockerExtractVolumeStep,
		"model_task_check":      ProcessModelTaskCheckStep,
		"file_exists": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
			if se != nil && se.StepID != 0 {
				return ProcessFileExistsStep(db, se, logger)
			}
			return processAllFileExistsSteps(db, logger)
		},
		"rubrics_import": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
			// If a specific step is provided, process just that step so injected flags (e.g., force) are honored
			if se != nil && se.StepID != 0 {
				return ProcessRubricsImportStep(db, se, logger)
			}
			// Otherwise, process all rubrics_import steps
			return processRubricsImportSteps(db, 0)
		},
		"rubric_set": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
			// If a specific step is provided (from ProcessSpecificStep), run only that.
			if se != nil && se.StepID != 0 {
				return ProcessRubricSetStep(db, se, logger, force)
			}
			// Otherwise (from executePendingSteps), run all rubric_set steps.
			return processAllRubricSetSteps(db, logger)
		},
		"rubric_shell": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
			// If a specific step is provided (from ProcessSpecificStep), run only that.
			if se != nil && se.StepID != 0 {
				return ProcessRubricShellStep(db, se, logger, force, golden)
			}
			// Otherwise (from executePendingSteps), run all rubric_shell steps.
			return processAllRubricShellSteps(db, logger, force, golden)
		},
		"dynamic_rubric": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
			if se != nil && se.StepID != 0 {
				return ProcessDynamicRubricStep(db, se, logger)
			}
			// For now, do not process all dynamic_rubric steps at once. Only log if such steps exist.
			rows, err := db.Query(`SELECT id FROM steps WHERE settings ? 'dynamic_rubric'`)
			if err != nil {
				logger.Printf("Could not check for dynamic_rubric steps: %v", err)
				return nil
			}
			defer rows.Close()
			if rows.Next() {
				logger.Printf("Skipping bulk processing of dynamic_rubric steps (not supported). Use specific StepID.")
			}
			return nil
		},
	}
}

// ProcessSteps is the main entry point for processing all pending steps.
func ProcessSteps(db *sql.DB) error {
	// Bulk runs default to golden=false
	return executePendingSteps(db, getStepProcessors(false, false))
}

// ProcessStepsForTask processes all steps for a specific task by ID, respecting dependencies.
func ProcessStepsForTask(db *sql.DB, taskID int, golden bool) error {
	// Fetch all steps for the given task, ordered by ID (can be improved to topological sort if needed)
	rows, err := db.Query(`SELECT id FROM steps WHERE task_id = $1 ORDER BY id`, taskID)
	if err != nil {
		return fmt.Errorf("failed to fetch steps for task %d: %w", taskID, err)
	}
	defer rows.Close()

	var stepIDs []int
	for rows.Next() {
		var stepID int
		if err := rows.Scan(&stepID); err != nil {
			return fmt.Errorf("failed to scan step ID: %w", err)
		}
		stepIDs = append(stepIDs, stepID)
	}

	if len(stepIDs) == 0 {
		return fmt.Errorf("no steps found for task %d", taskID)
	}

	for _, stepID := range stepIDs {
		fmt.Printf("Processing step ID %d...\n", stepID)
		if err := ProcessSpecificStep(db, stepID, false, golden); err != nil {
			fmt.Printf("Error processing step %d: %v\n", stepID, err)
			// Continue processing other steps even if one fails
		}
	}
	return nil
}

func executePendingSteps(db *sql.DB, stepProcessors map[string]func(*sql.DB, *models.StepExec, *log.Logger) error) error {
	// Iterate over the map and call each function
	for stepType, processorFunc := range stepProcessors {
		if err := processorFunc(db, &models.StepExec{}, log.New(os.Stdout, fmt.Sprintf("STEP [%s]: ", stepType), log.Ldate|log.Ltime|log.Lshortfile)); err != nil {
			fmt.Printf("Error processing %s steps: %v", stepType, err)
			// Decide if you want to continue or return on error
		}
	}

	// Wait for all goroutines to complete
	// This is a simplified approach; a sync.WaitGroup would be more robust
	// For now, assuming steps complete or timeout reasonably quickly
	time.Sleep(5 * time.Second)

	return nil
}

func CopyStep(db *sql.DB, fromStepID, toTaskID int) (int, error) {
	// Start a transaction
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("error starting transaction: %w", err)
	}
	defer tx.Rollback()

	// 1. Verify the target task exists
	var targetTaskExists bool
	err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1)", toTaskID).Scan(&targetTaskExists)
	if err != nil {
		return 0, fmt.Errorf("error checking target task: %w", err)
	}
	if !targetTaskExists {
		return 0, fmt.Errorf("target task with ID %d does not exist", toTaskID)
	}

	// 1. Get the source step's data
	var title, settings string
	err = tx.QueryRow(
		"SELECT title, settings FROM steps WHERE id = $1",
		fromStepID,
	).Scan(&title, &settings)
	if err != nil {
		return 0, fmt.Errorf("reading source step %d failed: %w", fromStepID, err)
	}

	// 2. (Future) Transform settings if needed, e.g., if it contains references
	// to other steps in the original task. For now, we do a direct copy.

	// 3. Create the new step in the target task
	var newStepID int
	err = tx.QueryRow(
		`INSERT INTO steps (task_id, title, settings, created_at, updated_at)
		 VALUES ($1, $2, $3::jsonb, now(), now())
		 RETURNING id`,
		toTaskID, title, settings,
	).Scan(&newStepID)
	if err != nil {
		return 0, fmt.Errorf("creating new step in task %d failed: %w", toTaskID, err)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("error committing transaction: %w", err)
	}

	return newStepID, nil
}

// CreateStep inserts a new step for a task and returns the new step's ID.
func CreateStep(db *sql.DB, taskRef string, title string, settings string) (int, error) {
	taskID, err := GetTaskID(db, taskRef)
	if err != nil {
		return 0, err
	}

	var stepID int
	err = db.QueryRow(
		"INSERT INTO steps (task_id, title, settings) VALUES ($1, $2, $3) RETURNING id",
		taskID, title, settings,
	).Scan(&stepID)
	if err != nil {
		return 0, fmt.Errorf("could not create step: %w", err)
	}
	return stepID, nil
}

// ListSteps prints all steps in the DB. If full is true, prints settings column too.
func ListSteps(db *sql.DB, full bool) error {
	return models.ListSteps(db, full)
}

func ClearStepResults(db *sql.DB, stepID int) error {
	result, err := db.Exec(
		"UPDATE steps SET results = NULL, updated_at = NOW() WHERE id = $1",
		stepID,
	)
	if err != nil {
		return fmt.Errorf("error clearing step results: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("error checking affected rows: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("no step found with ID %d", stepID)
	}

	return nil
}

type StepNode struct {
	ID       int
	Title    string
	TaskID   int
	Children []*StepNode
}

// TreeSteps fetches all steps and prints them as a dependency tree, grouped by task.
func TreeSteps(db *sql.DB, taskID int) error {
	// 1. Fetch tasks to get their names (filtered if taskID > 0)
	var taskRows *sql.Rows
	var err error
	if taskID > 0 {
		taskRows, err = db.Query(`SELECT id, name FROM tasks WHERE id = $1`, taskID)
	} else {
		taskRows, err = db.Query(`SELECT id, name FROM tasks ORDER BY id`)
	}
	if err != nil {
		return fmt.Errorf("querying tasks failed: %w", err)
	}
	defer taskRows.Close()

	taskNames := make(map[int]string)
	var taskIDs []int
	for taskRows.Next() {
		var id int
		var name string
		if err := taskRows.Scan(&id, &name); err != nil {
			return err
		}
		taskNames[id] = name
		taskIDs = append(taskIDs, id)
	}

	// 2. Fetch steps (filtered if taskID > 0)
	var stepRows *sql.Rows
	if taskID > 0 {
		stepRows, err = db.Query(`SELECT id, task_id, title, settings FROM steps WHERE task_id = $1 ORDER BY id`, taskID)
	} else {
		stepRows, err = db.Query(`SELECT id, task_id, title, settings FROM steps ORDER BY id`)
	}
	if err != nil {
		return err
	}
	defer stepRows.Close()

	nodes := make(map[int]*StepNode)
	dependencies := make(map[int][]int)
	taskSteps := make(map[int][]*StepNode)

	for stepRows.Next() {
		var id, taskID int
		var title, settingsStr string
		if err := stepRows.Scan(&id, &taskID, &title, &settingsStr); err != nil {
			return err
		}

		node := &StepNode{ID: id, TaskID: taskID, Title: title}
		nodes[id] = node
		taskSteps[taskID] = append(taskSteps[taskID], node)

		var topLevel map[string]json.RawMessage
		if err := json.Unmarshal([]byte(settingsStr), &topLevel); err != nil {
			continue
		}

		// Check for top-level depends_on
		if dependsOnRaw, ok := topLevel["depends_on"]; ok {
			var deps []models.Dependency
			if err := json.Unmarshal(dependsOnRaw, &deps); err == nil {
				for _, dep := range deps {
					dependencies[id] = append(dependencies[id], dep.ID)
				}
				continue // Found deps, go to next step
			}
		}

		// Fallback for nested depends_on
		for _, rawMessage := range topLevel {
			var nested struct {
				DependsOn []models.Dependency `json:"depends_on"`
			}
			if err := json.Unmarshal(rawMessage, &nested); err == nil {
				if len(nested.DependsOn) > 0 {
					for _, dep := range nested.DependsOn {
						dependencies[id] = append(dependencies[id], dep.ID)
					}
					break // Found deps, break from inner loop
				}
			}
		}
	}

	// 3. Build the dependency tree
	isChild := make(map[int]bool)
	for childID, parentIDs := range dependencies {
		for _, parentID := range parentIDs {
			if parentNode, ok := nodes[parentID]; ok {
				if childNode, ok := nodes[childID]; ok {
					parentNode.Children = append(parentNode.Children, childNode)
					isChild[childID] = true
				}
			}
		}
	}

	// 4. Sort children for each node
	for _, node := range nodes {
		sort.Slice(node.Children, func(i, j int) bool {
			return node.Children[i].ID < node.Children[j].ID
		})
	}

	// 5. Print the tree, grouped by task
	sort.Ints(taskIDs) // Sort tasks by ID for consistent output
	for _, taskID := range taskIDs {
		taskName := taskNames[taskID]
		fmt.Printf("%d-%s\n", taskID, taskName)

		if steps, ok := taskSteps[taskID]; ok {
			var rootNodes []*StepNode
			for _, node := range steps {
				if !isChild[node.ID] {
					rootNodes = append(rootNodes, node)
				}
			}

			// Helper to extract rubric number from title
			extractRubricNum := func(title string) (int, bool) {
				var n int
				if _, err := fmt.Sscanf(title, "Rubric %d", &n); err == nil {
					return n, true
				}
				return 0, false
			}

			sort.Slice(rootNodes, func(i, j int) bool {
				numI, okI := extractRubricNum(rootNodes[i].Title)
				numJ, okJ := extractRubricNum(rootNodes[j].Title)
				if okI && okJ {
					return numI < numJ
				}
				if okI {
					return true
				}
				if okJ {
					return false
				}
				return rootNodes[i].ID < rootNodes[j].ID
			})
			printChildren(rootNodes, "")
		}
	}

	return nil
}

func printChildren(nodes []*StepNode, prefix string) {
	for i, node := range nodes {
		connector := "├── "
		newPrefix := prefix + "│   "
		if i == len(nodes)-1 {
			connector = "╰── "
			newPrefix = prefix + "    "
		}
		fmt.Printf("%s%s%d-%s\n", prefix, connector, node.ID, node.Title)
		printChildren(node.Children, newPrefix)
	}
}

// ProcessSpecificStep processes a single step by its ID.
func ProcessSpecificStep(db *sql.DB, stepID int, force bool, golden bool) error {
	// Fetch the full step details including task_id
	var stepExec models.StepExec
	err := db.QueryRow("SELECT s.id, s.task_id, s.title, s.settings, t.base_path FROM steps s JOIN tasks t ON s.task_id = t.id WHERE s.id = $1", stepID).Scan(&stepExec.StepID, &stepExec.TaskID, &stepExec.Title, &stepExec.Settings, &stepExec.BasePath)
	if err != nil {
		hostname, errHost := os.Hostname()
		if errHost != nil {
			log.Printf("Host error, failed to fetch step %d: %v", stepID, err)
		} else {
			log.Printf("Host: %s, failed to fetch step %d: %v", hostname, stepID, err)
		}
		return fmt.Errorf("failed to fetch step %d: %w", stepID, err)
	}

	// Fetch the Status from the tasks table using taskID
	var status string
	err = db.QueryRow("SELECT status FROM tasks WHERE id = $1", stepExec.TaskID).Scan(&status)
	if err != nil {
		hostname, errHost := os.Hostname()
		if errHost != nil {
			log.Printf("Host error, failed to fetch task base path/status for task ID %d: %v", stepExec.TaskID, err)
		} else {
			log.Printf("Host: %s, failed to fetch task base path/status for task ID %d: %v", hostname, stepExec.TaskID, err)
		}
		return fmt.Errorf("failed to fetch task base path/status for task ID %d: %w", stepExec.TaskID, err)
	}
	if status != "active" {
		log.Printf("STEP %d: Skipping execution because parent task %d status is not active (status=%q)", stepID, stepExec.TaskID, status)
		return nil
	}

	// Determine the step type from settings
	var settings map[string]json.RawMessage
	err = json.Unmarshal([]byte(stepExec.Settings), &settings)
	if err != nil {
		return fmt.Errorf("failed to unmarshal settings for step %d: %w", stepID, err)
	}

	// Inject the force flag into the settings if it's true
	if force {
		for key := range settings {
			// Assuming there's only one key in settings for the step type
			// and it corresponds to the step's configuration struct.
			// We need to unmarshal, set force, and re-marshal.
			var stepConfig map[string]interface{}
			if err := json.Unmarshal(settings[key], &stepConfig); err != nil {
				return fmt.Errorf("failed to unmarshal step config for force injection: %w", err)
			}
			stepConfig["force"] = true
			updatedConfig, err := json.Marshal(stepConfig)
			if err != nil {
				return fmt.Errorf("failed to marshal step config after force injection: %w", err)
			}
			settings[key] = updatedConfig
			break // Assuming only one step config per settings map
		}
		updatedSettings, err := json.Marshal(settings)
		if err != nil {
			return fmt.Errorf("failed to marshal updated settings after force injection: %w", err)
		}
		stepExec.Settings = string(updatedSettings)
	}

	var stepType string
	processors := getStepProcessors(force, golden)
	for key := range settings {
		if _, exists := processors[key]; exists {
			stepType = key
			break
		}
	}

	if stepType == "" {
		return fmt.Errorf("unknown or no matching step type found for step %d", stepID)
	}

	stepLogger := log.New(os.Stdout, fmt.Sprintf("STEP %d [%s]: ", stepID, stepType), log.Ldate|log.Ltime|log.Lshortfile)

	// If --force, clear step results before running
	if force {
		if err := ClearStepResults(db, stepID); err != nil {
			stepLogger.Printf("Warning: could not clear step results for step %d: %v", stepID, err)
		}
	}

	if processor, exists := processors[stepType]; exists {
		return processor(db, &stepExec, stepLogger)
	} else {
		return fmt.Errorf("no processor found for step type %s of step %d", stepType, stepID)
	}
}

// DeleteStep removes a step from the database by its ID.
func DeleteStep(db *sql.DB, stepID int) error {
	result, err := db.Exec("DELETE FROM steps WHERE id = $1", stepID)
	if err != nil {
		return fmt.Errorf("failed to delete step %d: %w", stepID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected for step %d: %w", stepID, err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("no step found with ID %d", stepID)
	}

	return nil
}

// ProcessDockerExtractVolumeStep handles the execution of a docker_extract_volume step.
func ProcessDockerExtractVolumeStep(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
	// Unmarshal settings to get docker_extract_volume config
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(se.Settings), &settings); err != nil {
		return fmt.Errorf("failed to unmarshal settings: %w", err)
	}
	var config models.DockerExtractVolumeConfig
	if err := json.Unmarshal(settings["docker_extract_volume"], &config); err != nil {
		return fmt.Errorf("failed to unmarshal docker_extract_volume settings: %w", err)
	}

	// Add hash check logic using utility function (skip when forced)
	if config.Force {
		logger.Printf("Step %d: Force flag set; bypassing triggers.files hash check and executing.", se.StepID)
	} else if len(config.Triggers.Files) > 0 {
		changed, err := models.CheckFileHashChanges(se.BasePath, config.Triggers.Files, logger)
		if err != nil {
			return fmt.Errorf("error checking file hashes: %w", err)
		}
		if !changed {
			logger.Printf("Step %d: No changes detected in triggers.files. Skipping execution.", se.StepID)
			return nil
		}
	}

	// Log the step configuration for debugging
	configJSON, _ := json.Marshal(config)
	logger.Printf("Step configuration: %s", string(configJSON))

	// Construct volume name based on task ID
	volumeName := fmt.Sprintf("volume_task_%d", se.TaskID)

	// Fetch image ID from step config or fall back to task settings
	imageID := config.GetImageID()
	if imageID == "" {
		var taskSettings string
		err := db.QueryRow("SELECT settings FROM tasks WHERE id = $1", se.TaskID).Scan(&taskSettings)
		if err != nil {
			return fmt.Errorf("failed to fetch task settings: %w", err)
		}
		var taskConfig struct {
			Docker struct {
				ImageID string `json:"image_id"`
			} `json:"docker"`
		}
		if err := json.Unmarshal([]byte(taskSettings), &taskConfig); err != nil {
			return fmt.Errorf("failed to unmarshal task settings: %w", err)
		}
		imageID = taskConfig.Docker.ImageID
	}
	logger.Printf("Using image ID: %s for step %d", imageID, se.StepID)

	// Check if the Docker image exists
	checkCmd := CommandFunc("docker", "image", "inspect", imageID)
	if err := checkCmd.Run(); err != nil {
		return fmt.Errorf("Docker image %s does not exist: %w", imageID, err)
	}

	// Store volume name in task settings (do not modify app_folder here)
	taskSettings, err := models.GetTaskSettings(db, se.TaskID)
	if err != nil {
		return fmt.Errorf("failed to get task settings: %w", err)
	}
	taskSettings.VolumeName = volumeName

	err = models.UpdateTaskSettings(db, se.TaskID, taskSettings)
	if err != nil {
		return fmt.Errorf("failed to update task settings: %w", err)
	}
	logger.Printf("Updated task settings with volume name: %s", volumeName)

	// Create Docker volume
	cmd := CommandFunc("docker", "volume", "create", volumeName)
	logger.Printf("Executing command: docker volume create %s", volumeName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Command failed: %s", string(output))
		return fmt.Errorf("failed to create docker volume: %w", err)
	}

	appHostPath := filepath.Join(se.BasePath, "original/") + "/"
	solution1Path := filepath.Join(se.BasePath, "volume_solution1/") + "/"
	solution2Path := filepath.Join(se.BasePath, "volume_solution2/") + "/"
	solution3Path := filepath.Join(se.BasePath, "volume_solution3/") + "/"
	solution4Path := filepath.Join(se.BasePath, "volume_solution4/") + "/"
	goldenPath := filepath.Join(se.BasePath, "volume_golden/") + "/"
	containerName := fmt.Sprintf("extract_vol_container_%d", se.StepID)
	// Best-effort cleanup in case a previous run left the helper container
	preCleanup := CommandFunc("docker", "rm", "-f", containerName)
	logger.Printf("Ensuring no leftover helper container: docker rm -f %s", containerName)
	_ = preCleanup.Run()
	cmd = CommandFunc("docker", "run", "-d", "--platform", "linux/amd64", "--name", containerName, "-v", volumeName+":/original_volume", "-v", appHostPath+":/original", "-v", solution1Path+":/solution1", "-v", solution2Path+":/solution2", "-v", solution3Path+":/solution3", "-v", solution4Path+":/solution4", "-v", goldenPath+":/golden", imageID, "tail", "-f", "/dev/null")
	logger.Printf("Executing command: docker run -d --platform linux/amd64 --name %s -v %s:/original_volume -v %s:/original -v %s:/solution1 -v %s:/solution2 -v %s:/solution3 -v %s:/solution4 -v %s:/golden %s tail -f /dev/null", containerName, volumeName, appHostPath, solution1Path, solution2Path, solution3Path, solution4Path, goldenPath, imageID)
	output, err = cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Command failed: %s", string(output))
		return fmt.Errorf("failed to start container: %w", err)
	}
	logger.Printf("Command succeeded: container started %s", containerName)
	// Always clean up the helper container after we're done
	defer func() {
		cleanupCmd := CommandFunc("docker", "rm", "-f", containerName)
		logger.Printf("Cleaning up helper container: docker rm -f %s", containerName)
		_ = cleanupCmd.Run()
	}()

	installCmd := "apt-get update && apt-get install -y rsync"
	execCmd := CommandFunc("docker", "exec", containerName, "bash", "-c", installCmd)
	logger.Printf("Executing command: docker exec %s bash -c '%s'", containerName, installCmd)
	output, err = execCmd.CombinedOutput()
	if err != nil {
		logger.Printf("Command failed: %s", string(output))
		cleanupCmd := CommandFunc("docker", "rm", "-f", containerName)
		cleanupCmd.Run()
		return fmt.Errorf("failed to install rsync: %w", err)
	}
	logger.Printf("Command succeeded: rsync installed")

	rsyncCmd1 := fmt.Sprintf("rsync -a --delete-during %s/ /original/", config.AppFolder)
	execCmd = CommandFunc("docker", "exec", containerName, "bash", "-c", rsyncCmd1)
	logger.Printf("Executing command: docker exec %s bash -c '%s'", containerName, rsyncCmd1)
	output, err = execCmd.CombinedOutput()
	if err != nil {
		logger.Printf("Command failed: %s", string(output))
		cleanupCmd := CommandFunc("docker", "rm", "-f", containerName)
		cleanupCmd.Run()
		return fmt.Errorf("rsync from /src/ to /original/ failed: %w", err)
	}
	logger.Printf("Command succeeded: rsync from %s to /original/ completed", config.AppFolder)

	rsyncCmd2 := "rsync -a --delete-during /original/ /solution1/"
	execCmd = CommandFunc("docker", "exec", containerName, "bash", "-c", rsyncCmd2)
	logger.Printf("Executing command: docker exec %s bash -c '%s'", containerName, rsyncCmd2)
	output, err = execCmd.CombinedOutput()
	if err != nil {
		logger.Printf("Command failed: %s", string(output))
		cleanupCmd := CommandFunc("docker", "rm", "-f", containerName)
		cleanupCmd.Run()
		return fmt.Errorf("rsync from /original/ to /solution1/ failed: %w", err)
	}
	logger.Printf("Command succeeded: rsync from /original/ to /solution1/ completed")

	rsyncCmd3 := "rsync -a --delete-during /original/ /solution2/"
	execCmd = CommandFunc("docker", "exec", containerName, "bash", "-c", rsyncCmd3)
	logger.Printf("Executing command: docker exec %s bash -c '%s'", containerName, rsyncCmd3)
	output, err = execCmd.CombinedOutput()
	if err != nil {
		logger.Printf("Command failed: %s", string(output))
		cleanupCmd := CommandFunc("docker", "rm", "-f", containerName)
		cleanupCmd.Run()
		return fmt.Errorf("rsync from /original/ to /solution2/ failed: %w", err)
	}
	logger.Printf("Command succeeded: rsync from /original/ to /solution2/ completed")

	rsyncCmd4 := "rsync -a --delete-during /original/ /solution3/"
	execCmd = CommandFunc("docker", "exec", containerName, "bash", "-c", rsyncCmd4)
	logger.Printf("Executing command: docker exec %s bash -c '%s'", containerName, rsyncCmd4)
	output, err = execCmd.CombinedOutput()
	if err != nil {
		logger.Printf("Command failed: %s", string(output))
		cleanupCmd := CommandFunc("docker", "rm", "-f", containerName)
		cleanupCmd.Run()
		return fmt.Errorf("rsync from /original/ to /solution3/ failed: %w", err)
	}
	logger.Printf("Command succeeded: rsync from /original/ to /solution3/ completed")

	rsyncCmd5 := "rsync -a --delete-during /original/ /solution4/"
	execCmd = CommandFunc("docker", "exec", containerName, "bash", "-c", rsyncCmd5)
	logger.Printf("Executing command: docker exec %s bash -c '%s'", containerName, rsyncCmd5)
	output, err = execCmd.CombinedOutput()
	if err != nil {
		logger.Printf("Command failed: %s", string(output))
		cleanupCmd := CommandFunc("docker", "rm", "-f", containerName)
		cleanupCmd.Run()
		return fmt.Errorf("rsync from /original/ to /solution4/ failed: %w", err)
	}
	logger.Printf("Command succeeded: rsync from /original/ to /solution4/ completed")

	// Mirror solution1 behavior for golden workspace
	rsyncCmdGolden := "rsync -a --delete-during /original/ /golden/"
	execCmd = CommandFunc("docker", "exec", containerName, "bash", "-c", rsyncCmdGolden)
	logger.Printf("Executing command: docker exec %s bash -c '%s'", containerName, rsyncCmdGolden)
	output, err = execCmd.CombinedOutput()
	if err != nil {
		logger.Printf("Command failed: %s", string(output))
		cleanupCmd := CommandFunc("docker", "rm", "-f", containerName)
		cleanupCmd.Run()
		return fmt.Errorf("rsync from /original/ to /golden/ failed: %w", err)
	}
	logger.Printf("Command succeeded: rsync from /original/ to /golden/ completed")

	// After successful execution, update file hashes
	if err := models.UpdateFileHashes(db, se.StepID, se.BasePath, config.Triggers.Files, logger); err != nil {
		logger.Printf("Error updating file hashes for step %d: %v", se.StepID, err)
	} else {
		logger.Printf("File hashes updated successfully for step %d", se.StepID)
	}

	return nil
}
