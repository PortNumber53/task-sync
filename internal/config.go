package internal

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Config holds values parsed from ~/.config/task/task.conf
type Config struct {
	LogFile    string
	PassMarker string
	FailMarker string
}

// LoadConfig loads config from ~/.config/task/task.conf (if present)
func LoadConfig() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	confPath := filepath.Join(home, ".config", "task", "task.conf")
	cfg := &Config{}
	file, err := os.Open(confPath)
	if err != nil {
		// File does not exist: return empty config, no error
		return cfg, nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if eq := strings.Index(line, "="); eq > 0 {
			key := strings.TrimSpace(line[:eq])
			val := strings.TrimSpace(line[eq+1:])
			switch key {
			case "LOG_FILE":
				cfg.LogFile = val
			case "PASS_MARKER":
				cfg.PassMarker = val
			case "FAIL_MARKER":
				cfg.FailMarker = val
			}
		}
	}
	return cfg, scanner.Err()
}
