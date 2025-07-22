package models

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RunDockerVolumePoolStep handles the execution logic for Docker volume pool steps.
// Moved from internal/process_docker_volume_pool.go for better maintainability.
func RunDockerVolumePoolStep(db *sql.DB, stepExec *StepExec, logger *log.Logger) error {
	var settings struct {
		DockerVolumePool DockerVolumePoolConfig `json:"docker_volume_pool"`
	}
	if err := json.Unmarshal([]byte(stepExec.Settings), &settings); err != nil {
		return fmt.Errorf("failed to unmarshal step settings: %w", err)
	}
	config := &settings.DockerVolumePool
	
	// Initialize flags for container recreation
	recreateNeeded := false
	forceRecreate := config.Force

	// Get task settings to access local_path and app_folder
	taskSettings, err := GetTaskSettings(db, stepExec.TaskID)
	if err != nil {
		return fmt.Errorf("failed to get task settings: %w", err)
	}

	// Ensure we have app_folder set
	if taskSettings.AppFolder == "" {
		// Fallback to fetching from dependency if not set in task settings
		appFolder, err := FetchAppFolderFromDependency(db, stepExec, config, logger) 
		if err != nil {
			return fmt.Errorf("failed to fetch app_folder from dependency: %w", err)
		}
		taskSettings.AppFolder = appFolder
	}

	// Initialize or update Triggers.Containers if empty or if any container name is empty
	recreateContainers := false
	if len(config.Triggers.Containers) == 0 {
		recreateContainers = true
	} else {
		// Check if any container name is empty
		for patchName, containerName := range config.Triggers.Containers {
			if containerName == "" {
				logger.Printf("Empty container name found for %s, will recreate containers", patchName)
				recreateContainers = true
				break
			}
		}
	}

	if recreateContainers {
		// If no solutions specified, use default solution files based on pool_size
		if len(config.Solutions) == 0 {
			config.Solutions = make([]string, config.PoolSize)
			for i := 0; i < config.PoolSize; i++ {
				config.Solutions[i] = fmt.Sprintf("solution%d.patch", i+1)
			}
			logger.Printf("Initialized solutions from pool_size: %v", config.Solutions)
		}

		// Initialize container map with proper names
		containerMap := make(map[string]string)
		for _, solution := range config.Solutions {
			containerMap[solution] = GenerateDVContainerName(stepExec.TaskID, solution) 
		}
		config.Triggers.Containers = containerMap
		logger.Printf("Initialized Triggers.Containers: %v", config.Triggers.Containers)

		// Force recreation of containers since we just initialized the container map
		recreateNeeded = true
	}

	// Check if we need to recreate containers based on image tag/ID changes or missing containers
	recreateNeeded = false // Reset the flag before checking containers

	// Check each container individually
	for patchName, containerName := range config.Triggers.Containers {
		exists, err := CheckContainerExists(containerName) 
		if err != nil {
			logger.Printf("Error checking if container %s exists: %v", containerName, err)
			recreateNeeded = true
			break
		}

		if !exists {
			logger.Printf("Container %s (for %s) does not exist, will recreate all", containerName, patchName)
			recreateNeeded = true
			break
		}

		// Check if container needs recreation due to image changes
		shouldRecreate, err := ShouldRecreateContainer(containerName, config.Triggers.ImageTag, config.Triggers.ImageID, logger) 
		if err != nil {
			logger.Printf("Error checking if container %s needs recreation: %v", containerName, err)
			recreateNeeded = true
			break
		}

		if shouldRecreate {
			logger.Printf("Container %s (for %s) needs recreation due to image change", containerName, patchName)
			recreateNeeded = true
			break
		}
	}

	// Check if volumes exist - we only care about the solution1_volume since that's what we're using
	solutionVolumePath := filepath.Join(stepExec.LocalPath, "volume_solution1")
	if _, err := os.Stat(solutionVolumePath); os.IsNotExist(err) {
		recreateNeeded = true
		logger.Printf("Volume directory %s doesn't exist, will recreate containers", solutionVolumePath)
	} else {
		logger.Printf("Volume directory %s exists, no need to recreate containers", solutionVolumePath)
	}

	// Even if no recreation is needed, we still need to apply patches
	if !recreateNeeded && !forceRecreate {
		logger.Println("All containers are up-to-date, but will ensure patches are applied")
	} else {
		logger.Println("Container recreation needed, will recreate containers and apply patches")
	}

	// Create containers for each solution
	for i, solutionFile := range config.Solutions {
		solutionNum := i + 1
		containerName, ok := config.Triggers.Containers[solutionFile]
		if !ok || containerName == "" {
			containerName = GenerateDVContainerName(stepExec.TaskID, solutionFile) 
			config.Triggers.Containers[solutionFile] = containerName
			logger.Printf("Generated container name for %s: %s", solutionFile, containerName)
		}
		solutionVolumePath := filepath.Join(stepExec.LocalPath, fmt.Sprintf("volume_solution%d", solutionNum))

		// Remove existing container if it exists
		if exists, _ := CheckContainerExists(containerName); exists {
			logger.Printf("Removing existing container: %s", containerName)
			if err := RemoveDockerContainer(containerName, logger); err != nil {
				return fmt.Errorf("failed to remove existing container: %w", err)
			}
		}

		// Check if we need to recreate the container
		shouldRecreate, err := ShouldRecreateContainer(containerName, config.Triggers.ImageTag, config.Triggers.ImageID, logger)
		if err != nil {
			return fmt.Errorf("error checking if container should be recreated: %w", err)
		}

		if shouldRecreate || forceRecreate {
			// Only recreate the container if needed
			if exists, _ := CheckContainerExists(containerName); exists {
				logger.Printf("Removing existing container: %s", containerName)
				if err := RemoveDockerContainer(containerName, logger); err != nil {
					return fmt.Errorf("failed to remove existing container: %w", err)
				}
			}

			// Start a new container with the volume mounted
			params := make([]string, 0, len(config.Parameters)+10) // Pre-allocate with extra capacity
			
			// Add base parameters
			params = append(params, "--platform", "linux/amd64", "-d")
			params = append(params, "--name", containerName)
			params = append(params, "-v", fmt.Sprintf("%s:%s", solutionVolumePath, taskSettings.AppFolder))
			
			// Add any additional parameters from config
			for _, param := range config.Parameters {
				// Replace placeholders in the parameter
				replaced := param
				replaced = strings.ReplaceAll(replaced, "%%HOSTPATH%%", solutionVolumePath)
				replaced = strings.ReplaceAll(replaced, "%%DOCKERVOLUME%%", taskSettings.AppFolder)
				replaced = strings.ReplaceAll(replaced, "%%IMAGETAG%%", config.Triggers.ImageTag)
				replaced = strings.ReplaceAll(replaced, "%%VOLUME_NAME%%", solutionVolumePath)
				replaced = strings.ReplaceAll(replaced, "%%CONTAINER_NAME%%", containerName)
				replaced = strings.ReplaceAll(replaced, "%%APP_FOLDER%%", taskSettings.AppFolder)
				
				// Handle parameters with spaces that aren't quoted
				if strings.Contains(replaced, " ") && !strings.HasPrefix(replaced, "\"") {
					params = append(params, strings.Fields(replaced)...)
				} else {
					params = append(params, replaced)
				}
			}

			// Ensure the image tag is included in the parameters if not already present
			hasImage := false
			for _, p := range params {
				if p == config.Triggers.ImageTag || strings.Contains(p, "/") {
					hasImage = true
					break
				}
			}

			if !hasImage && config.Triggers.ImageTag != "" {
				params = append(params, config.Triggers.ImageTag)
			}

			// Add keep-alive command if needed
			params = AddKeepAliveCommand(params, config.KeepForever, logger)

			if err := RunDockerCommand(params, containerName, logger, true); err != nil {
				logger.Printf("Error starting container %s: %v", containerName, err)
				return err
			}

			// Wait for container to be running
			if err := WaitForContainerRunning(containerName, 10, logger); err != nil {
				logger.Printf("Error waiting for container %s to start: %v", containerName, err)
				return err
			}
			logger.Printf("Successfully started container %s with volume %s mounted to %s", 
				containerName, solutionVolumePath, taskSettings.AppFolder)

			// Update the stored image ID with the current value after container start
			currentImageID, err := GetCurrentImageID(config.Triggers.ImageTag)
			if err != nil {
				logger.Printf("Error getting current image ID: %v", err)
			} else {
				oldImageID := config.Triggers.ImageID
				config.Triggers.ImageID = currentImageID
				logger.Printf("Debug: Image ID updated from %s to %s", oldImageID, currentImageID)
			}
		} else {
			// Make sure the container is running
			if err := WaitForContainerRunning(containerName, 5, logger); err != nil {
				logger.Printf("Container %s is not running, attempting to start it", containerName)
				startCmd := exec.Command("docker", "start", containerName)
				if output, err := startCmd.CombinedOutput(); err != nil {
					return fmt.Errorf("failed to start container %s: %v, output: %s", containerName, err, string(output))
				}
				if err := WaitForContainerRunning(containerName, 10, logger); err != nil {
					return fmt.Errorf("container %s failed to start: %w", containerName, err)
				}
				logger.Printf("Successfully started container %s", containerName)
			}
		} 

		// Prepare patch file path if it exists
		patchFile := ""
		if solutionFile != "" {
			patchFile = filepath.Join(stepExec.LocalPath, solutionFile)
			if _, err := os.Stat(patchFile); os.IsNotExist(err) {
				logger.Printf("Patch file not found: %s, skipping patch application", patchFile)
				patchFile = ""
			} else {
				logger.Printf("Found patch file: %s", patchFile)
			}
		}

		// Apply git cleanup and patches to the container
		logger.Printf("Applying git cleanup and patches to container %s", containerName)
		if err := ApplyGitCleanupAndPatch(containerName, solutionFile, config.HeldOutTestFile, config.GradingSetupScript, logger); err != nil {
			return fmt.Errorf("failed to apply git cleanup and patches to container %s: %w", containerName, err)
		}

		logger.Printf("Successfully processed container %s with volume %s mounted to %s and applied git cleanup%s", 
			containerName, solutionVolumePath, taskSettings.AppFolder, func() string {
				if patchFile != "" {
					return fmt.Sprintf(" and patch %s", filepath.Base(patchFile))
				}
				return ""
			}())

		// Update the config with the container names for future reference
		if config.Artifacts == nil {
			config.Artifacts = make(map[string]interface{})
		}
		// Store a copy of the container map in Artifacts for backward compatibility
		config.Artifacts["containers"] = config.Triggers.Containers
	}

	// After successful execution, update stored hashes for triggers
	logger.Printf("Debug: Attempting to update step settings with image_id: %s", config.Triggers.ImageID)
	if err := json.Unmarshal([]byte(stepExec.Settings), &settings); err != nil {
		return fmt.Errorf("failed to unmarshal settings for update: %w", err)
	}
	settings.DockerVolumePool.Triggers.ImageID = config.Triggers.ImageID
	updatedSettings, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("failed to marshal updated settings: %w", err)
	}
	_, err = db.Exec("UPDATE steps SET settings = $1 WHERE id = $2", string(updatedSettings), stepExec.StepID)
	if err != nil {
		return fmt.Errorf("failed to update step settings in database: %w", err)
	}
	return nil
}

