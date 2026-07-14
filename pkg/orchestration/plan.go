package orchestration

// PlanConfig holds plan-specific default settings from .grove-plan.yml.
type PlanConfig struct {
	Model               string            `yaml:"model,omitempty"`
	Worktree            string            `yaml:"worktree,omitempty"`
	Status              string            `yaml:"status,omitempty"`
	Repos               []string          `yaml:"repos,omitempty"`                // List of repos to include in ecosystem worktree
	Notes               string            `yaml:"notes,omitempty"`                // User notes/description for the plan
	Inline              InlineConfig      `yaml:"inline,omitempty"`               // New field: controls which file types are inlined by default
	PrependDependencies bool              `yaml:"prepend_dependencies,omitempty"` // Deprecated: use inline instead
	Hooks               map[string]string `yaml:"hooks,omitempty"`
	Recipe              string            `yaml:"recipe,omitempty"` // Recipe used to create this plan
	// Playbook is the name of the playbook this plan is scoped to. Jobs
	// inherit this value unless they declare their own override. Primary
	// source of truth for $PLAYBOOK_ROOT env injection at execution time.
	Playbook string `yaml:"playbook,omitempty" json:"playbook,omitempty"`
	// Satellite designates the grove satellite this plan's remote work runs
	// on (written by `flow plan init --satellite <name>`). When set,
	// `flow plan run` defaults to dispatching jobs to that satellite as if
	// `--at satellite:<name>` had been passed; an explicit `--at` (any
	// target, or the reserved `--at satellite:local`) overrides it. Flow
	// treats the value as an opaque registry name — it is validated against
	// grove's satellite registry only at dispatch time, by the daemon.
	Satellite string `yaml:"satellite,omitempty" json:"satellite,omitempty"`
	// ArchiveAgentTranscripts opts in to copying the RAW per-agent jsonl
	// transcripts (agent-*.jsonl) for both workflow and standalone (Agent-tool)
	// subagents when archiving on job completion. Off by default: raw
	// transcripts can run to many MB per agent. Regardless of this flag, the
	// rendered markdown transcripts (agent-<id>.md) are ALWAYS written, as are
	// the journal, script, and summary; only the raw .jsonl copies are gated.
	ArchiveAgentTranscripts bool `yaml:"archive_agent_transcripts,omitempty"`
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
