package internal

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// execCommand is a package-level variable that can be mocked in tests.
var execCommand = exec.Command

// getDockerImageID retrieves the full image ID (SHA256 digest) for a given Docker image tag.
func getDockerImageID(tag string) (string, error) {
	// First, try inspecting the tag as is. This handles image IDs and fully-qualified tags.
	cmd := execCommand("docker", "inspect", "-f", "{{.Id}}", tag)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return strings.TrimSpace(out.String()), nil
	}

	originalErrStr := stderr.String()

	// If that failed and the tag does not contain a colon, it might be a repo name.
	// Try appending ":latest".
	if !strings.Contains(tag, ":") {
		latestTag := tag + ":latest"
		cmdLatest := execCommand("docker", "inspect", "-f", "{{.Id}}", latestTag)
		var outLatest, stderrLatest bytes.Buffer
		cmdLatest.Stdout = &outLatest
		cmdLatest.Stderr = &stderrLatest

		if errLatest := cmdLatest.Run(); errLatest == nil {
			return strings.TrimSpace(outLatest.String()), nil
		}
	}

	// If all attempts fail, return the original error for clarity.
	return "", fmt.Errorf("docker inspect failed for tag %s: %w, stderr: %s", tag, err, originalErrStr)
}
