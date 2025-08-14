package models

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"database/sql"
)

// ErrEmptyFile is returned when a file is empty.
var ErrEmptyFile = errors.New("file is empty")

// SHA256String returns the SHA256 hex digest of the input string.
func SHA256String(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// CalcRubricCriterionHash returns the SHA256 hex digest for rubric criterion fields.
func CalcRubricCriterionHash(score int, criterion string, required bool, criterionTestCommand string) string {
	str := fmt.Sprintf("%d|%s|%t|%s", score, criterion, required, criterionTestCommand)
	return SHA256String(str)
}

// GetSHA256 computes the SHA256 hash of a file.
func GetSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// CheckFileHashChanges checks if any file hashes have changed compared to stored hashes.
func CheckFileHashChanges(localPath string, files map[string]string, logger *log.Logger) (bool, error) {
	runNeeded := false
	for fileName, storedHash := range files {
		filePath := filepath.Join(localPath, fileName)
		currentHash, err := GetSHA256(filePath)
		if err != nil {
			logger.Printf("Error computing hash for %s: %v", filePath, err)
			return true, err // Treat hash error as a trigger to run
		}
		logger.Printf("Hash check for %s: computed %s, stored %s", filePath, currentHash, storedHash)
		if currentHash != storedHash {
			runNeeded = true
			logger.Printf("Hash mismatch detected for %s", filePath)
		} // Break removed to check all files
	}
	return runNeeded, nil
}

// UpdateFileHashes computes new hashes for the files and updates the step settings in the database.
func UpdateFileHashes(db *sql.DB, stepID int, localPath string, files map[string]string, logger *log.Logger) error {
	newHashes := make(map[string]string)
	for fileName := range files {
		filePath := filepath.Join(localPath, fileName)
		hash, err := GetSHA256(filePath)
		if err != nil {
			logger.Printf("Error computing hash for %s: %v", filePath, err)
			continue // Skip erroneous files
		}
		newHashes[fileName] = hash
	}
	// Serialize newHashes to JSON and update the step's settings in DB with correct path
	newTriggersFiles, err := json.Marshal(newHashes)
	if err != nil {
		return fmt.Errorf("failed to marshal new file hashes: %w", err)
	}
	_, err = db.Exec("UPDATE steps SET settings = jsonb_set(settings, '{docker_extract_volume,triggers,files}', $1::jsonb) WHERE id = $2", newTriggersFiles, stepID)
	if err != nil {
		return fmt.Errorf("failed to update step settings: %w", err)
	}
	logger.Printf("Updated file hashes for step %d", stepID)
	return nil
}

// GenerateRandomString generates a random hex string of the specified byte length.
func GenerateRandomString(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// InitStepLogger initializes the package-level step logger.
func InitStepLogger(writer io.Writer) {
	StepLogger = log.New(writer, "[StepExecutor] ", log.LstdFlags)
}

// GetAssignedContainersForStep returns a map of solution patch/assignment name to ContainerInfo for a step, checking step settings first, then falling back to task settings.
// - stepSettings: the raw JSON settings for the step (usually StepExec.Settings)
// - taskSettings: the parsed TaskSettings for the parent task
// Returns: map[patchName]ContainerInfo, error
func GetAssignedContainersForStep(stepSettings string, taskSettings *TaskSettings, logger *log.Logger) (map[string]ContainerInfo, error) {
	// Try step-level assign_containers or assigned_containers
	var stepMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stepSettings), &stepMap); err != nil {
		return nil, err
	}

	// Try to find assign_containers or assigned_containers in any config block
	var assignMap map[string]string
	for _, v := range stepMap {
		var inner struct {
			AssignContainers   map[string]string `json:"assign_containers"`
			AssignedContainers map[string]string `json:"assigned_containers"`
		}
		if err := json.Unmarshal(v, &inner); err == nil {
			if len(inner.AssignContainers) > 0 {
				assignMap = inner.AssignContainers
				break
			}
			if len(inner.AssignedContainers) > 0 {
				assignMap = inner.AssignedContainers
				break
			}
		}
	}

	// If not found at step level, try task-level
	if assignMap == nil && taskSettings != nil {
		if len(taskSettings.AssignContainers) > 0 {
			assignMap = taskSettings.AssignContainers
		} else if len(taskSettings.AssignedContainers) > 0 {
			assignMap = taskSettings.AssignedContainers
		}
	}

	result := make(map[string]ContainerInfo)
	// If we have a mapping (patch/solution → container name), resolve to ContainerInfo
	if len(assignMap) > 0 {
		// Build a lookup table for container name → ContainerInfo
		containerLookup := make(map[string]ContainerInfo)
		if taskSettings != nil {
			// Prefer new containers_map
			if taskSettings.ContainersMap != nil {
				for _, c := range taskSettings.ContainersMap {
					containerLookup[c.ContainerName] = c
				}
			} else if len(taskSettings.Containers) > 0 {
				// Legacy fallback
				for _, c := range taskSettings.Containers {
					containerLookup[c.ContainerName] = c
				}
			}
		}
		for patch, contName := range assignMap {
			if cinfo, ok := containerLookup[contName]; ok {
				result[patch] = cinfo
			} else {
				// Fallback: just provide name if no ID
				result[patch] = ContainerInfo{ContainerName: contName}
			}
		}
		if logger != nil {
			logger.Printf("DEBUG: Assignment map for step: %+v", result)
		}
		return result, nil
	}

	// If no assign_containers mapping, fallback to taskSettings containers by deterministic key order
	if taskSettings != nil {
		// Prefer containers_map in key order original, golden, solution1..solution4
		preferredKeys := []string{"original", "golden", "solution1", "solution2", "solution3", "solution4"}
		if taskSettings.ContainersMap != nil && len(taskSettings.ContainersMap) > 0 {
			i := 0
			for _, k := range preferredKeys {
				if c, ok := taskSettings.ContainersMap[k]; ok {
					key := fmt.Sprintf("container_%d", i)
					result[key] = c
					i++
				}
			}
			// Include any other keys deterministically (alphabetical) if present
			if logger != nil {
				logger.Printf("DEBUG: Fallback assignment map (containers_map) for step: %+v", result)
			}
			if len(result) > 0 {
				return result, nil
			}
		}
		// Legacy array fallback
		if len(taskSettings.Containers) > 0 {
			for i, c := range taskSettings.Containers {
				key := fmt.Sprintf("container_%d", i)
				result[key] = c
			}
			if logger != nil {
				logger.Printf("DEBUG: Fallback assignment map (legacy array) for step: %+v", result)
			}
			return result, nil
		}
	}

	return nil, fmt.Errorf("no container assignments found in step or task settings")
}

