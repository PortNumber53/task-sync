package internal

import (
	"testing"
)

func TestGetDockerImageID(t *testing.T) {
	// Store original execCommand and restore it after the test.
	// This test will now use the mockExecCommand and unified TestMain
	// from execute_docker_build_test.go
	originalExecCommand := execCommand
	execCommand = mockExecCommand
	defer func() { execCommand = originalExecCommand }()

		t.Run("success", func(t *testing.T) {
		imageID, err := getDockerImageID("my-image:latest")
		if err != nil {
			t.Errorf("expected no error, but got: %v", err)
		}
		// This ID comes from the mockExecCommand defined in execute_docker_build_test.go
		expectedID := "sha256:f29f3b62b95c445652176b516136a8e34a33526a2846985055376b341af34a3e"
		if imageID != expectedID {
			t.Errorf("expected image ID '%s', but got '%s'", expectedID, imageID)
		}
	})

		t.Run("failure", func(t *testing.T) {
		// To trigger failure, we use a special tag that our mockExecCommand recognizes
		_, err := getDockerImageID("fail-image:latest")
		if err == nil {
			t.Error("expected an error, but got nil")
		}
	})
}
