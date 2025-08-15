package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Function to add thousand separators to numbers
func addCommas(n int64) string {
	in := strconv.FormatInt(n, 10)
	out := &strings.Builder{}
	lenIn := len(in)
	for i, c := range in {
		if i > 0 && (lenIn-i)%3 == 0 {
			out.WriteRune(',')
		}
		out.WriteRune(c)
	}
	return out.String()
}

// ReportTask prints a step tree for a given task ID, showing rubric_shell results as icons.
func ReportTask(db *sql.DB, taskID int) error {
	// Load config for custom PASS/FAIL markers
	cfg, errCfg := LoadConfig()
	if errCfg != nil {
		fmt.Printf("Debug: Failed to load config: %v\n", errCfg)
		cfg = &Config{} // Fallback to default config
	}
	passMarker := cfg.PassMarker
	if passMarker == "" {
		passMarker = "#__PASS__#"
	}
	failMarker := cfg.FailMarker
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

	// Calculate output sizes per solution
	sizeMap := make(map[string]int64)
	for _, step := range steps {
		if strings.Contains(step.Settings, "rubric_shell") {
			if step.Results.Valid {
				var results map[string]interface{}
				if err := json.Unmarshal([]byte(step.Results.String), &results); err == nil {
					for patch, res := range results {
						if resStr, ok := res.(string); ok {
							// Extract output from the result string (format: "Status\nOutput: ...")
							if outputIndex := strings.Index(resStr, "Output: "); outputIndex != -1 {
								cmdOut := resStr[outputIndex+8:] // Skip "Output: "
								solKey := patch
								sizeMap[solKey] += int64(len(cmdOut))
							}
						}
					}
				}
			}
		}
	}

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

		headerPrinted := false
		for i, node := range nodes {
			connector := "├── "
			newPrefix := prefix + "│   "
			if i == len(nodes)-1 {
				connector = "╰── "
				newPrefix = prefix + "    "
			}
			if !headerPrinted && strings.Contains(node.Settings, "rubric_shell") {
				// Align header to this exact connector position
				pad := strings.Repeat(" ", utf8.RuneCountInString(connector))
				fmt.Printf("%s%s1  2  3  4  O  G\n", prefix, pad)
				headerPrinted = true
			}
			var icons string
			if strings.Contains(node.Settings, "rubric_shell") {
				var settingsMap map[string]interface{}
				if err := json.Unmarshal([]byte(node.Settings), &settingsMap); err == nil {
					// Check if we have results in the dedicated results column
					if node.Results.Valid {
						var resultsMap map[string]interface{}
						if err := json.Unmarshal([]byte(node.Results.String), &resultsMap); err == nil {
							resultMap := make(map[string]string)
							for k, v := range resultsMap {
								if str, ok := v.(string); ok {
									resultMap[k] = str
								}
							}
							for _, patch := range []string{"solution1.patch", "solution2.patch", "solution3.patch", "solution4.patch", "original", "golden.patch"} {
								if output, ok := resultMap[patch]; ok {
									if strings.Contains(output, passMarker) {
										icons += "✅ "
									} else if strings.Contains(output, failMarker) {
										icons += "❌ "
									} else {
										icons += "❔ "
									}
								} else {
									icons += "❔ "
								}
							}
						} else {
							icons = "❔ ❔ ❔ ❔ ❔ ❔ "
						}
					} else {
						icons = "❔ ❔ ❔ ❔ ❔ ❔ "
					}
				} else {
					icons = "❔ ❔ ❔ ❔ ❔ ❔ "
				}
			} else {
				icons = ""
			}
			idStr := fmt.Sprintf("%*d", maxIDWidth, node.ID)
			fmt.Printf("%s%s%s %s\n", prefix, connector, icons, idStr+" "+node.Title)
			print(node.Children, newPrefix)
		}
	}
	print(rootNodes, "")

    // After the print function call, add summary of output sizes sorted by solution number
    type solPair struct {
        n    int
        size int64
    }
    var sols []solPair
    for patch, size := range sizeMap {
        solNumStr := strings.TrimPrefix(strings.TrimSuffix(patch, ".patch"), "solution")
        if n, err := strconv.Atoi(solNumStr); err == nil {
            sols = append(sols, solPair{n: n, size: size})
        }
    }
    sort.Slice(sols, func(i, j int) bool { return sols[i].n < sols[j].n })

    // Compute maximum width for right-aligned numbers (with commas)
    maxWidth := 0
    for _, s := range sizeMap { // includes solutions, original, golden
        if l := len(addCommas(s)); l > maxWidth {
            maxWidth = l
        }
    }

    for _, sp := range sols {
        fmt.Printf("Solution %d combined output: %*s bytes\n", sp.n, maxWidth, addCommas(sp.size))
    }
    // Add Original (O) and Golden (G) combined output sizes
    if size, ok := sizeMap["original"]; ok {
        fmt.Printf("Original   combined output: %*s bytes\n", maxWidth, addCommas(size))
    }
    if size, ok := sizeMap["golden.patch"]; ok {
        fmt.Printf("Golden     combined output: %*s bytes\n", maxWidth, addCommas(size))
    }
    return nil
}
