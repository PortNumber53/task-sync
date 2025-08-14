package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
)

// getTaskContainers retrieves all running containers for the task from tasks.settings.containers_map
func getTaskContainers(db *sql.DB, taskID int, stepLogger *log.Logger) ([]string, error) {
    stepLogger.Printf("Debug: Getting task containers (containers_map) for task ID %d", taskID)
    query := `
        SELECT t.settings->'containers_map'
        FROM tasks t
        WHERE t.id = $1
    `
    var containersJSON []byte
    if err := db.QueryRow(query, taskID).Scan(&containersJSON); err != nil {
        return nil, fmt.Errorf("failed to query task containers_map: %w", err)
    }

    if len(containersJSON) == 0 || string(containersJSON) == "null" {
        stepLogger.Printf("Debug: containers_map is empty or null for task %d", taskID)
        return []string{}, nil
    }

    // containers_map: map[string]{container_id, container_name}
    var containersMap map[string]struct{
        ContainerID string `json:"container_id"`
        ContainerName string `json:"container_name"`
    }
    if err := json.Unmarshal(containersJSON, &containersMap); err != nil {
        return nil, fmt.Errorf("failed to unmarshal containers_map: %w", err)
    }

    // Collect names in preferred order
    preferredKeys := []string{"original", "golden", "solution1", "solution2", "solution3", "solution4"}
    var containers []string
    for _, k := range preferredKeys {
        if v, ok := containersMap[k]; ok && v.ContainerName != "" {
            containers = append(containers, v.ContainerName)
        }
    }
    // Append any other keys sorted for determinism
    other := make([]string, 0)
    for k, v := range containersMap {
        skip := false
        for _, pk := range preferredKeys { if k == pk { skip = true; break } }
        if skip { continue }
        if v.ContainerName != "" {
            other = append(other, v.ContainerName)
        }
    }
    sort.Strings(other)
    containers = append(containers, other...)

    stepLogger.Printf("Debug: Extracted containers from containers_map: %+v", containers)
    return containers, nil
}
