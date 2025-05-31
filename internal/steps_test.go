package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStepFileExistsLogic(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "Dockerfile")
	if err := os.WriteFile(testFile, []byte("FROM busybox"), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	tests := []struct {
		name        string
		localPath   string
		settings    map[string]interface{}
		filePresent bool
		wantResult  string
	}{
		{
			name:        "file exists",
			localPath:   tempDir,
			settings:    map[string]interface{}{ "file_exists": "Dockerfile" },
			filePresent: true,
			wantResult:  "success",
		},
		{
			name:        "file missing",
			localPath:   tempDir,
			settings:    map[string]interface{}{ "file_exists": "notfound.txt" },
			filePresent: false,
			wantResult:  "failure",
		},
		{
			name:        "invalid settings",
			localPath:   tempDir,
			settings:    map[string]interface{}{}, // no file_exists key
			filePresent: false,
			wantResult:  "", // should not update result
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate the step execution logic
			settingsJson, _ := json.Marshal(tc.settings)
			var settings map[string]interface{}
			_ = json.Unmarshal(settingsJson, &settings)

			filePath, ok := settings["file_exists"].(string)
			if ok {
				absPath := filepath.Join(tc.localPath, filePath)
				_, err := os.Stat(absPath)
				if tc.wantResult == "success" && err != nil {
					t.Errorf("expected file to exist, got error: %v", err)
				}
				if tc.wantResult == "failure" && err == nil {
					t.Errorf("expected file to be missing, but it exists")
				}
			} else {
				if tc.wantResult != "" {
					t.Errorf("expected file_exists logic to run, but it did not")
				}
			}
		})
	}
}
