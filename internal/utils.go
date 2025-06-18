package internal

import (
	"crypto/rand"
	"encoding/hex"
)

// GenerateRandomString generates a random hex string of the specified byte length.
// The resulting string will be twice the byte length.
func GenerateRandomString(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
