package internal

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestProcessFileExistsSteps(t *testing.T) {
	// Initialize logger to avoid nil pointer issues
	stepLogger = log.New(testWriter{}, "", 0)
	tempDir := t.TempDir()
	testFile1 := filepath.Join(tempDir, "testfile1.txt")
	if err := os.WriteFile(testFile1, []byte("content1"), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	query := `SELECT s.id, s.task_id, s.settings, t.local_path FROM steps s JOIN tasks t ON s.task_id = t.id WHERE t.status = 'active' AND t.local_path IS NOT NULL AND t.local_path <> '' AND s.settings ? 'file_exists'`

	testCases := []struct {
		name                 string
		stepID               int
		settings             map[string]interface{}
		expectSettingsUpdate bool
		expectedResult       map[string]interface{}
		expectResultAny      bool
	}{
		{
			name:   "all files exist",
			stepID: 1,
			settings: map[string]interface{}{
				"file_exists": map[string]interface{}{
					"files": map[string]interface{}{
						"testfile1.txt": "__last_modified__",
					},
				},
			},
			expectSettingsUpdate: true,
			expectedResult: map[string]interface{}{
				"result": "success",
			},
			expectResultAny: true, // because timestamp is dynamic
		},
		{
			name:   "one file does not exist",
			stepID: 2,
			settings: map[string]interface{}{
				"file_exists": map[string]interface{}{
					"files": map[string]interface{}{
						"testfile1.txt":   "__last_modified__",
						"nonexistent.txt": "__last_modified__",
					},
				},
			},
			expectSettingsUpdate: false,
			expectedResult: map[string]interface{}{
				"result":  "failure",
				"message": "file not found: nonexistent.txt",
			},
			expectResultAny: false,
		},
		{
			name:   "invalid settings structure - files key missing",
			stepID: 3,
			settings: map[string]interface{}{
				"file_exists": map[string]interface{}{
					"other_key": "value",
				},
			},
			expectSettingsUpdate: false,
			expectedResult: map[string]interface{}{
				"result":  "failure",
				"message": "'files' key missing in 'file_exists' settings",
			},
			expectResultAny: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
			}
			defer db.Close()

			settingsBytes, _ := json.Marshal(tc.settings)
			rows := sqlmock.NewRows([]string{"id", "task_id", "settings", "local_path"}).
				AddRow(tc.stepID, 1, string(settingsBytes), tempDir)

			mock.ExpectQuery(regexp.QuoteMeta(query)).WillReturnRows(rows)

			if tc.expectSettingsUpdate {
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2`)).
					WithArgs(sqlmock.AnyArg(), tc.stepID).
					WillReturnResult(sqlmock.NewResult(1, 1))
			}

			var expectedResultArg interface{}
			if tc.expectResultAny {
				expectedResultArg = sqlmock.AnyArg()
			} else {
				resultBytes, _ := json.Marshal(tc.expectedResult)
				expectedResultArg = string(resultBytes)
			}

			mock.ExpectExec(regexp.QuoteMeta(`UPDATE steps SET results = $1::jsonb, updated_at = now() WHERE id = $2`)).
				WithArgs(expectedResultArg, tc.stepID).
				WillReturnResult(sqlmock.NewResult(1, 1))

			processFileExistsSteps(db)

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("there were unfulfilled expectations: %s", err)
			}
		})
	}
}
