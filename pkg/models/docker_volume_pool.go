package models

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

	// Get task settings to access app_folder and docker.image_tag, with nil DB guard for tests
	var taskSettings TaskSettings
	if db == nil {
		// Test context: avoid DB usage. Do not rely on step.container_folder.
		// Do not hardcode app_folder here; it must come from tasks.settings.app_folder.
		// Leave taskSettings.AppFolder empty so downstream logic or callers must provide it.
	} else {
		ts, err := GetTaskSettings(db, stepExec.TaskID)
		if err != nil {
			return fmt.Errorf("failed to get task settings: %w", err)
		}
		taskSettings = *ts
	}

	// Ensure image tag is sourced from task settings when available
	if taskSettings.Docker.ImageTag != "" {
		if config.Triggers.ImageTag != taskSettings.Docker.ImageTag {
			logger.Printf("Debug: Using task.settings.docker.image_tag=%q (was %q)", taskSettings.Docker.ImageTag, config.Triggers.ImageTag)
		}
		config.Triggers.ImageTag = taskSettings.Docker.ImageTag
	}

	// Resolve and set ImageID from ImageTag for deterministic image matching
	if config.Triggers.ImageTag != "" {
		if id, err := GetCurrentImageID(config.Triggers.ImageTag); err == nil && id != "" {
			if config.Triggers.ImageID != id {
				logger.Printf("Debug: Resolved image ID for %q -> %q", config.Triggers.ImageTag, id)
			}
			config.Triggers.ImageID = id
		} else if err != nil {
			logger.Printf("Warning: failed to resolve image ID for %q: %v", config.Triggers.ImageTag, err)
		}
	}

	// Ensure we have app_folder set
	if taskSettings.AppFolder == "" && db != nil {
		// Fallback to fetching from dependency if not set in task settings
		appFolder, err := FetchAppFolderFromDependency(db, stepExec, config, logger)
		if err != nil {
			return fmt.Errorf("failed to fetch app_folder from dependency: %w", err)
		}
		taskSettings.AppFolder = appFolder
	}

	// Helper: resolve preferred container name for a base key
    // Preference order:
    // 1) Name from task.settings.containers_map (if present)
    // 2) Existing canonical name (GenerateDVContainerNameForBase)
    // 3) Existing legacy name (tasksync_<taskID>_<base>)
    // 4) Canonical name
    resolveContainerName := func(base string) string {
        // From task settings map
        if taskSettings.ContainersMap != nil {
            if c, ok := taskSettings.ContainersMap[base]; ok && c.ContainerName != "" {
                return c.ContainerName
            }
        }
        canonical := GenerateDVContainerNameForBase(stepExec.TaskID, base)
        // Prefer existing canonical
        if exists, _ := CheckContainerExists(canonical); exists {
            return canonical
        }
        // Legacy fallback
        legacy := fmt.Sprintf("tasksync_%d_%s", stepExec.TaskID, base)
        if exists, _ := CheckContainerExists(legacy); exists {
            return legacy
        }
        // Default to canonical
        return canonical
    }

	// Initialize or update Triggers.Containers if empty or if any container name is empty
	recreateContainers := false
	if len(config.Triggers.Containers) == 0 {
		recreateContainers = true
	} else {
		for patchName, containerName := range config.Triggers.Containers {
			if containerName == "" {
				logger.Printf("Empty container name found for %s, will recreate containers", patchName)
				recreateContainers = true
				break
			}
		}
	}

	if recreateContainers {
		// If no solutions specified, try to derive them from task.settings.containers_map.
		// Always consider "original" and "golden" (mapped to golden.patch) like docker_extract_volume does.
		// Fall back to pool_size if containers_map is empty, and finally to existing Triggers.Containers keys.
		if len(config.Solutions) == 0 {
			derived := make([]string, 0, 6)
			if taskSettings.ContainersMap != nil {
				for key, val := range taskSettings.ContainersMap {
					if val.ContainerName == "" { continue }
					if key == "original" {
						derived = append(derived, "original")
						continue
					}
					if key == "golden" {
						derived = append(derived, "golden.patch")
						continue
					}
					if strings.HasPrefix(key, "solution") {
						derived = append(derived, fmt.Sprintf("%s.patch", key))
					}
				}
			}
			if len(derived) == 0 && config.PoolSize > 0 {
				// Include original and golden by default
				derived = append(derived, "original", "golden.patch")
				for i := 0; i < config.PoolSize; i++ {
					derived = append(derived, fmt.Sprintf("solution%d.patch", i+1))
				}
			}
			if len(derived) == 0 && len(config.Triggers.Containers) > 0 {
				for k := range config.Triggers.Containers {
					if k == "golden" { derived = append(derived, "golden.patch"); continue }
					if k == "original" { derived = append(derived, "original"); continue }
					derived = append(derived, k)
				}
			}
			config.Solutions = derived
			logger.Printf("Initialized solutions: %v", config.Solutions)
		}

        // Initialize container map preferring existing containers to avoid duplication/recreation
        containerMap := make(map[string]string)
        for _, solution := range config.Solutions {
            base := strings.TrimSuffix(solution, filepath.Ext(solution))
            containerMap[solution] = resolveContainerName(base)
        }
        config.Triggers.Containers = containerMap
        logger.Printf("Initialized Triggers.Containers (aligned with task.settings when possible): %v", config.Triggers.Containers)

		// Force recreation of containers since we just initialized the container map
		recreateNeeded = true
	}

	// Ensure Solutions includes original and golden even when not recreating
	if len(config.Solutions) == 0 {
		// Derive from triggers if empty
		if len(config.Triggers.Containers) > 0 {
			for k := range config.Triggers.Containers {
				if k == "golden" {
					config.Solutions = append(config.Solutions, "golden.patch")
					continue
				}
				if k == "original" {
					config.Solutions = append(config.Solutions, "original")
					continue
				}
				config.Solutions = append(config.Solutions, k)
			}
		}
	}

	// Guarantee presence of original and golden in Solutions list
	ensureInSolutions := func(name string) {
		for _, s := range config.Solutions { if s == name { return } }
		config.Solutions = append(config.Solutions, name)
	}
	ensureInSolutions("original")
	ensureInSolutions("golden.patch")

	// Normalize solution order deterministically to avoid index-based name drift
	normalizeSolutions := func(in []string) []string {
		seen := make(map[string]bool)
		push := func(s string, out *[]string) {
			if s == "" { return }
			if !seen[s] { *out = append(*out, s); seen[s] = true }
		}
		out := make([]string, 0, len(in))
		// Preferred fixed order for stability
		push("original", &out)
		push("golden.patch", &out)
		for i := 1; i <= 4; i++ { push(fmt.Sprintf("solution%d.patch", i), &out) }
		// Add any remaining entries in alphabetical order
		extra := make([]string, 0, len(in))
		for _, s := range in { if !seen[s] { extra = append(extra, s) } }
		sort.Strings(extra)
		out = append(out, extra...)
		return out
	}
	config.Solutions = normalizeSolutions(config.Solutions)

	// Ensure Triggers.Containers has entries for all solutions (including original/golden)
	if config.Triggers.Containers == nil { config.Triggers.Containers = make(map[string]string) }
    for _, solution := range config.Solutions {
        if _, ok := config.Triggers.Containers[solution]; !ok || config.Triggers.Containers[solution] == "" {
            base := strings.TrimSuffix(solution, filepath.Ext(solution))
            config.Triggers.Containers[solution] = resolveContainerName(base)
        }
    }

	// Check if we need to recreate containers based on image tag/ID changes or missing containers
	recreateNeeded = false // Reset the flag before checking containers

	// Check each container individually and evaluate all of them (do not break early)
	for patchName, containerName := range config.Triggers.Containers {
		base := strings.TrimSuffix(patchName, filepath.Ext(patchName))
		exists, err := CheckContainerExists(containerName)
		if err != nil {
			logger.Printf("Error checking if container %s exists: %v", containerName, err)
			recreateNeeded = true
			continue
		}

		if !exists {
			logger.Printf("Container %s (for %s) does not exist, will recreate", containerName, patchName)
			recreateNeeded = true
			continue
		}

		// Golden gating: if golden container exists and golden flag is not set, skip considering it for recreation
		if base == "golden" && exists && !config.Golden {
			logger.Printf("Golden container %s exists and golden flag not set -> skipping recreation checks for golden", containerName)
			continue
		}
		// Original gating: if original container exists and original flag is not set, skip considering it for recreation
		if base == "original" && exists && !config.Original {
			logger.Printf("Original container %s exists and original flag not set -> skipping recreation checks for original", containerName)
			continue
		}

		// Check if container needs recreation due to image changes
		shouldRecreate, err := ShouldRecreateContainer(containerName, config.Triggers.ImageTag, config.Triggers.ImageID, logger)
		if err != nil {
			logger.Printf("Error checking if container %s needs recreation: %v", containerName, err)
			recreateNeeded = true
			continue
		}

		if shouldRecreate {
			logger.Printf("Container %s (for %s) needs recreation due to image change", containerName, patchName)
			recreateNeeded = true
		}
	}

	// Check if volumes exist - we only care about the solution1_volume since that's what we're using
	solutionVolumePath := filepath.Join(stepExec.BasePath, "volume_solution1")
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
	// If solutions are still empty but we have container assignments, derive from those keys.
	if len(config.Solutions) == 0 && len(config.Triggers.Containers) > 0 {
		for k := range config.Triggers.Containers {
			if k == "golden" { config.Solutions = append(config.Solutions, "golden.patch"); continue }
			if k == "original" { config.Solutions = append(config.Solutions, "original"); continue }
			config.Solutions = append(config.Solutions, k)
		}
		logger.Printf("Derived solutions from Triggers.Containers keys: %v", config.Solutions)
	}
	// Keep original and golden in the list if missing
	ensureInSolutions("original")
	ensureInSolutions("golden.patch")

	// Debug: log final solutions and containers map before creation loop
	logger.Printf("Final Solutions list: %v", config.Solutions)
	logger.Printf("Final Triggers.Containers: %v", config.Triggers.Containers)
	for _, solutionFile := range config.Solutions {
		containerName, ok := config.Triggers.Containers[solutionFile]
		if !ok || containerName == "" {
			base := strings.TrimSuffix(solutionFile, filepath.Ext(solutionFile))
			containerName = GenerateDVContainerNameForBase(stepExec.TaskID, base)
			config.Triggers.Containers[solutionFile] = containerName
			logger.Printf("Generated container name for %s: %s", solutionFile, containerName)
		}
		// Build host mount path. For 'original', mount the original directory created by docker_extract_volume.
		// Otherwise, mount the corresponding volume_<name> directory (e.g., solution1 -> volume_solution1, golden -> volume_golden)
		baseName := strings.TrimSuffix(solutionFile, filepath.Ext(solutionFile))
		solutionVolumePath := filepath.Join(stepExec.BasePath, fmt.Sprintf("volume_%s", baseName))
		if solutionFile == "original" {
			solutionVolumePath = filepath.Join(stepExec.BasePath, "original")
		}

		// Golden gating at execution time: if golden container exists and golden flag not set, and not forcing, skip actions for golden
		if baseName == "golden" && !config.Golden {
			if exists, _ := CheckContainerExists(containerName); exists && !forceRecreate {
				logger.Printf("Golden container %s exists and golden flag not set (and not forced) -> skipping start/recreate/patch for golden", containerName)
				continue
			}
		}
		// Original gating at execution time: if original container exists and original flag not set, and not forcing, skip actions for original
		if baseName == "original" && !config.Original {
			if exists, _ := CheckContainerExists(containerName); exists && !forceRecreate {
				logger.Printf("Original container %s exists and original flag not set (and not forced) -> skipping start/recreate/patch for original", containerName)
				continue
			}
		}

		// Remove existing container if it exists
		// Note: Do not remove the container unconditionally here.
		// Removal is handled below only when recreation is needed.

		// Check if we need to recreate the container
		shouldRecreate, err := ShouldRecreateContainer(containerName, config.Triggers.ImageTag, config.Triggers.ImageID, logger)
		if err != nil {
			return fmt.Errorf("error checking if container should be recreated: %w", err)
		}

		logger.Printf("Decision: container %s shouldRecreate=%v force=%v", containerName, shouldRecreate, forceRecreate)
		if shouldRecreate || forceRecreate {
			// Only recreate the container if needed
			if exists, _ := CheckContainerExists(containerName); exists {
				logger.Printf("Removing existing container: %s", containerName)
				if err := RemoveDockerContainer(containerName, logger); err != nil {
					return fmt.Errorf("failed to remove existing container: %w", err)
				}
			}

			// Start a new container with the volume mounted
			// We must ensure all options (including --name) appear BEFORE the image name.
			preImage := make([]string, 0, len(config.Parameters)+12) // docker run options (before IMAGE)
			postImage := make([]string, 0, 8)                        // args passed to ENTRYPOINT/CMD (after IMAGE)
			foundImage := false

			// Base options
			platform := taskSettings.Platform
			if platform == "" {
				platform = "linux/amd64"
			}
			preImage = append(preImage, "--platform", platform, "-d")
			preImage = append(preImage, "-v", fmt.Sprintf("%s:%s", solutionVolumePath, taskSettings.AppFolder))

			// Parse user parameters:
			//  - strip any --name occurrences
			//  - detect first occurrence of the image tag to split pre/post image args
			for _, param := range config.Parameters {
				// Replace placeholders
				replaced := param
				replaced = strings.ReplaceAll(replaced, "%%HOSTPATH%%", solutionVolumePath)
				replaced = strings.ReplaceAll(replaced, "%%DOCKERVOLUME%%", taskSettings.AppFolder)
				replaced = strings.ReplaceAll(replaced, "%%IMAGETAG%%", config.Triggers.ImageTag)
				replaced = strings.ReplaceAll(replaced, "%%VOLUME_NAME%%", solutionVolumePath)
				replaced = strings.ReplaceAll(replaced, "%%CONTAINER_NAME%%", containerName)
				replaced = strings.ReplaceAll(replaced, "%%APP_FOLDER%%", taskSettings.AppFolder)

				// Tokenize conservatively
				tokens := strings.Fields(replaced)
				i := 0
				for i < len(tokens) {
					tok := tokens[i]
					// Drop any attempt to set container name
					if tok == "--name" {
						end := i + 2
						if end > len(tokens) {
							end = len(tokens)
						}
						logger.Printf("Stripping user parameter that attempts to set container name: %q", strings.Join(tokens[i:end], " "))
						// skip flag and its value if present
						if i+1 < len(tokens) {
							i += 2
						} else {
							i++
						}
						continue
					}
					// Drop any user-specified platform to enforce task.settings.platform
					if tok == "--platform" {
						end := i + 2
						if end > len(tokens) {
							end = len(tokens)
						}
						logger.Printf("Stripping user parameter that attempts to set platform: %q", strings.Join(tokens[i:end], " "))
						if i+1 < len(tokens) {
							i += 2
						} else {
							i++
						}
						continue
					}
					// Detect the image token; everything after goes to postImage
					if !foundImage && tok == config.Triggers.ImageTag {
						foundImage = true
						i++
						// Remaining tokens from this parameter are post-image
						if i < len(tokens) {
							postImage = append(postImage, tokens[i:]...)
						}
						break
					}
					// Otherwise, before image -> pre-image options
					if !foundImage {
						preImage = append(preImage, tok)
					} else {
						postImage = append(postImage, tok)
					}
					i++
				}
			}

			// Enforce our container name BEFORE the image
			preImage = append(preImage, "--name", containerName)

			// Decide which image to use
			imageName := config.Triggers.ImageTag
			if imageName == "" {
				if !foundImage {
					return fmt.Errorf("no image specified: config.triggers.image_tag is empty and none found in parameters")
				}
				// If foundImage is true but imageName empty, we already split at it; keep imageName empty here
			}

			// Build final params: options, IMAGE, then args
			params := make([]string, 0, len(preImage)+1+len(postImage)+4)
			params = append(params, preImage...)
			if imageName != "" {
				params = append(params, imageName)
			}
			params = append(params, postImage...)

			// Add keep-alive command if needed (after image)
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
		// Do not apply a solution patch for the original container; it's a baseline
		if solutionFile != "" && solutionFile != "original" {
			patchFile = filepath.Join(stepExec.BasePath, solutionFile)
			if fi, err := os.Stat(patchFile); err != nil {
				if os.IsNotExist(err) {
					logger.Printf("Patch file not found: %s, skipping patch application", patchFile)
					patchFile = ""
				} else {
					logger.Printf("Error stating patch file %s: %v (skipping)", patchFile, err)
					patchFile = ""
				}
			} else if fi.IsDir() {
				logger.Printf("Patch path is a directory, not a file: %s; skipping", patchFile)
				patchFile = ""
			} else {
				logger.Printf("Found patch file: %s", patchFile)
			}
		}

		// Apply git cleanup and patches to the container
		logger.Printf("Applying git cleanup and patches to container %s", containerName)
		workingDir := taskSettings.AppFolder
		if workingDir == "" {
			workingDir = taskSettings.AppFolder
		}
		// Resolve held-out tests patch relative to task base path when provided
		heldOutPath := config.HeldOutTestFile
		if heldOutPath != "" && !filepath.IsAbs(heldOutPath) {
			heldOutPath = filepath.Join(stepExec.BasePath, heldOutPath)
		}
		if err := ApplyGitCleanupAndPatch(containerName, workingDir, patchFile, heldOutPath, config.GradingSetupScript, logger); err != nil {
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
		// Temporary policy: do not persist artifacts in step settings.
		// Container assignments should be sourced from task.settings.containers_map.
	}

	// After successful execution, update stored hashes for triggers and perform settings cleanup
	logger.Printf("Debug: Attempting to update step settings with image_id: %s", config.Triggers.ImageID)
	if err := json.Unmarshal([]byte(stepExec.Settings), &settings); err != nil {
		return fmt.Errorf("failed to unmarshal settings for update: %w", err)
	}
	settings.DockerVolumePool.Triggers.ImageID = config.Triggers.ImageID
	if settings.DockerVolumePool.Force {
		logger.Printf("Cleanup: disabling docker_volume_pool.force (was true)")
	}
	settings.DockerVolumePool.Force = false
	// Do not persist runtime golden flag
	if settings.DockerVolumePool.Golden {
		logger.Printf("Cleanup: disabling docker_volume_pool.golden (was true)")
	}
	settings.DockerVolumePool.Golden = false
	// Do not persist runtime original flag
	if settings.DockerVolumePool.Original {
		logger.Printf("Cleanup: disabling docker_volume_pool.original (was true)")
	}
	settings.DockerVolumePool.Original = false

	// Temporary cleanup: remove artifacts, pool_size, solutions and any '--platform' parameters from settings
	if settings.DockerVolumePool.Artifacts != nil {
		logger.Printf("Cleanup: removing docker_volume_pool.artifacts from step settings")
		settings.DockerVolumePool.Artifacts = nil
	}
	if settings.DockerVolumePool.ContainerFolder != "" {
		logger.Printf("Cleanup: clearing docker_volume_pool.container_folder from step settings (use task.settings.app_folder)")
		settings.DockerVolumePool.ContainerFolder = ""
	}
	if settings.DockerVolumePool.PoolSize != 0 {
		logger.Printf("Cleanup: clearing docker_volume_pool.pool_size from step settings")
		settings.DockerVolumePool.PoolSize = 0
	}
	if len(settings.DockerVolumePool.Solutions) > 0 {
		logger.Printf("Cleanup: clearing docker_volume_pool.solutions from step settings")
		settings.DockerVolumePool.Solutions = nil
	}
	if len(settings.DockerVolumePool.Parameters) > 0 {
		filtered := make([]string, 0, len(settings.DockerVolumePool.Parameters))
		for _, p := range settings.DockerVolumePool.Parameters {
			if strings.Contains(p, "--platform") {
				logger.Printf("Cleanup: stripping parameter from step settings: %q", p)
				continue
			}
			filtered = append(filtered, p)
		}
		settings.DockerVolumePool.Parameters = filtered
	}

	updatedSettings, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("failed to marshal updated settings: %w", err)
	}

	// Belt-and-suspenders: explicitly remove docker_volume_pool.artifacts at JSON level
	var settingsMap map[string]interface{}
	if err := json.Unmarshal(updatedSettings, &settingsMap); err == nil {
		if dvpRaw, ok := settingsMap["docker_volume_pool"].(map[string]interface{}); ok {
			if _, exists := dvpRaw["artifacts"]; exists {
				logger.Printf("Cleanup (explicit): deleting docker_volume_pool.artifacts from settings JSON")
				delete(dvpRaw, "artifacts")
			}
		}
		if rem, err := json.Marshal(settingsMap); err == nil {
			updatedSettings = rem
		}
	}

	if db != nil {
		if _, err := db.Exec("UPDATE steps SET settings = $1 WHERE id = $2", string(updatedSettings), stepExec.StepID); err != nil {
			return fmt.Errorf("failed to update step settings in database: %w", err)
		}
	} else {
		logger.Printf("Skipping DB update of step settings for step %d (nil DB)", stepExec.StepID)
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
        // If using bash entrypoint, prefer -lc so we can pass a single string command
        usesBash := false
        for i := 0; i < len(params)-1; i++ {
            if params[i] == "--entrypoint" && strings.Contains(params[i+1], "bash") {
                usesBash = true
                break
            }
        }
        // Remove stray --login which conflicts with -lc
        sanitized := make([]string, 0, len(params))
        for _, p := range params {
            if p == "--login" {
                continue
            }
            sanitized = append(sanitized, p)
        }
        params = sanitized

        script := "while true; do sleep 30; done"
        if usesBash {
            // Pass as: bash -lc "<script>"
            params = append(params, "-lc", script)
        } else {
            // Fall back to sh -c
            params = append(params, "sh", "-c", script)
        }
        logger.Printf("Added keep-alive command to parameters: %v", params)
    }
    return params
}

