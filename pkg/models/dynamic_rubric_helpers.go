package models

// DynamicRubricConfig represents the configuration for a dynamic_rubric step.
type DynamicRubricConfig struct {
    DynamicRubric struct {
        Files       map[string]string `json:"files,omitempty"`
        Rubrics     []string          `json:"rubrics,omitempty"`
        Hash        string            `json:"hash,omitempty"`
        Environment struct {
            Docker   bool   `json:"docker"`
            ImageID  string `json:"image_id,omitempty"`
            ImageTag string `json:"image_tag,omitempty"`
        } `json:"environment"`
        DependsOn  []Dependency `json:"depends_on,omitempty"`
    } `json:"dynamic_rubric"`
}

func (c *DynamicRubricConfig) GetImageTag() string      { return c.DynamicRubric.Environment.ImageTag }
func (c *DynamicRubricConfig) GetImageID() string       { return c.DynamicRubric.Environment.ImageID }
func (c *DynamicRubricConfig) HasImage() bool           { return c.DynamicRubric.Environment.ImageID != "" && c.DynamicRubric.Environment.ImageTag != "" }
func (c *DynamicRubricConfig) GetDependsOn() []Dependency { return c.DynamicRubric.DependsOn }
