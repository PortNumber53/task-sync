package internal

import (
	"log"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)


func TestProcessDockerPullSteps(t *testing.T) {
	// Initialize logger to avoid nil pointer issues
	stepLogger = log.New(testWriter{}, "", 0)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	// Set up the mock for execCommand
	originalExecCommand := execCommand
	execCommand = mockExecCommand
	defer func() { execCommand = originalExecCommand }()

	t.Run("success case - one step found", func(t *testing.T) {
		settings := `{
			"docker_pull": {
				"image_tag": "hello-world:latest",
				"depends_on": []
			}
		}`

		rows := sqlmock.NewRows([]string{"id", "task_id", "settings", "local_path"}).
			AddRow(1, 1, settings, "/tmp")
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT s.id, s.task_id, s.settings, t.local_path FROM steps s JOIN tasks t ON s.task_id = t.id WHERE s.status = 'active' AND t.status = 'active' AND s.settings ? 'docker_pull'`)).
			WillReturnRows(rows)

		// Mock the three database updates in order. We use sqlmock.AnyArg() for the JSON
		// payloads because they contain dynamic data (like timestamps) and are complex to match exactly.

		// 1. Update settings with the new image ID.
		mock.ExpectExec(regexp.QuoteMeta(`UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2`)).
			WithArgs(sqlmock.AnyArg(), 1).
			WillReturnResult(sqlmock.NewResult(1, 1))

		// 2. Update settings again with the new 'PreventRunBefore' timestamp.
		mock.ExpectExec(regexp.QuoteMeta(`UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2`)).
			WithArgs(sqlmock.AnyArg(), 1).
			WillReturnResult(sqlmock.NewResult(1, 1))

		// 3. Store the final success result.
		mock.ExpectExec(regexp.QuoteMeta(`UPDATE steps SET results = $1::jsonb, updated_at = now() WHERE id = $2`)).
			WithArgs(sqlmock.AnyArg(), 1).
			WillReturnResult(sqlmock.NewResult(1, 1))

		processDockerPullSteps(db)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})
}
