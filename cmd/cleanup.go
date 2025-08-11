package cmd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
)

// HandleCleanup parses CLI args for `cleanup` and runs cleanup operations.
func HandleCleanup(db *sql.DB) {
	if len(os.Args) < 3 {
		fmt.Println("Usage: cleanup <operation>")
		fmt.Println("Available operations:")
		fmt.Println("  legacy-results  - Remove legacy 'results' fields from step settings")
		os.Exit(1)
	}

	operation := os.Args[2]
	switch operation {
	case "legacy-results":
		if err := cleanupLegacyResults(db); err != nil {
			fmt.Printf("Cleanup error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("Unknown cleanup operation: %s\n", operation)
		os.Exit(1)
	}
}

// cleanupLegacyResults removes any legacy 'results' fields from rubric_shell step settings
func cleanupLegacyResults(db *sql.DB) error {
	fmt.Println("Starting cleanup of legacy 'results' fields from step settings...")

	// Query for all steps that contain rubric_shell settings
	query := `
		SELECT id, settings 
		FROM steps 
		WHERE settings ? 'rubric_shell'
		  AND settings->'rubric_shell' ? 'results'
	`
	
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query for steps with legacy results: %w", err)
	}
	defer rows.Close()

	var cleanedCount int
	for rows.Next() {
		var stepID int
		var settingsJSON string
		
		if err := rows.Scan(&stepID, &settingsJSON); err != nil {
			fmt.Printf("Warning: failed to scan step: %v\n", err)
			continue
		}

		// Parse the settings
		var settings map[string]interface{}
		if err := json.Unmarshal([]byte(settingsJSON), &settings); err != nil {
			fmt.Printf("Warning: failed to unmarshal settings for step %d: %v\n", stepID, err)
			continue
		}

		// Remove the results field from rubric_shell
		if rubricShell, ok := settings["rubric_shell"].(map[string]interface{}); ok {
			if _, hasResults := rubricShell["results"]; hasResults {
				delete(rubricShell, "results")
				
				// Marshal back to JSON
				cleanedSettings, err := json.Marshal(settings)
				if err != nil {
					fmt.Printf("Warning: failed to marshal cleaned settings for step %d: %v\n", stepID, err)
					continue
				}

				// Update the database
				_, err = db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(cleanedSettings), stepID)
				if err != nil {
					fmt.Printf("Warning: failed to update settings for step %d: %v\n", stepID, err)
					continue
				}

				fmt.Printf("Cleaned step %d: removed legacy 'results' field\n", stepID)
				cleanedCount++
			}
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating through rows: %w", err)
	}

	fmt.Printf("Cleanup completed. Cleaned %d steps.\n", cleanedCount)
	return nil
}
