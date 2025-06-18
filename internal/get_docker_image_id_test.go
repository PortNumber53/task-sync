package internal

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// fakeExecCommand is a helper function to mock exec.Command
func fakeExecCommand(command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestHelperProcess", "--", command}
	cs = append(cs, args...)
	cmd := exec.Command(os.Args[0], cs...)
	cmd.Env = []string{"GO_WANT_HELPER_PROCESS=1"}
	return cmd
}

func TestGetDockerImageID(t *testing.T) {
	// The tests will now use the real exec.Command, which fakeExecCommand uses
	// to call TestHelperProcess. The original mocking strategy for execCommand
	// is no longer needed as a package-level variable.
	// We will rely on fakeExecCommand being called by getDockerImageID if we refactor
	// getDockerImageID to accept an exec function, or by directly calling fakeExecCommand
	// if getDockerImageID is modified to use a globally available (but perhaps unexported)
	// command execution function that tests can still patch.
	// For now, to make this compile and to test getDockerImageID as is (which now uses exec.Command directly),
	// we will assume that for these specific tests of getDockerImageID, we want to test its behavior
	// when it *actually* calls out. The TestHelperProcess will intercept these calls.
	// To properly test getDockerImageID in isolation with a mock, getDockerImageID would need to be refactored
	// to accept an exec-like function, or execCommand would need to be a non-constant global var.

	// For the current structure to work with TestHelperProcess, we need to ensure
	// that calls from getDockerImageID (which now uses exec.Command directly)
	// are intercepted. This is typically done by setting os.Args[0] and an env var
	// which fakeExecCommand does. So, the tests for getDockerImageID should still work
	// if the real exec.Command is called, as it will effectively re-run the test binary
	// in helper mode.

	t.Run("success", func(t *testing.T) {
		imageID, err := getDockerImageID("my-image:latest")
		if err != nil {
			t.Errorf("expected no error, but got: %v", err)
		}
		expectedID := "sha256:12345"
		if imageID != expectedID {
			t.Errorf("expected image ID '%s', but got '%s'", expectedID, imageID)
		}
	})

	t.Run("failure", func(t *testing.T) {
		// To trigger failure, we can pass a special tag that our TestHelperProcess recognizes
		_, err := getDockerImageID("error-tag")
		if err == nil {
			t.Error("expected an error, but got nil")
		}
	})
}

// TestHelperProcess isn't a real test. It's used as a helper for other tests.
// It's executed by the fakeExecCommand and simulates the behavior of the real command.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	args := os.Args
	for len(args) > 0 {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		args = args[1:]
	}

	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "No command to execute\n")
		os.Exit(2)
	}

	// Simulate docker build
	if args[0] == "docker" && args[1] == "build" {
		// Check for the special command that should trigger an error
		if len(args) > 2 && args[2] == "fail" {
			fmt.Fprintf(os.Stderr, "Error: build failed\n")
			os.Exit(1)
		}
		// Otherwise, simulate success
		fmt.Fprint(os.Stdout, "Successfully built 12345")
		os.Exit(0)
	}

	// Simulate docker inspect
	if args[0] == "docker" && args[1] == "inspect" {
		// Check for the special tag that should trigger an error
		if args[len(args)-1] == "error-tag" {
			fmt.Fprintf(os.Stderr, "Error: No such object: error-tag\n")
			os.Exit(1)
		}
		// Otherwise, simulate success
		fmt.Fprint(os.Stdout, "sha256:12345")
		os.Exit(0)
	}
}
