package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
)

// getTaskContainers retrieves all running containers for the task
func getTaskContainers(db *sql.DB, taskID int, stepLogger *log.Logger) ([]string, error) {
	stepLogger.Printf("Debug: Getting task containers for task ID %d", taskID)
	// Query for docker_pool steps in this task that have running containers
	query := `
		SELECT s.settings->'docker_pool'->'containers' 
		FROM steps s 
		WHERE s.task_id = $1 
		  AND s.settings ? 'docker_pool'
	`
	rows, err := db.Query(query, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to query docker_pool steps: %w", err)
	}
	defer rows.Close()

	var containers []string
	for rows.Next() {
		var containersJSON []byte
		if err := rows.Scan(&containersJSON); err != nil {
			stepLogger.Printf("Failed to scan containers: %v", err)
			continue
		}
		stepLogger.Printf("Debug: Queried containers JSON: %s", string(containersJSON))

		// Parse the containers array
		var containerList []struct {
			ContainerName string `json:"container_name"`
		}
		if err := json.Unmarshal(containersJSON, &containerList); err != nil {
			stepLogger.Printf("Failed to unmarshal containers: %v", err)
			continue
		}
		stepLogger.Printf("Debug: Unmarshaled container list: %+v", containerList)

		// Extract container names
		for _, c := range containerList {
			if c.ContainerName != "" {
				containers = append(containers, c.ContainerName)
			}
		}
		stepLogger.Printf("Debug: Extracted containers: %+v", containers)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating container rows: %w", err)
	}

	// Sort containers for consistent ordering
	sort.Strings(containers)
	return containers, nil
}

