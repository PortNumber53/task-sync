package internal

import (
    "database/sql"
    "fmt"
    "os"
    "strings"

    "github.com/PortNumber53/task-sync/pkg/models"
)

// RunTaskGolden runs all rubric_shell steps for a given task in golden mode.
// TODO: Enhance to ensure golden volume/container exist and perform held_out_tests.patch-based selective restore/remove.
func RunTaskGolden(db *sql.DB, taskID int) error {
    if db == nil {
        return fmt.Errorf("db is nil")
    }
    // Init a logger consistent with step processors
    if stepLogger == nil {
        InitStepLogger(os.Stdout)
    }
    // Ensure models logger too
    models.InitStepLogger(os.Stdout)

    // Verify task is active
    var status string
    if err := db.QueryRow("SELECT status FROM tasks WHERE id = $1", taskID).Scan(&status); err != nil {
        return fmt.Errorf("failed to fetch task status: %w", err)
    }
    if status != "active" {
        return fmt.Errorf("task %d status is '%s' (must be 'active')", taskID, status)
    }

    stepLogger.Printf("[GOLDEN] Preparing golden artifacts for task %d", taskID)

    // Ensure golden volume and container exist; rsync from original only on first create
    if err := ensureGoldenArtifacts(db, taskID); err != nil {
        return err
    }

    stepLogger.Printf("[GOLDEN] Running all rubric_shell steps for task %d in golden mode", taskID)

    // Ensure rubric_shell runs in golden-only assignment mode for this command
    restore := setRubricRunMode("golden-only")
    defer restore()

    // Query rubric_shell steps for the task
    rows, err := db.Query(`
        SELECT s.id, s.title, s.settings, COALESCE(t.local_path, '') AS base_path
        FROM steps s
        JOIN tasks t ON s.task_id = t.id
        WHERE s.task_id = $1 AND s.settings ? 'rubric_shell'
    `, taskID)
    if err != nil {
        return fmt.Errorf("failed to list rubric_shell steps for task %d: %w", taskID, err)
    }
    defer rows.Close()

    count := 0
    for rows.Next() {
        var (
            stepID   int
            title    string
            settings string
            basePath string
        )
        if err := rows.Scan(&stepID, &title, &settings, &basePath); err != nil {
            return fmt.Errorf("failed to scan step: %w", err)
        }
        se := &models.StepExec{StepID: stepID, TaskID: taskID, Title: title, Settings: settings, BasePath: basePath}
        if err := ProcessRubricShellStep(db, se, stepLogger, true /*force*/, true /*golden*/); err != nil {
            return fmt.Errorf("rubric_shell step %d failed: %w", stepID, err)
        }
        count++
    }
    if err := rows.Err(); err != nil {
        return err
    }

    if count == 0 {
        stepLogger.Printf("[GOLDEN] No rubric_shell steps found for task %d", taskID)
    }
    stepLogger.Printf("[GOLDEN] Completed golden run for task %d (%d steps)", taskID, count)
    return nil
}

