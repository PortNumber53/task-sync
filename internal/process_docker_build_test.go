package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestProcessDockerBuildSteps(t *testing.T) {
	// Mock execCommand
	originalExecCommand := execCommand
	execCommand = mockExecCommand
	t.Cleanup(func() { execCommand = originalExecCommand })

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	tempDir, err := os.MkdirTemp("", "test-docker-build")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dockerfilePath := filepath.Join(tempDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte("FROM busybox"), 0644); err != nil {
		t.Fatalf("Failed to write dummy Dockerfile: %v", err)
	}

	fileInfo, _ := os.Stat(dockerfilePath)
	initialModTime := fileInfo.ModTime().Format(time.RFC3339)

	t.Run("builds when file timestamp has changed", func(t *testing.T) {
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
		mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}))

		updatedSettings, _ := json.Marshal(DockerBuildConfig{
			DockerBuild: DockerBuild{
				Files:    map[string]string{"Dockerfile": initialModTime},
				ImageTag: "test-image",
				Params:   []string{"-t test-image"},
				ImageID:  "sha256:f29f3b62b95c445652176b516136a8e34a33526a2846985055376b341af34a3e",
			},
		})
		mock.ExpectExec("UPDATE steps SET settings").WithArgs(string(updatedSettings), 1).WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectExec("UPDATE steps SET results").WillReturnResult(sqlmock.NewResult(1, 1))

		processDockerBuildSteps(db)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("skips build when file timestamp is unchanged", func(t *testing.T) {
		settings := `{
			"docker_build": {
				"files": {
					"Dockerfile": "` + initialModTime + `"
				},
				"image_id": "existing_image_id",
				"image_tag": "test-image"
			}
		}`

		rows := sqlmock.NewRows([]string{"id", "task_id", "settings", "local_path"}).
			AddRow(1, 1, settings, tempDir)

		mock.ExpectQuery("SELECT").WillReturnRows(rows)
		mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectExec("UPDATE steps SET results").WithArgs(sqlmock.AnyArg(), 1).WillReturnResult(sqlmock.NewResult(1, 1))

		processDockerBuildSteps(db)

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})
}
