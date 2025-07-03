package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// CleanupOldRubricShells finds and deletes rubric_shell steps with the old, flat JSON format.
func CleanupOldRubricShells(db *sql.DB, parentStepID int) error {
	// This query finds all steps that have a dependency on the parentStepID.
		// This query finds all steps that have a dependency on the parentStepID.
	// We cast $1 to an integer to avoid a 'could not determine data type' error with JSONB operations.
	query := `SELECT id, settings FROM steps WHERE settings @> jsonb_build_object('depends_on', jsonb_build_array(jsonb_build_object('id', $1::int)))`
	rows, err := db.Query(query, parentStepID)
	if err != nil {
		return fmt.Errorf("failed to query for dependent steps: %w", err)
	}
	defer rows.Close()

	var stepsToDelete []int
	for rows.Next() {
		var stepID int
		var settingsJSON string
		if err := rows.Scan(&stepID, &settingsJSON); err != nil {
			return fmt.Errorf("failed to scan step row: %w", err)
		}

		var settings map[string]interface{}
		if err := json.Unmarshal([]byte(settingsJSON), &settings); err != nil {
			fmt.Printf("Warning: could not unmarshal settings for step %d, skipping: %v\n", stepID, err)
			continue
		}

		// If 'rubric_shell' key does not exist, it's an old format.
		if _, ok := settings["rubric_shell"]; !ok {
			// Add extra checks to avoid deleting the parent step itself or other important steps.
			if _, ok := settings["rubric_set"]; ok {
				continue
			}
			if _, ok := settings["rubrics_import"]; ok {
				continue
			}
			if _, ok := settings["dynamic_rubric"]; ok {
				continue
			}
			stepsToDelete = append(stepsToDelete, stepID)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating over step rows: %w", err)
	}

	if len(stepsToDelete) == 0 {
		fmt.Println("No old rubric shell steps found to delete.")
		return nil
	}

	fmt.Printf("Found %d old rubric shell steps to delete.\n", len(stepsToDelete))

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() // Rollback on error, commit will override this if successful.

	for _, stepID := range stepsToDelete {
		fmt.Printf("Deleting step %d...\n", stepID)
		if err := models.DeleteStepInTx(tx, stepID); err != nil {
			// The transaction will be rolled back.
			return fmt.Errorf("failed to delete step %d: %w", stepID, err)
		}
	}

	return tx.Commit()
}