// ensureGoldenArtifacts creates the golden Docker volume (and container) and rsyncs from
// the Original volume only when the golden volume does not exist yet. If both exist, re-use.
func ensureGoldenArtifacts(db *sql.DB, taskID int) error {
    // Load task settings for volume name, app folder, and image tag
    ts, err := models.GetTaskSettings(db, taskID)
    if err != nil {
        return fmt.Errorf("failed to get task settings: %w", err)
    }
    if ts == nil {
        return fmt.Errorf("task settings not found for task %d", taskID)
    }
    if ts.VolumeName == "" {
        return fmt.Errorf("task.settings.volume_name is empty; cannot locate Original volume")
    }
    if ts.Docker.ImageTag == "" {
        return fmt.Errorf("task.settings.docker.image_tag is empty; required to start containers")
    }
    appFolder := ts.AppFolder
    if appFolder == "" {
        appFolder = "/app"
    }

    originalVol := ts.VolumeName
    goldenVol := ts.VolumeName + "_golden"

    // Create golden volume if missing
    exists, err := models.CheckVolumeExists(goldenVol)
    if err != nil {
        return fmt.Errorf("failed to check golden volume existence: %w", err)
    }
    if !exists {
        stepLogger.Printf("[GOLDEN] Creating golden volume %s", goldenVol)
        if err := runCmd("docker", "volume", "create", goldenVol); err != nil {
            return fmt.Errorf("failed to create golden volume %s: %w", goldenVol, err)
        }

        // Use a helper container to rsync from original -> golden (first time only)
        helper := fmt.Sprintf("golden_sync_%d", taskID)
        // Best-effort cleanup
        _ = runCmd("docker", "rm", "-f", helper)
        args := []string{
            "run", "-d", "--platform", "linux/amd64",
            "--name", helper,
            "-v", originalVol + ":/original",
            "-v", goldenVol + ":/golden",
            ts.Docker.ImageTag, "tail", "-f", "/dev/null",
        }
        stepLogger.Printf("[GOLDEN] Starting helper container: docker %s", strings.Join(args, " "))
        if err := runCmd("docker", args...); err != nil {
            _ = runCmd("docker", "rm", "-f", helper)
            return fmt.Errorf("failed to start golden helper container: %w", err)
        }
        defer func() { _ = runCmd("docker", "rm", "-f", helper) }()

        // Install rsync and perform sync
        if err := runCmd("docker", "exec", helper, "sh", "-c", "apk add --no-cache rsync || (apt-get update && apt-get install -y rsync)"); err != nil {
            stepLogger.Printf("[GOLDEN] rsync install attempt returned error (may be ok if image already has rsync): %v", err)
        }
        if err := runCmd("docker", "exec", helper, "sh", "-c", "rsync -a --delete-during /original/ /golden/"); err != nil {
            return fmt.Errorf("failed to rsync original->golden: %w", err)
        }
        stepLogger.Printf("[GOLDEN] Synced original -> golden volume")
    } else {
        stepLogger.Printf("[GOLDEN] Reusing existing golden volume %s", goldenVol)
    }

    // Ensure golden container exists and is recorded in containers_map
    goldenName := models.GenerateDVContainerNameForBase(taskID, "golden")
    if ts.ContainersMap != nil {
        if c, ok := ts.ContainersMap["golden"]; ok && c.ContainerName != "" {
            goldenName = c.ContainerName
        }
    }
    running, err := models.CheckContainerExists(goldenName)
    if err != nil {
        return fmt.Errorf("failed to check golden container: %w", err)
    }
    if !running {
        stepLogger.Printf("[GOLDEN] Starting golden container %s", goldenName)
        args := []string{
            "run", "-d", "--platform", "linux/amd64",
            "--name", goldenName,
            "-v", goldenVol + ":" + appFolder,
            ts.Docker.ImageTag, "tail", "-f", "/dev/null",
        }
        if err := runCmd("docker", args...); err != nil {
            return fmt.Errorf("failed to start golden container: %w", err)
        }
    } else {
        stepLogger.Printf("[GOLDEN] Reusing existing golden container %s", goldenName)
    }

    // Persist containers_map.golden if missing
    if ts.ContainersMap == nil {
        ts.ContainersMap = map[string]models.ContainerInfo{}
    }
    if c, ok := ts.ContainersMap["golden"]; !ok || c.ContainerName == "" {
        ts.ContainersMap["golden"] = models.ContainerInfo{ContainerName: goldenName}
        if err := models.UpdateTaskSettings(db, taskID, ts); err != nil {
            stepLogger.Printf("[GOLDEN] Warning: failed to persist containers_map.golden: %v", err)
        }
    }

    return nil
}

// runCmd is a tiny wrapper to run a command and capture combined output into the logger.
func runCmd(name string, args ...string) error {
    cmd := CommandFunc(name, args...)
    out, err := cmd.CombinedOutput()
    if err != nil {
        stepLogger.Printf("[CMD] %s %s failed: %v\n%s", name, strings.Join(args, " "), err, string(out))
        return err
    }
    stepLogger.Printf("[CMD] %s %s\n%s", name, strings.Join(args, " "), string(out))
    return nil
}
