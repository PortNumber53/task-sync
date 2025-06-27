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
	cmd := execCommand("docker", "inspect", "-f", "{{.Id}}", tag)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker inspect failed for tag %s: %w, stderr: %s", tag, err, stderr.String())
	}

	return strings.TrimSpace(out.String()), nil
}
