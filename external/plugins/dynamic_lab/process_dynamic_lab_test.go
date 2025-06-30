package dynamic_lab

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/PortNumber53/task-sync/pkg/models"
	"github.com/stretchr/testify/assert"
)

// mockFileSystem is a mock implementation of fileSystem
type mockFileSystem struct{}

// mockRubricParser is a mock implementation of rubricParser
type mockRubricParser struct{}

// Run mocks the Run function
func (m *mockFileSystem) Run(localPath string, files []string, oldHashes map[string]string) (map[string]string, bool, error) {
	return map[string]string{}, true, nil
}

// RunRubric mocks the RunRubric function
func (m *mockRubricParser) RunRubric(localPath, file, hash string) ([]models.Criterion, string, bool, error) {
	return []models.Criterion{{
		Title:       "crit-1",
		HeldOutTest: "test.sh",
	}}, "new_rubric_hash", true, nil
}

// TestProcessDynamicLabSteps_ContainerIdInheritance tests that a dynamic_lab step
// correctly inherits the container_id from its dependencies, generates new steps
// based on its rubric, and updates the database accordingly.
func TestProcessDynamicLabSteps_ContainerIdInheritance(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	// Create mocks
	fs := &mockFileSystem{}
	rp := &mockRubricParser{}

	// Save original functions and replace with mocks
	originalFS := fileSystemImpl
	originalRP := rubricParserImpl

	fileSystemImpl = fs
	rubricParserImpl = rp

	// Restore original functions when test is done
	defer func() {
		fileSystemImpl = originalFS
		rubricParserImpl = originalRP
	}()

	// --- Test Data ---
	dependencyResults, _ := json.Marshal(map[string]interface{}{"container_id": "test_container_123"})
	dynamicLabSettings, _ := json.Marshal(map[string]interface{}{
		"dynamic_lab": map[string]interface{}{
			"files":       []string{"file1.txt"},
			"rubric_file": "rubric.json",
			"depends_on":  []map[string]interface{}{{"id": 2}},
		},
	})

	rows := sqlmock.NewRows([]string{"id", "task_id", "title", "settings", "local_path"}).
		AddRow(1, 1, "Test Inheritance Step", string(dynamicLabSettings), "/tmp")

	mock.ExpectQuery("SELECT s.id, s.task_id, s.title, s.settings, COALESCE").WillReturnRows(rows)
	mock.ExpectQuery("SELECT results FROM steps WHERE id").WithArgs(2).WillReturnRows(sqlmock.NewRows([]string{"results"}).AddRow(sql.NullString{String: string(dependencyResults), Valid: true}))
	mock.ExpectExec(`DELETE FROM steps WHERE settings->'generated_by' \? \$1`).WithArgs("1").WillReturnResult(sqlmock.NewResult(1, 1)) // deleteGeneratedSteps
	mock.ExpectQuery("SELECT id FROM tasks WHERE").WithArgs("1").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1)) // getTaskByID

	// This is an INSERT ... RETURNING id, so it's a query
	mock.ExpectQuery("INSERT INTO steps").WithArgs(1, "crit-1", sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(100))

	mock.ExpectExec("UPDATE steps SET settings").WithArgs(sqlmock.AnyArg(), 1).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE steps SET results").WithArgs(sqlmock.AnyArg(), 1).WillReturnResult(sqlmock.NewResult(1, 1))

	err = processDynamicLabSteps(db)
	assert.NoError(t, err)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("there were unfulfilled expectations: %s", err)
	}
}
