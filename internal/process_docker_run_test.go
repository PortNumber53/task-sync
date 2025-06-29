package internal

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestProcessDockerRunSteps(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	t.Run("no active docker run steps", func(t *testing.T) {
		query := `SELECT s.id, s.task_id, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE t.status = 'active'
		AND t.local_path IS NOT NULL
		AND t.local_path <> ''
		AND s.settings::text LIKE '%docker_run%'`

		rows := sqlmock.NewRows([]string{"id", "task_id", "settings", "local_path"})
		mock.ExpectQuery(query).WillReturnRows(rows)

		// This call should not panic
		processDockerRunSteps(db)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("unmarshals docker run step with array params", func(t *testing.T) {
		settingsJSON := `{
			"docker_run": {
				"parameters": ["--rm", "%%IMAGETAG%%"],
				"image_id": "sha256:fakeid"
			}
		}`

		query := `SELECT s.id, s.task_id, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE t.status = 'active'
		AND t.local_path IS NOT NULL
		AND t.local_path <> ''
		AND s.settings::text LIKE '%docker_run%'`

		rows := sqlmock.NewRows([]string{"id", "task_id", "settings", "local_path"}).
			AddRow(9, 1, settingsJSON, "/fake/path")
		mock.ExpectQuery(query).WillReturnRows(rows)

		// Mock dependency check to fail (return count > 0), which gracefully stops processing for this step.
		// This is sufficient to prove the unmarshaling was successful.
		mock.ExpectQuery("SELECT COUNT").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

		processDockerRunSteps(db)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})
}
