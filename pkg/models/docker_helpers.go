package models

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
)

// GetContainerDetails retrieves container ID and name from a step's settings.
func GetContainerDetails(db *sql.DB, stepID int, stepLogger *log.Logger) (containerID, containerName string, err error) {
	settingsStr, err := GetStepInfo(db, stepID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get step info for %d: %w", stepID, err)
	}

	var configHolder StepConfigHolder
	if err := json.Unmarshal([]byte(settingsStr), &configHolder); err != nil {
		return "", "", fmt.Errorf("failed to unmarshal settings for step %d: %w", stepID, err)
	}

	switch {
	case configHolder.DockerRun != nil:
		// DockerRun steps don't have container info in config, checking dependencies
		stepLogger.Printf("DockerRun step %d doesn't have container info in config, checking dependencies\n", stepID)
	case configHolder.DockerPool != nil:
		// For DockerPool steps, directly access container info
		if len(configHolder.DockerPool.Containers) > 0 {
			container := configHolder.DockerPool.Containers[0]
			stepLogger.Printf("Found container_id '%s' and container_name '%s' in pool step %d\n", container.ContainerID, container.ContainerName, stepID)
			return container.ContainerID, container.ContainerName, nil
		}
	}

	// Check dependencies if not found
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(settingsStr), &rawMap); err != nil {
		return "", "", fmt.Errorf("failed to unmarshal settings as map for step %d: %w", stepID, err)
	}

	var dependencies []Dependency
	if dependsOn, ok := rawMap["depends_on"]; ok {
		var deps []Dependency
		if err := json.Unmarshal(dependsOn, &deps); err == nil {
			dependencies = append(dependencies, deps...)
		}
	}

	// Check for nested depends_on in any object
	for _, rawVal := range rawMap {
		var nestedDep struct {
			DependsOn []Dependency `json:"depends_on"`
		}
		if err := json.Unmarshal(rawVal, &nestedDep); err == nil {
			dependencies = append(dependencies, nestedDep.DependsOn...)
		}
	}

	for _, dep := range dependencies {
		contID, contName, err := GetContainerDetails(db, dep.ID, stepLogger)
		if err != nil {
			return "", "", err
		}
		if contID != "" && contName != "" {
			stepLogger.Printf("Found container_id '%s' and container_name '%s' from step %d\n", contID, contName, dep.ID)
			return contID, contName, nil
		}
	}

	return "", "", nil
}

// GetContainerID is a helper function to extract container_id from a step's settings.
func GetContainerID(db *sql.DB, stepID int, stepLogger *log.Logger) (containerID string, err error) {
	contID, _, err := GetContainerDetails(db, stepID, stepLogger)
	if err != nil {
		return "", err
	}
	return contID, nil
}

// GetContainerName is a helper function to extract container_name from a step's settings.
func GetContainerName(db *sql.DB, stepID int, stepLogger *log.Logger) (containerName string, err error) {
	_, contName, err := GetContainerDetails(db, stepID, stepLogger)
	if err != nil {
		return "", err
	}
	return contName, nil
}