// FetchAppFolderFromDependency retrieves the app_folder from docker_extract_volume dependencies
func FetchAppFolderFromDependency(db *sql.DB, stepExec *StepExec, config *DockerVolumePoolConfig, logger *log.Logger) (string, error) {
	for _, dep := range config.DependsOn {
		idVal, ok := dep["id"]
		if !ok {
			logger.Printf("No id key in dependency map: %v", dep)
			continue
		}
		stepID := idVal // idVal is already int, no assertion needed
		settingsStr, err := GetStepInfo(db, stepID)
		if err != nil {
			logger.Printf("Error fetching dependent step with ID %d: %v", stepID, err)
			continue
		}
		// Unmarshal the settings string directly
		var depSettings map[string]interface{}
		if err := json.Unmarshal([]byte(settingsStr), &depSettings); err != nil {
			logger.Printf("Failed to unmarshal settings for step ID %d: %v", stepID, err)
			continue
		}
		extractSettings, ok := depSettings["docker_extract_volume"].(map[string]interface{})
		if !ok {
			logger.Printf("docker_extract_volume key not found or invalid in step settings for ID %d", stepID)
			continue
		}
		appFolderVal, exists := extractSettings["app_folder"]
		if !exists {
			logger.Printf("app_folder key not found in docker_extract_volume settings for ID %d", stepID)
			continue
		}
		appFolder, ok := appFolderVal.(string)
		if !ok {
			logger.Printf("app_folder is not a string for step ID %d: %v", stepID, appFolderVal)
			continue
		}
		logger.Printf("Found app_folder '%s' for step ID %d", appFolder, stepID)
		return appFolder, nil
	}
	return "", fmt.Errorf("no docker_extract_volume dependency found or app_folder not set")
}

