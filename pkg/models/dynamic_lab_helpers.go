package models

// DynamicLabConfig represents the configuration for a dynamic_lab step.
type DynamicLabConfig struct {
    DynamicLab struct {
        RubricFile  string            `json:"rubric_file"`
        Files       interface{}       `json:"files,omitempty"`
        Hashes      map[string]string `json:"hashes,omitempty"`
        Environment struct {
            Docker bool `json:"docker"`
        } `json:"environment"`
        ImageID       string       `json:"image_id"`
        Command       []string     `json:"command"`
        ContainerID   string       `json:"container_id"`
        ContainerName string       `json:"container_name"`
        DependsOn     []Dependency `json:"depends_on,omitempty"`
    } `json:"dynamic_lab"`
}

func (c *DynamicLabConfig) GetImageTag() string      { return "" }
func (c *DynamicLabConfig) GetImageID() string       { return c.DynamicLab.ImageID }
func (c *DynamicLabConfig) HasImage() bool           { return c.DynamicLab.ImageID != "" }
func (c *DynamicLabConfig) GetDependsOn() []Dependency { return c.DynamicLab.DependsOn }
