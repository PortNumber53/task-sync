package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ReportTask prints a step tree for a given task ID, showing rubric_shell results as icons.
func ReportTask(db *sql.DB, taskID int) error {
	// Load config for custom PASS/FAIL markers
	cfg, _ := LoadConfig()
	passMarker := cfg.PassMarker
	failMarker := cfg.FailMarker
	if passMarker == "" {
		passMarker = "#__PASS__#"
	}
	if failMarker == "" {
		failMarker = "#__FAIL__#"
	}
	// 1. Fetch the task name
	var taskName string
	err := db.QueryRow("SELECT name FROM tasks WHERE id = $1", taskID).Scan(&taskName)
	if err != nil {
		return fmt.Errorf("failed to fetch task name: %w", err)
	}

	fmt.Printf("%d-%s\n", taskID, taskName)

	// 2. Fetch all steps for this task
	rows, err := db.Query("SELECT id, title, settings, results FROM steps WHERE task_id = $1 ORDER BY id", taskID)
	if err != nil {
		return fmt.Errorf("failed to fetch steps: %w", err)
	}
	defer rows.Close()

	type stepRow struct {
		ID       int
		Title    string
		Settings string
		Results  sql.NullString
	}
	steps := make(map[int]*stepRow)
	var stepOrder []int
	for rows.Next() {
		var s stepRow
		if err := rows.Scan(&s.ID, &s.Title, &s.Settings, &s.Results); err != nil {
			return err
		}
		steps[s.ID] = &s
		stepOrder = append(stepOrder, s.ID)
	}

	// 3. Build dependency tree (same as TreeSteps)
	type stepNode struct {
		ID       int
		Title    string
		Children []*stepNode
		Settings string
		Results  sql.NullString
	}
	nodes := make(map[int]*stepNode)
	dependencies := make(map[int][]int)
	var rootNodes []*stepNode
	for _, id := range stepOrder {
		step := steps[id]
		node := &stepNode{ID: step.ID, Title: step.Title, Settings: step.Settings, Results: step.Results}
		nodes[id] = node
		// Parse depends_on
		var topLevel map[string]json.RawMessage
		if err := json.Unmarshal([]byte(step.Settings), &topLevel); err == nil {
			if dependsOnRaw, ok := topLevel["depends_on"]; ok {
				var deps []struct {
					ID int `json:"id"`
				}
				if err := json.Unmarshal(dependsOnRaw, &deps); err == nil {
					for _, dep := range deps {
						dependencies[id] = append(dependencies[id], dep.ID)
					}
				}
			}
			// Fallback for nested depends_on
			for _, raw := range topLevel {
				var nested struct {
					DependsOn []struct {
						ID int `json:"id"`
					} `json:"depends_on"`
				}
				if err := json.Unmarshal(raw, &nested); err == nil {
					for _, dep := range nested.DependsOn {
						dependencies[id] = append(dependencies[id], dep.ID)
					}
				}
			}
		}
	}
	// Build children
	isChild := make(map[int]bool)
	for childID, parentIDs := range dependencies {
		for _, parentID := range parentIDs {
			if parent, ok := nodes[parentID]; ok {
				if child, ok := nodes[childID]; ok {
					parent.Children = append(parent.Children, child)
					isChild[childID] = true
				}
			}
		}
	}
	for _, node := range nodes {
		if !isChild[node.ID] {
			rootNodes = append(rootNodes, node)
		}
	}
	// Sort root nodes
	// (optional: implement sorting if needed)

	// 4. Print tree with rubric_shell icons
	var print func(nodes []*stepNode, prefix string)
	print = func(nodes []*stepNode, prefix string) {
		// Determine max ID width for alignment
		maxIDWidth := 0
		for _, node := range nodes {
			idLen := len(fmt.Sprintf("%d", node.ID))
			if idLen > maxIDWidth {
				maxIDWidth = idLen
			}
		}
		// Sort rubric_shell nodes by rubric number, others by ID
		sort.Slice(nodes, func(i, j int) bool {
			getRubricNum := func(title string) int {
				idx := strings.Index(title, "Rubric ")
				if idx == -1 {
					return -1
				}
				rest := title[idx+7:]
				end := strings.Index(rest, ":")
				if end == -1 {
					return -1
				}
				numStr := strings.TrimSpace(rest[:end])
				if n, err := strconv.Atoi(numStr); err == nil {
					return n
				}
				return -1
			}
			left := getRubricNum(nodes[i].Title)
			right := getRubricNum(nodes[j].Title)
			if left != -1 && right != -1 {
				return left < right
			}
			if left != -1 {
				return true
			}
			if right != -1 {
				return false
			}
			return nodes[i].ID < nodes[j].ID
		})

		for i, node := range nodes {
			connector := "├── "
			newPrefix := prefix + "│   "
			if i == len(nodes)-1 {
				connector = "╰── "
				newPrefix = prefix + "    "
			}
			// Always show 4 icons for rubric_shell steps
			var icons string
			if strings.Contains(node.Settings, "rubric_shell") {
				var results map[string]map[string]interface{}
				if node.Results.Valid {
					_ = json.Unmarshal([]byte(node.Results.String), &results)
				}
				patches := []string{"solution1.patch", "solution2.patch", "solution3.patch", "solution4.patch"}
				for _, patch := range patches {
					icon := "❔"
					if results != nil {
						if res, ok := results[patch]; ok {
							if out, ok := res["output"].(string); ok {
								// Detect errorlevel=127 or similar (command not found)
								if strings.Contains(out, "errorlevel=127") || strings.Contains(out, "exit status 127") || strings.Contains(out, "command not found") || strings.Contains(out, "No such file or directory") {
									icon = "💀"
								} else if strings.Contains(out, passMarker) {
									icon = "✅"
								} else if strings.Contains(out, failMarker) {
									icon = "❌"
								}
							}
						}
					}
					icons += icon + " "
				}
			}
			idStr := fmt.Sprintf("%*d", maxIDWidth, node.ID)
			// Right-align rubric numbers in titles
			rubricNumWidth := 0
			for _, n := range nodes {
				if idx := strings.Index(n.Title, "Rubric "); idx != -1 {
					title := n.Title[idx+7:]
					end := strings.Index(title, ":")
					if end > 0 {
						numStr := strings.TrimSpace(title[:end])
						if len(numStr) > rubricNumWidth {
							rubricNumWidth = len(numStr)
						}
					}
				}
			}
			formatRubricNum := func(title string) string {
				idx := strings.Index(title, "Rubric ")
				if idx == -1 {
					return title
				}
				tail := title[idx+7:]
				end := strings.Index(tail, ":")
				if end <= 0 {
					return title
				}
				numStr := strings.TrimSpace(tail[:end])
				rightNum := fmt.Sprintf("%*s", rubricNumWidth, numStr)
				return title[:idx+7] + rightNum + tail[end:]
			}
			titleOut := formatRubricNum(node.Title)
			if strings.Contains(node.Settings, "rubric_shell") {
				fmt.Printf("%s%s%s%s-%s\n", prefix, connector, icons, idStr, titleOut)
			} else {
				fmt.Printf("%s%s%s-%s\n", prefix, connector, idStr, titleOut)
			}
			print(node.Children, newPrefix)
		}
	}
	print(rootNodes, "")
	return nil
}
