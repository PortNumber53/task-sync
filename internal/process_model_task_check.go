package internal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"path/filepath"
	"strings"
	"os"

	"github.com/PortNumber53/task-sync/pkg/models"
)

func updateModelTaskCheckFileHashes(db *sql.DB, stepID int, basePath string, config *models.ModelTaskCheckConfig, logger *log.Logger) error {
	// Start with existing hashes to preserve them
	existingHashes := make(map[string]string)
	if config.Triggers.Files != nil {
		for k, v := range config.Triggers.Files {
			existingHashes[k] = v
		}
	}

	// Define all files that should be tracked for this step
	filesToHash := []string{
		config.TaskPrompt,
		config.ModelPromptSample,
		config.TaskExplanation,
		config.RubricsJSON,
		config.HeldOutTests,
		"pre_patch.patch", // Standard file that may exist
	}

	// Calculate hashes for all relevant files
	for _, fileName := range filesToHash {
		if fileName == "" {
			continue
		}
		filePath := filepath.Join(basePath, fileName)
		
		// Check if file exists before trying to hash it
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			logger.Printf("File %s does not exist, skipping hash calculation", filePath)
			continue
		} else if err != nil {
			logger.Printf("Error checking file %s: %v", filePath, err)
			continue
		}
		
		hash, err := models.GetSHA256(filePath)
		if err != nil {
			logger.Printf("Error computing hash for %s: %v", filePath, err)
			continue // Skip erroneous files
		}
		existingHashes[fileName] = hash
		logger.Printf("Updated hash for %s: %s", fileName, hash)
	}

	config.Triggers.Files = existingHashes

	newSettings, err := json.Marshal(map[string]interface{}{"model_task_check": config})
	if err != nil {
		return fmt.Errorf("failed to marshal new settings: %w", err)
	}

	if err := models.UpdateStepSettings(db, stepID, string(newSettings)); err != nil {
		return fmt.Errorf("failed to update step settings: %w", err)
	}

	logger.Printf("Updated file hashes for step %d", stepID)
	return nil
}

// ProcessModelTaskCheckStep handles the logic for a 'model_task_check' step.
func ProcessModelTaskCheckStep(db *sql.DB, se *models.StepExec, logger *log.Logger) error {
	var settingsMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(se.Settings), &settingsMap); err != nil {
		return fmt.Errorf("error unmarshaling step settings into map: %w", err)
	}

	var config models.ModelTaskCheckConfig
	if err := json.Unmarshal(settingsMap["model_task_check"], &config); err != nil {
		return fmt.Errorf("error unmarshaling model_task_check settings: %w", err)
	}

	logger.Printf("ModelTaskCheckConfig loaded: %+v", config)
	logger.Printf("Using BasePath: %s", se.BasePath)
	logger.Printf("Processing model_task_check step: %s (ID: %d)", se.Title, se.StepID)

	shouldRun := config.Force // If force is true, we should run regardless of file changes
	if !shouldRun && len(config.Triggers.Files) == 0 {
		logger.Printf("No trigger files found for step %d, running for the first time.", se.StepID)
		shouldRun = true
	} else if !shouldRun { // Only check file hash changes if not already forced to run

		var err error
		shouldRun, err = models.CheckFileHashChanges(se.BasePath, config.Triggers.Files, logger)
		if err != nil {
			return fmt.Errorf("error checking file hash changes: %w", err)
		}
	}

	generatedFilePath := filepath.Join(se.BasePath, config.GeneratedFile)
	_, err := os.Stat(generatedFilePath)
	if os.IsNotExist(err) {
		logger.Printf("Generated file %s does not exist, forcing run.", generatedFilePath)
		shouldRun = true
	} else if err != nil {
		return fmt.Errorf("failed to check for generated file: %w", err)
	}

	if !shouldRun {
		logger.Printf("Skipping model_task_check step %d, no changes detected.", se.StepID)
		return nil
	}

	logger.Printf("Running model_task_check step %d, changes detected.", se.StepID)

	// Read the template file
	logger.Printf("Attempting to read model_prompt_sample: joining BasePath '%s' with ModelPromptSample '%s'", se.BasePath, config.ModelPromptSample)
	samplePath := filepath.Join(se.BasePath, config.ModelPromptSample)
	sampleContent, err := ioutil.ReadFile(samplePath)
	if err != nil {
		return fmt.Errorf("failed to read model_prompt_sample file %s: %w", samplePath, err)
	}

	// Read the replacement files
	logger.Printf("Attempting to read task_prompt: joining BasePath '%s' with TaskPrompt '%s'", se.BasePath, config.TaskPrompt)
	taskPromptPath := filepath.Join(se.BasePath, config.TaskPrompt)
	taskPromptContent, err := ioutil.ReadFile(taskPromptPath)
	if err != nil {
		return fmt.Errorf("failed to read task_prompt file %s: %w", taskPromptPath, err)
	}

	logger.Printf("Attempting to read rubrics_json: joining BasePath '%s' with RubricsJSON '%s'", se.BasePath, config.RubricsJSON)
	rubricsPath := filepath.Join(se.BasePath, config.RubricsJSON)
	rubricsContent, err := ioutil.ReadFile(rubricsPath)
	if err != nil {
		return fmt.Errorf("failed to read rubrics_json file %s: %w", rubricsPath, err)
	}

	logger.Printf("Attempting to read held_out_tests: joining BasePath '%s' with HeldOutTests '%s'", se.BasePath, config.HeldOutTests)
	heldOutTestsPath := filepath.Join(se.BasePath, config.HeldOutTests)
	heldOutTestsContent, err := ioutil.ReadFile(heldOutTestsPath)
	if err != nil {
		return fmt.Errorf("failed to read held_out_tests file %s: %w", heldOutTestsPath, err)
	}

	// Perform replacements
	generatedContent := strings.Replace(string(sampleContent), "{YOUR_TASK_PROMPT}", string(taskPromptContent), -1)
	generatedContent = strings.Replace(generatedContent, "{YOUR_RUBRIC}", string(rubricsContent), -1)
	generatedContent = strings.Replace(generatedContent, "{held_out_test_patch}", string(heldOutTestsContent), -1)

	// Write the generated file
	if err := ioutil.WriteFile(generatedFilePath, []byte(generatedContent), 0644); err != nil {
		return fmt.Errorf("failed to write generated file %s: %w", generatedFilePath, err)
	}

	logger.Printf("Successfully generated file: %s", generatedFilePath)

	// After successful execution, update file hashes
	return updateModelTaskCheckFileHashes(db, se.StepID, se.BasePath, &config, logger)
}
