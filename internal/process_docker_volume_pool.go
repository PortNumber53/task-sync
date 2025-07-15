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

	// Gather artifact container and volume names
	artifactContainers := make([]string, 0, len(config.Triggers.Containers))
	artifactVolumes := make([]string, 0, len(config.Triggers.Containers))
	for patch, container := range config.Triggers.Containers {
		artifactContainers = append(artifactContainers, container)
		artifactVolumes = append(artifactVolumes, fmt.Sprintf("task_%d_%s_volume", stepExec.TaskID, patch))
	}

	// Check image triggers
	imageChanged, err := checkImageTriggers(db, stepExec, config, stepLogger)
	if err != nil {
		return err
	}

	// Check container existence
	containersExist := CheckArtifactContainersExist(artifactContainers, stepLogger)

	// Check volume existence
	volumesExist := CheckArtifactVolumesExist(artifactVolumes, stepLogger)

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
	logger.Printf("Unmarshaled settings: %+v", settings.DockerVolumePool)
	logger.Printf("Solutions loaded: %v", settings.DockerVolumePool.Solutions)
	config := &settings.DockerVolumePool

	// Get task ID and solutions from Triggers.Containers keys
	taskID := stepExec.TaskID
	solutionsMap := config.Triggers.Containers
	solutions := make([]string, 0, len(solutionsMap))
	for sol := range solutionsMap {
		solutions = append(solutions, sol)
	}

	// Prepare to collect artifact container names for output
	artifactContainers := make(map[string]string)

	localPath := stepExec.LocalPath
	// --- PREPARATION: Create a single Docker volume for the task ---
	volumeName := fmt.Sprintf("step_%d_original_volume", stepExec.StepID)
	containerFolder := config.ContainerFolder
	imageTag := config.Triggers.ImageTag

	// Create the Docker volume if it doesn't exist
	logger.Printf("[VOLUME] Checking if Docker volume exists: %s", volumeName)
	if !dockerVolumeExists(volumeName) {
		logger.Printf("[VOLUME] Creating Docker volume: %s", volumeName)
		if err := createDockerVolume(volumeName, logger); err != nil {
			logger.Printf("[VOLUME] Error creating Docker volume %s: %v", volumeName, err)
			return err
		}
		logger.Printf("[VOLUME] Successfully created Docker volume: %s", volumeName)
	} else {
		logger.Printf("[VOLUME] Docker volume %s already exists", volumeName)
	}

	// Start a persistent container to copy container_folder into the Docker volume
	prepContainerName := fmt.Sprintf("task_%d_step_%d_prep", taskID, stepExec.StepID)
	prepParams := []string{
		"-d", // detached
		"--name", prepContainerName,
		"-v", fmt.Sprintf("%s:%s", volumeName, "/original"),
		"--entrypoint", "/bin/bash",
		imageTag,
		"-c", fmt.Sprintf("cp -r %s/. /original/ && while true; do sleep 30; done", containerFolder),
	}
	logger.Printf("[PREP] Running Docker command to create persistent prep container: %s with params: %v", prepContainerName, prepParams)
	if err := runDockerCommand(prepParams, prepContainerName, logger, true); err != nil {
		logger.Printf("[PREP] Error running Docker command for prep container %s: %v", prepContainerName, err)
		return err
	}
	logger.Printf("[PREP] Prep container %s created and running", prepContainerName)

	// --- For each solution: create host folder and copy from Docker volume ---
	var hostFolder string
	for i, solution := range solutions {
		hostFolder = filepath.Join(localPath, fmt.Sprintf("step_%d_volume_solution%d", stepExec.StepID, i+1))
		logger.Printf("[SOLUTION] Preparing host folder for solution %s", solution)
		if _, err := os.Stat(hostFolder); os.IsNotExist(err) {
			logger.Printf("[SOLUTION] Host folder %s does not exist. Creating...", hostFolder)
			if err := os.MkdirAll(hostFolder, 0755); err != nil {
				logger.Printf("[SOLUTION] Error creating host folder %s: %v", hostFolder, err)
				return err
			}
			logger.Printf("[SOLUTION] Successfully created host folder: %s", hostFolder)
		} else if err != nil {
			logger.Printf("[SOLUTION] Error checking host folder %s: %v", hostFolder, err)
			return err
		} else {
			logger.Printf("[SOLUTION] Host folder %s already exists", hostFolder)
		}

		// Use a temp container to copy from Docker volume to host folder
		tmpCopyContainer := fmt.Sprintf("task_%d_step_%d_copyout%d", taskID, stepExec.StepID, i)
		tmpCopyParams := []string{
			"--rm",
			"-v", fmt.Sprintf("%s:/original:ro", volumeName),
			"-v", fmt.Sprintf("%s:%s", hostFolder, containerFolder),
			"--entrypoint", "/bin/bash",
			imageTag,
			"-c", fmt.Sprintf("cp -r /original/. %s", containerFolder),
		}
		logger.Printf("[COPYOUT] Copying from Docker volume to host folder using container %s", tmpCopyContainer)
		if err := runDockerCommand(tmpCopyParams, tmpCopyContainer, logger, false); err != nil {
			logger.Printf("[COPYOUT] Error copying from Docker volume to host folder in container %s: %v", tmpCopyContainer, err)
			return err
		}

		artifactContainerName := fmt.Sprintf("task_%d_step_%d_artifact%d", taskID, stepExec.StepID, i+1)
		artifactParams := []string{
			"--user=0:0", // Run as root by default
			"--rm",
			"--platform", "linux/amd64",
			"--cpus", "2",
			"--memory", "1G",
			"--pids-limit", "512",
			"-v", fmt.Sprintf("%s:%s", hostFolder, containerFolder), // bind mount
			"-v", fmt.Sprintf("%s:/original:ro", volumeName), // docker volume read-only
			"--entrypoint", "/bin/bash",
			imageTag,
			"--login",
		}
		artifactParams = addKeepAliveCommand(artifactParams, config.KeepForever, logger)
		logger.Printf("[CONTAINER] Running Docker command to create artifact container: %s with params: %v", artifactContainerName, artifactParams)
		if err := runDockerCommand(artifactParams, artifactContainerName, logger, config.KeepForever); err != nil {
			logger.Printf("[CONTAINER] Error running Docker command for artifact container %s: %v", artifactContainerName, err)
			return err
		}
		logger.Printf("[CONTAINER] Artifact container %s created successfully", artifactContainerName)
		// Step 6: Run git commands and apply patch inside artifact container
		gitSafeCmd := "git -c safe.directory=/app/ansible reset --hard && git -c safe.directory=/app/ansible clean -fd"
		if err := execInContainer(artifactContainerName, gitSafeCmd, logger); err != nil {
			if !config.KeepForever {
				removeDockerContainer(artifactContainerName, logger)
			}
			return err
		}
		gitCleanCmd := "git -c safe.directory=/app/ansible reset --hard && git -c safe.directory=/app/ansible clean -fd"
		if err := execInContainer(artifactContainerName, gitCleanCmd, logger); err != nil {
			if !config.KeepForever {
				removeDockerContainer(artifactContainerName, logger)
			}
			return err
		}
		// Copy patch file from host into the container before applying
		patchHostPath := filepath.Join(stepExec.LocalPath, solution)
		patchContainerPath := fmt.Sprintf("/app/ansible/%s", solution)
		cpCmd := exec.Command("docker", "cp", patchHostPath, artifactContainerName+":"+patchContainerPath)
		logger.Printf("Copying patch file %s to container %s:%s", patchHostPath, artifactContainerName, patchContainerPath)
		if output, err := cpCmd.CombinedOutput(); err != nil {
			logger.Printf("Error copying patch file: %v, output: %s", err, string(output))
			if !config.KeepForever {
				removeDockerContainer(artifactContainerName, logger)
			}
			return fmt.Errorf("failed to copy patch file to container: %w", err)
		}
		// Apply solution patch
		patchCmd := fmt.Sprintf("git -c safe.directory=/app/ansible apply %s", patchContainerPath)
		if err := execInContainer(artifactContainerName, patchCmd, logger); err != nil {
			if !config.KeepForever {
				removeDockerContainer(artifactContainerName, logger)
			}
			return err
		}
		logger.Printf("Applied patch %s in container %s", solution, artifactContainerName)
		artifactContainers[solution] = artifactContainerName
	}

	// After all containers, store artifact container names and image ID in Artifacts
	if config.Artifacts == nil {
		config.Artifacts = make(map[string]interface{})
	}
	config.Artifacts["containers"] = artifactContainers

	// Update triggers.containers with artifact container names
	if config.Triggers.Containers == nil {
		config.Triggers.Containers = make(map[string]string)
	}
	for patch, container := range artifactContainers {
		config.Triggers.Containers[patch] = container
	}

	// Always update assigned_containers in task settings
	taskSettings, err := models.GetTaskSettings(db, stepExec.TaskID)
	if err != nil {
		logger.Printf("Warning: Could not fetch task settings to update assigned_containers: %v", err)
	} else {
		taskSettings.AssignedContainers = make(map[string]string)
		for patch, container := range artifactContainers {
			taskSettings.AssignedContainers[patch] = container
		}
		if err := models.UpdateTaskSettings(db, stepExec.TaskID, taskSettings); err != nil {
			logger.Printf("Warning: Could not update assigned_containers in task settings: %v", err)
		} else {
			logger.Printf("Updated assigned_containers in task settings for task %d", stepExec.TaskID)
		}
	}

	// Update triggers.files with current file hashes
	if config.Triggers.Files == nil {
		config.Triggers.Files = make(map[string]string)
	}
	for file := range config.Triggers.Files {
		filePath := filepath.Join(stepExec.LocalPath, file)
		hash, err := models.GetSHA256(filePath)
		if err != nil {
			logger.Printf("Warning: Failed to compute hash for %s: %v", filePath, err)
			continue
		}
		config.Triggers.Files[file] = hash
	}

	// Retrieve image ID for the used image tag
	imageID := ""
	inspectCmd := exec.Command("docker", "inspect", "--format", "{{.Id}}", config.Triggers.ImageTag)
	if output, err := inspectCmd.CombinedOutput(); err == nil {
		imageID = strings.TrimSpace(string(output))
	} else {
		logger.Printf("Warning: Failed to get image ID for image tag %s: %v, output: %s", config.Triggers.ImageTag, err, string(output))
	}
	config.Artifacts["image_id"] = imageID

	// Update triggers.image_id as well
	config.Triggers.ImageID = imageID

	// Persist updated settings
	settings.DockerVolumePool = *config
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		logger.Printf("Error marshaling updated settings: %v", err)
		return err
	}
	if err := models.UpdateStepSettings(db, stepExec.StepID, string(settingsJSON)); err != nil {
		logger.Printf("Error updating step settings: %v", err)
		return err
	}
	logger.Printf("Updated step settings with artifact container names, image ID, triggers.containers, and triggers.files.")

	return nil
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

// General function to run Docker command with given parameters and name
// Polls for the container to be running, up to timeoutSeconds
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
		if params[i] == "-c" && i+1 < len(params) {
			flattened = append(flattened, "-c", params[i+1])
			i++ // skip next, already appended
		} else {
			parts := strings.Split(params[i], " ")
			flattened = append(flattened, parts...)
		}
	}
	if detached {
		flattened = append([]string{"-d"}, flattened...)
	}
	cmdArgs := append([]string{"run", "--name", containerName}, flattened...)
	logger.Printf("Running Docker command: docker %s", strings.Join(cmdArgs, " "))
	cmd := exec.Command("docker", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Error running Docker command for container %s: %v, output: %s", containerName, err, string(output))
		return fmt.Errorf("failed to run Docker command: %w", err)
	}
	logger.Printf("Successfully ran Docker command for container %s", containerName)
	if detached {
		// Wait for container to be running
		if err := waitForContainerRunning(containerName, 15, logger); err != nil {
			logger.Printf("Container %s did not reach running state: %v", containerName, err)
			return err
		}
		logger.Printf("Container %s is running", containerName)
	}
	return nil
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
