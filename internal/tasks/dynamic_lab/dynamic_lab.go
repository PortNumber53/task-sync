package dynamic_lab

import (
	"fmt"
	"os"
	"path/filepath"
)

// Run checks for file changes by comparing current hashes with stored hashes.
func Run(localPath string, files map[string]string) (newHashes map[string]string, changed bool, err error) {
	newHashes = make(map[string]string)
	changed = false

	for file, oldHash := range files {
		filePath := filepath.Join(localPath, file)

		// Check if file exists
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			return nil, false, fmt.Errorf("file %s does not exist", filePath)
		}

		// Calculate current hash
		currentHash, err := calculateHash(filePath)
		if err != nil {
			return nil, false, fmt.Errorf("failed to calculate hash for %s: %w", filePath, err)
		}

		newHashes[file] = currentHash
		if currentHash != oldHash {
			changed = true
		}
	}

	return newHashes, changed, nil
}
