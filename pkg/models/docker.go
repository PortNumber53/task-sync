package models

import (
	"database/sql"
	"log"
)

// DockerBuildConfig represents the configuration for a docker_build step.
type DockerBuildConfig struct {
	Dockerfile string `json:"dockerfile,omitempty"`
	ImageID    string `json:"image_id,omitempty"`
	ImageTag   string `json:"image_tag,omitempty"`
	DependsOn  []Dependency      `json:"depends_on,omitempty"`
	Triggers   struct {
		Files map[string]string `json:"files,omitempty"`
	} `json:"triggers,omitempty"`
	Parameters []string `json:"parameters,omitempty"`
}

func (c *DockerBuildConfig) GetImageTag() string      { return c.ImageTag }
func (c *DockerBuildConfig) GetImageID() string       { return c.ImageID }
func (c *DockerBuildConfig) HasImage() bool           { return c.ImageTag != "" && c.ImageID != "" }
func (c *DockerBuildConfig) GetDependsOn() []Dependency { return c.DependsOn }

type DockerPullConfig struct {
	ImageID          string       `json:"image_id,omitempty"`
	ImageTag         string       `json:"image_tag,omitempty"`
	DependsOn        []Dependency `json:"depends_on,omitempty"`
	PreventRunBefore string       `json:"prevent_run_before,omitempty"`
}

func (c *DockerPullConfig) GetImageTag() string      { return c.ImageTag }
func (c *DockerPullConfig) GetImageID() string       { return c.ImageID }
func (c *DockerPullConfig) HasImage() bool           { return c.ImageTag != "" && c.ImageID != "" }
func (c *DockerPullConfig) GetDependsOn() []Dependency { return c.DependsOn }

type DockerRunConfig struct {
	ImageID       string       `json:"-"`
	ImageTag      string       `json:"-"`
	DependsOn     []Dependency `json:"depends_on,omitempty"`
	ContainerID   string       `json:"container_id,omitempty"`
	ContainerName string       `json:"container_name,omitempty"`
	Parameters    []string     `json:"parameters,omitempty"`
	KeepForever   bool         `json:"keep_forever,omitempty"`
}

func (c *DockerRunConfig) GetImageTag() string      { return c.ImageTag }
func (c *DockerRunConfig) GetImageID() string       { return c.ImageID }
func (c *DockerRunConfig) HasImage() bool           { return c.ImageTag != "" && c.ImageID != "" }
func (c *DockerRunConfig) GetDependsOn() []Dependency { return c.DependsOn }

// ContainerInfo represents information about a Docker container.
type ContainerInfo struct {
	ContainerID   string `json:"container_id"`
	ContainerName string `json:"container_name"`
	// Add other fields as per project context if known
}

type DockerPoolConfig struct {
	SourceStepID int            `json:"source_step_id,omitempty"`
	ImageID      string         `json:"-"`
	ImageTag     string         `json:"-"`
	DependsOn    []Dependency   `json:"depends_on,omitempty"`
	PoolSize     int            `json:"pool_size,omitempty"`
	Containers   []ContainerInfo `json:"containers,omitempty"`
	Parameters   []string       `json:"parameters,omitempty"`
	KeepForever  bool           `json:"keep_forever,omitempty"`
}

func (c *DockerPoolConfig) GetImageTag() string      { return c.ImageTag }
func (c *DockerPoolConfig) GetImageID() string       { return c.ImageID }
func (c *DockerPoolConfig) HasImage() bool           { return c.ImageTag != "" && c.ImageID != "" }
func (c *DockerPoolConfig) GetDependsOn() []Dependency { return c.DependsOn }

type DockerShellConfig struct {
	Docker struct {
		ImageID  string `json:"-"`
		ImageTag string `json:"-"`
	} `json:"docker,omitempty"`
	DependsOn []Dependency        `json:"depends_on,omitempty"`
	Command   []map[string]string `json:"command,omitempty"`
}

func (c *DockerShellConfig) GetImageTag() string      { return c.Docker.ImageTag }
func (c *DockerShellConfig) GetImageID() string       { return c.Docker.ImageID }
func (c *DockerShellConfig) HasImage() bool           { return c.Docker.ImageTag != "" && c.Docker.ImageID != "" }
func (c *DockerShellConfig) GetDependsOn() []Dependency { return c.DependsOn }

type DockerRubricsConfig struct {
	DockerRubrics struct {
		Files     []string          `json:"files"`
		Hashes    map[string]string `json:"hashes"`
		ImageID   string            `json:"image_id"`
		ImageTag  string            `json:"image_tag"`
		DependsOn []Dependency      `json:"depends_on,omitempty"`
	} `json:"docker_rubrics"`
}

func (c *DockerRubricsConfig) GetImageTag() string      { return c.DockerRubrics.ImageTag }
func (c *DockerRubricsConfig) GetImageID() string       { return c.DockerRubrics.ImageID }
func (c *DockerRubricsConfig) HasImage() bool           { return c.DockerRubrics.ImageTag != "" && c.DockerRubrics.ImageID != "" }
func (c *DockerRubricsConfig) GetDependsOn() []Dependency { return c.DockerRubrics.DependsOn }

type DockerExtractVolumeConfig struct {
	VolumeName string `json:"volume_name"`
	ImageID    string `json:"image_id"`
	AppFolder  string `json:"app_folder"`
	Triggers   struct {
		Files map[string]string `json:"files,omitempty"`
	} `json:"triggers,omitempty"`
	DependsOn []Dependency `json:"depends_on,omitempty"`
	Force     bool        `json:"force,omitempty"`
}

func (c *DockerExtractVolumeConfig) GetImageTag() string { return "" }
func (c *DockerExtractVolumeConfig) GetImageID() string { return c.ImageID }
func (c *DockerExtractVolumeConfig) HasImage() bool { return c.ImageID != "" }
func (c *DockerExtractVolumeConfig) GetDependsOn() []Dependency { return c.DependsOn }

// FindImageDetailsRecursive recursively finds image details for a step.
func FindImageDetailsRecursive(db *sql.DB, stepID int, logger *log.Logger) (string, string, error) {
    // Temporary stub - should be implemented with proper logic
    return "", "", nil
}

// GetDockerImageID retrieves the Docker image ID for a step.
func GetDockerImageID(db *sql.DB, stepID int, logger *log.Logger) (string, string, error) {
    // Temporary stub - should be implemented with proper logic
    return "", "", nil
}
