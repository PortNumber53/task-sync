package internal

import (
	"io"
	"log"
	"os"
	"strings"
	"testing"
)

func TestExecuteDockerBuild(t *testing.T) {
	// Initialize the logger to avoid nil pointer dereference in the function under test.
	stepLogger = log.New(io.Discard, "", 0)

	// Monkey patch execCommand for the duration of the test
	// This relies on the fakeExecCommand defined in get_docker_image_id_test.go
	originalExecCommand := execCommand
	execCommand = fakeExecCommand
	defer func() { execCommand = originalExecCommand }()

	// Create a dummy config
	config := &DockerBuildConfig{}
	config.DockerBuild.ImageTag = "test-image:latest"
	config.DockerBuild.Shell = []string{"docker", "build", "-t", "%%IMAGE_TAG%%", "."}

	// Create a temporary directory for the build context
	workDir, err := os.MkdirTemp("", "test-build-dir")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(workDir)

	t.Run("successful build", func(t *testing.T) {
		// Reset image ID and tag for this test
		config.DockerBuild.ImageID = ""
		config.DockerBuild.ImageTag = "test-image:latest"
		config.DockerBuild.Shell = []string{"docker", "build", "-t", "%%IMAGE_TAG%%", "."}

		// The helper process will simulate a successful docker build and a successful docker inspect
		err := executeDockerBuild(workDir, config, 1, nil) // db is not used, so nil is fine
		if err != nil {
			t.Errorf("expected no error, but got: %v", err)
		}

		// Check if the image ID was updated in the config
		expectedID := "sha256:12345"
		if config.DockerBuild.ImageID != expectedID {
			t.Errorf("expected image ID '%s', but got '%s'", expectedID, config.DockerBuild.ImageID)
		}
	})

	t.Run("build failure", func(t *testing.T) {
		// Use a special shell command that the helper process will recognize as a failure
		config.DockerBuild.Shell = []string{"docker", "build", "fail"}

		err := executeDockerBuild(workDir, config, 1, nil)
		if err == nil {
			t.Error("expected an error for build failure, but got nil")
		}
		if !strings.Contains(err.Error(), "docker build failed") {
			t.Errorf("expected error message to contain 'docker build failed', but got: %v", err)
		}
	})

	t.Run("get image id failure", func(t *testing.T) {
		// To trigger this failure, the build must succeed, but the subsequent getDockerImageID must fail.
		// Our helper process can simulate this if we use a special tag.
		config.DockerBuild.ImageTag = "error-tag"
		config.DockerBuild.Shell = []string{"docker", "build", "-t", "%%IMAGE_TAG%%", "."}

		err := executeDockerBuild(workDir, config, 1, nil)
		if err == nil {
			t.Error("expected an error for get image id failure, but got nil")
		}
		if !strings.Contains(err.Error(), "failed to get image ID") {
			t.Errorf("expected error message to contain 'failed to get image ID', but got: %v", err)
		}
	})
}
