package models

import (
	"database/sql"
	"fmt"
)

// InsertRubricShellOutputHistory inserts a new record into rubric_shell_output_history.
func InsertRubricShellOutputHistory(db *sql.DB, rubricShellUUID, criterion string, required bool, score float64, command, solution1Output, solution2Output, solution3Output, solution4Output, moduleExplanation, exception string) error {
	query := `INSERT INTO rubric_shell_output_history (
		rubric_shell_uuid, criterion, required, score, command,
		solution1_output, solution2_output, solution3_output, solution4_output,
		module_explanation, exception
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	_, err := db.Exec(query,
		rubricShellUUID, criterion, required, score, command,
		solution1Output, solution2Output, solution3Output, solution4Output,
		moduleExplanation, exception,
	)
	if err != nil {
		return fmt.Errorf("failed to insert rubric_shell_output_history: %w", err)
	}
	return nil
}
