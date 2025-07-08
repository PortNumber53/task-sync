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
	"strconv"
	"sort"
	"strings"

	"errors"
)

// ErrEmptyFile is returned when a file is empty.
var ErrEmptyFile = errors.New("file is empty")

// StepExec holds the necessary information for executing a step.
// It's populated from a database query joining steps and tasks.
type StepExec struct {
	StepID    int
	TaskID    int
	Title     string
	Settings  string
	LocalPath string
}

// TaskSettings represents the settings stored in the `tasks.settings` JSONB column.
// DockerTaskConfig represents the 'docker' field within task settings.
type DockerTaskConfig struct {
	ImageTag string `json:"image_tag,omitempty"`
	ImageID  string `json:"image_id,omitempty"`
}

// TaskSettings represents the settings stored in the `tasks.settings` JSONB column.
type TaskSettings struct {
	AssignContainers map[string]string `json:"assign_containers,omitempty"`
	Docker           DockerTaskConfig  `json:"docker,omitempty"`
}

// StepConfig is an interface that all step configurations should implement.
type StepConfig interface {
	GetImageTag() string
	GetImageID() string
	HasImage() bool
	GetDependsOn() []Dependency
}

// --- Top-level Config Structs ---

// DockerBuildConfig represents the configuration for a docker_build step.
type DockerBuildConfig struct {
	Dockerfile string            `json:"dockerfile,omitempty"`
	ImageID    string            `json:"image_id,omitempty"`
	ImageTag   string            `json:"image_tag,omitempty"`
	DependsOn  []Dependency      `json:"depends_on,omitempty"`
	Files      map[string]string `json:"files,omitempty"`
	Parameters []string          `json:"parameters,omitempty"`
}

func (c *DockerBuildConfig) GetImageTag() string      { return c.ImageTag }
func (c *DockerBuildConfig) GetImageID() string       { return c.ImageID }
func (c *DockerBuildConfig) HasImage() bool           { return c.ImageTag != "" && c.ImageID != "" }
func (c *DockerBuildConfig) GetDependsOn() []Dependency { return c.DependsOn }

type DockerPullConfig struct {
	ImageID          string       `json:"image_id,omitempty"`
	ImageTag         string       `json:"image_tag,omitempty"`
	DependsOn        []Dependency `json:"depends_on,omitempty"`
	PreventRunBefore string       `json:"prevent_run_before,omitempty"`
}

func (c *DockerPullConfig) GetImageTag() string      { return c.ImageTag }
func (c *DockerPullConfig) GetImageID() string       { return c.ImageID }
func (c *DockerPullConfig) HasImage() bool           { return c.ImageTag != "" && c.ImageID != "" }
func (c *DockerPullConfig) GetDependsOn() []Dependency { return c.DependsOn }

type DockerRunConfig struct {
	ImageID       string       `json:"-"`
	ImageTag      string       `json:"-"`
	DependsOn     []Dependency `json:"depends_on,omitempty"`
	ContainerID   string       `json:"container_id,omitempty"`
	ContainerName string       `json:"container_name,omitempty"`
	Parameters    []string     `json:"parameters,omitempty"`
	KeepForever   bool         `json:"keep_forever,omitempty"`
}

func (c *DockerRunConfig) GetImageTag() string      { return c.ImageTag }
func (c *DockerRunConfig) GetImageID() string       { return c.ImageID }
func (c *DockerRunConfig) HasImage() bool           { return c.ImageTag != "" && c.ImageID != "" }
func (c *DockerRunConfig) GetDependsOn() []Dependency { return c.DependsOn }

type DockerPoolConfig struct {
	SourceStepID int			`json:"source_step_id,omitempty"`
	ImageID     string          `json:"-"`
	ImageTag    string          `json:"-"`
	DependsOn   []Dependency    `json:"depends_on,omitempty"`
	PoolSize    int             `json:"pool_size,omitempty"`
	Containers  []ContainerInfo `json:"containers,omitempty"`
	Parameters  []string        `json:"parameters,omitempty"`
	KeepForever bool            `json:"keep_forever,omitempty"`
}

func (c *DockerPoolConfig) GetImageTag() string      { return c.ImageTag }
func (c *DockerPoolConfig) GetImageID() string       { return c.ImageID }
func (c *DockerPoolConfig) HasImage() bool           { return c.ImageTag != "" && c.ImageID != "" }
func (c *DockerPoolConfig) GetDependsOn() []Dependency { return c.DependsOn }

