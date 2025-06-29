package internal

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/PortNumber53/task-sync/internal/tasks/dynamic_lab"
	"github.com/stretchr/testify/assert"
)



// TestProcessDynamicLabSteps_ContainerIdInheritance tests that a dynamic_lab step
// correctly inherits the container_id from its dependencies, generates new steps
// based on its rubric, and updates the database accordingly.
func TestProcessDynamicLabSteps_ContainerIdInheritance(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	// Mock the dynamic_lab functions
	originalDynamicLabRun := dynamicLabRun
	dynamicLabRun = func(localPath string, files []string, oldHashes map[string]string) (map[string]string, bool, error) {
		return map[string]string{}, true, nil
	}
	originalRubricRun := dynamicLabRubricRun
	dynamicLabRubricRun = func(path, file, hash string) ([]dynamic_lab.Criterion, string, bool, error) {
		return []dynamic_lab.Criterion{{Title: "crit-1", HeldOutTest: "test.sh"}}, "new_rubric_hash", true, nil
	}
	defer func() {
		dynamicLabRun = originalDynamicLabRun
		dynamicLabRubricRun = originalRubricRun
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
	mock.ExpectQuery("SELECT id FROM steps WHERE").WithArgs(1, 2).WillReturnRows(sqlmock.NewRows([]string{"id"})) // deleteGeneratedSteps
	mock.ExpectQuery("SELECT id FROM tasks WHERE").WithArgs(1).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1)) // getTaskByID

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
