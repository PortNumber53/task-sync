package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// processDockerPoolSteps processes docker pool steps for active tasks
func processDockerPoolSteps(db *sql.DB, stepID int) error {
	var query string
	var rows *sql.Rows
	var err error

	if stepID != 0 {
		query = `SELECT s.id, s.task_id, s.settings, t.base_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE s.id = $1 AND s.settings ? 'docker_pool'`
		rows, err = db.Query(query, stepID)
	} else {
		query = `SELECT s.id, s.task_id, s.settings, t.base_path
		FROM steps s
		JOIN tasks t ON s.task_id = t.id
		WHERE t.status = 'active'
		AND t.base_path IS NOT NULL
		AND t.base_path <> ''
		AND s.settings ? 'docker_pool'`
		rows, err = db.Query(query)
	}

	if err != nil {
		models.StepLogger.Println("Docker pool query error:", err)
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var step models.StepExec
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Settings, &step.BasePath); err != nil {
			models.StepLogger.Println("Row scan error:", err)
			continue
		}

		var configHolder models.StepConfigHolder
		if err := json.Unmarshal([]byte(step.Settings), &configHolder); err != nil {
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "invalid step config"})
			models.StepLogger.Printf("Step %d: invalid step config: %v\n", step.StepID, err)
			continue
		}

		config := configHolder.DockerPool
		if config == nil {
			models.StoreStepResult(db, step.StepID, map[string]interface{}{"result": "failure", "message": "docker_pool config not found"})
			models.StepLogger.Printf("Step %d: docker_pool config not found in step settings\n", step.StepID)
			continue
		}

		ok, err := models.CheckDependencies(db, &step)
		if err != nil {
			models.StepLogger.Printf("Step %d: error checking dependencies: %v\n", step.StepID, err)
			continue
		}
		if !ok {
			models.StepLogger.Printf("Step %d: waiting for dependencies to complete\n", step.StepID)
			continue
		}

		// Read image_tag directly from task.settings.docker.image_tag
		taskSettings, err := models.GetTaskSettings(db, step.TaskID)
		if err != nil {
			models.StepLogger.Printf("Step %d: CRITICAL: Could not load task settings for docker_pool. Error: %v\n", step.StepID, err)
			return err
		}
		imageTag := taskSettings.Docker.ImageTag
		if imageTag == "" {
			models.StepLogger.Printf("Step %d: CRITICAL: No image_tag found in task settings.\n", step.StepID)
			return fmt.Errorf("no image_tag found in task settings")
		}
		models.StepLogger.Printf("Step %d: Using image_tag '%s' from task settings\n", step.StepID, imageTag)

		// Resolve expected image ID for robust comparison (tags can vary)
		var expectedImageID string
		{
			out, err := exec.Command("docker", "image", "inspect", imageTag).CombinedOutput()
			if err == nil {
				var imgs []struct{ Id string `json:"Id"` }
				if json.Unmarshal(out, &imgs) == nil && len(imgs) > 0 {
					expectedImageID = imgs[0].Id
				}
			}
			if expectedImageID == "" {
				// Fallback to the tag itself if we couldn't resolve the ID
				expectedImageID = imageTag
			}
			models.StepLogger.Printf("Step %d: Resolved expected image id: %s\n", step.StepID, expectedImageID)
		}

		// We always manage exactly 6 containers mapped as:
		// original, golden, solution1..solution4
		desiredKeys := []string{"original", "golden", "solution1", "solution2", "solution3", "solution4"}

		// Load any existing containers from task.settings (new location) for continuity
		runningContainers := map[string]models.ContainerInfo{}
		usedIDs := map[string]bool{}

		// First, validate any existing taskSettings.ContainersMap entries and keep running ones with matching image
		if taskSettings.ContainersMap != nil {
			for key, container := range taskSettings.ContainersMap {
				inspectCmd := exec.Command("docker", "inspect", container.ContainerID)
				output, err := inspectCmd.CombinedOutput()
				if err == nil {
					var inspectResult []struct {
						Config struct {
							Image string `json:"Image"`
						} `json:"Config"`
						State struct {
							Running bool `json:"Running"`
						} `json:"State"`
						Image string `json:"Image"`
					}
					if err := json.Unmarshal(output, &inspectResult); err == nil && len(inspectResult) > 0 {
						img := inspectResult[0].Image
						running := inspectResult[0].State.Running
						if img != expectedImageID {
							// Stop and remove outdated container
							exec.Command("docker", "stop", container.ContainerID).Run()
							exec.Command("docker", "rm", container.ContainerID).Run()
							continue
						}
						if running {
							if usedIDs[container.ContainerID] {
								// avoid duplicate use
								continue
							}
							runningContainers[key] = container
							usedIDs[container.ContainerID] = true
						} else {
							// Try to start a stopped but valid container and keep the same key mapping
							startCmd := exec.Command("docker", "start", container.ContainerID)
							if out, sErr := startCmd.CombinedOutput(); sErr == nil {
								models.StepLogger.Printf("Step %d: started stopped container for key %s: %s", step.StepID, key, strings.TrimSpace(string(out)))
								if !usedIDs[container.ContainerID] {
									runningContainers[key] = container
									usedIDs[container.ContainerID] = true
								}
							} else {
								models.StepLogger.Printf("Step %d: failed to start existing container for key %s: %v. Output: %s", step.StepID, key, sErr, string(out))
								// If it cannot start, remove so we can recreate cleanly
								exec.Command("docker", "rm", container.ContainerID).Run()
							}
						}
					}
				}
			}
		}

		// Backward compatibility: also consider step.settings.docker_pool.containers if any
		for _, container := range config.Containers {
			inspectCmd := exec.Command("docker", "inspect", container.ContainerID)
			output, err := inspectCmd.CombinedOutput()
			if err == nil {
				var inspectResult []struct {
					Config struct {
						Image string `json:"Image"`
					} `json:"Config"`
					State struct {
						Running bool `json:"Running"`
					} `json:"State"`
					Image string `json:"Image"`
				}
				if err := json.Unmarshal(output, &inspectResult); err == nil && len(inspectResult) > 0 {
					img := inspectResult[0].Image
					running := inspectResult[0].State.Running
					if img != expectedImageID {
						// Stop and remove outdated container
						exec.Command("docker", "stop", container.ContainerID).Run()
						exec.Command("docker", "rm", container.ContainerID).Run()
					} else if running {
						// Place into the first empty desired key slot
						for _, key := range desiredKeys {
							if _, exists := runningContainers[key]; !exists {
								if usedIDs[container.ContainerID] {
									break
								}
								runningContainers[key] = container
								usedIDs[container.ContainerID] = true
								break
							}
						}
					} else {
						// Try to start stopped but valid container
						startCmd := exec.Command("docker", "start", container.ContainerID)
						if out, sErr := startCmd.CombinedOutput(); sErr == nil {
							models.StepLogger.Printf("Step %d: started stopped container (legacy list): %s", step.StepID, strings.TrimSpace(string(out)))
							for _, key := range desiredKeys {
								if _, exists := runningContainers[key]; !exists {
									if usedIDs[container.ContainerID] {
										break
									}
									runningContainers[key] = container
									usedIDs[container.ContainerID] = true
									break
								}
							}
						} else {
							models.StepLogger.Printf("Step %d: failed to start existing container (legacy list): %v. Output: %s", step.StepID, sErr, string(out))
							exec.Command("docker", "rm", container.ContainerID).Run()
						}
					}
				}
			}
		}

		// Determine docker run parameters: prefer task.settings.docker_run_parameters, else fall back to step config
		dockerRunParams := taskSettings.DockerRunParameters
		hadTaskLevelParams := len(dockerRunParams) > 0
		if !hadTaskLevelParams && len(config.Parameters) > 0 {
			dockerRunParams = config.Parameters
		}

		processedDockerRunParams := []string{}
		postImageArgs := []string{}
		for _, param := range dockerRunParams {
			replacedParam := strings.Replace(param, "%%IMAGETAG%%", imageTag, -1)
			if replacedParam != "--rm" {
				processedDockerRunParams = append(processedDockerRunParams, strings.Fields(replacedParam)...)
			}
		}

		if config.KeepForever {
			hasKeepAliveCmd := false
			for _, param := range dockerRunParams {
				if (strings.Contains(param, "while true") && strings.Contains(param, "sleep")) ||
					strings.Contains(param, "sleep infinity") ||
					strings.Contains(param, "tail -f") {
					hasKeepAliveCmd = true
					break
				}
			}

			if !hasKeepAliveCmd {
				// Choose keep-alive args based on entrypoint
				hasEntrypoint := false
				epShell := ""
				hasLogin := false
				for i := 0; i < len(processedDockerRunParams); i++ {
					tok := processedDockerRunParams[i]
					if tok == "--entrypoint" && i+1 < len(processedDockerRunParams) {
						hasEntrypoint = true
						val := processedDockerRunParams[i+1]
						if strings.Contains(val, "bash") {
							epShell = "bash"
						} else if strings.Contains(val, "sh") {
							epShell = "sh"
						}
						i++
						continue
					}
					if tok == "--login" {
						hasLogin = true
					}
				}

				var keepAliveArgs []string
				if hasEntrypoint {
					// Pass flags for the entrypoint shell directly
					if epShell == "bash" {
						if hasLogin {
							keepAliveArgs = []string{"-lc", "while true; do sleep 30; done"}
						} else {
							keepAliveArgs = []string{"-c", "while true; do sleep 30; done"}
						}
					} else {
						// default to POSIX sh style
						keepAliveArgs = []string{"-c", "while true; do sleep 30; done"}
					}
				} else {
					// No entrypoint specified: run via sh -c as command after image
					keepAliveArgs = []string{"sh", "-c", "while true; do sleep 30; done"}
				}
				// Keep-alive must run AFTER the image, not as pre-image options
				postImageArgs = append(postImageArgs, keepAliveArgs...)
			}
		}

		// Start missing containers to satisfy all desired keys (reuse by deterministic name when possible)
		for _, key := range desiredKeys {
			if _, exists := runningContainers[key]; exists {
				continue
			}
			            // Deterministic container name per task and key for stable reuse (shared with docker_volume_pool)
            containerName := models.GenerateDVContainerNameForBase(step.TaskID, key)
            // Also support legacy name used previously to avoid duplicates during transition
            legacyName := fmt.Sprintf("tasksync_%d_%s", step.TaskID, key)
            candidates := []string{containerName, legacyName}

            // If a container with any candidate name exists, try to reuse it
            reused := false
            for _, name := range candidates {
                out, err := exec.Command("docker", "inspect", name).CombinedOutput()
                if err != nil {
                    continue
                }
                var inspectResult []struct {
                    Image string `json:"Image"`
                    State struct { Running bool `json:"Running"` } `json:"State"`
                    ID    string `json:"Id"`
                }
                if json.Unmarshal(out, &inspectResult) == nil && len(inspectResult) > 0 {
                    imgID := inspectResult[0].Image
                    running := inspectResult[0].State.Running
                    id := inspectResult[0].ID
                    if imgID == expectedImageID {
                        if running {
                            runningContainers[key] = models.ContainerInfo{ContainerID: id, ContainerName: name}
                            usedIDs[id] = true
                            reused = true
                            break
                        }
                        if out2, sErr := exec.Command("docker", "start", name).CombinedOutput(); sErr == nil {
                            models.StepLogger.Printf("Step %d: started existing container for key %s: %s", step.StepID, key, strings.TrimSpace(string(out2)))
                            if out3, e2 := exec.Command("docker", "inspect", name).CombinedOutput(); e2 == nil {
                                var res2 []struct{ ID string `json:"Id"` }
                                if json.Unmarshal(out3, &res2) == nil && len(res2) > 0 {
                                    id2 := res2[0].ID
                                    runningContainers[key] = models.ContainerInfo{ContainerID: id2, ContainerName: name}
                                    usedIDs[id2] = true
                                    reused = true
                                    break
                                }
                            }
                        } else {
                            models.StepLogger.Printf("Step %d: failed to start existing container %s for key %s: %v Output: %s", step.StepID, name, key, sErr, string(out2))
                        }
                    } else {
                        // Image mismatch; remove so we can recreate cleanly below
                        exec.Command("docker", "rm", "-f", name).Run()
                    }
                }
            }
            if reused {
                continue
            }

			// Compute a per-key bind mount so each logical container sees the correct workspace
			// - original      -> <base_path>/original        mounted at <app_folder>
			// - solution{1-4} -> <base_path>/volume_solutionX mounted at <app_folder>
			// - golden        -> <base_path>/volume_golden    mounted at <app_folder>
			hostPath := ""
			switch key {
			case "original":
				hostPath = filepath.Join(step.BasePath, "original")
			case "golden":
				hostPath = filepath.Join(step.BasePath, "volume_golden")
			default:
				// solution1..solution4
				hostPath = filepath.Join(step.BasePath, fmt.Sprintf("volume_%s", key))
			}

			// Determine container mount point
			mountPoint := taskSettings.AppFolder

			// Ensure absolute paths for docker -v mapping
			volumeArg := "-v"
			volumeMap := fmt.Sprintf("%s:%s", hostPath, mountPoint)

			// Build final docker run args ensuring IMAGE appears before any command args
			baseArgs := []string{"run", "-d", "--name", containerName, volumeArg, volumeMap}

			// Split user-provided params into pre-image options and post-image args around the image
			preImage := []string{}
			postImage := []string{}
			foundImage := false
			for i := 0; i < len(processedDockerRunParams); i++ {
				tok := processedDockerRunParams[i]
				if !foundImage && tok == imageTag {
					foundImage = true
					// everything after image goes to postImage
					if i+1 < len(processedDockerRunParams) {
						postImage = append(postImage, processedDockerRunParams[i+1:]...)
					}
					break
				}
				preImage = append(preImage, tok)
			}

			// If task-level platform is set and not already present in pre-image opts, inject it
			if taskSettings.Platform != "" {
				hasPlatform := false
				for i := 0; i < len(preImage); i++ {
					tok := preImage[i]
					if tok == "--platform" || strings.HasPrefix(tok, "--platform=") {
						hasPlatform = true
						break
					}
				}
				if !hasPlatform {
					// Prepend --platform to ensure it appears before other options (order among options is fine)
					preImage = append([]string{"--platform", taskSettings.Platform}, preImage...)
				}
			}

			// Compose final args: base, pre-image options, IMAGE, post-image args
			cmdArgs := append([]string{}, baseArgs...)
			cmdArgs = append(cmdArgs, preImage...)
			// Ensure the image is present exactly once
			cmdArgs = append(cmdArgs, imageTag)
			// Append any post-image args found in parameters and keep-alive
			cmdArgs = append(cmdArgs, postImage...)
			cmdArgs = append(cmdArgs, postImageArgs...)

			models.StepLogger.Printf("Constructed docker command: docker %s\n", strings.Join(cmdArgs, " "))
			cmd := exec.Command("docker", cmdArgs...)
			output, err := cmd.CombinedOutput()
			models.StepLogger.Printf("Step %d: docker run output: %s", step.StepID, string(output))
			if err == nil {
				newContainerID := strings.TrimSpace(string(output))
				if usedIDs[newContainerID] {
					// Extremely unlikely for new run, but guard anyway
					models.StepLogger.Printf("Step %d: got duplicate container ID for key %s, removing and retrying", step.StepID, key)
					exec.Command("docker", "rm", "-f", newContainerID).Run()
				} else {
					runningContainers[key] = models.ContainerInfo{ContainerID: newContainerID, ContainerName: containerName}
					usedIDs[newContainerID] = true
				}
			} else {
				models.StepLogger.Printf("Step %d: failed to start container for key %s: %v. Output: %s\n", step.StepID, key, err, string(output))
			}
		}

		// Validate no stale Git index.lock exists inside containers before finalizing
		hasIndexLock := false
		lockedContainers := []string{}
		for key, c := range runningContainers {
			// Use sh -c to test for the lock file relative to app folder
			testCmd := exec.Command("docker", "exec", "-w", taskSettings.AppFolder, c.ContainerID, "sh", "-c", "if [ -e .git/index.lock ]; then echo exists; else echo ok; fi")
			out, err := testCmd.CombinedOutput()
			status := strings.TrimSpace(string(out))
			if err == nil && status == "exists" {
				hasIndexLock = true
				lockedContainers = append(lockedContainers, fmt.Sprintf("%s(%s)", key, c.ContainerID))
			}
		}

		// After containers are started/validated, run a git status check in each container.
		// The step will be considered successful only if all containers can run git status.
		gitStatusFailures := []string{}
		for key, c := range runningContainers {
			execCmd := exec.Command("docker", "exec", "-w", taskSettings.AppFolder, c.ContainerID, "git", "status")
			if out, err := execCmd.CombinedOutput(); err != nil {
				models.StepLogger.Printf("Step %d: git status failed in %s (%s): %v Output: %s", step.StepID, key, c.ContainerID, err, string(out))
				gitStatusFailures = append(gitStatusFailures, fmt.Sprintf("%s(%s)", key, c.ContainerID))
			}
		}

		// Write new state map for containers before updating task settings
		taskSettings.ContainersMap = make(map[string]models.ContainerInfo)
		for _, key := range desiredKeys {
			if c, ok := runningContainers[key]; ok {
				taskSettings.ContainersMap[key] = c
			}
		}

		// Migrate docker run parameters to task.settings ONLY if the task does not already have them
		migratedParams := false
		if len(taskSettings.DockerRunParameters) == 0 && len(dockerRunParams) > 0 {
			taskSettings.DockerRunParameters = dockerRunParams
			migratedParams = true
		}
		if err := models.UpdateTaskSettings(db, step.TaskID, taskSettings); err != nil {
			models.StepLogger.Printf("Step %d: Failed to update containers map/params in task settings: %v\n", step.StepID, err)
		} else {
			models.StepLogger.Printf("Step %d: Updated task settings with containers map and docker_run_parameters\n", step.StepID)
		}

		// 2) Minimize step.settings.docker_pool (remove containers to avoid duplication)
		//    Only remove parameters if the task has them already or we just migrated them
		config.Containers = nil
		if hadTaskLevelParams || migratedParams {
			config.Parameters = nil
		}
		// Keep depends_on and keep_forever, source_step_id, etc.
		updatedConfig := map[string]interface{}{
			"docker_pool": config,
		}
		newSettingsJSON, _ := json.Marshal(updatedConfig)

        if hasIndexLock {
            models.StoreStepResult(db, step.StepID, map[string]interface{}{
                "result":  "failure",
                "message": fmt.Sprintf("Detected .git/index.lock in containers: %s", strings.Join(lockedContainers, ", ")),
            })
        } else if len(gitStatusFailures) > 0 {
            models.StoreStepResult(db, step.StepID, map[string]interface{}{
                "result":  "failure",
                "message": fmt.Sprintf("git status failed in containers: %s", strings.Join(gitStatusFailures, ", ")),
            })
        } else {
            models.StoreStepResult(db, step.StepID, map[string]interface{}{
                "result":  "success",
                "message": fmt.Sprintf("Started/validated containers: %d; git status OK in all", len(taskSettings.ContainersMap)),
            })
        }
        db.Exec("UPDATE steps SET settings = $1, updated_at = NOW() WHERE id = $2", string(newSettingsJSON), step.StepID)
	}
	return nil
}
