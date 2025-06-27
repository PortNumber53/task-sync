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
	testFile := filepath.Join(tempDir, "testfile.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	query := `SELECT s.id, s.task_id, s.settings, t.local_path FROM steps s JOIN tasks t ON s.task_id = t.id WHERE s.status = 'active' AND t.status = 'active' AND t.local_path IS NOT NULL AND t.local_path <> '' AND s.settings ? 'file_exists'`

	testCases := []struct {
		name           string
		stepID         int
		settings       map[string]interface{}
		expectedResult map[string]interface{}
	}{
		{
			name:           "file exists",
			stepID:         1,
			settings:       map[string]interface{}{"file_exists": "testfile.txt"},
			expectedResult: map[string]interface{}{"result": "success"},
		},
		{
			name:           "file does not exist",
			stepID:         2,
			settings:       map[string]interface{}{"file_exists": "nonexistent.txt"},
			expectedResult: map[string]interface{}{"result": "failure", "message": "file not found: nonexistent.txt"},
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

			resultBytes, _ := json.Marshal(tc.expectedResult)
			mock.ExpectExec(regexp.QuoteMeta(`UPDATE steps SET results = $1::jsonb, updated_at = now() WHERE id = $2`)).
				WithArgs(string(resultBytes), tc.stepID).
				WillReturnResult(sqlmock.NewResult(1, 1))

			processFileExistsSteps(db)

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("there were unfulfilled expectations: %s", err)
			}
		})
	}
}
