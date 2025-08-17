package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"

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

	// Load task settings once; needed to source container assignments from task.settings
	taskSettings, err := models.GetTaskSettings(db, stepExec.TaskID)
	if err != nil {
		return fmt.Errorf("failed to get task settings: %w", err)
	}

	// Check for force flag
	if config.Force {
		stepLogger.Println("Force flag set; running step regardless of triggers")
		return models.RunDockerVolumePoolStep(db, stepExec, stepLogger) // Updated call to exported function
	}

	// Initialize or update Triggers.Containers if empty
	if len(config.Triggers.Containers) == 0 {
		// Prefer task.settings.containers_map if available
		if taskSettings != nil && taskSettings.ContainersMap != nil {
			preferredKeys := []string{"solution1", "solution2", "solution3", "solution4"}
			initialized := make(map[string]string)
			for idx, key := range preferredKeys {
				if c, ok := taskSettings.ContainersMap[key]; ok && c.ContainerName != "" {
					patch := fmt.Sprintf("solution%d.patch", idx+1)
					initialized[patch] = c.ContainerName
				}
			}
			if len(initialized) > 0 {
				config.Triggers.Containers = initialized
				stepLogger.Printf("Initialized Triggers.Containers from task.settings.containers_map: %v", config.Triggers.Containers)
			} else {
				// Fallback to generated names
				config.Triggers.Containers = models.InitializeContainerMap(stepExec.TaskID, config.Solutions)
				stepLogger.Printf("Initialized Triggers.Containers (fallback): %v", config.Triggers.Containers)
			}
		} else {
			// Fallback to generated names
			config.Triggers.Containers = models.InitializeContainerMap(stepExec.TaskID, config.Solutions)
			stepLogger.Printf("Initialized Triggers.Containers (no task settings): %v", config.Triggers.Containers)
		}
	}

	// Gather container and volume names for checks
	containerList := make([]string, 0, len(config.Triggers.Containers))
	volumeList := make([]string, 0, len(config.Triggers.Containers))
	for patch, container := range config.Triggers.Containers {
		containerList = append(containerList, container)
		volumeList = append(volumeList, fmt.Sprintf("task_%d_%s_volume", stepExec.TaskID, patch))
	}

	// Check image triggers
	imageChanged, err := models.CheckImageTriggers(db, stepExec, config, stepLogger) // Using exported function
	if err != nil {
		stepLogger.Printf("Error checking image triggers: %v; treating as change and running step", err)
		imageChanged = true // Treat error as a trigger to run
	}

	// Check container existence
	allContainersExist := true
	for _, container := range containerList {
		exists, err := models.CheckContainerExists(container)
		if err != nil {
			stepLogger.Printf("Error checking container %s: %v", container, err)
			return fmt.Errorf("container check error for %s: %w", container, err)
		}
		if !exists {
			allContainersExist = false
			break
		}
	}

	// Check volume existence
	allVolumesExist := true
	for _, volume := range volumeList {
		exists, err := models.CheckVolumeExists(volume)
		if err != nil {
			stepLogger.Printf("Error checking volume %s: %v", volume, err)
			return fmt.Errorf("volume check error for %s: %w", volume, err)
		}
		if !exists {
			allVolumesExist = false
			break
		}
	}

	// Check file hash triggers
	filesChanged, err := models.CheckFileHashChanges(stepExec.BasePath, config.Triggers.Files, stepLogger)
	if err != nil {
		return err
	}

	// Decision logic
	if imageChanged || !allContainersExist || !allVolumesExist || filesChanged {
		stepLogger.Printf("[Trigger] Image changed: %v, Containers exist: %v, Volumes exist: %v, Files changed: %v", imageChanged, allContainersExist, allVolumesExist, filesChanged)
		stepLogger.Println("Triggering full rebuild: recreate containers and volumes, run all steps")
		return models.RunDockerVolumePoolStep(db, stepExec, stepLogger) // Updated call to exported function
	}
	if filesChanged {
		stepLogger.Println("Triggering partial rebuild: files changed, redoing git operations")
		// Use previously loaded task settings to get working directory inside the container
		workingDir := taskSettings.AppFolder
		for patch, container := range config.Triggers.Containers {
			if containerExists, _ := models.CheckContainerExists(container); containerExists {
				patchPath := filepath.Join(stepExec.BasePath, patch) // patch key already includes .patch
				if err := models.ApplyGitCleanupAndPatch(container, workingDir, patchPath, config.HeldOutTestFile, config.GradingSetupScript, stepLogger); err != nil {
					return err
				}
			} else {
				stepLogger.Printf("Container %s missing, skipping git ops for patch %s", container, patch)
			}
		}
		return nil
	}

	stepLogger.Println("No triggers activated; skipping step execution")
	return nil
}
