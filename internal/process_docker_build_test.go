package internal

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestProcessDockerBuildSteps(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	t.Run("no active docker build steps", func(t *testing.T) {
		query := `SELECT s.id, s.task_id, s.settings, t.local_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.status = 'active'
		AND t.status = 'active'
		AND t.local_path IS NOT NULL
		AND t.local_path <> ''
		AND s.settings::text LIKE '%docker_build%'`

		rows := sqlmock.NewRows([]string{"id", "task_id", "settings", "local_path"})
		mock.ExpectQuery(query).WillReturnRows(rows)

		// This call should not panic
		processDockerBuildSteps(db)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	// TODO: Add more comprehensive tests
}