// GetPatchFileForContainerAssignments returns a map of container name to the patch file name to use, given assignments and files.
// - files: map of available files (file name → hash)
// Returns: map[container_name]patch_file_name
func GetPatchFileForContainerAssignments(assignments []RubricShellAssignment, files map[string]string) map[string]string {
	result := make(map[string]string)
	for _, assign := range assignments {
		patch := assign.Patch
		// If patch exists directly in files, use it
		if _, ok := files[patch]; ok {
			result[assign.Container] = patch
			continue
		}
		// If patch is a synthetic key like "container_2", try to map to solution2.patch or solution_2.patch
		var idx int
		if n, err := fmt.Sscanf(patch, "container_%d", &idx); n == 1 && err == nil {
			candidates := []string{
				fmt.Sprintf("solution%d.patch", idx+1), // 1-based (solution1.patch)
				fmt.Sprintf("solution_%d.patch", idx+1),
				fmt.Sprintf("solution%d.patch", idx),   // 0-based fallback
				fmt.Sprintf("solution_%d.patch", idx),
			}
			found := false
			for _, candidate := range candidates {
				if _, ok := files[candidate]; ok {
					result[assign.Container] = candidate
					found = true
					break
				}
			}
			if found {
				continue
			}
		}
		// Not found: log or skip
		result[assign.Container] = "" // Or omit key if you prefer
	}
	return result
}
