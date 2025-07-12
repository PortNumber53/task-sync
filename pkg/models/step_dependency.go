package models

import (
    "database/sql"
    "encoding/json" // Added for Unmarshal functions
    "fmt"
)

// Dependency defines a dependency on another step.
type Dependency struct {
    ID int `json:"id"`
}

// CheckDependencies checks if all dependencies for a given step are met.
func CheckDependencies(db *sql.DB, stepExec *StepExec) (bool, error) {
    // Implementation remains the same as in steps.go
    var settings map[string]interface{}
    if err := json.Unmarshal([]byte(stepExec.Settings), &settings); err != nil {
        return false, fmt.Errorf("unmarshaling settings failed: %w", err)
    }
    dependsOn, ok := settings["depends_on"].([]interface{})
    if !ok {
        return true, nil // No dependencies, so they are met
    }
    for _, depInterface := range dependsOn {
        depMap, ok := depInterface.(map[string]interface{})
        if !ok {
            continue // Skip malformed dependency
        }
        depIDFloat, ok := depMap["id"].(float64) // JSON numbers are float64 in Go
        if !ok {
            continue // Skip invalid ID
        }
        depID := int(depIDFloat)
        var status string
        err := db.QueryRow("SELECT status FROM steps WHERE id = $1", depID).Scan(&status)
        if err != nil {
            if err == sql.ErrNoRows {
                return false, nil // Dependency not found, so not met
            }
            return false, fmt.Errorf("querying dependency status failed: %w", err)
        }
        if status != "completed" {
            return false, nil // Dependency not completed
        }
    }
    return true, nil
}

// TreeSteps fetches all steps and prints them as a dependency tree, grouped by task.
func TreeSteps(db *sql.DB) error {
    // Implementation remains the same as in steps.go
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
        if dependsOnRaw, ok := topLevel["depends_on"]; ok {
            var deps []Dependency
            if err := json.Unmarshal(dependsOnRaw, &deps); err == nil {
                for _, dep := range deps {
                    dependencies[id] = append(dependencies[id], dep.ID)
                }
                continue
            }
        }
    }
    for id, deps := range dependencies {
        for _, depID := range deps {
            if depNode, ok := nodes[depID]; ok {
                nodes[id].Children = append(nodes[id].Children, depNode)
            }
        }
    }
    for _, taskID := range taskIDs {
        fmt.Printf("Task %d: %s\n", taskID, taskNames[taskID])
        for _, node := range taskSteps[taskID] {
            printChildren(node, "")
        }
    }
    return nil
}

// printChildren recursively prints the children of a step node with indentation.
func printChildren(node *StepNode, prefix string) {
    fmt.Printf("%sStep ID: %d, Title: %s\n", prefix, node.ID, node.Title)
    for i, child := range node.Children {
        newPrefix := prefix + "  "
        if i < len(node.Children)-1 {
            fmt.Print(prefix + "├─>")
            printChildren(child, newPrefix+"|")
        } else {
            fmt.Print(prefix + "└─>")
            printChildren(child, newPrefix+" ")
        }
    }
}
