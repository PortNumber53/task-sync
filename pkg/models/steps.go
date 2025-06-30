package models

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lib/pq"
)

// StepExec holds the necessary information for executing a step.
// It's populated from a database query joining steps and tasks.
type StepExec struct {
	StepID    int
	TaskID    int
	Title     string
	Settings  string
	LocalPath string
}

// --- Top-level Config Structs ---

// DockerBuildConfig represents the configuration for a docker_build step.
type DockerBuildConfig struct {
	ImageID   string            `json:"image_id,omitempty"`
	ImageTag  string            `json:"image_tag,omitempty"`
	DependsOn []Dependency      `json:"depends_on,omitempty"`
	Files     map[string]string `json:"files,omitempty"`
}

type DockerPullConfig struct {
	ImageID   string       `json:"image_id,omitempty"`
	ImageTag  string       `json:"image_tag,omitempty"`
	DependsOn []Dependency `json:"depends_on,omitempty"`
	PreventRunBefore string `json:"prevent_run_before,omitempty"`
}

type DockerRunConfig struct {
	ImageID       string       `json:"image_id,omitempty"`
	ImageTag      string       `json:"image_tag,omitempty"`
	DependsOn     []Dependency `json:"depends_on,omitempty"`
	ContainerID   string       `json:"container_id,omitempty"`
	ContainerName string       `json:"container_name,omitempty"`
	Parameters    []string     `json:"parameters,omitempty"`
	KeepForever   bool         `json:"keep_forever,omitempty"`
}

type DockerPoolConfig struct {
	ImageID      string         `json:"image_id,omitempty"`
	ImageTag     string         `json:"image_tag,omitempty"`
	DependsOn    []Dependency   `json:"depends_on,omitempty"`
	PoolSize     int            `json:"pool_size,omitempty"`
	Containers   []ContainerInfo `json:"containers,omitempty"`
	Parameters   []string       `json:"parameters,omitempty"`
	KeepForever  bool           `json:"keep_forever,omitempty"`
}

type DockerShellConfig struct {
	Docker struct {
		ImageID  string `json:"image_id,omitempty"`
		ImageTag string `json:"image_tag,omitempty"`
	} `json:"docker,omitempty"`
	DependsOn []Dependency `json:"depends_on,omitempty"`
	Command   []map[string]string `json:"command,omitempty"`
}

type Criterion struct {
	Title       string
	Score       int
	Required    bool
	Rubric      string
	HeldOutTest string
}

func ParseRubric(filePath string) ([]Criterion, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	text := string(content)
	// Split the text by criteria sections, which start with "### crit-"
	critSections := regexp.MustCompile(`(?m)^###\\s*(crit-\\d+.*)`).Split(text, -1)
	if len(critSections) < 2 {
		return nil, fmt.Errorf("no criteria sections found in %s", filePath)
	}

	criteria := []Criterion{}
	for _, section := range critSections[1:] { // Skip the part before the first criterion
		// Extract title: first line after ### crit-X
		titleMatch := regexp.MustCompile(`(?m)^\\s*([^\\n]+)`).FindStringSubmatch(section)
		if len(titleMatch) < 2 {
			continue
		}
		crit := Criterion{Title: strings.TrimSpace(titleMatch[1])}

		// Extract score: looks for "**Score:** X"
		scoreMatch := regexp.MustCompile(`\\*\\*Score:\\*\\*\\s*(\\d+)`).FindStringSubmatch(section)
		if len(scoreMatch) > 1 {
			score, err := strconv.Atoi(scoreMatch[1])
			if err == nil {
				crit.Score = score
			}
		}

		// Extract required: looks for "**Required:** (Yes|No)"
		requiredMatch := regexp.MustCompile(`\\*\\*Required:\\*\\*\\s*(Yes|No)`).FindStringSubmatch(section)
		if len(requiredMatch) > 1 && requiredMatch[1] == "Yes" {
			crit.Required = true
		}

		// Extract rubric: looks for "**Rubric:**\n` + "```bash\n...```"
		rubricRegex := regexp.MustCompile("(?s)\\\\*\\\\*Rubric:\\\\*\\\\*:\\\\n```(?:bash)?\\\\n(.*?)\\\\n```")
		rubricMatch := rubricRegex.FindStringSubmatch(section)
		if len(rubricMatch) > 1 {
			crit.Rubric = strings.TrimSpace(rubricMatch[1])
		}

		// Parse held-out test: looks for "**Held-out tests**:\n` + "```bash\n...```"
		testRegex := regexp.MustCompile("(?s)\\\\*\\\\*Held-out tests\\\\*\\\\*:\\\\n```(?:bash)?\\\\n(.*?)\\\\n```")
		testMatch := testRegex.FindStringSubmatch(section)
		if len(testMatch) > 1 {
			crit.HeldOutTest = strings.TrimSpace(testMatch[1])
		}

		criteria = append(criteria, crit)
	}

	return criteria, nil
}