// RunDockerCommand executes a Docker command with given parameters
func RunDockerCommand(params []string, containerName string, logger *log.Logger, detached bool) error {
    // Log raw params before processing
    logger.Printf("RunDockerCommand: raw params for %s: %v (detached=%v)", containerName, params, detached)

    // Remove any user-provided --name to avoid duplication/conflict
    cleaned := make([]string, 0, len(params))
    skipNext := false
    for i := 0; i < len(params); i++ {
        if skipNext {
            skipNext = false
            continue
        }
        if params[i] == "--name" {
            skipNext = true // skip its value
            continue
        }
        cleaned = append(cleaned, params[i])
    }
    params = cleaned

    // Flatten params: split on spaces except for -c keep-alive command
    flattened := []string{}
    for i := 0; i < len(params); i++ {
        // Preserve script as a single token after -c or -lc
        if (params[i] == "-c" || params[i] == "-lc") && i+1 < len(params) {
            flattened = append(flattened, params[i], params[i+1])
            i++
            continue
        }
        parts := strings.Split(params[i], " ")
        for _, part := range parts {
            trimmedPart := strings.TrimSpace(part)
            if trimmedPart != "" {
                flattened = append(flattened, trimmedPart)
            }
        }
    }
    // Avoid duplicate -d if already provided in parameters
    hasDetach := false
    for _, p := range flattened {
        if p == "-d" || p == "--detach" {
            hasDetach = true
            break
        }
    }
    if detached && !hasDetach {
        flattened = append([]string{"-d"}, flattened...)
    }
    cmdArgs := append([]string{"run", "--name", containerName}, flattened...)
    logger.Printf("RunDockerCommand: flattened args for %s: %v", containerName, flattened)
    logger.Printf("Constructed Docker command for container %s: docker %s", containerName, strings.Join(cmdArgs, " "))

    cmd := exec.Command("docker", cmdArgs...)
    output, err := cmd.CombinedOutput()
    if err != nil {
        logger.Printf("Error running Docker command for container %s: %v, output: %s", containerName, err, string(output))
        if vout, verr := exec.Command("docker", "--version").CombinedOutput(); verr == nil {
            logger.Printf("docker --version: %s", strings.TrimSpace(string(vout)))
        }
        if iout, ierr := exec.Command("docker", "info", "--format", "{{json .Server.Version}} {{json .OSType}} {{json .Driver}} {{json .LoggingDriver}}").CombinedOutput(); ierr == nil {
            logger.Printf("docker info (subset): %s", strings.TrimSpace(string(iout)))
        }
        return fmt.Errorf("failed to run Docker command: %w", err)
    }
    // Log output even on success (usually the container ID)
    logger.Printf("Docker run output for container %s: %s", containerName, strings.TrimSpace(string(output)))
    logger.Printf("Successfully ran Docker command for container %s", containerName)

    if detached {
        if err := WaitForContainerRunning(containerName, 15, logger); err != nil {
            logger.Printf("Container %s did not reach running state: %v", containerName, err)
            return err
        }
        logger.Printf("Container %s is running", containerName)
        // Inspect resource limits and mounts to diagnose issues
        if insp, ierr := exec.Command("docker", "inspect", "--format",
            "Status={{.State.Status}} OOMKilled={{.State.OOMKilled}} ExitCode={{.State.ExitCode}} Error={{.State.Error}} Memory={{.HostConfig.Memory}} PidsLimit={{.HostConfig.PidsLimit}} Mounts={{range .Mounts}}{{.Destination}}<-{{.Source}};{{end}}",
            containerName).CombinedOutput(); ierr == nil {
            logger.Printf("Post-run inspect %s: %s", containerName, strings.TrimSpace(string(insp)))
        }
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
} // Added closing brace here

// ApplyGitCleanupAndPatch applies git cleanup and patches in a container.
// If workingDir is non-empty, commands execute with docker exec -w <workingDir>.
func ApplyGitCleanupAndPatch(containerName string, workingDir string, patchFile string, heldOutTestFile string, gradingSetupScript string, logger *log.Logger) error {
    logger.Printf("ApplyGitCleanupAndPatch: container=%s workingDir=%s patchFile=%s heldOutTestFile=%s gradingSetupScript=%s", containerName, workingDir, patchFile, heldOutTestFile, gradingSetupScript)
    commands := []string{
        "cd " + workingDir,
        // Preflight diagnostics to aid debugging of failures
        "pwd",
        "ls -la",
        "git config --global --add safe.directory '" + workingDir + "' || true",
        "git status -s || true",
        // Cleanup before applying patches
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
        // Guard: remove a stale .git/index.lock if present before each git-related step
        guard := "if [ -e .git/index.lock ]; then echo '[guard] removing .git/index.lock'; rm -f .git/index.lock; fi"
        if strings.Contains(cmdStr, "git ") || strings.HasPrefix(cmdStr, "git") {
            var guardCmd *exec.Cmd
            if workingDir != "" {
                guardCmd = exec.Command("docker", "exec", "-w", workingDir, containerName, "bash", "-c", guard)
            } else {
                guardCmd = exec.Command("docker", "exec", containerName, "bash", "-c", guard)
            }
            if gout, gerr := guardCmd.CombinedOutput(); gerr != nil {
                logger.Printf("Warning: guard before '%s' failed in %s: %v\nOutput: %s", cmdStr, containerName, gerr, string(gout))
                // Continue regardless; attempt the command anyway
            }
        }

        var execCmd *exec.Cmd
        var execArgs []string
        if workingDir != "" {
            execArgs = []string{"exec", "-w", workingDir, containerName, "bash", "-c", cmdStr}
            execCmd = exec.Command("docker", execArgs...)
        } else {
            execArgs = []string{"exec", containerName, "bash", "-c", cmdStr}
            execCmd = exec.Command("docker", execArgs...)
        }
        logger.Printf("About to exec in %s: docker %s", containerName, strings.Join(execArgs, " "))
        output, err := execCmd.CombinedOutput()
        if err != nil {
            // Do not abort on git apply failures; capture and continue
            if strings.Contains(cmdStr, "git apply") {
                logger.Printf("Non-fatal apply failure in %s: %s\nError: %v\nOutput: %s", containerName, cmdStr, err, string(output))
                continue
            }
            logger.Printf("Command failed in container %s: %s\nError: %v\nOutput: %s", containerName, cmdStr, err, string(output))
            // Extra diagnostics on failure: container state and recent logs
            if inspectOut, ierr := exec.Command("docker", "inspect", "--format", "Status={{.State.Status}} OOMKilled={{.State.OOMKilled}} ExitCode={{.State.ExitCode}} Error={{.State.Error}}", containerName).CombinedOutput(); ierr == nil {
                logger.Printf("Container state %s: %s", containerName, strings.TrimSpace(string(inspectOut)))
            } else {
                logger.Printf("Failed to inspect container %s: %v", containerName, ierr)
            }
            if logsOut, lerr := exec.Command("docker", "logs", "--tail", "100", containerName).CombinedOutput(); lerr == nil {
                logger.Printf("Recent logs from %s:\n%s", containerName, string(logsOut))
            } else {
                logger.Printf("Failed to get logs for %s: %v", containerName, lerr)
            }
            return fmt.Errorf("failed to execute command in container: %w", err)
        }
        // Log output for useful commands
        if strings.HasPrefix(cmdStr, "git ") || cmdStr == "pwd" || strings.HasPrefix(cmdStr, "ls ") {
            trimmed := strings.TrimSpace(string(output))
            if trimmed != "" {
                logger.Printf("Output (%s):\n%s", cmdStr, trimmed)
            }
        }
        logger.Printf("Executed in container %s: %s", containerName, cmdStr)
    }

	return nil
}

// GenerateDVContainerName generates a consistent container name for Docker volume pool steps
// Format: task_<taskID>_volume_<index> where index is 1-based
func GenerateDVContainerName(taskID int, index int) string {
    return fmt.Sprintf("task_%d_volume_%d", taskID, index)
}

// GenerateDVContainerNameForBase generates a stable container name for a logical base key
// base is one of: original, golden, solution1..solutionN (trimmed of file extension)
func GenerateDVContainerNameForBase(taskID int, base string) string {
    switch base {
    case "original":
        return fmt.Sprintf("task_%d_volume_original", taskID)
    case "golden":
        return fmt.Sprintf("task_%d_volume_golden", taskID)
    default:
        // For solutionN or any other stable key, embed the key
        // e.g., task_123_volume_solution1
        cleaned := strings.ReplaceAll(base, "/", "_")
        cleaned = strings.ReplaceAll(cleaned, " ", "_")
        return fmt.Sprintf("task_%d_volume_%s", taskID, cleaned)
    }
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
        // Resolve the expected image ID to its full digest (in case a short ID was provided)
        cmd = exec.Command("docker", "inspect", "--format", "{{.ID}}", expectedImageID)
        expectedFullImageID, err := cmd.CombinedOutput()
        if err != nil {
            // If we cannot resolve the expected ID, log and fall back to tag comparison below
            logger.Printf("Warning: failed to resolve expected image ID %q: %v", expectedImageID, err)
        } else {
            trimmedExpectedID := strings.TrimSpace(string(expectedFullImageID))
            logger.Printf("Comparing image IDs - Current: %s, Expected: %s", currentImageID, trimmedExpectedID)
            if currentImageID != trimmedExpectedID {
                logger.Printf("Container %s: Image ID changed from %s to %s", containerName, currentImageID, trimmedExpectedID)
                return true, nil
            }
            // IDs match -> no recreation needed; ignore tag differences
            logger.Printf("Container %s: Image ID matches expected; no recreation needed", containerName)
            return false, nil
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
		containers[fmt.Sprintf("solution%d.patch", i)] = GenerateDVContainerName(taskID, i)
	}
	return containers
}

// CheckFileHashTriggers checks if file hashes have changed for Docker volume pool steps
func CheckFileHashTriggers(basePath string, config *DockerVolumePoolConfig, logger *log.Logger) (bool, error) {
	runNeeded := false
	for fileName := range config.Triggers.Files {
		filePath := filepath.Join(basePath, fileName)
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
