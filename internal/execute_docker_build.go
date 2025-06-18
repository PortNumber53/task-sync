package internal

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// executeDockerBuild executes the docker build command and captures the image ID
func executeDockerBuild(workDir string, config *DockerBuildConfig, stepID int, db *sql.DB) error {
	// Replace image tag placeholder in shell command
	cmdParts := make([]string, len(config.DockerBuild.Shell))
	for i, part := range config.DockerBuild.Shell {
		cmdParts[i] = strings.ReplaceAll(part, "%%IMAGE_TAG%%", config.DockerBuild.ImageTag)
	}

	// Create buffers to capture output
	var stdoutBuf, stderrBuf bytes.Buffer

	// Execute the command
	cmd := exec.Command(cmdParts[0], cmdParts[1:]...)
	cmd.Dir = workDir

	// Create a multi-writer that writes to both the buffer and stdout/stderr
	stdoutWriters := []io.Writer{&stdoutBuf, os.Stdout}
	stderrWriters := []io.Writer{&stderrBuf, os.Stderr}

	cmd.Stdout = io.MultiWriter(stdoutWriters...)
	cmd.Stderr = io.MultiWriter(stderrWriters...)

	stepLogger.Printf("Step %d: Executing docker build: %v\n", stepID, strings.Join(cmdParts, " "))
	err := cmd.Run()

	// Always log the full output for debugging
	stdoutOutput := stdoutBuf.String()
	stderrOutput := stderrBuf.String()

	if len(stdoutOutput) > 0 {
		stepLogger.Printf("Step %d: Docker build stdout:\n%s\n", stepID, stdoutOutput)
	}
	if len(stderrOutput) > 0 {
		stepLogger.Printf("Step %d: Docker build stderr:\n%s\n", stepID, stderrOutput)
	}

	if err != nil {
		// Include both stdout and stderr in the error message
		return fmt.Errorf("docker build failed: %v\nStdout:\n%s\nStderr:\n%s",
			err, stdoutOutput, stderrOutput)
	}

	// Get the image ID
	imageID, err := getDockerImageID(config.DockerBuild.ImageTag)
	if err != nil {
		return fmt.Errorf("failed to get image ID: %w", err)
	}

	// Update the config with the new image ID
	config.DockerBuild.ImageID = imageID

	// The caller (processDockerBuildSteps) is now responsible for marshalling and saving the updated config.
	// This function's responsibility is to execute the build and update the ImageID in the passed config object.

	return nil
}
