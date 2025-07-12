package models

import (
    "database/sql"
    "fmt"
    "strconv"
)

// CreateStep inserts a new step for a task and returns the new step's ID.
func CreateStep(db *sql.DB, taskRef string, title, settings string) (int, error) {
    // Implementation remains the same as in steps.go
    var taskID int
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

// UpdateStep updates a step's title and settings.
func UpdateStep(db *sql.DB, stepID int, title, settings string) error {
    // Implementation remains the same as in steps.go
    _, err := db.Exec("UPDATE steps SET title = $1, settings = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $3", title, settings, stepID)
    if err != nil {
        return fmt.Errorf("updating step failed: %w", err)
    }
    return nil
}

// DeleteStep removes a step from the database by its ID.
func DeleteStep(db *sql.DB, stepID int) error {
    // Implementation remains the same as in steps.go
    _, err := db.Exec("DELETE FROM steps WHERE id = $1", stepID)
    if err != nil {
        return fmt.Errorf("deleting step failed: %w", err)
    }
    return nil
}

// DeleteStepInTx removes a step from the database by its ID within a transaction.
func DeleteStepInTx(tx *sql.Tx, stepID int) error {
    // Implementation remains the same as in steps.go
    _, err := tx.Exec("DELETE FROM steps WHERE id = $1", stepID)
    if err != nil {
        return fmt.Errorf("deleting step in transaction failed: %w", err)
    }
    return nil
}

// ListSteps prints all steps in the DB. If full is true, prints settings column too.
func ListSteps(db *sql.DB, full bool) error {
    // Implementation remains the same as in steps.go
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
        var settings sql.NullString
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
