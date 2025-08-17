package models

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// Docker holds the Docker image settings.
type Docker struct {
	ImageID  string `json:"image_id"`
	ImageTag string `json:"image_tag"`
}

// TaskSettings holds the settings for a task.
type TaskSettings struct {
	Docker Docker `json:"docker"`
	AssignContainers map[string]string `json:"assign_containers"`
	AssignedContainers map[string]string `json:"assigned_containers"`
	VolumeName string `json:"volume_name"`
	AppFolder string `json:"app_folder"` // Stores the application folder path for docker_extract_volume
	Platform string `json:"platform,omitempty"` // Target platform for docker builds (e.g., linux/amd64)
	// Legacy containers array (kept for backward compatibility; we will not write to it going forward)
	Containers []ContainerInfo `json:"containers,omitempty"`
	// New canonical containers mapping stored in task.settings as containers_map
	ContainersMap map[string]ContainerInfo `json:"containers_map,omitempty"`
	// New location for docker run parameters
	DockerRunParameters []string `json:"docker_run_parameters,omitempty"`
	BasePath string `json:"base_path,omitempty"` // Renamed from LocalPath
	Rubrics map[string]string `json:"rubrics,omitempty"` // Legacy: Stores rubric UUID -> hash
	RubricSet map[string]string `json:"rubric_set,omitempty"` // New: criterionID -> hash including counter & command
	// Add other fields as needed based on project requirements
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
	} else {
		currentMap = make(map[string]json.RawMessage)
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
	// Sanitize deprecated MHTML keys from the merged JSON before persisting (temporary migration)
	mergedSettingsBytes = SanitizeRawJSONRemoveMHTML(mergedSettingsBytes)

	// Update the database
	_, err = db.Exec("UPDATE tasks SET settings = $1 WHERE id = $2", string(mergedSettingsBytes), taskID)
	if err != nil {
		return fmt.Errorf("failed to update task settings for task %d: %w", taskID, err)
	}

	return nil
}
