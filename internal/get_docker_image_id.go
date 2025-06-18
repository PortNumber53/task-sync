package internal

import (
	"bytes"
	"os/exec"
	"strings"
)

// getDockerImageID retrieves the full image ID (SHA256 digest) for a given Docker image tag.
func getDockerImageID(tag string) (string, error) {
	cmd := exec.Command("docker", "inspect", "-f", "{{.Id}}", tag)
	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return "", err
	}

	return strings.TrimSpace(out.String()), nil
}
