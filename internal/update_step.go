package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// StepInfo holds detailed information about a single step.
type StepInfo struct {
	ID        int                    `json:"id"`
	TaskID    int                    `json:"task_id"`
	Title     string                 `json:"title"`
	Settings  map[string]interface{} `json:"settings"`
	Results   map[string]interface{} `json:"results"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// GetStepInfo retrieves detailed information about a specific step by ID
func GetStepInfo(db *sql.DB, stepID int) (*StepInfo, error) {
	var info StepInfo
	var settingsJSON, resultsJSON sql.NullString

	err := db.QueryRow(`
		SELECT s.id, s.task_id, s.title, s.settings::text, s.results::text, s.created_at, s.updated_at
		FROM steps s
		WHERE s.id = $1
	`, stepID).Scan(
		&info.ID, &info.TaskID, &info.Title,
		&settingsJSON, &resultsJSON, &info.CreatedAt, &info.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no step found with ID %d", stepID)
	}
	if err != nil {
		return nil, err
	}

	// Only parse settings if they exist and are not null
	if settingsJSON.Valid && settingsJSON.String != "" && settingsJSON.String != "null" {
		info.Settings = make(map[string]interface{})
		decoder := json.NewDecoder(strings.NewReader(settingsJSON.String))
		decoder.UseNumber()
		if err := decoder.Decode(&info.Settings); err != nil {
			return nil, fmt.Errorf("error parsing settings: %w", err)
		}
	}

	// Only parse results if they exist and are not null
	if resultsJSON.Valid && resultsJSON.String != "" && resultsJSON.String != "null" {
		info.Results = make(map[string]interface{})
		decoder := json.NewDecoder(strings.NewReader(resultsJSON.String))
		decoder.UseNumber()
		if err := decoder.Decode(&info.Results); err != nil {
			return nil, fmt.Errorf("error parsing results: %w", err)
		}
	}

	return &info, nil
}

// setNestedValue sets a value in a nested map based on a dot-separated path.
// It creates intermediate maps if they don't exist.
func setNestedValue(dataMap map[string]interface{}, path string, value interface{}) error {
	parts := strings.Split(path, ".")
	current := dataMap

	for i, part := range parts {
		if i == len(parts)-1 { // Last part, set the value
			current[part] = value
		} else { // Intermediate part, navigate or create map
			if _, ok := current[part]; !ok {
				// Part doesn't exist, create a new map
				current[part] = make(map[string]interface{})
			}

			nextMap, ok := current[part].(map[string]interface{})
			if !ok {
				// Part exists but is not a map, cannot traverse
				return fmt.Errorf("cannot set value at path '%s': segment '%s' is not an object", path, part)
			}
			current = nextMap
		}
	}
	return nil
}

// UpdateStepFieldOrSetting updates a direct field of a step or a key within its settings JSON.
// For settings, dot notation (e.g., "docker_run.image_tag") is supported for nested keys.
// It attempts to parse valueToSet as JSON; if it fails, valueToSet is treated as a string.
func UpdateStepFieldOrSetting(db *sql.DB, stepID int, keyToSet string, valueToSet string) error {
	// List of updatable direct columns in the 'steps' table
	validFields := map[string]bool{
		"title": true,
	}

	// If the key is a direct field on the 'steps' table
	if _, ok := validFields[keyToSet]; ok {
		if keyToSet != "title" { // Ensure it's one of the explicitly handled direct fields
			return fmt.Errorf("invalid field to update: %s", keyToSet)
		}

		query := fmt.Sprintf("UPDATE steps SET %s = $1, updated_at = NOW() WHERE id = $2", keyToSet) // keyToSet is safe due to check above
		result, err := db.Exec(query, valueToSet, stepID)
		if err != nil {
			return fmt.Errorf("error updating step field %s: %w", keyToSet, err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("error checking affected rows for step field %s update: %w", keyToSet, err)
		}
		if rowsAffected == 0 {
			return fmt.Errorf("no step found with ID %d, or %s was already set to '%s'", stepID, keyToSet, valueToSet)
		}
		return nil
	} else {
		// Assume keyToSet is for the 'settings' JSON field
		stepInfo, err := GetStepInfo(db, stepID)
		if err != nil {
			return fmt.Errorf("failed to get step info for step %d: %w", stepID, err)
		}

		if stepInfo.Settings == nil {
			stepInfo.Settings = make(map[string]interface{})
		}

		var jsonValue interface{}
		// Attempt to unmarshal valueToSet to see if it's a JSON primitive (number, boolean, null) or a pre-formatted JSON object/array.
		err = json.Unmarshal([]byte(valueToSet), &jsonValue)
		if err == nil {
			// It's a valid JSON value (e.g. "123", "true", "null", "{\"a\":1}")
			if errSet := setNestedValue(stepInfo.Settings, keyToSet, jsonValue); errSet != nil {
				return fmt.Errorf("failed to set nested key '%s' in settings for step %d: %w", keyToSet, stepID, errSet)
			}
		} else {
			// Not a valid JSON value on its own, so treat it as a plain string.
			if valueToSet == "" {
				// Special case: empty string means remove the key.
				keys := strings.Split(keyToSet, ".")
				currentMap := stepInfo.Settings
				for i, key := range keys {
					if i == len(keys)-1 {
						delete(currentMap, key)
						break
					}
					next, ok := currentMap[key]
					if !ok {
						break
					}
					nextMap, ok := next.(map[string]interface{})
					if !ok {
						// Path is not a map, can't continue.
						break
					}
					currentMap = nextMap
				}
			} else {
				if errSet := setNestedValue(stepInfo.Settings, keyToSet, valueToSet); errSet != nil {
					return fmt.Errorf("failed to set nested key '%s' in settings for step %d: %w", keyToSet, stepID, errSet)
				}
			}
		}

		updatedSettingsBytes, err := json.Marshal(stepInfo.Settings)
		if err != nil {
			return fmt.Errorf("failed to marshal updated settings for step %d: %w", stepID, err)
		}

		result, err := db.Exec(
			"UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2",
			string(updatedSettingsBytes),
			stepID,
		)
		if err != nil {
			return fmt.Errorf("error updating step settings in database for step %d: %w", stepID, err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("error checking affected rows for step ID %d settings update: %w", stepID, err)
		}
		if rowsAffected == 0 {
			return fmt.Errorf("no step found with ID %d during settings update", stepID)
		}
		return nil
	}
}