// AddKeepAliveCommand adds a keep-alive command to parameters if keepForever is true
func AddKeepAliveCommand(params []string, keepForever bool, logger *log.Logger) []string {
	hasKeepAliveCmd := false
	for _, param := range params {
		if (strings.Contains(param, "while true") && strings.Contains(param, "sleep")) ||
			strings.Contains(param, "sleep infinity") ||
			strings.Contains(param, "tail -f") {
			hasKeepAliveCmd = true
			break
		}
	}
	if keepForever && !hasKeepAliveCmd {
		keepAliveArgs := []string{"-c", "while true; do sleep 30; done"}
		params = append(params, keepAliveArgs...)
		logger.Printf("Added keep-alive command to parameters: %v", params)
	}
	return params
}

// RunDockerCommand executes a Docker command with given parameters
func RunDockerCommand(params []string, containerName string, logger *log.Logger, detached bool) error {
	// Add container name conflict handling by removing existing container
	removeCmd := exec.Command("docker", "rm", "-f", containerName)
	removeOutput, removeErr := removeCmd.CombinedOutput()
	if removeErr != nil && !strings.Contains(string(removeOutput), "No such container") {
		logger.Printf("Error removing existing container %s: %v, output: %s", containerName, removeErr, string(removeOutput))
		return fmt.Errorf("failed to remove existing container: %w", removeErr)
	}
	// Flatten params: split on spaces except for -c keep-alive command
	flattened := []string{}
	for i := 0; i < len(params); i++ {
		if params[i] == "-c" && i+1 < len(params) {
			flattened = append(flattened, "-c", params[i+1])
			i++ // Skip next, already appended
		} else {
			parts := strings.Split(params[i], " ")
			for _, part := range parts {
				trimmedPart := strings.TrimSpace(part)
				if trimmedPart != "" {
					flattened = append(flattened, trimmedPart)
				}
			}
		}
	}
	if detached {
		flattened = append([]string{"-d"}, flattened...)
	}
	cmdArgs := append([]string{"run", "--name", containerName}, flattened...)
	logger.Printf("Constructed Docker command for container %s: docker %s", containerName, strings.Join(cmdArgs, " "))
	cmd := exec.Command("docker", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Error running Docker command for container %s: %v, output: %s", containerName, err, string(output))
		return fmt.Errorf("failed to run Docker command: %w", err)
	}
	logger.Printf("Successfully ran Docker command for container %s", containerName)
	if detached {
		if err := WaitForContainerRunning(containerName, 15, logger); err != nil {
			logger.Printf("Container %s did not reach running state: %v", containerName, err)
			return err
		}
		logger.Printf("Container %s is running", containerName)
	}
	return nil
}

