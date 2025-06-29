package internal

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os/exec"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestProcessDockerShellSteps(t *testing.T) {
	// Replace the real execCommand with our mock version for all tests in this file.
	originalExecCommand := execCommand
	execCommand = func(name string, arg ...string) *exec.Cmd {
		return mockExecCommand(name, arg...)
	}
	defer func() { execCommand = originalExecCommand }()

	t.Run("inherits image details from dependency", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
		}
		defer db.Close()

		// Capture log output
		var logBuf bytes.Buffer
		stepLogger = log.New(&logBuf, "", 0)
		defer func() {
			// Reset logger
			stepLogger = log.New(io.Discard, "", 0)
		}()

		// --- Test Data ---
		dependencyStepSettings, _ := json.Marshal(map[string]interface{}{
			"docker_run": map[string]interface{}{
				"image_id":  "sha256:abcde12345",
				"image_tag": "dependency-image:v1",
			},
		})

		shellStepSettings, _ := json.Marshal(map[string]interface{}{
			"docker_shell": map[string]interface{}{
				"docker": map[string]interface{}{
					"image_id": "", // Intentionally empty
					"image_tag": "", // Intentionally empty
				},
				"depends_on": []map[string]interface{}{
					{"id": 9},
				},
				"command": []map[string]interface{}{
					{"run": "echo 'hello'"},
				},
			},
		})

		// --- Mock Expectations ---
		mock.ExpectQuery("SELECT s.id, s.task_id, s.settings FROM steps").
			WillReturnRows(sqlmock.NewRows([]string{"id", "task_id", "settings"}).
				AddRow(106, 1, string(shellStepSettings)))

		mock.ExpectQuery("SELECT COUNT").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

		mock.ExpectQuery("SELECT settings FROM steps WHERE id").
			WithArgs(9).
			WillReturnRows(sqlmock.NewRows([]string{"settings"}).
				AddRow(string(dependencyStepSettings)))

		// Make findContainerByImageTag fail to stop execution gracefully after inheritance
		mockShouldFail = true
		defer func() { mockShouldFail = false }()

		mock.ExpectExec("INSERT INTO step_results").
			WithArgs(106, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(1, 1))

		// --- Run Test ---
		processDockerShellSteps(db)

		// --- Assertions ---
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}

		expectedLog := "Step 106: Inherited ImageID 'sha256:abcde12345' and ImageTag 'dependency-image:v1' from dependency step 9"
		if !strings.Contains(logBuf.String(), expectedLog) {
			t.Errorf("expected log to contain '%s', but it was:\n%s", expectedLog, logBuf.String())
		}
	})
}
