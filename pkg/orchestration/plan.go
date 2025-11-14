package orchestration

// PlanConfig holds plan-specific default settings from .grove-plan.yml.
type PlanConfig struct {
	Model                string            `yaml:"model,omitempty"`
	Worktree             string            `yaml:"worktree,omitempty"`
	TargetAgentContainer string            `yaml:"target_agent_container,omitempty"`
	Status               string            `yaml:"status,omitempty"`
	Repos                []string          `yaml:"repos,omitempty"`             // List of repos to include in ecosystem worktree
	Notes                string            `yaml:"notes,omitempty"`             // User notes/description for the plan
	PrependDependencies  bool              `yaml:"prepend_dependencies,omitempty"` // Inline dependency content into prompt body by default
	Hooks                map[string]string `yaml:"hooks,omitempty"`
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

