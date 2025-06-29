package internal

import (
	"database/sql"
	"log"
	"strings"
	"testing"
)

// TestExecuteDockerBuild tests the executeDockerBuild function.
func TestExecuteDockerBuild(t *testing.T) {
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
		config      *DockerBuildConfig
		workDir     string
		stepID      int
		db          *sql.DB
		expectErr   bool
		errContains string
	}{
		{
			name: "success",
			config: &DockerBuildConfig{
				DockerBuild: DockerBuild{
					ImageTag: "test-image:latest",
					Params:   []string{"--platform linux/amd64", "-t %%IMAGETAG%%"},
				},
			},
			workDir:   ".",
			stepID:    1,
			db:        nil,
			expectErr: false,
		},
		{
			name: "build failure",
			config: &DockerBuildConfig{
				DockerBuild: DockerBuild{
					ImageTag: "fail-image:latest",
					Params:   []string{"--platform linux/amd64", "-t %%IMAGETAG%%"},
				},
			},
			workDir:     ".",
			stepID:      1,
			db:          nil,
			expectErr:   true,
			errContains: "docker build failed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Set the mock's behavior based on the test case expectation
			mockShouldFail = tc.expectErr

			err := executeDockerBuild(tc.workDir, tc.config, tc.stepID, tc.db)

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