type DynamicRubricConfig struct {
	DynamicRubric struct {
		Files       map[string]string `json:"files,omitempty"`
		Hashes      map[string]string `json:"hashes,omitempty"`
		Rubrics     []string          `json:"rubrics,omitempty"`
		Hash        string            `json:"hash,omitempty"`
		Environment struct {
			Docker   bool   `json:"docker"`
			ImageID  string `json:"image_id,omitempty"`
			ImageTag string `json:"image_tag,omitempty"`
		} `json:"environment"`
		DependsOn []Dependency `json:"depends_on,omitempty"`
	} `json:"dynamic_rubric"`
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

// DependencyHolder is a helper struct for unmarshaling nested dependencies
type DependencyHolder struct {
	DependsOn []Dependency `json:"depends_on,omitempty"`
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
		ImageID       string       `json:"image_id"`
		Command       []string     `json:"command"`
		ContainerID   string       `json:"container_id"`
		ContainerName string       `json:"container_name"`
		Parameters    []string     `json:"parameters"`
		DependsOn     []Dependency `json:"depends_on,omitempty"`
	} `json:"dynamic_lab"`
}

type StepConfigHolder struct {
	DockerBuild   *DockerBuildConfig   `json:"docker_build,omitempty"`
	DockerPull    *DockerPullConfig    `json:"docker_pull,omitempty"`
	DockerRun     *DockerRunConfig     `json:"docker_run,omitempty"`
	DockerPool    *DockerPoolConfig    `json:"docker_pool,omitempty"`
	DockerShell   *DockerShellConfig   `json:"docker_shell,omitempty"`
	DynamicRubric *DynamicRubricConfig `json:"dynamic_rubric,omitempty"`
	DynamicLab    *DynamicLabConfig    `json:"dynamic_lab,omitempty"`
	FileExists    *FileExistsConfig    `json:"file_exists,omitempty"`
	DockerRubrics *DockerRubricsConfig `json:"docker_rubrics,omitempty"`
}

// --- Detail Structs ---