type DockerShellConfig struct {
	Docker struct {
		ImageID  string `json:"-"`
		ImageTag string `json:"-"`
	} `json:"docker,omitempty"`
	DependsOn []Dependency        `json:"depends_on,omitempty"`
	Command   []map[string]string `json:"command,omitempty"`
}

func (c *DockerShellConfig) GetImageTag() string      { return c.Docker.ImageTag }
func (c *DockerShellConfig) GetImageID() string       { return c.Docker.ImageID }
func (c *DockerShellConfig) HasImage() bool           { return c.Docker.ImageTag != "" && c.Docker.ImageID != "" }
func (c *DockerShellConfig) GetDependsOn() []Dependency { return c.DependsOn }

type Criterion struct {
	Title       string
	Score       int
	Required    bool
	Rubric      string
	HeldOutTest string
	Counter     string
}

// ParseRubric extracts rubric criteria from a markdown file.
// Each criterion is expected to be in a section that starts with a header like:
// ### #1: d0aba505-cc93-489c-bc8b-da566a1f0af5
// ParseRubric extracts rubric criteria from a markdown file.
// Each criterion is expected to be in a section that starts with a header like:
// ### #1: d0aba505-cc93-489c-bc8b-da566a1f0af5
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
	// Go's regexp engine doesn't support lookaheads. Instead, we find the start
	// index of each delimiter and slice the text into sections manually.
	re := regexp.MustCompile(`(?m)^\s*###\s*#\d+:\s*[a-fA-F0-9-]{36}`)
	matches := re.FindAllStringIndex(text, -1)

	if len(matches) == 0 {
		return nil, fmt.Errorf("no criteria sections found in %s", filePath)
	}

	var sections []string
	for i := 0; i < len(matches); i++ {
		start := matches[i][0]
		var end int
		if i+1 < len(matches) {
			end = matches[i+1][0]
		} else {
			end = len(text)
		}
		sections = append(sections, text[start:end])
	}

	var criteria []Criterion

	// Regexes to parse components of a rubric section.
	headerRe := regexp.MustCompile(`(?m)^\s*###\s*#(\d+):\s*([a-fA-F0-9-]{36})`)
	scoreRe := regexp.MustCompile(`\*\*Score\*\*:\s*(\d+)`)
	requiredRe := regexp.MustCompile(`\*\*Required\*\*:\s*(true|false)`)
	criterionRe := regexp.MustCompile(`(?s)\*\*Criterion\*\*:\s*(.*?)(?:\n\n|$)`)
	heldOutTestRe := regexp.MustCompile("(?s)\\*\\*Held-out tests\\*\\*:\\n```(?:bash)?\\n(.*?)\\n```")

	for _, section := range sections {
		if strings.TrimSpace(section) == "" {
			continue
		}

		headerMatch := headerRe.FindStringSubmatch(section)
		if len(headerMatch) < 3 {
			continue // Not a valid criterion section
		}

		crit := Criterion{
			Counter: strings.TrimSpace(headerMatch[1]),
			Title:   strings.TrimSpace(headerMatch[2]),
		}

		scoreMatch := scoreRe.FindStringSubmatch(section)
		if len(scoreMatch) > 1 {
			if score, err := strconv.Atoi(scoreMatch[1]); err == nil {
				crit.Score = score
			}
		}

		requiredMatch := requiredRe.FindStringSubmatch(section)
		if len(requiredMatch) > 1 {
			crit.Required = (requiredMatch[1] == "true")
		}

		criterionMatch := criterionRe.FindStringSubmatch(section)
		if len(criterionMatch) > 1 {
			crit.Rubric = strings.TrimSpace(criterionMatch[1])
		}

		heldOutTestMatch := heldOutTestRe.FindStringSubmatch(section)
		if len(heldOutTestMatch) > 1 {
			crit.HeldOutTest = strings.TrimSpace(heldOutTestMatch[1])
		}

		// Only add if we have the essential parts
		if crit.Title != "" && crit.HeldOutTest != "" {
			criteria = append(criteria, crit)
		}
	}

	return criteria, nil
}

type DynamicRubricConfig struct {
	DynamicRubric struct {
		Files       map[string]string `json:"files,omitempty"`
		Rubrics          []string          `json:"rubrics,omitempty"`
		Hash             string            `json:"hash,omitempty"`

		Environment      struct {
			Docker   bool   `json:"docker"`
			ImageID  string `json:"image_id,omitempty"`
			ImageTag string `json:"image_tag,omitempty"`
		} `json:"environment"`
		DependsOn []Dependency `json:"depends_on,omitempty"`
	} `json:"dynamic_rubric"`
}

