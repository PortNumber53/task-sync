package internal

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

	"github.com/PortNumber53/task-sync/pkg/models"
)

// ProcessDockerVolumePoolStep handles the execution of a docker_volume_pool step.
// It checks triggers for file hash changes, image mismatches, and container assignments,
// and supports a force parameter to override conditions.
func ProcessDockerVolumePoolStep(db *sql.DB, stepExec *models.StepExec, stepLogger *log.Logger) error {
	stepLogger.Printf("Debug: Raw step settings: %s", string(stepExec.Settings))
	var settings struct {
		DockerVolumePool models.DockerVolumePoolConfig `json:"docker_volume_pool"`
	}
	if err := json.Unmarshal([]byte(stepExec.Settings), &settings); err != nil {
		return fmt.Errorf("failed to unmarshal step settings: %w", err)
	}
	stepLogger.Printf("Unmarshaled settings: %+v", settings.DockerVolumePool)
	stepLogger.Printf("Solutions loaded: %v", settings.DockerVolumePool.Solutions)
	config := &settings.DockerVolumePool

	// Check for force flag
	if config.Force {
		stepLogger.Println("Force flag set; running step regardless of triggers")
		return runDockerVolumePoolStep(db, stepExec, stepLogger)
	}

	// Initialize or update Triggers.Containers if empty
	if len(config.Triggers.Containers) == 0 {
		config.Triggers.Containers = initializeContainerMap(stepExec.TaskID, config.Solutions)
		stepLogger.Printf("Initialized Triggers.Containers: %v", config.Triggers.Containers)
	}

	// Gather container and volume names for checks
	containerList := make([]string, 0, len(config.Triggers.Containers))
	volumeList := make([]string, 0, len(config.Triggers.Containers))
	for patch, container := range config.Triggers.Containers {
		containerList = append(containerList, container)
		volumeList = append(volumeList, fmt.Sprintf("task_%d_%s_volume", stepExec.TaskID, patch))
	}

	// Check image triggers
	imageChanged, err := checkImageTriggers(db, stepExec, config, stepLogger)
	if err != nil {
		return err
	}

	// Check container existence
	containersExist := CheckArtifactContainersExist(containerList, stepLogger)

	// Check volume existence
	volumesExist := CheckArtifactVolumesExist(volumeList, stepLogger)

	// Check file hash triggers
	filesChanged, err := checkFileHashTriggers(db, stepExec, config, stepLogger)
	if err != nil {
		return err
	}

	// Decision logic
	if imageChanged || !containersExist || !volumesExist {
		stepLogger.Printf("[Trigger] Image changed: %v, Containers exist: %v, Volumes exist: %v", imageChanged, containersExist, volumesExist)
		stepLogger.Println("Triggering full rebuild: recreate containers and volumes, run all steps")
		return runDockerVolumePoolStep(db, stepExec, stepLogger)
	}
	if filesChanged {
		stepLogger.Println("Triggering partial rebuild: files changed, redoing file/patch/git/volume steps, containers reused")
		// TODO: Implement partial step (reuse containers/volumes, only redo file ops)
		return runDockerVolumePoolStep(db, stepExec, stepLogger) // Placeholder: full run for now
	}

	stepLogger.Println("No triggers activated; skipping step execution")
	return nil
}

// Helper function to check file hash triggers
func checkFileHashTriggers(db *sql.DB, stepExec *models.StepExec, config *models.DockerVolumePoolConfig, logger *log.Logger) (bool, error) {
	runNeeded := false
	for fileName, storedHash := range config.Triggers.Files {
		filePath := filepath.Join(stepExec.LocalPath, fileName)
		currentHash, err := models.GetSHA256(filePath) // Use existing models.GetSHA256
		if err != nil {
			logger.Printf("Error computing hash for %s: %v", filePath, err)
			return true, err // Treat hash error as a trigger to run
		}
		if currentHash != storedHash {
			runNeeded = true
			// Update stored hash if needed, but for now just flag run
		}
	}
	return runNeeded, nil
}

// Helper function to check image triggers
func checkImageTriggers(db *sql.DB, stepExec *models.StepExec, config *models.DockerVolumePoolConfig, logger *log.Logger) (bool, error) {
	taskSettings, err := models.GetTaskSettings(db, stepExec.TaskID)
	if err != nil {
		return false, fmt.Errorf("failed to get task settings: %w", err)
	}
	runNeeded := false
	if config.Triggers.ImageID != "" && config.Triggers.ImageID != taskSettings.Docker.ImageID {
		runNeeded = true
	}
	if config.Triggers.ImageTag != "" && config.Triggers.ImageTag != taskSettings.Docker.ImageTag {
		runNeeded = true
	}
	return runNeeded, nil
}

