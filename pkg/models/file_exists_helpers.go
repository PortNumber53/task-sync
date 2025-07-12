package models

// FileExistsConfig represents the configuration for a file_exists step.
type FileExistsConfig struct {
    FileExists []string `json:"file_exists"`
}

func (c *FileExistsConfig) GetImageTag() string      { return "" }
func (c *FileExistsConfig) GetImageID() string       { return "" }
func (c *FileExistsConfig) HasImage() bool           { return false }
func (c *FileExistsConfig) GetDependsOn() []Dependency { return nil }