func (c *DynamicRubricConfig) GetImageTag() string {
	return c.DynamicRubric.Environment.ImageTag
}
func (c *DynamicRubricConfig) GetImageID() string {
	return c.DynamicRubric.Environment.ImageID
}
func (c *DynamicRubricConfig) HasImage() bool {
	return c.DynamicRubric.Environment.ImageTag != "" && c.DynamicRubric.Environment.ImageID != ""
}
func (c *DynamicRubricConfig) GetDependsOn() []Dependency {
	return c.DynamicRubric.DependsOn
}

// FileExistsConfig represents the configuration for a file_exists step.
type FileExistsConfig struct {
	FileExists []string `json:"file_exists"`
}

func (c *FileExistsConfig) GetImageTag() string      { return "" }
func (c *FileExistsConfig) GetImageID() string       { return "" }
func (c *FileExistsConfig) HasImage() bool           { return false }
func (c *FileExistsConfig) GetDependsOn() []Dependency { return nil }

// RubricsImportConfig represents the configuration for a rubrics_import step.
type RubricsImportConfig struct {
	MHTMLFile string `json:"mhtml_file"`
	MDFile    string `json:"md_file"`
	DependsOn []Dependency `json:"depends_on,omitempty"`
}

func (c *RubricsImportConfig) GetImageTag() string      { return "" }
func (c *RubricsImportConfig) GetImageID() string       { return "" }
func (c *RubricsImportConfig) HasImage() bool           { return false }
func (c *RubricsImportConfig) GetDependsOn() []Dependency { return c.DependsOn }

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

func (c *DockerRubricsConfig) GetImageTag() string      { return c.DockerRubrics.ImageTag }
func (c *DockerRubricsConfig) GetImageID() string       { return c.DockerRubrics.ImageID }
func (c *DockerRubricsConfig) HasImage() bool           { return c.DockerRubrics.ImageTag != "" && c.DockerRubrics.ImageID != "" }
func (c *DockerRubricsConfig) GetDependsOn() []Dependency { return c.DockerRubrics.DependsOn }

// RubricShellConfig represents the configuration for a rubric_shell step.
type RubricShellConfig struct {
	ImageID          string            `json:"image_id,omitempty"`
	ImageTag         string            `json:"image_tag,omitempty"`
	Command          string            `json:"command"`
	CriterionID      string            `json:"criterion_id,omitempty"`
	Counter          string            `json:"counter,omitempty"`
	Score            int               `json:"score,omitempty"`
	Required         bool              `json:"required,omitempty"`
	DependsOn        []Dependency      `json:"depends_on,omitempty"`
	GeneratedBy      string            `json:"generated_by,omitempty"`
	ContainerName    string            `json:"container_name,omitempty"`
	LastRun          map[string]string `json:"last_run,omitempty"`
	Files            map[string]string `json:"files,omitempty"`
}

func (c *RubricShellConfig) GetImageTag() string      { return c.ImageTag }
func (c *RubricShellConfig) GetImageID() string       { return c.ImageID }
func (c *RubricShellConfig) HasImage() bool           { return c.ImageTag != "" && c.ImageID != "" }
func (c *RubricShellConfig) GetDependsOn() []Dependency { return c.DependsOn }

// RubricSetConfig represents the configuration for a rubric_set step.
// RubricSetConfig represents the configuration for a rubric_set step.
// - File: the main rubric markdown file
// - Files: additional files to track for hash changes (name->relative path)
// - Hashes: map of filename to last known SHA256 hash
// When any file's hash changes, the step should re-run.
type RubricSetConfig struct {
	File             string            `json:"file"`
	Files            map[string]string `json:"files,omitempty"`
	HeldOutTest      string            `json:"held_out_test,omitempty"`
	Solution1        string            `json:"solution_1,omitempty"`
	Solution2        string            `json:"solution_2,omitempty"`
	Solution3        string            `json:"solution_3,omitempty"`
	Solution4        string            `json:"solution_4,omitempty"`

	DependsOn        []Dependency      `json:"depends_on,omitempty"`
}

func (c *RubricSetConfig) GetImageTag() string      { return "" }
func (c *RubricSetConfig) GetImageID() string       { return "" }
func (c *RubricSetConfig) HasImage() bool           { return false }
func (c *RubricSetConfig) GetDependsOn() []Dependency { return c.DependsOn }

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
		DependsOn     []Dependency `json:"depends_on,omitempty"`
	} `json:"dynamic_lab"`
}

