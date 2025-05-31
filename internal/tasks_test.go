package internal

import (
	"testing"
)

func TestIsValidTaskStatus(t *testing.T) {
	valid := []string{"active", "inactive", "disabled", "running"}
	for _, status := range valid {
		if !isValidTaskStatus(status) {
			t.Errorf("expected status %q to be valid", status)
		}
	}
	invalid := []string{"foo", "bar", "done", ""}
	for _, status := range invalid {
		if isValidTaskStatus(status) {
			t.Errorf("expected status %q to be invalid", status)
		}
	}
}