// Helper function to check container triggers
func checkContainerTriggers(db *sql.DB, stepExec *models.StepExec, config *models.DockerVolumePoolConfig, logger *log.Logger) (bool, error) {
	taskSettings, err := models.GetTaskSettings(db, stepExec.TaskID)
	if err != nil {
		return false, fmt.Errorf("failed to get task settings: %w", err)
	}

	// Use AssignedContainers from struct (task.settings.assigned_containers)
	containerMap := taskSettings.AssignedContainers
	logger.Printf("[TriggerCheck] Using taskSettings.AssignedContainers: %v", containerMap)

	logger.Printf("[TriggerCheck] Comparing step triggers.containers: %v", config.Triggers.Containers)
	logger.Printf("[TriggerCheck] Against task assigned_containers: %v", containerMap)
	runNeeded := false
	for fileName, expectedContainer := range config.Triggers.Containers {
		actualContainer, exists := containerMap[fileName]
		if !exists {
			logger.Printf("[TriggerCheck] File %s missing in assigned_containers, expected: %s", fileName, expectedContainer)
			runNeeded = true
		} else if actualContainer != expectedContainer {
			logger.Printf("[TriggerCheck] File %s container mismatch: assigned=%s, expected=%s", fileName, actualContainer, expectedContainer)
			runNeeded = true
		} else {
			logger.Printf("[TriggerCheck] File %s container matches: %s", fileName, actualContainer)
		}
	}
	return runNeeded, nil
}

// Helper function to replace placeholders in parameters slice
func replacePlaceholders(params []string, replacements map[string]string) []string {
	result := make([]string, len(params))
	for i, param := range params {
		for key, value := range replacements {
			param = strings.ReplaceAll(param, key, value)
		}
		result[i] = param
	}
	return result
}