func (c *DynamicLabConfig) GetImageTag() string      { return "" }
func (c *DynamicLabConfig) GetImageID() string       { return c.DynamicLab.ImageID }
func (c *DynamicLabConfig) HasImage() bool           { return c.DynamicLab.ImageID != "" }
func (c *DynamicLabConfig) GetDependsOn() []Dependency { return c.DynamicLab.DependsOn }

type StepConfigHolder struct {
	DockerBuild   *DockerBuildConfig   `json:"docker_build,omitempty"`
	DockerPull    *DockerPullConfig    `json:"docker_pull,omitempty"`
	DockerRun     *DockerRunConfig     `json:"docker_run,omitempty"`
	DockerPool    *DockerPoolConfig    `json:"docker_pool,omitempty"`
	DockerShell   *DockerShellConfig   `json:"docker_shell,omitempty"`
	DynamicRubric *DynamicRubricConfig `json:"dynamic_rubric,omitempty"`
	DynamicLab    *DynamicLabConfig    `json:"dynamic_lab,omitempty"`
	FileExists    *FileExistsConfig    `json:"file_exists,omitempty"`
	RubricsImport *RubricsImportConfig `json:"rubrics_import,omitempty"`
	DockerRubrics *DockerRubricsConfig `json:"docker_rubrics,omitempty"`
	RubricShell   *RubricShellConfig   `json:"rubric_shell,omitempty"`
	RubricSet     *RubricSetConfig     `json:"rubric_set,omitempty"`
}

// GetConfig returns the non-nil configuration from the holder.
func (h *StepConfigHolder) GetConfig() (StepConfig, error) {
	if h.DockerBuild != nil {
		return h.DockerBuild, nil
	}
	if h.DockerPull != nil {
		return h.DockerPull, nil
	}
	if h.DockerRun != nil {
		return h.DockerRun, nil
	}
	if h.DockerPool != nil {
		return h.DockerPool, nil
	}
	if h.DockerShell != nil {
		return h.DockerShell, nil
	}
	if h.DynamicRubric != nil {
		return h.DynamicRubric, nil
	}
	if h.FileExists != nil {
		return h.FileExists, nil
	}
	if h.RubricsImport != nil {
		return h.RubricsImport, nil
	}
	if h.DockerRubrics != nil {
		return h.DockerRubrics, nil
	}
	if h.RubricShell != nil {
		return h.RubricShell, nil
	}
	if h.RubricSet != nil {
		return h.RubricSet, nil
	}
	return nil, fmt.Errorf("no configuration found in StepConfigHolder")
}

