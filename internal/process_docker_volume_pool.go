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

	// Check for force flag
	if config.Force {
		stepLogger.Println("Force flag set; running step regardless of triggers")
		return models.RunDockerVolumePoolStep(db, stepExec, stepLogger) // Updated call to exported function
	}

	// Initialize or update Triggers.Containers if empty
	if len(config.Triggers.Containers) == 0 {
		config.Triggers.Containers = models.InitializeContainerMap(stepExec.TaskID, config.Solutions) // Using exported function
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
	filesChanged, err := models.CheckFileHashChanges(stepExec.LocalPath, config.Triggers.Files, stepLogger)
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
		for patch, container := range config.Triggers.Containers {
			if containerExists, _ := models.CheckContainerExists(container); containerExists {
				if err := models.ApplyGitCleanupAndPatch(container, filepath.Join(stepExec.LocalPath, patch+".patch"), config.HeldOutTestFile, config.GradingSetupScript, stepLogger); err != nil {
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
