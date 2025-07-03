package internal

import (
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// TestExecuteDockerBuild tests the executeDockerBuild function.
func TestExecuteDockerBuild(t *testing.T) {
	// Initialize models.StepLogger
	models.InitStepLogger(log.Writer())
	// Initialize the stepLogger to avoid a nil pointer dereference in the function under test.
	// A testWriter is used to discard log output, keeping test results clean.
	stepLogger = log.New(testWriter{}, "", 0)
	// Set up the mock for execCommand
	originalExecCommand := execCommand
	execCommand = mockExecCommand
	defer func() { execCommand = originalExecCommand }()

	// Save and restore the global mock state to prevent test pollution
	originalMockShouldFail := mockShouldFail
	defer func() { mockShouldFail = originalMockShouldFail }()

	testCases := []struct {
		name        string
		config      *models.DockerBuildConfig
		workDir     string
		stepID      int
		db          *sql.DB
		expectErr   bool
		errContains string
	}{
		{
			name: "success",
			config: &models.DockerBuildConfig{
				ImageTag: "test-image:latest",
				ImageID:  "",
			},
			stepID:    1,
			db:        nil,
			expectErr: false,
		},
		{
			name: "build failure",
			config: &models.DockerBuildConfig{
				ImageTag: "fail-image:latest",
				ImageID:  "",
			},
			stepID:      1,
			db:          nil,
			expectErr:   true,
			errContains: "failed to get image ID",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a temporary directory for the test case
			tempDir, err := os.MkdirTemp("", "test-docker-build")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tempDir)

			// Write a dummy Dockerfile into the temporary directory
			dockerfilePath := filepath.Join(tempDir, "Dockerfile")
			dockerfileContent := []byte("FROM busybox")
			if err := os.WriteFile(dockerfilePath, dockerfileContent, 0644); err != nil {
				t.Fatalf("Failed to write dummy Dockerfile: %v", err)
			}

			// Set the workDir for the test case
			tc.workDir = tempDir

			// Set the mock's behavior based on the test case expectation
			mockShouldFail = tc.expectErr

			err = executeDockerBuild(tc.workDir, tc.config, tc.stepID, tc.db)

			if tc.expectErr {
				if err == nil {
					t.Fatal("expected an error, but got nil")
				}
				if !strings.Contains(err.Error(), tc.errContains) {
					t.Errorf("expected error to contain '%s', but got '%v'", tc.errContains, err)
				}
			} else if err != nil {
				t.Fatalf("expected no error, but got %v", err)
			}
		})
	}
}
