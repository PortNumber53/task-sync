package internal

import (
	"bytes"
	"os/exec"
	"strings"
)

// getDockerImageID retrieves the image ID for a given tag
var execCommand = exec.Command

func getDockerImageID(tag string) (string, error) {
	cmd := execCommand("docker", "inspect", "-f", "{{.Id}}", tag)
	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return "", err
	}

	return strings.TrimSpace(out.String()), nil
}
