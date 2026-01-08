package orchestration

// PlanConfig holds plan-specific default settings from .grove-plan.yml.
type PlanConfig struct {
	Model                string            `yaml:"model,omitempty"`
	Worktree             string            `yaml:"worktree,omitempty"`
	TargetAgentContainer string            `yaml:"target_agent_container,omitempty"`
	Status               string            `yaml:"status,omitempty"`
	Repos                []string          `yaml:"repos,omitempty"`                // List of repos to include in ecosystem worktree
	Notes                string            `yaml:"notes,omitempty"`                // User notes/description for the plan
	Inline               InlineConfig      `yaml:"inline,omitempty"`               // New field: controls which file types are inlined by default
	PrependDependencies  bool              `yaml:"prepend_dependencies,omitempty"` // Deprecated: use inline instead
	Hooks                map[string]string `yaml:"hooks,omitempty"`
	Recipe               string            `yaml:"recipe,omitempty"` // Recipe used to create this plan
}

// ShouldInline checks if a specific category should be inlined by default for jobs in this plan.
// It first checks the new Inline field, then falls back to PrependDependencies for backwards compatibility.
func (pc *PlanConfig) ShouldInline(category InlineCategory) bool {
	if pc == nil {
		return false
	}
	// Check new inline field first
	for _, v := range pc.Inline.Categories {
		if v == category {
			return true
		}
	}
	// Backwards compat: prepend_dependencies maps to inline: [dependencies]
	if category == InlineDependencies && pc.PrependDependencies {
		return true
	}
	return false
}

// Plan represents a collection of orchestration jobs.
type Plan struct {
	Name          string            // Name of the plan (directory name)
	Directory     string            // Root directory of the plan
	Jobs          []*Job            // List of all jobs
	JobsByID      map[string]*Job   // Keyed by job ID
	SpecFile      string            // Path to spec.md if exists
	Orchestration *Config           // Orchestration configuration
	Context       *ExecutionContext // Execution context for the plan
	Config        *PlanConfig       // Plan-specific configuration from .grove-plan.yml
}

