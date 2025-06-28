package internal

import (
	"errors"
	"log"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestProcessDockerRubricsSteps(t *testing.T) {
	// Initialize logger to avoid nil pointer issues
	stepLogger = log.New(testWriter{}, "", 0)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	t.Run("no active steps", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"id", "task_id", "settings", "local_path"})
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT s.id, s.task_id, s.settings, t.local_path FROM steps s JOIN tasks t ON s.task_id = t.id WHERE t.status = 'active' AND t.local_path IS NOT NULL AND t.local_path <> '' AND s.settings::text LIKE '%docker_rubrics%'`)).
			WillReturnRows(rows)

		processDockerRubricsSteps(db)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("query error", func(t *testing.T) {
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT s.id, s.task_id, s.settings, t.local_path FROM steps s JOIN tasks t ON s.task_id = t.id WHERE t.status = 'active' AND t.local_path IS NOT NULL AND t.local_path <> '' AND s.settings::text LIKE '%docker_rubrics%'`)).
			WillReturnError(errors.New("db error"))

		processDockerRubricsSteps(db)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})
}