// Dependency defines a dependency on another step.
type Dependency struct {
	ID int `json:"id"`
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

// StepNode represents a step in a task for tree display.
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

// ClearStepResults clears the results for a step
func ClearStepResults(db *sql.DB, stepID int) error {
	result, err := db.Exec(
		"UPDATE steps SET results = NULL, updated_at = NOW() WHERE id = $1",
		stepID,
	)
	if err != nil {
		return fmt.Errorf("failed to clear results for step %d: %w", stepID, err)
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

// StoreStepResult stores the execution result of a step
func StoreStepResult(db *sql.DB, stepID int, result map[string]interface{}) error {
	resJson, _ := json.Marshal(result)
	resultExec, err := db.Exec(`UPDATE steps SET results = $1::jsonb, updated_at = now() WHERE id = $2`, string(resJson), stepID)
	if err != nil {
		return fmt.Errorf("failed to update results for step %d: %w", stepID, err)
	}
	rowsAffected, err := resultExec.RowsAffected()
	if err != nil {
		return fmt.Errorf("error checking affected rows: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("no step found with ID %d", stepID)
	}
	return nil
}

// CheckDependencies checks if all dependencies for a given step are met.
func CheckDependencies(db *sql.DB, stepExec *StepExec) (bool, error) {
	// Query to get the 'depends_on' array from the step's settings
	var dependsOnJSON string
	query := `SELECT COALESCE((SELECT value FROM jsonb_each(settings) WHERE key = 'depends_on'), '[]'::jsonb)::text FROM steps WHERE id = $1`
	err := db.QueryRow(query, stepExec.StepID).Scan(&dependsOnJSON)
	if err != nil {
		return false, fmt.Errorf("failed to query depends_on for step %d: %w", stepExec.StepID, err)
	}

	var dependencies []Dependency
	if err := json.Unmarshal([]byte(dependsOnJSON), &dependencies); err != nil {
		return false, fmt.Errorf("failed to unmarshal depends_on JSON for step %d: %w", stepExec.StepID, err)
	}

	if len(dependencies) == 0 {
		return true, nil // No dependencies, so they are met
	}

	// Extract dependency IDs
	var depIDs []int
	for _, dep := range dependencies {
		depIDs = append(depIDs, dep.ID)
	}

	// Query to check if any dependent steps are not in a 'success' state
	// This includes steps with NULL results or results where 'result' is not 'success'
	checkQuery := `SELECT NOT EXISTS(
		SELECT 1 FROM steps s
		WHERE s.id = ANY($1::int[])
		AND (s.results->>'result' IS NULL OR s.results->>'result' != 'success')
	)`

	var allDependenciesMet bool
	err = db.QueryRow(checkQuery, pq.Array(depIDs)).Scan(&allDependenciesMet)
	if err != nil {
		return false, fmt.Errorf("failed to check status of dependent steps for step %d: %w", stepExec.StepID, err)
	}

	return allDependenciesMet, nil
}

// GetStepInfo retrieves the settings for a given step ID.
func GetStepInfo(db *sql.DB, stepID int) (string, error) {
	var settings string
	err := db.QueryRow("SELECT settings FROM steps WHERE id = $1", stepID).Scan(&settings)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("step with ID %d not found", stepID)
		}
		return "", fmt.Errorf("error retrieving settings for step %d: %w", stepID, err)
	}
	return settings, nil
}

// getStepsByType retrieves all steps of a given type.
func GetStepsByType(db *sql.DB, stepType string) ([]StepExec, error) {
	rows, err := db.Query("SELECT s.id, s.task_id, s.title, s.settings, COALESCE(s.local_path, '') FROM steps s WHERE s.settings->>$1 IS NOT NULL ORDER BY s.id", stepType)
	if err != nil {
		return nil, fmt.Errorf("error querying steps by type %s: %w", stepType, err)
	}
	defer rows.Close()

	var steps []StepExec
	for rows.Next() {
		var s StepExec
		var localPath sql.NullString
		if err := rows.Scan(&s.StepID, &s.TaskID, &s.Title, &s.Settings, &localPath); err != nil {
			return nil, fmt.Errorf("error scanning step: %w", err)
		}
		s.LocalPath = localPath.String
		steps = append(steps, s)
	}
	return steps, nil
}

// deleteGeneratedSteps deletes steps that were generated by a dynamic_lab step.
func DeleteGeneratedSteps(db *sql.DB, generatedByStepID int) error {
	_, err := db.Exec("DELETE FROM steps WHERE settings->'generated_by' ? $1", strconv.Itoa(generatedByStepID))
	if err != nil {
		return fmt.Errorf("failed to delete generated steps for %d: %w", generatedByStepID, err)
	}
	return nil
}

// generatedStepsExist checks if there are any steps generated by a given dynamic_lab step.
func GeneratedStepsExist(db *sql.DB, generatedByStepID int) (bool, error) {
	var exists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM steps WHERE settings->'generated_by' ? $1)", fmt.Sprintf("%d", generatedByStepID)).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check for generated steps for %d: %w", generatedByStepID, err)
	}
	return exists, nil
}

