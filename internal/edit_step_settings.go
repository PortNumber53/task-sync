package internal

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// EditStepSettings updates a specific field in the step's settings using dot notation
// For example: EditStepSettings(db, 1, "docker_run.image_tag", "new-image:latest")
// Special case: if path is "results", it will update the results column directly
func EditStepSettings(db *sql.DB, stepID int, path string, value interface{}) error {
	// Special case for updating results directly
	if path == "results" {
		if value == nil {
			return ClearStepResults(db, stepID) // ClearStepResults is in steps.go
		}
		// Convert value to JSON string if it's not already
		resultsJSON, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("error marshaling results: %w", err)
		}
		_, err = db.Exec(
			"UPDATE steps SET results = $1::jsonb, updated_at = NOW() WHERE id = $2",
			string(resultsJSON),
			stepID,
		)
		return err
	}
	// Get current settings
	var settingsJSON []byte
	err := db.QueryRow("SELECT settings FROM steps WHERE id = $1", stepID).Scan(&settingsJSON)
	if err != nil {
		return fmt.Errorf("error getting step settings: %w", err)
	}

	// Parse settings into a map
	var settings map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(settingsJSON))
	decoder.UseNumber() // Preserve number types
	if err := decoder.Decode(&settings); err != nil {
		return fmt.Errorf("error parsing settings: %w", err)
	}

	// Split the path by dots to traverse the JSON structure
	parts := strings.Split(path, ".")
	current := settings

	// Navigate to the parent of the target field
	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]
		val, ok := current[part]
		if !ok {
			// Path doesn't exist, create it.
			newMap := make(map[string]interface{})
			current[part] = newMap
			current = newMap
			continue
		}

		// Path exists, ensure it's a map.
		nextMap, isMap := val.(map[string]interface{})
		if !isMap {
			return fmt.Errorf("cannot update path, '%s' is not a map", strings.Join(parts[:i+1], "."))
		}
		current = nextMap
	}

	// Set the value at the final path component
	current[parts[len(parts)-1]] = value

	// Convert back to JSON without HTML escaping
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "") // No indentation to match existing format
	if err := encoder.Encode(settings); err != nil {
		return fmt.Errorf("error marshaling updated settings: %w", err)
	}

	// Update the database
	_, err = db.Exec(
		"UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2",
		strings.TrimSpace(buf.String()),
		stepID,
	)
	if err != nil {
		return fmt.Errorf("error updating step: %w", err)
	}

	return nil
}
