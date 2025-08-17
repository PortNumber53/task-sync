package models

// RubricsImportConfig represents the configuration for a rubrics_import step.
type RubricsImportConfig struct {
    MDFile    string `json:"md_file"`
    JSONFile  string `json:"json_file"`
    DependsOn []Dependency `json:"depends_on,omitempty"`
    Triggers  Triggers     `json:"triggers,omitempty"`
}  // Added Triggers field for file change detection

func (c *RubricsImportConfig) GetImageTag() string      { return "" }
func (c *RubricsImportConfig) GetImageID() string       { return "" }
func (c *RubricsImportConfig) HasImage() bool           { return false }
func (c *RubricsImportConfig) GetDependsOn() []Dependency { return c.DependsOn }
