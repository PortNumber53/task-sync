package models

import (
    "database/sql"
    "fmt"
)

// GetStepInfo retrieves the settings for a given step ID.
func GetStepInfo(db *sql.DB, stepID int) (string, error) {
    var settings string
    err := db.QueryRow("SELECT settings FROM steps WHERE id = $1", stepID).Scan(&settings)
    if err != nil {
        if err == sql.ErrNoRows {
            return "", fmt.Errorf("no step found with ID %d", stepID)
        }
        return "", fmt.Errorf("querying step info failed: %w", err)
    }
    return settings, nil
}

// GetStepsByType retrieves all steps of a given type.
func GetStepsByType(db *sql.DB, stepType string) ([]StepExec, error) {
    rows, err := db.Query("SELECT id, task_id, title, settings FROM steps WHERE settings LIKE $1 ORDER BY id", "%\"type\":"+stepType+"%")
    if err != nil {
        return nil, fmt.Errorf("querying steps by type failed: %w", err)
    }
    defer rows.Close()

    var steps []StepExec
    for rows.Next() {
        var s StepExec
        if err := rows.Scan(&s.StepID, &s.TaskID, &s.Title, &s.Settings); err != nil {
            return nil, err
        }
        steps = append(steps, s)
    }
    if err = rows.Err(); err != nil {
        return nil, err
    }
    return steps, nil
}