// CreateStep inserts a new step for a task and returns the new step's ID.
func CreateStep(db *sql.DB, taskRef, title, settings string) (int, error) {
	var taskID int
	err := db.QueryRow("SELECT id FROM tasks WHERE ref = $1", taskRef).Scan(&taskID)
	if err != nil {
		return 0, fmt.Errorf("error finding task by ref: %w", err)
	}

	var stepID int
	err = db.QueryRow(
		`INSERT INTO steps (task_id, title, settings, created_at, updated_at)
		 VALUES ($1, $2, $3::jsonb, now(), now())
		 RETURNING id`,
		taskID, title, settings,
	).Scan(&stepID)
	if err != nil {
		return 0, fmt.Errorf("creating step failed: %w", err)
	}

	return stepID, nil
}

// ListSteps prints all steps in the DB. If full is true, prints settings column too.
func ListSteps(db *sql.DB, full bool) error {
	var query string
	if full {
		query = "SELECT id, task_id, title, settings FROM steps ORDER BY id"
	} else {
		query = "SELECT id, task_id, title FROM steps ORDER BY id"
	}

	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("querying steps failed: %w", err)
	}
	defer rows.Close()

	fmt.Println("Steps:")
	for rows.Next() {
		var id, taskID int
		var title string
		var settings sql.NullString // Use NullString to handle potential NULL settings

		if full {
			if err := rows.Scan(&id, &taskID, &title, &settings); err != nil {
				return err
			}
			fmt.Printf("  ID: %d, TaskID: %d, Title: %s, Settings: %s\n", id, taskID, title, settings.String)
		} else {
			if err := rows.Scan(&id, &taskID, &title); err != nil {
				return err
			}
			fmt.Printf("  ID: %d, TaskID: %d, Title: %s\n", id, taskID, title)
		}
	}
	return nil
}

// findImageDetailsRecursive is a helper function to recursively find image_id and image_tag
// in the dependency chain of a step.
func FindImageDetailsRecursive(db *sql.DB, stepID int, visited map[int]bool, stepLogger *log.Logger) (imageID, imageTag string, err error) {
	if visited[stepID] {
		return "", "", nil // Already visited this step, break recursion
	}
	visited[stepID] = true

	stepLogger.Printf("Searching for image details in step %d\n", stepID)

	settingsStr, err := GetStepInfo(db, stepID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get step info for %d: %w", stepID, err)
	}

	var configHolder StepConfigHolder
	if err := json.Unmarshal([]byte(settingsStr), &configHolder); err != nil {
		return "", "", fmt.Errorf("failed to unmarshal settings for step %d: %w", stepID, err)
	}

	// Check current step for image details
	if configHolder.DockerBuild != nil && configHolder.DockerBuild.ImageID != "" && configHolder.DockerBuild.ImageTag != "" {
		stepLogger.Printf("Found image_id '%s' and image_tag '%s' from build dependency step %d\n", configHolder.DockerBuild.ImageID, configHolder.DockerBuild.ImageTag, stepID)
		return configHolder.DockerBuild.ImageID, configHolder.DockerBuild.ImageTag, nil
	} else if configHolder.DockerPull != nil && configHolder.DockerPull.ImageID != "" && configHolder.DockerPull.ImageTag != "" {
		stepLogger.Printf("Found image_id '%s' and image_tag '%s' from pull dependency step %d\n", configHolder.DockerPull.ImageID, configHolder.DockerPull.ImageTag, stepID)
		return configHolder.DockerPull.ImageID, configHolder.DockerPull.ImageTag, nil
	} else if configHolder.DockerRun != nil && configHolder.DockerRun.ImageID != "" && configHolder.DockerRun.ImageTag != "" {
		stepLogger.Printf("Found image_id '%s' and image_tag '%s' from run dependency step %d\n", configHolder.DockerRun.ImageID, configHolder.DockerRun.ImageTag, stepID)
		return configHolder.DockerRun.ImageID, configHolder.DockerRun.ImageTag, nil
	} else if configHolder.DockerPool != nil && configHolder.DockerPool.ImageID != "" && configHolder.DockerPool.ImageTag != "" {
		stepLogger.Printf("Found image_id '%s' and image_tag '%s' from pool dependency step %d\n", configHolder.DockerPool.ImageID, configHolder.DockerPool.ImageTag, stepID)
		return configHolder.DockerPool.ImageID, configHolder.DockerPool.ImageTag, nil
	} else if configHolder.DockerShell != nil && configHolder.DockerShell.Docker.ImageID != "" && configHolder.DockerShell.Docker.ImageTag != "" {
		stepLogger.Printf("Found image_id '%s' and image_tag '%s' from shell dependency step %d\n", configHolder.DockerShell.Docker.ImageID, configHolder.DockerShell.Docker.ImageTag, stepID)
		return configHolder.DockerShell.Docker.ImageID, configHolder.DockerShell.Docker.ImageTag, nil
	} else if configHolder.DynamicRubric != nil && configHolder.DynamicRubric.DynamicRubric.Environment.ImageID != "" && configHolder.DynamicRubric.DynamicRubric.Environment.ImageTag != "" {
		stepLogger.Printf("Found image_id '%s' and image_tag '%s' from dynamic rubric dependency step %d\n", configHolder.DynamicRubric.DynamicRubric.Environment.ImageID, configHolder.DynamicRubric.DynamicRubric.Environment.ImageTag, stepID)
		return configHolder.DynamicRubric.DynamicRubric.Environment.ImageID, configHolder.DynamicRubric.DynamicRubric.Environment.ImageTag, nil
	}

	// Recursively check dependencies
	var dependencies []Dependency

	// Try to unmarshal top-level depends_on
	var topLevel struct {
		DependsOn []Dependency `json:"depends_on"`
	}
	if err := json.Unmarshal([]byte(settingsStr), &topLevel); err == nil {
		dependencies = topLevel.DependsOn
	} else {
		// Fallback for nested depends_on (e.g., within docker_build, docker_run, etc.)
		var rawMap map[string]json.RawMessage
		if err := json.Unmarshal([]byte(settingsStr), &rawMap); err != nil {
			return "", "", fmt.Errorf("failed to unmarshal settings into raw map for step %d: %w", stepID, err)
		}
		for _, rawVal := range rawMap {
			var nestedDep struct {
				DependsOn []Dependency `json:"depends_on"`
			}
			if err := json.Unmarshal(rawVal, &nestedDep); err == nil {
				dependencies = append(dependencies, nestedDep.DependsOn...)
			}
		}
	}

	for _, dep := range dependencies {
		imgID, imgTag, err := FindImageDetailsRecursive(db, dep.ID, visited, stepLogger)
		if err != nil {
			return "", "", err
		}
		if imgID != "" && imgTag != "" {
			stepLogger.Printf("Found ImageID '%s' and ImageTag '%s' from dependency step %d\n", imgID, imgTag, dep.ID)
			return imgID, imgTag, nil
		}
	}

	return "", "", nil
}

