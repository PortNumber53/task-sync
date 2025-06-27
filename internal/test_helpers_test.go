package internal

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// A simple io.Writer that discards its input, to suppress log output during tests.
type testWriter struct{}

func (tw testWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}

// mockExecCommand replaces the real exec.Command with a call to a helper process.
// This allows us to simulate the behavior of external commands without actually running them.
func mockExecCommand(command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestHelperProcess", "--", command}
	cs = append(cs, args...)
	cmd := exec.Command(os.Args[0], cs...)
	// This environment variable is crucial for the helper process to identify when it's being run as a mock.
	cmd.Env = []string{"GO_WANT_HELPER_PROCESS=1"}
	return cmd
}

// TestHelperProcess is a helper process that gets executed by mockExecCommand.
// It checks the command line arguments to determine which command is being simulated
// and provides the appropriate mock output.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	// os.Args are: [/path/to/test/binary, -test.run=TestHelperProcess, --, command, arg1, arg2, ...]
	args := os.Args[3:]
	cmd, cmdArgs := args[0], args[1:]

	// Check for the failure tag in the arguments to simulate an error
	isFailureCase := false
	for _, arg := range cmdArgs {
		if arg == "fail-image:latest" {
			isFailureCase = true
			break
		}
	}

	if cmd == "docker" {
		if isFailureCase {
			fmt.Fprintln(os.Stderr, "simulated docker error for fail-image:latest")
			os.Exit(1)
		}

		switch cmdArgs[0] {
		case "inspect":
			// Handle different inspect formats based on args
			formatArg := cmdArgs[1]
			if formatArg == "-f" {
				formatString := cmdArgs[2]
				if formatString == "{{.Id}}" {
					// Mock for getDockerImageID
					fmt.Fprintln(os.Stdout, "sha256:f29f3b62b95c445652176b516136a8e34a33526a2846985055376b341af34a3e")
				} else if formatString == "{{.State.Running}}" {
					// Mock for processDockerShellSteps
					fmt.Fprintln(os.Stdout, "true")
				}
			}
		case "build":
			// Mock output for 'docker build'
			fmt.Fprintln(os.Stdout, "Successfully tagged my-image:latest")
			fmt.Fprintln(os.Stdout, "sha256:9e34b637a77e3f2d6d4da1cec246f945d8866567c5354522c685252d4c5a369e")
		case "exec":
			// Mock for 'docker exec'
			fmt.Fprint(os.Stdout, "")
		}
	}

	// If we haven't exited with an error, exit with success.
	os.Exit(0)
}
