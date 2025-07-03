package dynamic_lab

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// fileSystem is an interface for file system operations
type fileSystem interface {
	Run(localPath string, files []string, oldHashes map[string]string) (map[string]string, bool, error)
}

// rubricParser is an interface for parsing rubrics
type rubricParser interface {
	RunRubric(localPath, file, hash string) ([]models.Criterion, string, bool, error)
}

// defaultFileSystem is the default implementation of fileSystem
type defaultFileSystem struct{}

// defaultRubricParser is the default implementation of rubricParser
type defaultRubricParser struct{}

// Run checks for file changes by comparing current hashes with stored hashes.
func (d *defaultFileSystem) Run(localPath string, files []string, oldHashes map[string]string) (map[string]string, bool, error) {
	newHashes := make(map[string]string)
	changed := false

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

// RunRubric runs the rubric parser on the given file
func (d *defaultRubricParser) RunRubric(localPath, file, hash string) ([]models.Criterion, string, bool, error) {
	return models.RunRubric(localPath, file, hash)
}

// Default implementations
var (
	fileSystemImpl   fileSystem   = &defaultFileSystem{}
	rubricParserImpl rubricParser = &defaultRubricParser{}
)

// Run is a convenience function that uses the default file system implementation
func Run(localPath string, files []string, oldHashes map[string]string) (map[string]string, bool, error) {
	return fileSystemImpl.Run(localPath, files, oldHashes)
}

// RunRubric is a convenience function that uses the default rubric parser implementation
func RunRubric(localPath, file, hash string) ([]models.Criterion, string, bool, error) {
	return rubricParserImpl.RunRubric(localPath, file, hash)
}