func runDockerVolumePoolStep(db *sql.DB, stepExec *models.StepExec, logger *log.Logger) error {
	var settings struct {
		DockerVolumePool models.DockerVolumePoolConfig `json:"docker_volume_pool"`
	}
	if err := json.Unmarshal([]byte(stepExec.Settings), &settings); err != nil {
		return fmt.Errorf("failed to unmarshal step settings: %w", err)
	}
	config := &settings.DockerVolumePool
	
	// Initialize flags for container recreation
	recreateNeeded := false
	forceRecreate := config.Force

	// Get task settings to access local_path and app_folder
	taskSettings, err := models.GetTaskSettings(db, stepExec.TaskID)
	if err != nil {
		return fmt.Errorf("failed to get task settings: %w", err)
	}

	// Ensure we have app_folder set
	if taskSettings.AppFolder == "" {
		// Fallback to fetching from dependency if not set in task settings
		appFolder, err := fetchAppFolderFromDependency(db, stepExec, config, logger)
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
			containerMap[solution] = getContainerName(stepExec.TaskID, solution)
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
		exists, err := checkContainerExists(containerName)
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
		shouldRecreate, err := shouldRecreateContainer(containerName, config.Triggers.ImageTag, config.Triggers.ImageID, logger)
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
	solutionVolumePath := filepath.Join(stepExec.LocalPath, "solution1_volume")
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
		// If recreation is needed, we'll handle it in the loop below
		logger.Println("Container recreation needed, will recreate containers and apply patches")
	}

	// Create containers for each solution
	for i, solutionFile := range config.Solutions {
		solutionNum := i + 1
		containerName, ok := config.Triggers.Containers[solutionFile]
		if !ok || containerName == "" {
			containerName = getContainerName(stepExec.TaskID, solutionFile)
			config.Triggers.Containers[solutionFile] = containerName
			logger.Printf("Generated container name for %s: %s", solutionFile, containerName)
		}
		solutionVolumePath := filepath.Join(stepExec.LocalPath, fmt.Sprintf("solution%d_volume", solutionNum))

		// Remove existing container if it exists
		if exists, _ := checkContainerExists(containerName); exists {
			logger.Printf("Removing existing container: %s", containerName)
			if err := removeDockerContainer(containerName, logger); err != nil {
				return fmt.Errorf("failed to remove existing container: %w", err)
			}
		}

		// Check if we need to recreate the container
		shouldRecreate, err := shouldRecreateContainer(containerName, config.Triggers.ImageTag, config.Triggers.ImageID, logger)
		if err != nil {
			return fmt.Errorf("error checking if container should be recreated: %w", err)
		}

		if shouldRecreate || forceRecreate {
			// Only recreate the container if needed
			if exists, _ := checkContainerExists(containerName); exists {
				logger.Printf("Removing existing container: %s", containerName)
				if err := removeDockerContainer(containerName, logger); err != nil {
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
			params = addKeepAliveCommand(params, config.KeepForever, logger)

			if err := runDockerCommand(params, containerName, logger, true); err != nil {
				logger.Printf("Error starting container %s: %v", containerName, err)
				return err
			}

			// Wait for container to be running
			if err := waitForContainerRunning(containerName, 10, logger); err != nil {
				logger.Printf("Error waiting for container %s to start: %v", containerName, err)
				return err
			}
			logger.Printf("Successfully started container %s with volume %s mounted to %s", 
				containerName, solutionVolumePath, taskSettings.AppFolder)
		} else {
			// Make sure the container is running
			if err := waitForContainerRunning(containerName, 5, logger); err != nil {
				logger.Printf("Container %s is not running, attempting to start it", containerName)
				startCmd := exec.Command("docker", "start", containerName)
				if output, err := startCmd.CombinedOutput(); err != nil {
					return fmt.Errorf("failed to start container %s: %v, output: %s", containerName, err, string(output))
				}
				if err := waitForContainerRunning(containerName, 10, logger); err != nil {
					return fmt.Errorf("container %s failed to start: %w", containerName, err)
				}
				logger.Printf("Successfully started container %s", containerName)
			}
		} // Added closing brace here

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
		if err := applyGitCleanupAndPatch(containerName, solutionFile, config.HeldOutTestFile, config.GradingSetupScript, logger); err != nil {
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

	// Persist updated settings
	settings.DockerVolumePool = *config
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("error marshaling updated settings: %w", err)
	}
	if err := models.UpdateStepSettings(db, stepExec.StepID, string(settingsJSON)); err != nil {
		return fmt.Errorf("error updating step settings: %w", err)
	}

	return nil
}

// Helper function to fetch app_folder from docker_extract_volume dependency
func fetchAppFolderFromDependency(db *sql.DB, stepExec *models.StepExec, config *models.DockerVolumePoolConfig, logger *log.Logger) (string, error) {
	for _, depMap := range config.DependsOn {
		// Get the step ID from the dependency map
		stepID, ok := depMap["id"]
		if !ok {
			logger.Printf("No id key in dependency map: %v", depMap)
			continue
		}
		depStep, err := GetStepInfo(db, stepID)
		if err != nil {
			logger.Printf("Error fetching dependent step with ID %d: %v", stepID, err)
			continue
		}

		logger.Printf("Dependency step ID %d settings: %v", stepID, depStep.Settings)

		// Get docker_extract_volume settings
		extractSettings, ok := depStep.Settings["docker_extract_volume"].(map[string]interface{})
		if !ok {
			logger.Printf("docker_extract_volume key not found or invalid in step settings for ID %d", stepID)
			continue
		}

		// Get app_folder from docker_extract_volume settings
		appFolderVal, exists := extractSettings["app_folder"]
		if !exists {
			logger.Printf("app_folder key not found in docker_extract_volume settings for step ID %d", stepID)
			continue
		}

		// Convert app_folder to string
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

// Utility function to add keep-alive command if not already present
func addKeepAliveCommand(params []string, keepForever bool, logger *log.Logger) []string {
	if keepForever {
		hasKeepAliveCmd := false
		for _, param := range params {
			if (strings.Contains(param, "while true") && strings.Contains(param, "sleep")) ||
				strings.Contains(param, "sleep infinity") ||
				strings.Contains(param, "tail -f") {
				hasKeepAliveCmd = true
				break
			}
		}
		if !hasKeepAliveCmd {
			keepAliveArgs := []string{"-c", "while true; do sleep 30; done"}
			params = append(params, keepAliveArgs...)
			logger.Printf("Added keep-alive command to parameters: %v", params)
		}
	}
	return params
}

func runDockerCommand(params []string, containerName string, logger *log.Logger, detached bool) error {
	// Add container name conflict handling
	removeCmd := exec.Command("docker", "rm", "-f", containerName)
	removeOutput, removeErr := removeCmd.CombinedOutput()
	if removeErr != nil && !strings.Contains(string(removeOutput), "No such container") {
		logger.Printf("Error removing existing container %s: %v, output: %s", containerName, removeErr, string(removeOutput))
		return fmt.Errorf("failed to remove existing container: %w", removeErr)
	}
	// Flatten params: split on spaces except for -c keep-alive command
	flattened := []string{}
	for i := 0; i < len(params); i++ {
		if params[i] == "-c" && i+1 < len(params) { // Use && for logical AND
			flattened = append(flattened, "-c", params[i+1])
			i++ // skip next, already appended
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
	logger.Printf("Constructed Docker command for container %s: docker %s", containerName, strings.Join(cmdArgs, " ")) // Log the full constructed command with container name
	cmd := exec.Command("docker", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Error running Docker command for container %s: %v, output: %s", containerName, err, string(output))
		return fmt.Errorf("failed to run Docker command: %w", err)
	}
	logger.Printf("Successfully ran Docker command for container %s", containerName)
	if detached {
		if err := waitForContainerRunning(containerName, 15, logger); err != nil {
			logger.Printf("Container %s did not reach running state: %v", containerName, err)
			return err
		}
		logger.Printf("Container %s is running", containerName)
	}
	return nil
}

func waitForContainerRunning(containerName string, timeoutSeconds int, logger *log.Logger) error {
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

func dockerVolumeExists(volumeName string) bool {
	cmd := exec.Command("docker", "volume", "inspect", volumeName)
	err := cmd.Run()
	return err == nil
}

func createDockerVolume(name string, logger *log.Logger) error {
	cmd := exec.Command("docker", "volume", "create", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Error creating Docker volume %s: %v, output: %s", name, err, string(output))
		return fmt.Errorf("failed to create Docker volume: %w", err)
	}
	logger.Printf("Created Docker volume: %s", name)
	return nil
}

func execInContainer(containerName, command string, logger *log.Logger) error {
	cmd := exec.Command("docker", "exec", containerName, "bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Error executing in container %s: %v, output: %s", containerName, err, string(output))
		return fmt.Errorf("failed to execute command in container: %w", err)
	}
	logger.Printf("Executed in container %s: %s", containerName, command)
	return nil
}

func removeDockerContainer(name string, logger *log.Logger) error {
	cmd := exec.Command("docker", "rm", "-f", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Error removing container %s: %v, output: %s", name, err, string(output))
		return fmt.Errorf("failed to remove Docker container: %w", err)
	}
	logger.Printf("Removed container: %s", name)
	return nil
}

// getContainerName generates a consistent container name for a given task and patch file
func getContainerName(taskID int, patchName string) string {
	return fmt.Sprintf("task_%d_%s_container", taskID, strings.TrimSuffix(patchName, ".patch"))
}

// initializeContainerMap initializes the Triggers.Containers map with consistent container names
func initializeContainerMap(taskID int, solutions []string) map[string]string {
	containers := make(map[string]string)
	for i := 1; i <= len(solutions); i++ {
		patchName := fmt.Sprintf("solution%d.patch", i)
		containers[patchName] = getContainerName(taskID, patchName)
	}
	return containers
}

// mapValues returns a slice of all values in the map
func mapValues(m map[string]string) []string {
	values := make([]string, 0, len(m))
	for _, v := range m {
		values = append(values, v)
	}
	return values
}

// applyGitCleanupAndPatch performs git cleanup and applies the solution patch and held_out_test_file in a container
func applyGitCleanupAndPatch(containerName string, patchFile string, heldOutTestFile string, gradingSetupScript string, logger *log.Logger) error {
	// Change to the app directory in the container
	commands := []string{
		"cd /app/ansible",
		"git reset --hard HEAD",
		"git checkout -- .",
		"git clean -fd",
	}

	// If a grading setup script is provided, apply it first
	if gradingSetupScript != "" {
		if _, err := os.Stat(gradingSetupScript); err == nil {
			// Copy the grading setup script to the container
			copyCmd := exec.Command("docker", "cp", gradingSetupScript, fmt.Sprintf("%s:/tmp/grading_setup.patch", containerName))
			if output, err := copyCmd.CombinedOutput(); err != nil {
				logger.Printf("Failed to copy grading setup script to container %s: %v, output: %s", containerName, err, string(output))
				return fmt.Errorf("failed to copy grading setup script: %w", err)
			}

			// Add grading setup script application command
			commands = append(commands, "git apply /tmp/grading_setup.patch")
		} else {
			logger.Printf("Grading setup script %s not found, skipping", gradingSetupScript)
		}
	}

	// If a patch file is provided, apply it
	if patchFile != "" {
		// Copy the patch file to the container
		copyCmd := exec.Command("docker", "cp", patchFile, fmt.Sprintf("%s:/tmp/solution.patch", containerName))
		if output, err := copyCmd.CombinedOutput(); err != nil {
			logger.Printf("Failed to copy patch file to container %s: %v, output: %s", containerName, err, string(output))
			return fmt.Errorf("failed to copy patch file: %w", err)
		}

		// Add patch application command
		commands = append(commands, "git apply /tmp/solution.patch")
	}

	// Check if held_out_test_file is specified and exists in the current directory
	if heldOutTestFile != "" {
		if _, err := os.Stat(heldOutTestFile); err == nil {
			// Copy held_out_test_file to the container
			heldOutCopyCmd := exec.Command("docker", "cp", heldOutTestFile, fmt.Sprintf("%s:/tmp/held_out_test.patch", containerName))
			if output, err := heldOutCopyCmd.CombinedOutput(); err != nil {
				logger.Printf("Failed to copy %s to container %s: %v, output: %s", heldOutTestFile, containerName, err, string(output))
				return fmt.Errorf("failed to copy %s: %w", heldOutTestFile, err)
			}
			// Add held_out_test_file application command
			commands = append(commands, "git apply /tmp/held_out_test.patch")
		} else {
			logger.Printf("%s not found in current directory, skipping", heldOutTestFile)
		}
	}

	// Execute all commands in the container
	for _, cmd := range commands {
		execCmd := exec.Command("docker", "exec", containerName, "bash", "-c", cmd)
		if output, err := execCmd.CombinedOutput(); err != nil {
			logger.Printf("Command failed in container %s: %s\nError: %v\nOutput: %s", containerName, cmd, err, string(output))
			return fmt.Errorf("failed to execute command in container: %w", err)
		}
		logger.Printf("Executed in container %s: %s", containerName, cmd)
	}

	return nil
}

// shouldRecreateContainer checks if a container needs to be recreated based on image tag or ID changes
func shouldRecreateContainer(containerName, expectedImageTag, expectedImageID string, logger *log.Logger) (bool, error) {
	logger.Printf("Checking if container %s needs recreation (expected tag: %s, expected ID: %s)", 
		containerName, expectedImageTag, expectedImageID)

	// First check if container exists
	exists, err := checkContainerExists(containerName)
	if err != nil {
		return false, fmt.Errorf("failed to check if container exists: %w", err)
	}
	if !exists {
		logger.Printf("Container %s does not exist, needs recreation", containerName)
		return true, nil
	}

	// Get the current container's image ID
	cmd := exec.Command("docker", "inspect", "--format", "{{.Image}}", containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to get container image: %w", err)
	}
	currentImageID := strings.TrimSpace(string(output))
	logger.Printf("Container %s current image ID: %s", containerName, currentImageID)

	// If we have an expected image ID, compare it with the current one
	if expectedImageID != "" {
		// Get the full image ID for the expected image
		cmd = exec.Command("docker", "inspect", "--format", "{{.ID}}", expectedImageID)
		expectedFullImageID, err := cmd.CombinedOutput()
		if err != nil {
			return false, fmt.Errorf("failed to get expected image ID: %w", err)
		}
		
		trimmedExpectedID := strings.TrimSpace(string(expectedFullImageID))
		logger.Printf("Comparing image IDs - Current: %s, Expected: %s", currentImageID, trimmedExpectedID)
		
		// Compare the full image IDs
		if !strings.HasPrefix(trimmedExpectedID, currentImageID) {
			logger.Printf("Container %s: Image ID changed from %s to %s", 
				containerName, currentImageID, trimmedExpectedID)
			return true, nil
		}
	}

	// If we have an expected image tag, check if it matches the current container's image
	if expectedImageTag != "" {
		// Get the current container's image tag
		cmd = exec.Command("docker", "inspect", "--format", "{{.Config.Image}}", containerName)
		output, err = cmd.CombinedOutput()
		if err != nil {
			return false, fmt.Errorf("failed to get container image tag: %w", err)
		}
		currentImageTag := strings.TrimSpace(string(output))
		logger.Printf("Container %s current image tag: %s, expected: %s", 
			containerName, currentImageTag, expectedImageTag)

		// Compare the image tags
		if currentImageTag != expectedImageTag {
			logger.Printf("Container %s: Image tag changed from %s to %s", 
				containerName, currentImageTag, expectedImageTag)
			return true, nil
		}
	}

	logger.Printf("Container %s is up-to-date, no need to recreate", containerName)
	return false, nil
}

func checkContainerExists(containerName string) (bool, error) {
	cmd := exec.Command("docker", "inspect", "--type=container", containerName)
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("failed to check container %s: %w", containerName, err)
	}
	return true, nil
}
