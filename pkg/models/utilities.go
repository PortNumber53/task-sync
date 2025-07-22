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
