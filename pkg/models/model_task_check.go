package models

// ModelTaskCheckConfig defines the structure for the 'model_task_check' step.
// This step is responsible for generating a task prompt by combining various source files.
type ModelTaskCheckConfig struct {
	DependsOn         []Dependency      `json:"depends_on"`
	TaskPrompt        string            `json:"task_prompt"`
	ModelPromptSample string            `json:"model_prompt_sample"`
	TaskExplanation   string            `json:"task_explanation"`
	GeneratedFile     string            `json:"generated_file"`
	RubricsJSON       string            `json:"rubrics_json"`
	HeldOutTests      string            `json:"held_out_tests"`
	Triggers          Triggers          `json:"triggers"`
}

func (c *ModelTaskCheckConfig) GetImageTag() string      { return "" }
func (c *ModelTaskCheckConfig) GetImageID() string       { return "" }
func (c *ModelTaskCheckConfig) HasImage() bool           { return false }
func (c *ModelTaskCheckConfig) GetDependsOn() []Dependency { return c.DependsOn }
