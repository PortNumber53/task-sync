package internal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/lib/pq"
)

// stepExec holds the necessary information for executing a step.
// It's populated from a database query joining steps and tasks.
type stepExec struct {
	StepID    int
	TaskID    int
	Settings  string
	LocalPath string
}

// --- Top-level Config Structs ---

// DockerBuildConfig represents the configuration for a docker_build step.
type DockerBuildConfig struct {
	DockerBuild DockerBuild `json:"docker_build"`
}

// DockerPullConfig represents the configuration for a docker_pull step.
type DockerPullConfig struct {
	DockerPull DockerPull `json:"docker_pull"`
}

// DockerRunConfig represents the configuration for a docker_run step.
type DockerRunConfig struct {
	DockerRun DockerRun `json:"docker_run"`
}

// DockerPoolConfig represents the configuration for a docker_pool step.

type DockerPoolConfig struct {
	DockerPool DockerPool `json:"docker_pool"`
}

// FileExistsConfig represents the configuration for a file_exists step.
type FileExistsConfig struct {
	FileExists []string `json:"file_exists"`
}

// DockerRubricsConfig represents the configuration for a docker_rubrics step.
type DockerRubricsConfig struct {
	DockerRubrics struct {
		Files     []string          `json:"files"`
		Hashes    map[string]string `json:"hashes"`
		ImageID   string            `json:"image_id"`
		ImageTag  string            `json:"image_tag"`
		DependsOn []Dependency      `json:"depends_on,omitempty"`
	} `json:"docker_rubrics"`
}

// DockerShellConfig represents the configuration for a docker_shell step.
type DockerShellConfig struct {
	DockerShell struct {
		Command []map[string]string `json:"command"`
		Docker  struct {
			ContainerID   string `json:"container_id"`
			ContainerName string `json:"container_name"`
			ImageID       string `json:"image_id"`
			ImageTag      string `json:"image_tag"`
		} `json:"docker"`
		DependsOn []Dependency `json:"depends_on,omitempty"`
	} `json:"docker_shell"`
}

// DynamicLabConfig represents the configuration for a dynamic_lab step.
type DynamicLabConfig struct {
	DynamicLab struct {
		RubricFile  string            `json:"rubric_file"`
		Files       interface{}       `json:"files,omitempty"`
		Hashes      map[string]string `json:"hashes,omitempty"`
		Environment struct {
			Docker bool `json:"docker"`
		} `json:"environment"`
		DependsOn []Dependency `json:"depends_on,omitempty"`
	} `json:"dynamic_lab"`
}

// --- Detail Structs ---

// Dependency defines a dependency on another step.
type Dependency struct {
	ID int `json:"id"`
}

type DependencyHolder struct {
	DependsOn []Dependency `json:"depends_on"`
}

type StepConfigHolder struct {
	DockerBuild *DependencyHolder `json:"docker_build,omitempty"`
	DockerRun   *DependencyHolder `json:"docker_run,omitempty"`
	DockerPool  *DependencyHolder `json:"docker_pool,omitempty"`
	DockerShell *DependencyHolder `json:"docker_shell,omitempty"`
}

// DockerBuild contains details for the docker build process.
type DockerBuild struct {
	Context   string            `json:"context"`
	Tags      []string          `json:"tags"`
	ImageTag  string            `json:"image_tag"`
	Params    []string          `json:"params"`
	Files     map[string]string `json:"files"`
	ImageID   string            `json:"image_id"`
	DependsOn []Dependency      `json:"depends_on,omitempty"`
}

// DockerPull contains details for the docker pull process.
type DockerPull struct {
	Image            string       `json:"image"`
	ImageTag         string       `json:"image_tag"`
	ImageID          string       `json:"image_id"`
	PreventRunBefore string       `json:"prevent_run_before,omitempty"`
	DependsOn        []Dependency `json:"depends_on,omitempty"`
}

// DockerRun contains details for the docker run process.
type DockerRun struct {
	Image         string       `json:"image"`
	ImageTag      string       `json:"image_tag"`
	ImageID       string       `json:"image_id"`
	Command       []string     `json:"command"`
	ContainerID   string       `json:"container_id"`
	ContainerName string       `json:"container_name"`
	Parameters    []string     `json:"parameters"`
	DependsOn     []Dependency `json:"depends_on,omitempty"`
	KeepForever   bool         `json:"keep_forever,omitempty"`
}

// DockerPool contains details for the docker pool process.
type DockerPool struct {
	Image       string          `json:"image"`
	ImageTag    string          `json:"image_tag"`
	ImageID     string          `json:"image_id"`
	PoolSize    int             `json:"pool_size"`
	Containers  []ContainerInfo `json:"containers"`
	Parameters  []string        `json:"parameters"`
	DependsOn   []Dependency    `json:"depends_on,omitempty"`
	KeepForever bool            `json:"keep_forever,omitempty"`
}

// ContainerInfo holds information about a single container in a pool.
type ContainerInfo struct {
	ContainerID   string `json:"container_id"`
	ContainerName string `json:"container_name"`
}

// CreateStep inserts a new step for a task and returns the new step's ID.
func CreateStep(db *sql.DB, taskRef, title, settings string) (int, error) {
	var js interface{}
	if err := json.Unmarshal([]byte(settings), &js); err != nil {
		return 0, fmt.Errorf("settings must be valid JSON: %w", err)
	}

	var taskID int
	if id, err := strconv.Atoi(taskRef); err == nil {
		err = db.QueryRow("SELECT id FROM tasks WHERE id = $1", id).Scan(&taskID)
		if err != nil {
			return 0, fmt.Errorf("no task found with id %d", id)
		}
	} else {
		err = db.QueryRow("SELECT id FROM tasks WHERE name = $1", taskRef).Scan(&taskID)
		if err != nil {
			return 0, fmt.Errorf("no task found with name '%s'", taskRef)
		}
	}

	var newStepID int
	err := db.QueryRow(`INSERT INTO steps (task_id, title, settings, created_at, updated_at) VALUES ($1, $2, $3::jsonb, now(), now()) RETURNING id`, taskID, title, settings).Scan(&newStepID)
	if err != nil {
		return 0, err
	}
	return newStepID, nil
}

