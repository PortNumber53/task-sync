package internal

import (
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/PortNumber53/task-sync/pkg/models"
)

func TestStoreStepResult(t *testing.T) {
	result := map[string]interface{}{"score": 42}
	resultJSON := `{"score":42}`

	t.Run("success", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		mock.ExpectExec(`UPDATE steps SET results = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`).
			WithArgs(resultJSON, 1).
			WillReturnResult(sqlmock.NewResult(0, 1))

		err = models.StoreStepResult(db, 1, result)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("step not found", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		mock.ExpectExec(`UPDATE steps SET results = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`).
			WithArgs(resultJSON, 1).
			WillReturnResult(sqlmock.NewResult(0, 0)) // 0 rows affected

		err = models.StoreStepResult(db, 1, result)
		if err == nil {
			t.Error("expected error for missing step, got nil")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("db error", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		mock.ExpectExec(`UPDATE steps SET results = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`).
			WithArgs(resultJSON, 2).
			WillReturnError(sql.ErrConnDone)

		err = models.StoreStepResult(db, 2, result)
		if err == nil {
			t.Error("expected error for DB failure, got nil")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}
