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
var stepProcessors = map[string]func(*sql.DB, *models.StepExec, *log.Logger) error{
	"docker_pull":  func(db *sql.DB, se *models.StepExec, logger *log.Logger) error { processDockerPullSteps(db); return nil },
	"docker_build": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error { processDockerBuildSteps(db, logger); return nil },
	"docker_run":   func(db *sql.DB, se *models.StepExec, logger *log.Logger) error { processDockerRunSteps(db); return nil },
	"docker_pool":  func(db *sql.DB, se *models.StepExec, logger *log.Logger) error { processDockerPoolSteps(db); return nil },
	"docker_shell": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error { processDockerShellSteps(db); return nil },
	"file_exists":  func(db *sql.DB, se *models.StepExec, logger *log.Logger) error { processFileExistsSteps(db); return nil },
	"rubrics_import": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error { processRubricsImportSteps(db); return nil },
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
			return ProcessRubricShellStep(db, se, logger)
		}
		// Otherwise (from executePendingSteps), run all rubric_shell steps.
		return processAllRubricShellSteps(db, logger)
	},
	"dynamic_rubric": func(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
		if se != nil && se.StepID != 0 {
			return ProcessDynamicRubricStep(db, se, logger)
		}
		// For now, running all dynamic_rubric steps is not supported via the general runner.
		return fmt.Errorf("processing all dynamic_rubric steps at once is not supported")
	},
}

// ProcessSteps is the main entry point for processing all pending steps.
func ProcessSteps(db *sql.DB) error {
	return executePendingSteps(db, stepProcessors)
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
func CreateStep(db *sql.DB, taskRef, title, settings string) (int, error) {
	return models.CreateStep(db, taskRef, title, settings)
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
func TreeSteps(db *sql.DB) error {
	// 1. Fetch all tasks to get their names
	taskRows, err := db.Query(`SELECT id, name FROM tasks ORDER BY id`)
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

	// 2. Fetch all steps
	stepRows, err := db.Query(`SELECT id, task_id, title, settings FROM steps ORDER BY id`)
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
		if steps, ok := taskSteps[taskID]; ok {
			if taskName, ok := taskNames[taskID]; ok {
				fmt.Printf("%d-%s\n", taskID, taskName)

				var rootNodes []*StepNode
				for _, node := range steps {
					if !isChild[node.ID] {
						rootNodes = append(rootNodes, node)
					}
				}

				sort.Slice(rootNodes, func(i, j int) bool {
					return rootNodes[i].ID < rootNodes[j].ID
				})

				printChildren(rootNodes, "")
			}
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
func ProcessSpecificStep(db *sql.DB, stepID int) error {
	// Fetch the full step details including task_id
	var stepExec models.StepExec
	err := db.QueryRow("SELECT id, task_id, title, settings FROM steps WHERE id = $1", stepID).Scan(&stepExec.StepID, &stepExec.TaskID, &stepExec.Title, &stepExec.Settings)
	if err != nil {
		return fmt.Errorf("failed to fetch step %d: %w", stepID, err)
	}

	// Fetch the LocalPath from the tasks table using taskID
	var localPath string
	err = db.QueryRow("SELECT local_path FROM tasks WHERE id = $1", stepExec.TaskID).Scan(&localPath)
	if err != nil {
		return fmt.Errorf("failed to fetch task local path for task ID %d: %w", stepExec.TaskID, err)
	}
	stepExec.LocalPath = localPath

	// Determine the step type from settings
	var settings map[string]json.RawMessage
	err = json.Unmarshal([]byte(stepExec.Settings), &settings)
	if err != nil {
		return fmt.Errorf("failed to unmarshal settings for step %d: %w", stepID, err)
	}

	var stepType string
	for key := range settings {
		if _, exists := stepProcessors[key]; exists {
			stepType = key
			break
		}
	}

	if stepType == "" {
		return fmt.Errorf("unknown or no matching step type found for step %d", stepID)
	}

	stepLogger := log.New(os.Stdout, fmt.Sprintf("STEP %d [%s]: ", stepID, stepType), log.Ldate|log.Ltime|log.Lshortfile)

	if processor, exists := stepProcessors[stepType]; exists {
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
