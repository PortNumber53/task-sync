package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// StoreStepResult stores the execution result of a step
func StoreStepResult(db *sql.DB, stepID int, result map[string]interface{}) error {
	resJson, _ := json.Marshal(result)
	resultExec, err := db.Exec(`UPDATE steps SET results = $1::jsonb, updated_at = now() WHERE id = $2`, string(resJson), stepID)
	if err != nil {
		return fmt.Errorf("failed to update results for step %d: %w", stepID, err)
	}
	rowsAffected, err := resultExec.RowsAffected()
	if err != nil {
		return fmt.Errorf("error checking affected rows: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("no step found with ID %d", stepID)
	}
	return nil
}
