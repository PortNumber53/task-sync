package internal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/PortNumber53/task-sync/pkg/models"
)

func TestProcessDockerBuildSteps(t *testing.T) {
	// Mock execCommand
	originalExecCommand := execCommand
	execCommand = mockExecCommand
	t.Cleanup(func() { execCommand = originalExecCommand })



	tempDir, err := os.MkdirTemp("", "test-docker-build")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dockerfilePath := filepath.Join(tempDir, "Dockerfile")
		dockerfileContent := []byte("FROM busybox")
		if err := os.WriteFile(dockerfilePath, dockerfileContent, 0644); err != nil {
			t.Fatalf("Failed to write dummy Dockerfile: %v", err)
		}

		h := sha256.New()
		h.Write(dockerfileContent)
		dockerfileHash := hex.EncodeToString(h.Sum(nil))

		t.Run("builds when file timestamp has changed", func(t *testing.T) {
		db, mock, err := sqlmock.New() // Create new mock for this sub-test
		if err != nil {
			t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
		}
		defer db.Close()

		settings := `{
			"docker_build": {
				"files": {
					"Dockerfile": "2024-01-01T00:00:00Z"
				},
				"image_tag": "test-image",
				"params": ["-t %%IMAGETAG%%"]
			}
		}`

		rows := sqlmock.NewRows([]string{"id", "task_id", "settings", "local_path"}).
			AddRow(1, 1, settings, tempDir)

		mock.ExpectQuery("SELECT").WillReturnRows(rows)

		expectedUpdatedConfig := models.DockerBuildConfig{
			ImageID:  "sha256:f29f3b62b95c445652176b516136a8e34a33526a2846985055376b341af34a3e", // Actual ImageID from mockExecCommand
			ImageTag: "test-image",
			Files: map[string]string{
				"Dockerfile": dockerfileHash, // Actual hash of the file
			},
		}
		expectedUpdatedSettings, _ := json.Marshal(map[string]interface{}{
			"docker_build": expectedUpdatedConfig,
		})
		mock.ExpectExec("UPDATE steps SET settings").WithArgs(string(expectedUpdatedSettings), 1).WillReturnResult(sqlmock.NewResult(1, 1))

		// Create a test logger
		logger := log.New(os.Stdout, "[TEST] ", log.LstdFlags)
		processDockerBuildSteps(db, logger)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("skips build when file timestamp is unchanged", func(t *testing.T) {
		db, mock, err := sqlmock.New() // Create new mock for this sub-test
		if err != nil {
			t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
		}
		defer db.Close()



		/*
		// For the 'skips build' test case, we need a consistent timestamp
		// to simulate an unchanged file. We'll use a fixed time.
		initialModTime := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
		*/

		settings := `{
			"docker_build": {
				"files": {
					"Dockerfile": initialModTime,
				},
				"image_id": "existing_image_id",
				"image_tag": "test-image"
			}
		}`

		rows := sqlmock.NewRows([]string{"id", "task_id", "settings", "local_path"}).
			AddRow(1, 1, settings, tempDir)

		mock.ExpectQuery("SELECT").WillReturnRows(rows)

		// Create a test logger
		logger := log.New(os.Stdout, "[TEST] ", log.LstdFlags)
		processDockerBuildSteps(db, logger)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})
}
