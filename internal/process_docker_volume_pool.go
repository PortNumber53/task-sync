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
	var settings struct {
		DockerVolumePool models.DockerVolumePoolConfig `json:"docker_volume_pool"`
	}
	if err := json.Unmarshal([]byte(stepExec.Settings), &settings); err != nil {
		return fmt.Errorf("failed to unmarshal step settings: %w", err)
	}
	config := &settings.DockerVolumePool

	// Check for force flag
	if config.Force {
		stepLogger.Println("Force flag set; running step regardless of triggers")
		// Proceed with step execution (stub for now)
		return runDockerVolumePoolStep(db, stepExec, stepLogger)
	}

	// Check file hash triggers
	runNeeded, err := checkFileHashTriggers(db, stepExec, config, stepLogger)
	if err != nil {
		return err
	}
	if runNeeded {
		stepLogger.Println("File hash change detected; running step")
		return runDockerVolumePoolStep(db, stepExec, stepLogger)
	}

	// Check image mismatch triggers
	runNeeded, err = checkImageTriggers(db, stepExec, config, stepLogger)
	if err != nil {
		return err
	}
	if runNeeded {
		stepLogger.Println("Image mismatch detected; running step")
		return runDockerVolumePoolStep(db, stepExec, stepLogger)
	}

	// Check container assignment triggers
	runNeeded, err = checkContainerTriggers(db, stepExec, config, stepLogger)
	if err != nil {
		return err
	}
	if runNeeded {
		stepLogger.Println("Container assignment mismatch detected; running step")
		return runDockerVolumePoolStep(db, stepExec, stepLogger)
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
	runNeeded := false
	for fileName, expectedContainer := range config.Triggers.Containers {
		actualContainer, exists := taskSettings.AssignContainers[fileName] // Changed to AssignContainers
		if !exists || actualContainer != expectedContainer {
			runNeeded = true
		}
	}
	return runNeeded, nil
}

// Stub for actual step execution logic
func runDockerVolumePoolStep(db *sql.DB, stepExec *models.StepExec, logger *log.Logger) error {
	logger.Println("Docker volume pool step execution not yet implemented")
	return nil // Replace with actual logic later
}