// WaitForContainerRunning waits for a container to reach running state
func WaitForContainerRunning(containerName string, timeoutSeconds int, logger *log.Logger) error {
	for i := 0; i < timeoutSeconds; i++ {
		inspectCmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", containerName)
		out, err := inspectCmd.CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) == "true" {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("container %s did not reach running state within %d seconds", containerName, timeoutSeconds)
}

// RemoveDockerContainer removes a Docker container forcefully
func RemoveDockerContainer(name string, logger *log.Logger) error {
	cmd := exec.Command("docker", "rm", "-f", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Error removing container %s: %v, output: %s", name, err, string(output))
		return fmt.Errorf("failed to remove Docker container: %w", err)
	}
	logger.Printf("Removed container: %s", name)
	return nil
}

// ApplyGitCleanupAndPatch applies git cleanup and patches in a container
func ApplyGitCleanupAndPatch(containerName string, patchFile string, heldOutTestFile string, gradingSetupScript string, logger *log.Logger) error {
	commands := []string{
		"cd /app/ansible",
		"git reset --hard HEAD",
		"git checkout -- .",
		"git clean -fd",
	}

	if gradingSetupScript != "" {
		if _, err := os.Stat(gradingSetupScript); err == nil {
			copyCmd := exec.Command("docker", "cp", gradingSetupScript, fmt.Sprintf("%s:/tmp/grading_setup.patch", containerName))
			if output, err := copyCmd.CombinedOutput(); err != nil {
				logger.Printf("Failed to copy grading setup script to container %s: %v, output: %s", containerName, err, string(output))
				return fmt.Errorf("failed to copy grading setup script: %w", err)
			}
			commands = append(commands, "git apply /tmp/grading_setup.patch")
		} else {
			logger.Printf("Grading setup script %s not found, skipping", gradingSetupScript)
		}
	}

	if patchFile != "" {
		copyCmd := exec.Command("docker", "cp", patchFile, fmt.Sprintf("%s:/tmp/solution.patch", containerName))
		if output, err := copyCmd.CombinedOutput(); err != nil {
			logger.Printf("Failed to copy patch file to container %s: %v, output: %s", containerName, err, string(output))
			return fmt.Errorf("failed to copy patch file: %w", err)
		}
		commands = append(commands, "git apply /tmp/solution.patch")
	}

	if heldOutTestFile != "" {
		if _, err := os.Stat(heldOutTestFile); err == nil {
			heldOutCopyCmd := exec.Command("docker", "cp", heldOutTestFile, fmt.Sprintf("%s:/tmp/held_out_test.patch", containerName))
			if output, err := heldOutCopyCmd.CombinedOutput(); err != nil {
				logger.Printf("Failed to copy %s to container %s: %v, output: %s", heldOutTestFile, containerName, err, string(output))
				return fmt.Errorf("failed to copy %s: %w", heldOutTestFile, err)
			}
			commands = append(commands, "git apply /tmp/held_out_test.patch")
		} else {
			logger.Printf("%s not found in current directory, skipping", heldOutTestFile)
		}
	}

	for _, cmdStr := range commands {
		execCmd := exec.Command("docker", "exec", containerName, "bash", "-c", cmdStr)
		if output, err := execCmd.CombinedOutput(); err != nil {
			logger.Printf("Command failed in container %s: %s\nError: %v\nOutput: %s", containerName, cmdStr, err, string(output))
			return fmt.Errorf("failed to execute command in container: %w", err)
		}
		logger.Printf("Executed in container %s: %s", containerName, cmdStr)
	}

	return nil
}

// GenerateDVContainerName generates a consistent container name for Docker volume pool steps
func GenerateDVContainerName(taskID int, patchName string) string {
	return fmt.Sprintf("task_%d_%s_container", taskID, strings.TrimSuffix(patchName, ".patch"))
}

// ShouldRecreateContainer checks if a container needs to be recreated based on image tag or ID changes
func ShouldRecreateContainer(containerName, expectedImageTag, expectedImageID string, logger *log.Logger) (bool, error) {
	logger.Printf("Checking if container %s needs recreation (expected tag: %s, expected ID: %s)", containerName, expectedImageTag, expectedImageID)

	exists, err := CheckContainerExists(containerName)
	if err != nil {
		return false, fmt.Errorf("failed to check if container exists: %w", err)
	}
	if !exists {
		logger.Printf("Container %s does not exist, needs recreation", containerName)
		return true, nil
	}

	cmd := exec.Command("docker", "inspect", "--format", "{{.Image}}", containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to get container image: %w", err)
	}
	currentImageID := strings.TrimSpace(string(output))
	logger.Printf("Container %s current image ID: %s", containerName, currentImageID)

	if expectedImageID != "" {
		cmd = exec.Command("docker", "inspect", "--format", "{{.ID}}", expectedImageID)
		expectedFullImageID, err := cmd.CombinedOutput()
		if err != nil {
			return false, fmt.Errorf("failed to get expected image ID: %w", err)
		}
		trimmedExpectedID := strings.TrimSpace(string(expectedFullImageID))
		logger.Printf("Comparing image IDs - Current: %s, Expected: %s", currentImageID, trimmedExpectedID)
		if !strings.HasPrefix(trimmedExpectedID, currentImageID) {
			logger.Printf("Container %s: Image ID changed from %s to %s", containerName, currentImageID, trimmedExpectedID)
			return true, nil
		}
	}

	if expectedImageTag != "" {
		cmd = exec.Command("docker", "inspect", "--format", "{{.Config.Image}}", containerName)
		output, err = cmd.CombinedOutput()
		if err != nil {
			return false, fmt.Errorf("failed to get container image tag: %w", err)
		}
		currentImageTag := strings.TrimSpace(string(output))
		logger.Printf("Container %s current image tag: %s, expected: %s", containerName, currentImageTag, expectedImageTag)
		if currentImageTag != expectedImageTag {
			logger.Printf("Container %s: Image tag changed from %s to %s", containerName, currentImageTag, expectedImageTag)
			return true, nil
		}
	}

	logger.Printf("Container %s is up-to-date, no need to recreate", containerName)
	return false, nil
}

// CheckContainerExists checks if a Docker container exists
func CheckContainerExists(containerName string) (bool, error) {
	hostname, errHost := os.Hostname()
	if errHost != nil {
		fmt.Printf("Error getting hostname: %v\n", errHost)
	} else {
		fmt.Printf("Host: %s, Checking container existence for %s\n", hostname, containerName)
	}
	cmd := exec.Command("docker", "inspect", "--type=container", containerName)
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("failed to check container %s: %w", containerName, err)
	}
	return true, nil
}

