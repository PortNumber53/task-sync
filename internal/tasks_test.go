package internal

import (
	"database/sql"
	"fmt"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestIsValidTaskStatus(t *testing.T) {
	valid := []string{"active", "inactive", "disabled", "running"}
	for _, status := range valid {
		if !isValidTaskStatus(status) {
			t.Errorf("expected status %q to be valid", status)
		}
	}
	invalid := []string{"foo", "bar", "done", ""}
	for _, status := range invalid {
		if isValidTaskStatus(status) {
			t.Errorf("expected status %q to be invalid", status)
		}
	}
}

func TestEditTask(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	tests := []struct {
		name         string
		taskID       int
		updates      map[string]string
		mockSetup    func(mock sqlmock.Sqlmock)
		expectErr    bool
		expectErrStr string
	}{
		{
			name:    "Success - Update name",
			taskID:  1,
			updates: map[string]string{"name": "New Name"},
			mockSetup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE tasks SET name = $1, updated_at = now() WHERE id = $2`)).
					WithArgs("New Name", 1).
					WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit()
			},
			expectErr: false,
		},
		{
			name:    "Success - Update status and localpath",
			taskID:  2,
			updates: map[string]string{"status": "active", "localpath": "/new/path"},
			mockSetup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE tasks SET status = $1, local_path = $2, updated_at = now() WHERE id = $3`)).
					WithArgs("active", "/new/path", 2).
					WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit()
			},
			expectErr: false,
		},
		{
			name:    "Success - Set localpath to empty (NULL)",
			taskID:  3,
			updates: map[string]string{"localpath": ""},
			mockSetup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE tasks SET local_path = $1, updated_at = now() WHERE id = $2`)).
					WithArgs(sql.NullString{String: "", Valid: false}, 3).
					WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit()
			},
			expectErr: false,
		},
		{
			name:         "Error - No updates provided",
			taskID:       1,
			updates:      map[string]string{},
			mockSetup:    func(mock sqlmock.Sqlmock) {},
			expectErr:    true,
			expectErrStr: "no updates provided",
		},
		{
			name:         "Error - Invalid field",
			taskID:       1,
			updates:      map[string]string{"invalid_field": "value"},
			mockSetup:    func(mock sqlmock.Sqlmock) {},
			expectErr:    true,
			expectErrStr: "invalid field to update: invalid_field",
		},
		{
			name:         "Error - Invalid status",
			taskID:       1,
			updates:      map[string]string{"status": "invalid_status"},
			mockSetup:    func(mock sqlmock.Sqlmock) {},
			expectErr:    true,
			expectErrStr: "invalid status: invalid_status (must be one of active|inactive|disabled|running)",
		},
		{
			name:    "Error - Task not found",
			taskID:  99,
			updates: map[string]string{"name": "No Task"},
			mockSetup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE tasks SET name = $1, updated_at = now() WHERE id = $2`)).
					WithArgs("No Task", 99).
					WillReturnResult(sqlmock.NewResult(0, 0)) // 0 rows affected
				mock.ExpectRollback() // Expect rollback because no rows were affected
			},
			expectErr:    true,
			expectErrStr: "task with ID 99 not found or no changes made",
		},
		{
			name:    "Error - DB Commit fails",
			taskID:  1,
			updates: map[string]string{"name": "Commit Fail"},
			mockSetup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE tasks SET name = $1, updated_at = now() WHERE id = $2`)).
					WithArgs("Commit Fail", 1).
					WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit().WillReturnError(fmt.Errorf("commit error"))
			},
			expectErr:    true,
			expectErrStr: "failed to commit transaction: commit error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mockSetup(mock)
			err := EditTask(db, tt.taskID, tt.updates)

			if tt.expectErr {
				if err == nil {
					t.Errorf("expected error, got nil")
					return
				}
				if err.Error() != tt.expectErrStr {
					t.Errorf("expected error string '%s', got '%s'", tt.expectErrStr, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %s", err)
				}
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("there were unfulfilled expectations: %s", err)
			}
		})
	}
}