// ListSteps prints all steps in the DB. If full is true, prints settings column too.
func ListSteps(db *sql.DB, full bool) error {
	var rows *sql.Rows
	var err error
	if full {
		rows, err = db.Query(`SELECT id, task_id, title, settings, created_at, updated_at FROM steps ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		fmt.Printf("%-4s %-7s %-20s %-30s %-25s %-25s\n", "ID", "TaskID", "Title", "Settings", "Created At", "Updated At")
		for rows.Next() {
			var id, taskID int
			var title, settings, createdAt, updatedAt string
			if err := rows.Scan(&id, &taskID, &title, &settings, &createdAt, &updatedAt); err != nil {
				return err
			}
			fmt.Printf("%-4d %-7d %-20s %-30s %-25s %-25s\n", id, taskID, title, settings, createdAt, updatedAt)
		}
	} else {
		rows, err = db.Query(`SELECT id, task_id, title, created_at, updated_at FROM steps ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		fmt.Printf("%-4s %-7s %-20s %-25s %-25s\n", "ID", "TaskID", "Title", "Created At", "Updated At")
		for rows.Next() {
			var id, taskID int
			var title, createdAt, updatedAt string
			if err := rows.Scan(&id, &taskID, &title, &createdAt, &updatedAt); err != nil {
				return err
			}
			fmt.Printf("%-4d %-7d %-20s %-25s %-25s\n", id, taskID, title, createdAt, updatedAt)
		}
	}
	return nil
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
func checkDependencies(db *sql.DB, stepID int, stepLogger *log.Logger) (bool, error) {
	var dependsOnJSON string
	// Correctly extract the top-level 'depends_on' key
	err := db.QueryRow(`
		SELECT COALESCE(
			(SELECT value FROM jsonb_each(settings) WHERE key = 'depends_on'),
			'[]'::jsonb
		)::text
		FROM steps WHERE id = $1
	`, stepID).Scan(&dependsOnJSON)

	if err != nil {
		if err == sql.ErrNoRows {
			return true, nil // Step not found, no dependencies to check.
		}
		return false, fmt.Errorf("could not retrieve dependencies for step %d: %w", stepID, err)
	}

	var dependsOn []struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal([]byte(dependsOnJSON), &dependsOn); err != nil {
		// It's possible the settings don't have a top-level depends_on, but a nested one.
		// This is a fallback to a more general, but less efficient, parsing.
		var settingsMap map[string]json.RawMessage
		if err2 := json.Unmarshal([]byte(dependsOnJSON), &settingsMap); err2 != nil {
			return false, fmt.Errorf("could not parse dependencies for step %d: %w", stepID, err)
		}
		if val, ok := settingsMap["depends_on"]; ok {
			if err3 := json.Unmarshal(val, &dependsOn); err3 != nil {
				return false, fmt.Errorf("could not parse nested dependencies for step %d: %w", stepID, err3)
			}
		}
	}

	if len(dependsOn) == 0 {
		return true, nil
	}

	depIDs := make([]int, len(dependsOn))
	for i, dep := range dependsOn {
		depIDs[i] = dep.ID
	}

	query := `
		SELECT NOT EXISTS (
			SELECT 1
			FROM steps s
			WHERE s.id = ANY($1::int[])
			AND (s.results->>'result' IS NULL OR s.results->>'result' != 'success')
		)`

	stepLogger.Printf("Step %d: running dependency check query: %s with args %v\n", stepID, query, depIDs)

	var allDepsCompleted bool
	err = db.QueryRow(query, pq.Array(depIDs)).Scan(&allDepsCompleted)
	stepLogger.Printf("Step %d: dependency check result: %v, error: %v\n", stepID, allDepsCompleted, err)

	return allDepsCompleted, err
}

// ProcessSteps is the main entry point for processing all pending steps.
func ProcessSteps(db *sql.DB) error {
	stepProcessors := map[string]func(*sql.DB) error{
		"dynamic_lab":    processDynamicLabSteps,
		"docker_pull":    func(db *sql.DB) error { processDockerPullSteps(db); return nil },
		"docker_build":   func(db *sql.DB) error { processDockerBuildSteps(db); return nil },
		"docker_run":     processDockerRunSteps,
		"docker_pool":    processDockerPoolSteps,
		"docker_shell":   func(db *sql.DB) error { processDockerShellSteps(db); return nil },
		"docker_rubrics": func(db *sql.DB) error { processDockerRubricsSteps(db); return nil },
		"file_exists":    func(db *sql.DB) error { processFileExistsSteps(db); return nil },
	}

	return executePendingSteps(db, stepProcessors)
}

func executePendingSteps(db *sql.DB, stepProcessors map[string]func(*sql.DB) error) error {
	// Process dynamic rubrics first to generate other steps
	if err := processDynamicRubricSteps(db); err != nil {
		log.Printf("Error processing dynamic_rubric steps: %v", err)
	}

	// Iterate over the map and call each function
	for stepType, processorFunc := range stepProcessors {
		if err := processorFunc(db); err != nil {
			log.Printf("Error processing %s steps: %v", stepType, err)
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

// ClearStepResults clears the results for a step
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
			var deps []Dependency
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
				DependsOn []Dependency `json:"depends_on"`
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
