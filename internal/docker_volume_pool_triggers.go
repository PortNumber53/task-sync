package internal

import (
	"log"
	"os/exec"
	"strings"
)

// TriggerCheckResult describes what needs to be rebuilt or rerun
// If RecreateContainers is true, all steps must be rerun (including container/volume creation)
// If RedoFileOps is true, file/patch/git/volume steps must be rerun (but containers are valid)
type TriggerCheckResult struct {
	RecreateContainers bool
	RedoFileOps        bool
}

// CheckArtifactContainersExist returns true if all containers exist (docker ps -a)
func CheckArtifactContainersExist(containerNames []string, logger *log.Logger) bool {
	for _, name := range containerNames {
		cmd := exec.Command("docker", "ps", "-a", "--format", "{{.Names}}")
		output, err := cmd.CombinedOutput()
		if err != nil {
			logger.Printf("Error checking for container %s: %v, output: %s", name, err, string(output))
			return false
		}
		found := false
		for _, line := range splitLines(string(output)) {
			if line == name {
				found = true
				break
			}
		}
		if !found {
			logger.Printf("Container %s not found", name)
			return false
		}
	}
	return true
}

// CheckArtifactVolumesExist returns true if all named volumes exist (docker volume ls)
func CheckArtifactVolumesExist(volumeNames []string, logger *log.Logger) bool {
	cmd := exec.Command("docker", "volume", "ls", "--format", "{{.Name}}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Printf("Error checking volumes: %v, output: %s", err, string(output))
		return false
	}
	lines := splitLines(string(output))
	for _, v := range volumeNames {
		found := false
		for _, line := range lines {
			if line == v {
				found = true
				break
			}
		}
		if !found {
			logger.Printf("Volume %s not found", v)
			return false
		}
	}
	return true
}

func splitLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
