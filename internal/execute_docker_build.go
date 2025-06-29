package internal

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"
)

// executeDockerBuild executes the docker build command and captures the image ID
func executeDockerBuild(workDir string, config *DockerBuildConfig, stepID int, db *sql.DB) error {
	// Process docker build parameters, replacing the image tag placeholder
	var buildParams []string
	for i, param := range config.DockerBuild.Params {
		if strings.Contains(param, "%%IMAGETAG%%") {
			config.DockerBuild.Params[i] = strings.ReplaceAll(param, "%%IMAGETAG%%", config.DockerBuild.ImageTag)
		}
		buildParams = append(buildParams, strings.Fields(param)...)
	}

	// Defensive check for empty params
	if len(buildParams) == 0 {
		return fmt.Errorf("step %d: docker build params are empty", stepID)
	}

	// Construct the full command
	cmdArgs := append([]string{"build"}, buildParams...)
	cmdArgs = append(cmdArgs, workDir) // Append the build context path
	stepLogger.Printf("Step %d: constructing docker build command: docker %s", stepID, strings.Join(cmdArgs, " "))

	cmd := execCommand("docker", cmdArgs...)
	cmd.Dir = workDir

	// Create buffers and multi-writers to capture output
	var stdoutBuf, stderrBuf bytes.Buffer
	stdoutWriters := []io.Writer{&stdoutBuf, os.Stdout}
	stderrWriters := []io.Writer{&stderrBuf, os.Stderr}
	cmd.Stdout = io.MultiWriter(stdoutWriters...)
	cmd.Stderr = io.MultiWriter(stderrWriters...)

	if err := cmd.Run(); err != nil {
		// Always log the full output for debugging
		stdoutOutput := stdoutBuf.String()
		stderrOutput := stderrBuf.String()
		if len(stdoutOutput) > 0 {
			stepLogger.Printf("Step %d: Docker build stdout:\n%s\n", stepID, stdoutOutput)
		}
		if len(stderrOutput) > 0 {
			stepLogger.Printf("Step %d: Docker build stderr:\n%s\n", stepID, stderrOutput)
		}
		return fmt.Errorf("docker build failed: %v", err)
	}

	// Log the full output for debugging on success as well
	stdoutOutput := stdoutBuf.String()
	stderrOutput := stderrBuf.String()

	if len(stdoutOutput) > 0 {
		stepLogger.Printf("Step %d: Docker build stdout:\n%s\n", stepID, stdoutOutput)
	}
	if len(stderrOutput) > 0 {
		stepLogger.Printf("Step %d: Docker build stderr:\n%s\n", stepID, stderrOutput)
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
