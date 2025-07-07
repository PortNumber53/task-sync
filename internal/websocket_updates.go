package internal

import (
	"database/sql"
	"fmt"
)

// InsertWebsocketUpdate inserts a new update into the websocket_updates table.
// Pass nil for taskID or stepID if not applicable.
func InsertWebsocketUpdate(db *sql.DB, updateType string, taskID *int, stepID *int, payload string) error {
	query := `INSERT INTO websocket_updates (update_type, task_id, step_id, payload) VALUES ($1, $2, $3, $4)`
	_, err := db.Exec(query, updateType, taskID, stepID, payload)
	if err != nil {
		return fmt.Errorf("failed to insert websocket update: %w", err)
	}
	return nil
}
