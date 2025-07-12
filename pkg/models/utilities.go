package models

import (
    "crypto/rand"
    "crypto/sha256"
    "encoding/hex"
    "errors"
    "fmt"
    "io"
    "log"
    "os"
)

// ErrEmptyFile is returned when a file is empty.
var ErrEmptyFile = errors.New("file is empty")

// GetSHA256 calculates the SHA256 hash of a file, returning ErrEmptyFile if it's empty.
func GetSHA256(filePath string) (string, error) {
    file, err := os.Open(filePath)
    if err != nil {
        return "", fmt.Errorf("failed to open file %s: %w", filePath, err)
    }
    defer file.Close()

    info, err := file.Stat()
    if err != nil {
        return "", fmt.Errorf("failed to get file info for %s: %w", filePath, err)
    }

    if info.Size() == 0 {
        return "", ErrEmptyFile
    }

    h := sha256.New()
    if _, err := io.Copy(h, file); err != nil {
        return "", fmt.Errorf("failed to calculate hash for %s: %w", filePath, err)
    }

    return hex.EncodeToString(h.Sum(nil)), nil
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