// getDockerImageID is a helper function to extract image_id and image_tag from a step's settings.
// It recursively checks dependencies if not found in the current step.
func GetDockerImageID(db *sql.DB, stepID int, stepLogger *log.Logger) (imageID, imageTag string, err error) {
	visited := make(map[int]bool)
	imgID, imgTag, err := FindImageDetailsRecursive(db, stepID, visited, stepLogger)
	if err != nil {
		return "", "", err
	}
	if imgID != "" && imgTag != "" {
		stepLogger.Printf("Prioritizing image_id '%s' and image_tag '%s' from build dependency.\n", imgID, imgTag)
		return imgID, imgTag, nil
	}

	return "", "", nil
}

// GetContainerDetails retrieves container ID and name from a step's settings.
func GetContainerDetails(db *sql.DB, stepID int, stepLogger *log.Logger) (containerID, containerName string, err error) {
	settingsStr, err := GetStepInfo(db, stepID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get step info for %d: %w", stepID, err)
	}

	var configHolder StepConfigHolder
	if err := json.Unmarshal([]byte(settingsStr), &configHolder); err != nil {
		return "", "", fmt.Errorf("failed to unmarshal settings for step %d: %w", stepID, err)
	}

	switch {
	case configHolder.DockerRun != nil:
		// DockerRun steps don't have container info in their config
		// We'll need to check dependencies instead
		stepLogger.Printf("DockerRun step %d doesn't have container info in config, checking dependencies\n", stepID)
	case configHolder.DockerPool != nil:
		// For DockerPool steps, we need to unmarshal into DockerPool struct to get container info
		var pool DockerPool
		if err := json.Unmarshal([]byte(settingsStr), &pool); err == nil {
			if len(pool.Containers) > 0 {
				// Return the first container in the pool
				container := pool.Containers[0]
				stepLogger.Printf("Found container_id '%s' and container_name '%s' in pool step %d\n",
					container.ContainerID, container.ContainerName, stepID)
				return container.ContainerID, container.ContainerName, nil
			}
		}
	}

	// If we got here, we need to check dependencies
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(settingsStr), &rawMap); err != nil {
		return "", "", fmt.Errorf("failed to unmarshal settings as map for step %d: %w", stepID, err)
	}

	var dependencies []Dependency
	if dependsOn, ok := rawMap["depends_on"]; ok {
		var deps []Dependency
		if err := json.Unmarshal(dependsOn, &deps); err == nil {
			dependencies = append(dependencies, deps...)
		}
	}

	// Check for nested depends_on in any object
	for _, rawVal := range rawMap {
		var nestedDep struct {
			DependsOn []Dependency `json:"depends_on"`
		}
		if err := json.Unmarshal(rawVal, &nestedDep); err == nil {
			dependencies = append(dependencies, nestedDep.DependsOn...)
		}
	}

	for _, dep := range dependencies {
		contID, contName, err := GetContainerDetails(db, dep.ID, stepLogger)
		if err != nil {
			return "", "", err
		}
		if contID != "" && contName != "" {
			stepLogger.Printf("Found container_id '%s' and container_name '%s' from dependency step %d\n", contID, contName, dep.ID)
			return contID, contName, nil
		}
	}

	return "", "", nil
}

