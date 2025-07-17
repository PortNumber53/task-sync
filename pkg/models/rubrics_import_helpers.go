package models

// RubricsImportConfig represents the configuration for a rubrics_import step.
type RubricsImportConfig struct {
    MHTMLFile string `json:"mhtml_file"`
    MDFile    string `json:"md_file"`
    JSONFile  string `json:"json_file"`
    DependsOn []Dependency `json:"depends_on,omitempty"`
}  // Added JSONFile field to support JSON rubric imports

func (c *RubricsImportConfig) GetImageTag() string      { return "" }
func (c *RubricsImportConfig) GetImageID() string       { return "" }
func (c *RubricsImportConfig) HasImage() bool           { return false }
func (c *RubricsImportConfig) GetDependsOn() []Dependency { return c.DependsOn }
