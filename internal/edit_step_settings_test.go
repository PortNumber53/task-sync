package internal

import (
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEditStepSettings(t *testing.T) {
	t.Run("update nested setting", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		mock.ExpectQuery("SELECT settings FROM steps WHERE id = $1").
			WithArgs(1).
			WillReturnRows(sqlmock.NewRows([]string{"settings"}).AddRow(`{"docker_run":{"image_tag":"old"}}`))
		mock.ExpectExec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2").
			WithArgs(`{"docker_run":{"image_tag":"newtag"}}`, 1).
			WillReturnResult(sqlmock.NewResult(0, 1))

		err = EditStepSettings(db, 1, "docker_run.image_tag", "newtag")
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err = mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("update results field", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		mock.ExpectExec(`UPDATE steps SET results = $1::jsonb, updated_at = NOW() WHERE id = $2`).
			WithArgs(`{"score":100}`, 2).
			WillReturnResult(sqlmock.NewResult(0, 1))

		err = EditStepSettings(db, 2, "results", map[string]interface{}{"score": 100})
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err = mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("clear results field", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		mock.ExpectExec(`UPDATE steps SET results = NULL, updated_at = NOW() WHERE id = $1`).
			WithArgs(3).
			WillReturnResult(sqlmock.NewResult(0, 1))

		err = EditStepSettings(db, 3, "results", nil)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err = mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("step not found", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		mock.ExpectQuery("SELECT settings FROM steps WHERE id = $1").
			WithArgs(404).
			WillReturnError(sql.ErrNoRows)

		err = EditStepSettings(db, 404, "docker_run.image_tag", "foo")
		if err == nil {
			t.Error("expected error for missing step, got nil")
		}
		if err = mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("db error on update", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		mock.ExpectQuery("SELECT settings FROM steps WHERE id = $1").
			WithArgs(5).
			WillReturnRows(sqlmock.NewRows([]string{"settings"}).AddRow(`{"foo":"bar"}`))
		mock.ExpectExec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2").
			WithArgs(`{"foo":"baz"}`, 5).
			WillReturnError(sql.ErrConnDone)

		err = EditStepSettings(db, 5, "foo", "baz")
		if err == nil {
			t.Error("expected error for DB failure, got nil")
		}
		if err = mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("invalid JSON in settings", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		mock.ExpectQuery("SELECT settings FROM steps WHERE id = $1").
			WithArgs(6).
			WillReturnRows(sqlmock.NewRows([]string{"settings"}).AddRow(`not-json`))

		err = EditStepSettings(db, 6, "foo", "bar")
		if err == nil {
			t.Error("expected error for invalid JSON, got nil")
		}
		if err = mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}