// GetContainerID is a helper function to extract container_id from a step's settings.
// It recursively checks dependencies if not found in the current step.
func GetContainerID(db *sql.DB, stepID int, stepLogger *log.Logger) (containerID string, err error) {
	contID, _, err := GetContainerDetails(db, stepID, stepLogger)
	if err != nil {
		return "", err
	}
	return contID, nil
}

// GetContainerName is a helper function to extract container_name from a step's settings.
// It recursively checks dependencies if not found in the current step.
func GetContainerName(db *sql.DB, stepID int, stepLogger *log.Logger) (containerName string, err error) {
	_, contName, err := GetContainerDetails(db, stepID, stepLogger)
	if err != nil {
		return "", err
	}
	return contName, nil
}

// getSHA256 calculates the SHA256 hash of a file.
func GetSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer file.Close()

	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", fmt.Errorf("failed to calculate hash for %s: %w", filePath, err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// RunRubric checks if the rubric file has changed and parses it.
// It returns the parsed criteria, the new hash, a boolean indicating if the content has changed, and an error.
func RunRubric(localPath, file, oldHash string) ([]Criterion, string, bool, error) {
	filePath := filepath.Join(localPath, file)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil, "", false, fmt.Errorf("rubric file %s does not exist", filePath)
	}

	currentHash, err := GetSHA256(filePath)
	if err != nil {
		return nil, "", false, fmt.Errorf("failed to calculate hash for %s: %w", filePath, err)
	}

	// Always parse the rubric to get the criteria
	criteria, err := ParseRubric(filePath)
	if err != nil {
		// If parsing fails, we can't determine 'changed' status reliably based on content,
		// but we can still return the hash and an error. Let's return the current hash
		// and a 'false' changed status to avoid re-runs on a broken file.
		return nil, currentHash, false, fmt.Errorf("failed to parse rubric file %s: %w", filePath, err)
	}

	// The 'changed' flag is determined by the hash comparison
	changed := currentHash != oldHash

	return criteria, currentHash, changed, nil
}

// GenerateRandomString generates a random hex string of the specified byte length.
// The resulting string will be twice the byte length.
func GenerateRandomString(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// stepLogger is a global logger for step-related messages.
var StepLogger = log.New(os.Stdout, "[StepExecutor] ", log.Ldate|log.Ltime|log.Lshortfile)

// stepExec is a global variable that holds the necessary information for executing a step.
// It's populated from a database query joining steps and tasks.
