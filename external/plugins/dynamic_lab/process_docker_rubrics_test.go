package dynamic_lab

import (
	"log"

	"github.com/PortNumber53/task-sync/pkg/models"
)

// testWriter is a mock io.Writer for testing
type testWriter struct{}

func (tw testWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}

func init() {
	models.InitStepLogger(log.Writer())
}