// GetCurrentImageTag retrieves the current image tag for a given image
func GetCurrentImageTag(imageTag string) (string, error) {
	cmd := exec.Command("docker", "inspect", "--format", "{{.Config.Image}}", imageTag)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get current image tag: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// GetCurrentImageID retrieves the current image ID for a given image
func GetCurrentImageID(imageID string) (string, error) {
	cmd := exec.Command("docker", "inspect", "--format", "{{.ID}}", imageID)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get current image ID: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// CheckVolumeExists checks if a Docker volume exists
func CheckVolumeExists(volumeName string) (bool, error) {
	cmd := exec.Command("docker", "volume", "inspect", volumeName)
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("failed to check volume %s: %w", volumeName, err)
	}
	return true, nil
}

// InitializeContainerMap initializes a container map for Docker volume pool steps
func InitializeContainerMap(taskID int, solutions []string) map[string]string {
	containers := make(map[string]string)
	for i := 1; i <= len(solutions); i++ {
		patchName := fmt.Sprintf("solution%d.patch", i)
		containers[patchName] = GenerateDVContainerName(taskID, patchName)
	}
	return containers
}

// CheckFileHashTriggers checks if file hashes have changed for Docker volume pool steps
func CheckFileHashTriggers(db *sql.DB, stepExec *StepExec, config *DockerVolumePoolConfig, logger *log.Logger) (bool, error) {
	runNeeded := false
	for fileName := range config.Triggers.Files {
		filePath := filepath.Join(stepExec.LocalPath, fileName)
		currentHash, err := GetSHA256(filePath)
		if err != nil {
			logger.Printf("Error computing hash for %s: %v", filePath, err)
			return true, err // Treat hash error as a trigger to run
		}
		if currentHash != config.Triggers.Files[fileName] {
			runNeeded = true
		}
	}
	return runNeeded, nil
}

// CheckImageTriggers checks if image triggers have changed for Docker volume pool steps
func CheckImageTriggers(db *sql.DB, stepExec *StepExec, config *DockerVolumePoolConfig, logger *log.Logger) (bool, error) {
	imageChanged := false
	if config.Triggers.ImageTag != "" {
		currentImageTag, err := GetCurrentImageTag(config.Triggers.ImageTag)
		if err != nil {
			logger.Printf("Error getting current image tag: %v; assuming change", err)
			return true, nil
		}
		if currentImageTag != config.Triggers.ImageTag {
			imageChanged = true
		}
	}
	if config.Triggers.ImageID != "" {
		currentImageID, err := GetCurrentImageID(config.Triggers.ImageID)
		if err != nil {
			logger.Printf("Error getting current image ID: %v; assuming change", err)
			return true, nil
		}
		if currentImageID != config.Triggers.ImageID {
			imageChanged = true
		}
	}
	return imageChanged, nil
}

// ... rest of the code remains the same ...
