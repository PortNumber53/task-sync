package internal

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// executeDockerBuild executes the docker build command and captures the image ID
func executeDockerBuild(workDir string, config *models.DockerBuildConfig, stepID int, db *sql.DB, stepLogger *log.Logger) error {
	// Process docker build parameters, replacing the image tag placeholder
	var buildParams []string
	// Convert DependsOn to a map for easier access
	dependsOnMap := make(map[int]bool)
	for _, dep := range config.DependsOn {
		dependsOnMap[dep.ID] = true
	}

	// Process parameters from the config
	for _, param := range config.Parameters {
		// Replace placeholder for image tag
		processedParam := strings.Replace(param, "%%IMAGETAG%%", config.ImageTag, -1)
		// Split the parameter string into parts to handle flags and their values
		parts := strings.Fields(processedParam)
		buildParams = append(buildParams, parts...)
	}

	// Ensure the -t flag is present if not already added by parameters
	hasTagFlag := false
	for i, param := range buildParams {
		if param == "-t" || param == "--tag" {
			// If -t is a parameter, ensure it has a value.
			// The placeholder replacement should have handled this.
			if i+1 < len(buildParams) {
				hasTagFlag = true
				break
			}
		}
	}
	if !hasTagFlag {
		buildParams = append(buildParams, "-t", config.ImageTag)
	}

	// Defensive check for empty params
	if len(buildParams) == 0 {
		return fmt.Errorf("step %d: docker build params are empty", stepID)
	}

	// Construct the full command
	cmdArgs := append([]string{"build"}, buildParams...)
	stepLogger.Printf("Step %d: constructing docker build command: docker %s %s", stepID, strings.Join(cmdArgs, " "), workDir)

	cmd := exec.Command("docker", append(cmdArgs, workDir)...)
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
		return fmt.Errorf("docker build failed: %w", err)
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
	imageID, err := getDockerImageID(config.ImageTag)
	if err != nil {
		return fmt.Errorf("failed to get image ID: %w", err)
	}

	// Update the config with the new image ID
	config.ImageID = imageID

	// The caller (processDockerBuildSteps) is now responsible for marshalling and saving the updated config.
	// This function's responsibility is to execute the build and update the ImageID in the passed config object.

	return nil
}
