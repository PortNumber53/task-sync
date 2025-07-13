package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// stepProcessors maps step types to their respective processor functions with consistent signature using wrappers.
// getStepProcessors returns the step processor map, parameterized by force flag for rubric_shell steps.
func getStepProcessors(force bool) map[string]func(*sql.DB, *models.StepExec, *log.Logger) error {
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
		"docker_volume_pool": ProcessDockerVolumePoolStep,
		"file_exists": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
			if se != nil && se.StepID != 0 {
				return ProcessFileExistsStep(db, se, logger)
			}
			return processAllFileExistsSteps(db, logger)
		},
		"rubrics_import": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error { return processRubricsImportSteps(db, se.StepID) },
		"rubric_set": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
			// If a specific step is provided (from ProcessSpecificStep), run only that.
			if se != nil && se.StepID != 0 {
				return ProcessRubricSetStep(db, se, logger)
			}
			// Otherwise (from executePendingSteps), run all rubric_set steps.
			return processAllRubricSetSteps(db, logger)
		},
		"rubric_shell": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
			// If a specific step is provided (from ProcessSpecificStep), run only that.
			if se != nil && se.StepID != 0 {
				return ProcessRubricShellStep(db, se, logger, force)
			}
			// Otherwise (from executePendingSteps), run all rubric_shell steps.
			return processAllRubricShellSteps(db, logger)
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
	return executePendingSteps(db, getStepProcessors(false))
}

// ProcessStepsForTask processes all steps for a specific task by ID, respecting dependencies.
func ProcessStepsForTask(db *sql.DB, taskID int) error {
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
		if err := ProcessSpecificStep(db, stepID, false); err != nil {
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
func ProcessSpecificStep(db *sql.DB, stepID int, force bool) error {
	// Fetch the full step details including task_id
	var stepExec models.StepExec
	err := db.QueryRow("SELECT id, task_id, title, settings FROM steps WHERE id = $1", stepID).Scan(&stepExec.StepID, &stepExec.TaskID, &stepExec.Title, &stepExec.Settings)
	if err != nil {
		return fmt.Errorf("failed to fetch step %d: %w", stepID, err)
	}

	// Fetch the LocalPath and Status from the tasks table using taskID
	var localPath, status string
	err = db.QueryRow("SELECT local_path, status FROM tasks WHERE id = $1", stepExec.TaskID).Scan(&localPath, &status)
	if err != nil {
		return fmt.Errorf("failed to fetch task local path/status for task ID %d: %w", stepExec.TaskID, err)
	}
	if status != "active" {
		log.Printf("STEP %d: Skipping execution because parent task %d status is not active (status=%q)", stepID, stepExec.TaskID, status)
		return nil
	}
	stepExec.LocalPath = localPath

	// Determine the step type from settings
	var settings map[string]json.RawMessage
	err = json.Unmarshal([]byte(stepExec.Settings), &settings)
	if err != nil {
		return fmt.Errorf("failed to unmarshal settings for step %d: %w", stepID, err)
	}

	var stepType string
	processors := getStepProcessors(force)
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
