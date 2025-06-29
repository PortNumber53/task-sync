package dynamic_lab

import (
	"fmt"
	"os"
	"path/filepath"
)

// Run checks for file changes by comparing current hashes with stored hashes.
func Run(localPath string, files []string, oldHashes map[string]string) (newHashes map[string]string, changed bool, err error) {
	newHashes = make(map[string]string)
	changed = false

	if oldHashes == nil {
		oldHashes = make(map[string]string)
	}

	for _, file := range files {
		filePath := filepath.Join(localPath, file)

		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			return nil, false, fmt.Errorf("file %s does not exist", filePath)
		}

		currentHash, err := calculateHash(filePath)
		if err != nil {
			return nil, false, fmt.Errorf("failed to calculate hash for %s: %w", filePath, err)
		}

		newHashes[file] = currentHash
		if currentHash != oldHashes[file] {
			changed = true
		}
	}

	if len(newHashes) != len(oldHashes) {
		changed = true
	}

	return newHashes, changed, nil
}