// AllDependencies collects and returns all `depends_on` entries from the held configurations.
func (h *StepConfigHolder) AllDependencies() []Dependency {
	var deps []Dependency
	if h.DockerBuild != nil {
		deps = append(deps, h.DockerBuild.DependsOn...)
	}
	if h.DockerPull != nil {
		deps = append(deps, h.DockerPull.DependsOn...)
	}
	if h.DockerRun != nil {
		deps = append(deps, h.DockerRun.DependsOn...)
	}
	if h.DockerPool != nil {
		deps = append(deps, h.DockerPool.DependsOn...)
	}
	if h.DockerShell != nil {
		deps = append(deps, h.DockerShell.DependsOn...)
	}
	if h.DynamicRubric != nil {
		deps = append(deps, h.DynamicRubric.DynamicRubric.DependsOn...)
	}
	if h.DynamicLab != nil {
		deps = append(deps, h.DynamicLab.DynamicLab.DependsOn...)
	}
	if h.RubricsImport != nil {
		deps = append(deps, h.RubricsImport.DependsOn...)
	}
	if h.DockerRubrics != nil {
		deps = append(deps, h.DockerRubrics.DockerRubrics.DependsOn...)
	}
	if h.RubricShell != nil {
		deps = append(deps, h.RubricShell.DependsOn...)
	}
	if h.RubricSet != nil {
		deps = append(deps, h.RubricSet.DependsOn...)
	}
	return deps
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

// UpdateStep updates a step's title and settings.
func UpdateStep(db *sql.DB, stepID int, title, settings string) error {
	query := `UPDATE steps SET title = $1, settings = $2 WHERE id = $3`
	_, err := db.Exec(query, title, settings, stepID)
	if err != nil {
		return fmt.Errorf("failed to update step %d: %w", stepID, err)
	}
	return nil
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

// DeleteStepInTx removes a step from the database by its ID within a transaction.
func DeleteStepInTx(tx *sql.Tx, stepID int) error {
	result, err := tx.Exec("DELETE FROM steps WHERE id = $1", stepID)
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
	resultExec, err := db.Exec(`UPDATE steps SET results = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, string(resJson), stepID)
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
	var dependsOnSQL sql.NullString
	query := `SELECT settings->'depends_on' FROM steps WHERE id = $1`
	err := db.QueryRow(query, stepExec.StepID).Scan(&dependsOnSQL)
	if err != nil {
		return false, fmt.Errorf("failed to query depends_on for step %d: %w", stepExec.StepID, err)
	}

	var dependsOnJSON string
	if dependsOnSQL.Valid {
		dependsOnJSON = dependsOnSQL.String
	} else {
		dependsOnJSON = "[]" // Default to empty array if depends_on is NULL
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
		WHERE s.id IN (?)
		AND (s.results->>'result' IS NULL OR s.results->>'result' != 'success')
	)`

	// Build the IN clause dynamically
	placeholders := make([]string, len(depIDs))
	args := make([]interface{}, len(depIDs))
	for i, id := range depIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	
	checkQuery = strings.Replace(checkQuery, "IN (?)", "IN ("+strings.Join(placeholders, ",")+ ")", 1)

	var allDependenciesMet bool
	err = db.QueryRow(checkQuery, args...).Scan(&allDependenciesMet)
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
		var localPath sql.NullString // Use NullString to handle potential NULL settings

		if err := rows.Scan(&s.StepID, &s.TaskID, &s.Title, &s.Settings, &localPath); err != nil {
			return nil, fmt.Errorf("error scanning step: %w", err)
		}
		s.LocalPath = localPath.String
		steps = append(steps, s)
	}
	return steps, nil
}

// Step represents a single step in a task, holding its core attributes.
type Step struct {
	ID        int
	TaskID    int
	Title     string
	Settings  string
	Results   string
	CreatedAt string
	UpdatedAt string
}

// GetGeneratedSteps retrieves all steps generated by a specific parent step.
func GetGeneratedSteps(db *sql.DB, generatedByStepID int) ([]Step, error) {
	query := `
		SELECT id, task_id, title, settings, results, created_at, updated_at
		FROM steps
		WHERE (settings->'rubric_shell'->>'generated_by' = $1)
	`
	rows, err := db.Query(query, strconv.Itoa(generatedByStepID))
	if err != nil {
		return nil, fmt.Errorf("failed to query for generated steps for %d: %w", generatedByStepID, err)
	}
	defer rows.Close()

	var steps []Step
	for rows.Next() {
		var s Step
		var results sql.NullString
		if err := rows.Scan(&s.ID, &s.TaskID, &s.Title, &s.Settings, &results, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan generated step: %w", err)
		}
		s.Results = results.String
		steps = append(steps, s)
	}
	return steps, nil
}

// UpdateStepSettings updates the settings of a specific step.
func UpdateStepSettings(db *sql.DB, stepID int, settings string) error {
	query := `UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2`
	_, err := db.Exec(query, settings, stepID)
	if err != nil {
		return fmt.Errorf("failed to update settings for step %d: %w", stepID, err)
	}
	return nil
}

// DeleteGeneratedSteps deletes steps that were generated by another step.
func DeleteGeneratedSteps(db *sql.DB, generatedByStepID int) error {
	// This query targets steps where the 'generated_by' key matches the parent step ID.
	// It handles both nested (e.g., in 'rubric_shell') and top-level 'generated_by' fields.
	query := `DELETE FROM steps WHERE (settings->'rubric_shell'->>'generated_by' = $1) OR (settings->>'generated_by' = $1)`
	_, err := db.Exec(query, strconv.Itoa(generatedByStepID))
	if err != nil {
		return fmt.Errorf("failed to delete generated steps for %d: %w", generatedByStepID, err)
	}
	return nil
}

// GeneratedStepsExist checks if there are any steps generated by a given step.
func GeneratedStepsExist(db *sql.DB, generatedByStepID int) (bool, error) {
	var exists bool
	// This query checks for the existence of steps where the 'generated_by' key matches the parent step ID.
	// It handles both nested (e.g., in 'rubric_shell') and top-level 'generated_by' fields.
	query := `SELECT EXISTS(SELECT 1 FROM steps WHERE (settings->'rubric_shell'->>'generated_by' = $1) OR (settings->>'generated_by' = $1))`
	err := db.QueryRow(query, strconv.Itoa(generatedByStepID)).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check for generated steps for %d: %w", generatedByStepID, err)
	}
	return exists, nil
}


// CreateStep inserts a new step for a task and returns the new step's ID.
func CreateStep(db *sql.DB, taskRef, title, settings string) (int, error) {
	var taskID int
	// Convert taskRef (string) to int for querying by ID
	id, err := strconv.Atoi(taskRef)
	if err != nil {
		return 0, fmt.Errorf("invalid task ID: %w", err)
	}

	err = db.QueryRow("SELECT id FROM tasks WHERE id = $1", id).Scan(&taskID)
	if err != nil {
		return 0, fmt.Errorf("error finding task by ID: %w", err)
	}

	var stepID int
	err = db.QueryRow(
		`INSERT INTO steps (task_id, title, settings, created_at, updated_at)
		 VALUES ($1, $2, $3, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
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

// FindImageDetailsRecursive searches for Docker image details by traversing up the dependency chain.
func FindImageDetailsRecursive(db *sql.DB, stepID int, stepLogger *log.Logger) (string, string, error) {
	stepLogger.Printf("FindImageDetailsRecursive: Checking step ID: %d", stepID)

	// 1. Get task_id and settings for the current step
	var taskID int
	var stepSettingsJSON string
	query := `SELECT task_id, settings FROM steps WHERE id = $1`
	err := db.QueryRow(query, stepID).Scan(&taskID, &stepSettingsJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", "", fmt.Errorf("step with ID %d not found", stepID)
		}
		return "", "", err
	}

	// 2. Check the step's own config for image details first.
	var holder StepConfigHolder
	if err := json.Unmarshal([]byte(stepSettingsJSON), &holder); err != nil {
		return "", "", fmt.Errorf("error unmarshalling step %d settings: %w", stepID, err)
	}

	config, err := holder.GetConfig()
	if err != nil {
		return "", "", fmt.Errorf("error getting config from step %d: %w", stepID, err)
	}

	if config.HasImage() {
		stepLogger.Printf("FindImageDetailsRecursive: Found image details in step %d config", stepID)
		return config.GetImageTag(), config.GetImageID(), nil
	}

	// Special handling for docker_pool steps which have a direct source step
	if poolConfig, ok := config.(*DockerPoolConfig); ok {
		if poolConfig.SourceStepID != 0 {
			stepLogger.Printf("FindImageDetailsRecursive: Step %d is a docker_pool, recursing to source step %d", stepID, poolConfig.SourceStepID)
			return FindImageDetailsRecursive(db, poolConfig.SourceStepID, stepLogger)
		}
	}

	// 3. If not found, recurse through dependencies.
	if len(config.GetDependsOn()) > 0 {
		stepLogger.Printf("FindImageDetailsRecursive: Step %d has dependencies, recursing.", stepID)
		for _, dep := range config.GetDependsOn() {
			imageTag, imageID, err := FindImageDetailsRecursive(db, dep.ID, stepLogger)
			if err != nil {
				stepLogger.Printf("FindImageDetailsRecursive: Error traversing dependency %d for step %d: %v", dep.ID, stepID, err)
				continue
			}
			if imageTag != "" || imageID != "" {
				stepLogger.Printf("FindImageDetailsRecursive: Found image details in dependency step %d for step %d", dep.ID, stepID)
				return imageTag, imageID, nil
			}
		}
	}

	// 4. If not in step config or dependencies, check the associated task's settings as a fallback.
	stepLogger.Printf("FindImageDetailsRecursive: Checking task %d settings for image details as fallback for step %d", taskID, stepID)
	var taskSettingsJSON sql.NullString
	err = db.QueryRow("SELECT settings FROM tasks WHERE id = $1", taskID).Scan(&taskSettingsJSON)
	if err != nil || !taskSettingsJSON.Valid || taskSettingsJSON.String == "" {
		return "", "", nil
	}

	var taskSettings map[string]interface{}
	if err := json.Unmarshal([]byte(taskSettingsJSON.String), &taskSettings); err != nil {
		return "", "", fmt.Errorf("failed to unmarshal task settings for task %d: %w", taskID, err)
	}

	if dockerInfo, ok := taskSettings["docker"].(map[string]interface{}); ok {
		imageTag, _ := dockerInfo["image_tag"].(string)
		imageHash, _ := dockerInfo["image_hash"].(string)
		if imageTag != "" || imageHash != "" {
			stepLogger.Printf("FindImageDetailsRecursive: Found image details in task %d settings", taskID)
			return imageTag, imageHash, nil
		}
	}

	// 5. If not found anywhere, return empty.
	return "", "", nil
}

// GetDockerImageID retrieves image_id and image_tag from the task settings for the given step ID.
// FindAncestorStepSettings recursively searches the dependency tree for the first ancestor
// of a given stepType (e.g., "docker_pool") and returns its unmarshalled settings.
func FindAncestorStepSettings(db *sql.DB, stepID int, stepType string, stepLogger *log.Logger) (json.RawMessage, error) {
	var settingsStr string
	err := db.QueryRow(`SELECT settings FROM steps WHERE id = $1`, stepID).Scan(&settingsStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // Step not found, end of this branch.
		}
		return nil, fmt.Errorf("failed to get settings for step %d: %w", stepID, err)
	}

	var settingsMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(settingsStr), &settingsMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal settings for step %d: %w", stepID, err)
	}

	// Check if the current step is of the desired type
	if settings, ok := settingsMap[stepType]; ok {
		return settings, nil
	}

	// If not, recurse up the dependency tree by parsing the nested depends_on field.
	for _, stepConfigJSON := range settingsMap {
		var holder DependencyHolder
		// The stepConfigJSON is the value part of the map, e.g., the JSON for "rubric_shell"
		if err := json.Unmarshal(stepConfigJSON, &holder); err != nil {
			// This config doesn't have a depends_on field, or it's malformed. Skip it.
			continue
		}

		// Now we have the dependencies for the current step
		for _, dep := range holder.DependsOn {
			ancestorSettings, err := FindAncestorStepSettings(db, dep.ID, stepType, stepLogger)
			if err != nil {
				stepLogger.Printf("error searching for ancestor of type '%s' in dependency %d: %v", stepType, dep.ID, err)
				continue
			}
			if ancestorSettings != nil {
				return ancestorSettings, nil
			}
		}
		// We only need to check the one actual step config in the map.
		break
	}

	return nil, nil // Not found in this branch
}

func GetDockerImageID(db *sql.DB, stepID int, stepLogger *log.Logger) (imageID, imageTag string, err error) {
	imgID, imgTag, err := FindImageDetailsRecursive(db, stepID, stepLogger)
	if err != nil {
		return "", "", err
	}
	if imgID != "" && imgTag != "" {
		stepLogger.Printf("Using image_id '%s' and image_tag '%s' from task settings.\n", imgID, imgTag)
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
		// For DockerPool steps, we can directly access the container info
		if len(configHolder.DockerPool.Containers) > 0 {
			// Return the first available container
			container := configHolder.DockerPool.Containers[0]
			stepLogger.Printf("Found container_id '%s' and container_name '%s' in pool step %d\n",
				container.ContainerID, container.ContainerName, stepID)
			return container.ContainerID, container.ContainerName, nil
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
			stepLogger.Printf("Found container_id '%s' and container_name '%s' from step %d\n", contID, contName, dep.ID)
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

// GetRubricSetFromDependencies recursively searches for a rubric_set step in the dependency tree.
func GetRubricSetFromDependencies(db *sql.DB, stepID int, stepLogger *log.Logger) (*RubricSetConfig, error) {
	var settings string
	err := db.QueryRow("SELECT settings FROM steps WHERE id = $1", stepID).Scan(&settings)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // Step not found, not an error
		}
		return nil, fmt.Errorf("failed to query step %d: %w", stepID, err)
	}

	var holder StepConfigHolder
	if err := json.Unmarshal([]byte(settings), &holder); err != nil {
		return nil, fmt.Errorf("failed to unmarshal settings for step %d: %w", stepID, err)
	}

	if holder.RubricSet != nil {
		return holder.RubricSet, nil
	}

	// If not found, recurse through dependencies
	for _, dep := range holder.AllDependencies() {
		rubricSet, err := GetRubricSetFromDependencies(db, dep.ID, stepLogger)
		if err != nil {
			stepLogger.Printf("Error searching for rubric_set in dependency %d: %v", dep.ID, err)
			continue // Log error and continue searching other branches
		}
		if rubricSet != nil {
			return rubricSet, nil // Found in a dependency branch
		}
	}

	return nil, nil // Not found in this branch
}

// GetTaskSettings retrieves and unmarshals the settings for a given task.
func GetTaskSettings(db *sql.DB, taskID int) (*TaskSettings, error) {
	var settingsJSON sql.NullString
	err := db.QueryRow("SELECT settings FROM tasks WHERE id = $1", taskID).Scan(&settingsJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			return &TaskSettings{}, nil // No task found, return empty settings
		}
		return nil, fmt.Errorf("failed to query task settings for task %d: %w", taskID, err)
	}

	var settings TaskSettings
	if settingsJSON.Valid && settingsJSON.String != "" && settingsJSON.String != "null" {
		if err := json.Unmarshal([]byte(settingsJSON.String), &settings); err != nil {
			return nil, fmt.Errorf("failed to unmarshal task settings for task %d: %w", taskID, err)
		}
	}

	return &settings, nil
}

// UpdateTaskSettings marshals and saves the settings for a given task.
func UpdateTaskSettings(db *sql.DB, taskID int, newSettings *TaskSettings) error {
	// Get current settings from the database
	var currentSettingsJSON sql.NullString
	err := db.QueryRow("SELECT settings FROM tasks WHERE id = $1", taskID).Scan(&currentSettingsJSON)
	if err != nil {
		return fmt.Errorf("failed to query current task settings for task %d: %w", taskID, err)
	}

	var currentMap map[string]json.RawMessage
	if currentSettingsJSON.Valid && currentSettingsJSON.String != "" && currentSettingsJSON.String != "null" {
		if err := json.Unmarshal([]byte(currentSettingsJSON.String), &currentMap); err != nil {
			return fmt.Errorf("failed to unmarshal current task settings for task %d: %w", taskID, err)
		}
	}

	// Marshal the new settings into a map
	newMapBytes, err := json.Marshal(newSettings)
	if err != nil {
		return fmt.Errorf("failed to marshal new task settings for task %d: %w", taskID, err)
	}

	var newMap map[string]json.RawMessage
	if err := json.Unmarshal(newMapBytes, &newMap); err != nil {
		return fmt.Errorf("failed to unmarshal new task settings into map for task %d: %w", taskID, err)
	}

	// Merge new settings into current settings
	// This simple merge overwrites top-level keys. For nested structures like 'docker', we need a deeper merge.
	for k, v := range newMap {
		currentMap[k] = v
	}

	// Special handling for 'docker' field to merge its contents
	if newDockerRaw, ok := newMap["docker"]; ok {
		var newDockerMap map[string]json.RawMessage
		if err := json.Unmarshal(newDockerRaw, &newDockerMap); err != nil {
			return fmt.Errorf("failed to unmarshal new docker settings for task %d: %w", taskID, err)
		}

		var currentDockerMap map[string]json.RawMessage
		if currentDockerRaw, ok := currentMap["docker"]; ok {
			if err := json.Unmarshal(currentDockerRaw, &currentDockerMap); err != nil {
				return fmt.Errorf("failed to unmarshal current docker settings for task %d: %w", taskID, err)
			}
		} else {
			currentDockerMap = make(map[string]json.RawMessage)
		}

		for k, v := range newDockerMap {
			currentDockerMap[k] = v
		}
		mergedDockerBytes, err := json.Marshal(currentDockerMap)
		if err != nil {
			return fmt.Errorf("failed to marshal merged docker settings for task %d: %w", taskID, err)
		}
		currentMap["docker"] = mergedDockerBytes
	}

	// Marshal the merged settings back to JSON
	mergedSettingsBytes, err := json.Marshal(currentMap)
	if err != nil {
		return fmt.Errorf("failed to marshal merged task settings for task %d: %w", taskID, err)
	}

	// Update the database
	_, err = db.Exec("UPDATE tasks SET settings = $1 WHERE id = $2", string(mergedSettingsBytes), taskID)
	if err != nil {
		return fmt.Errorf("failed to update task settings for task %d: %w", taskID, err)
	}

	return nil
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

// GetSHA256 calculates the SHA256 hash of a file, returning ErrEmptyFile if it's empty.
func GetSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to get file info for %s: %w", filePath, err)
	}

	if info.Size() == 0 {
		return "", ErrEmptyFile
	}

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
var StepLogger *log.Logger

// InitStepLogger initializes the package-level step logger.
func InitStepLogger(writer io.Writer) {
	StepLogger = log.New(writer, "[StepExecutor] ", log.LstdFlags)
}

// stepExec is a global variable that holds the necessary information for executing a step.
// It's populated from a database query joining steps and tasks.
