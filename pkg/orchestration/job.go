package orchestration

import (
	"strings"
	"time"

	"github.com/grovetools/core/pkg/models"
)

// InlineCategory represents a category of files that can be inlined.
type InlineCategory string

const (
	InlineDependencies InlineCategory = "dependencies" // Output from upstream jobs in the pipeline
	InlineInclude      InlineCategory = "include"      // Files specified in include: frontmatter
	InlineContext      InlineCategory = "context"      // cx-generated context file (.grove/context)
)

// InlineConfig controls which file types are embedded directly in the prompt vs uploaded as attachments.
// It can be specified as:
// - An array of categories: ["dependencies", "include", "context"]
// - A shorthand string: "none" (default), "all", or a single category like "dependencies"
type InlineConfig struct {
	Categories []InlineCategory
}

// UnmarshalYAML implements custom YAML unmarshaling to support both array and string syntax.
func (ic *InlineConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// First try to unmarshal as a string (shorthand syntax)
	var shorthand string
	if err := unmarshal(&shorthand); err == nil {
		switch strings.ToLower(shorthand) {
		case "none", "":
			ic.Categories = nil
		case "all":
			ic.Categories = []InlineCategory{InlineDependencies, InlineInclude, InlineContext}
		case "files":
			// Shorthand for dependencies + include (excludes context)
			ic.Categories = []InlineCategory{InlineDependencies, InlineInclude}
		default:
			// Single category specified as string
			ic.Categories = []InlineCategory{InlineCategory(shorthand)}
		}
		return nil
	}

	// Otherwise try to unmarshal as an array
	var categories []string
	if err := unmarshal(&categories); err != nil {
		return err
	}
	ic.Categories = make([]InlineCategory, len(categories))
	for i, cat := range categories {
		ic.Categories[i] = InlineCategory(cat)
	}
	return nil
}

// MarshalYAML implements custom YAML marshaling.
func (ic InlineConfig) MarshalYAML() (interface{}, error) {
	if len(ic.Categories) == 0 {
		return nil, nil // Omit empty
	}
	// Convert to string array for output
	categories := make([]string, len(ic.Categories))
	for i, cat := range ic.Categories {
		categories[i] = string(cat)
	}
	return categories, nil
}

// IsEmpty returns true if no categories are configured.
func (ic InlineConfig) IsEmpty() bool {
	return len(ic.Categories) == 0
}

// JobStatus represents the current state of a job.
type JobStatus string

const (
	JobStatusPending     JobStatus = "pending"
	JobStatusRunning     JobStatus = "running"
	JobStatusCompleted   JobStatus = "completed"
	JobStatusFailed      JobStatus = "failed"
	JobStatusBlocked     JobStatus = "blocked"
	JobStatusNeedsReview JobStatus = "needs_review"
	JobStatusPendingUser JobStatus = "pending_user"
	JobStatusPendingLLM  JobStatus = "pending_llm"
	JobStatusHold        JobStatus = "hold"
	JobStatusTodo        JobStatus = "todo"
	JobStatusAbandoned   JobStatus = "abandoned"
	JobStatusIdle        JobStatus = "idle" // Agent finished responding, waiting for next input
)

// JobType represents the type of job execution.
type JobType string

const (
	JobTypeOneshot          JobType = "oneshot"
	JobTypeAgent            JobType = "agent"
	JobTypeHeadlessAgent    JobType = "headless_agent"
	JobTypeShell            JobType = "shell"
	JobTypeChat             JobType = "chat"
	JobTypeInteractiveAgent JobType = "interactive_agent"
	JobTypeIsolatedAgent    JobType = "isolated_agent"
	JobTypeGenerateRecipe   JobType = "generate-recipe"
	JobTypeFile             JobType = "file" // Non-executable job for storing context/reference content
)

// InheritsPlanModel reports whether a plan-level default model should be
// stamped onto a newly created job of this type. Only oneshot/chat jobs have
// flow itself resolve a model and pass it to an LLM, so the plan default
// (typically a gemini-* chat model) belongs to them. Agent jobs
// (interactive/headless/isolated) run a CLI agent that selects its own model;
// the actual model is recorded at launch via the executor backfill. Stamping
// the chat/oneshot default onto an agent job made its `model:` frontmatter
// advertise a model it never ran. This predicate must be applied at EVERY job
// creation site (CLI add, TUI add-job wizard, recipe expansion, plan extract).
func (t JobType) InheritsPlanModel() bool {
	return t == JobTypeOneshot || t == JobTypeChat
}

// Job represents a single orchestration job.
type Job struct {
	// Core fields
	ID            string    `yaml:"id" json:"id" jsonschema:"description=Unique identifier for the job"`
	Title         string    `yaml:"title" json:"title" jsonschema:"description=Human-readable title for the job"`
	Status        JobStatus `yaml:"status" json:"status" jsonschema:"description=Current execution status (pending/running/completed/failed)"`
	Type          JobType   `yaml:"type" json:"type" jsonschema:"description=Job type determining execution behavior (oneshot/chat/interactive_agent/headless_agent/shell/file)"`
	Responder     string    `yaml:"responder,omitempty" json:"responder,omitempty" jsonschema:"enum=oracle,enum=agent,description=Who authors the response turns of a chat job: oracle (default; stateless LLM API call over inlined context) or agent (fresh agent session with file access per turn; never dispatched to an LLM API)"`
	Model         string    `yaml:"model,omitempty" json:"model,omitempty" jsonschema:"description=LLM model to use for this job"`
	Effort        string    `yaml:"effort,omitempty" json:"effort,omitempty" jsonschema:"description=Effort level for claude agent jobs; passed to the claude CLI as --effort (claude owns the accepted levels)"`
	Template      string    `yaml:"template,omitempty" json:"template,omitempty" jsonschema:"description=Template name for generating the job prompt"`
	Skill         string    `yaml:"skill,omitempty" json:"skill,omitempty" jsonschema:"description=Skill name to inject into the agent context (resolved via skills package)"`
	SkillSequence []string  `yaml:"skill_sequence,omitempty" json:"skill_sequence,omitempty" jsonschema:"description=List of skills to execute in sequence"`

	// Playbook is a per-job override for the plan-level playbook setting.
	// Jobs normally inherit the playbook from the parent plan's
	// .grove-plan.yml; setting this field here is an escape hatch for
	// running a job under a different playbook's context (rare).
	Playbook string `yaml:"playbook,omitempty" json:"playbook,omitempty" jsonschema:"description=Per-job playbook override (normally inherited from .grove-plan.yml)"`

	// Dependencies and context
	DependsOn []string `yaml:"depends_on,omitempty" json:"depends_on,omitempty" jsonschema:"description=List of job IDs that must complete before this job runs"`
	Include   []string `yaml:"include,omitempty" json:"include,omitempty" jsonschema:"description=Files or globs to include as context in the job prompt"`
	// PinnedContext lists files placed in a stable, cacheable region of an
	// Anthropic chat request — after the cold/CLAUDE.md stable half and before
	// the per-turn volatile context — each with its own cache_control breakpoint.
	// Unlike Include (volatile half, re-cached every turn), pinned files added
	// mid-chat preserve the already-cached prefix on the turn they appear and
	// cache durably thereafter. Append new entries to the END of the list; order
	// is persisted so prior members keep their positions (see the pinned-context
	// order artifact). Anthropic (claude) oracle chats only: on gemini models the
	// files are folded into the volatile upload with a warning; agents read raw
	// files so it is a no-op there. Files are uploaded raw (no comment stripping).
	PinnedContext []string `yaml:"pinned_context,omitempty" json:"pinned_context,omitempty" jsonschema:"description=Files placed in a stable cached region of Anthropic chat requests with a dedicated cache_control breakpoint; append new entries to the end. Anthropic oracle-chat only"`
	SourceBlock   string   `yaml:"source_block,omitempty" json:"source_block,omitempty" jsonschema:"description=Reference to a named block in another job to use as input"`
	SourceFile    string   `yaml:"source_file,omitempty" json:"source_file,omitempty" jsonschema:"description=Path to source file for context"`
	Memory        *bool    `yaml:"memory,omitempty" json:"memory,omitempty" jsonschema:"description=Whether to inject related memories into the prompt (default: true)"`
	// StripComments removes code comments from the generated repository
	// context before it is sent to the LLM, giving a comment-free view of the
	// code. Enabled by default; set to false to keep comments. Uses a *bool so
	// "unset" defaults to true (see IsStripCommentsEnabled).
	StripComments *bool `yaml:"strip_comments,omitempty" json:"strip_comments,omitempty" jsonschema:"description=Strip code comments from generated context before sending to the LLM (default: true). Oracle-only concern: a no-op on responder: agent chats (agents read raw files)"`
	// NoCache disables provider prompt caching for this job's LLM calls.
	// Caching is on by default: the first request pays a ~1.25x write premium
	// on the context documents and any repeat within the cache TTL (retries,
	// later chat turns, sibling jobs with identical context) reads them at
	// ~0.1x. Set no_cache: true only for large-context jobs known to run
	// exactly once, where the write premium buys nothing. Oracle-only concern.
	NoCache bool `yaml:"no_cache,omitempty" json:"no_cache,omitempty" jsonschema:"description=Disable provider prompt caching for this job's LLM calls (default: caching enabled). Only worthwhile for large-context jobs that run exactly once"`

	// Worktree configuration
	Repository string `yaml:"repository,omitempty" json:"repository,omitempty" jsonschema:"description=Git repository URL for worktree creation"`
	Branch     string `yaml:"branch,omitempty" json:"branch,omitempty" jsonschema:"description=Git branch name for worktree"`
	Worktree   string `yaml:"worktree,omitempty" json:"worktree,omitempty" jsonschema:"description=Worktree name for isolated execution"`

	// Inlining configuration
	Inline              InlineConfig `yaml:"inline,omitempty" json:"inline,omitempty" jsonschema:"description=Controls which content types are inlined vs uploaded. Oracle-only concern: responder: agent chats read dependencies from disk and ignore inlining"`
	PrependDependencies bool         `yaml:"prepend_dependencies,omitempty" json:"prepend_dependencies,omitempty" jsonschema:"description=DEPRECATED: Use inline: [dependencies] instead"`

	// Lifecycle hooks
	OnCompleteStatus string `yaml:"on_complete_status,omitempty" json:"on_complete_status,omitempty" jsonschema:"description=Status to set on dependent jobs when this job completes"`
	NotifyOnComplete string `yaml:"notify_on_complete,omitempty" json:"notify_on_complete,omitempty" jsonschema:"description=Channel to notify when job reaches terminal state"`
	AutoComplete     bool   `yaml:"auto_complete,omitempty" json:"auto_complete,omitempty" jsonschema:"description=For chat jobs: transition to completed instead of pending_user (bypassing review gate)"`
	RetryTransient   int    `yaml:"retry_transient,omitempty" json:"retry_transient,omitempty" jsonschema:"description=Number of retries for transient failures (default 1)"`

	// Timestamps (auto-managed)
	CreatedAt   time.Time     `yaml:"created_at,omitempty" json:"created_at,omitempty" jsonschema:"description=When the job was created"`
	UpdatedAt   time.Time     `yaml:"updated_at,omitempty" json:"updated_at,omitempty" jsonschema:"description=When the job was last modified"`
	CompletedAt time.Time     `yaml:"completed_at,omitempty" json:"completed_at,omitempty" jsonschema:"description=When the job completed execution"`
	Duration    time.Duration `yaml:"duration,omitempty" json:"duration,omitempty" jsonschema:"description=Total execution time"`

	// Recipe and plan generation
	SourcePlan       string `yaml:"source_plan,omitempty" json:"source_plan,omitempty" jsonschema:"description=Reference to the plan this job was generated from"`
	RecipeName       string `yaml:"recipe_name,omitempty" json:"recipe_name,omitempty" jsonschema:"description=Name of the recipe used to create this job"`
	GeneratePlanFrom bool   `yaml:"generate_plan_from,omitempty" json:"generate_plan_from,omitempty" jsonschema:"description=Generate a new plan from this job's output"`

	// Context gathering
	GitChanges         bool   `yaml:"git_changes,omitempty" json:"git_changes,omitempty" jsonschema:"description=Include git diff/status in job context"`
	GatherConceptNotes bool   `yaml:"gather_concept_notes,omitempty" json:"gather_concept_notes,omitempty" jsonschema:"description=Include related concept notes in context"`
	GatherConceptPlans bool   `yaml:"gather_concept_plans,omitempty" json:"gather_concept_plans,omitempty" jsonschema:"description=Include related concept plans in context"`
	RulesFile          string `yaml:"rules_file,omitempty" json:"rules_file,omitempty" jsonschema:"description=Path to rules file for agent behavior"`
	UsedRulesFile      string `yaml:"used_rules_file,omitempty" json:"used_rules_file,omitempty" jsonschema:"description=Archived rules file used during last execution"`
	NoteRef            string `yaml:"note_ref,omitempty" json:"note_ref,omitempty" jsonschema:"description=Reference to a notebook entry for context"`

	// Channel & Autonomous support (for interactive_agent jobs)
	Channels     []string                 `yaml:"channels,omitempty" json:"channels,omitempty" jsonschema:"description=External channels to enable (e.g. signal)"`
	SignalTarget string                   `yaml:"signal_target,omitempty" json:"signal_target,omitempty" jsonschema:"description=Named signal target (contact or group) for outbound messages"`
	Autonomous   *models.AutonomousConfig `yaml:"autonomous,omitempty" json:"autonomous,omitempty" jsonschema:"description=Autonomous idle pinger configuration"`

	// Skill fidelity tracking (populated post-execution from status.json files)
	SkillFidelity []SkillFidelityState `yaml:"skill_fidelity,omitempty" json:"skill_fidelity,omitempty" jsonschema:"description=Skill sequence execution fidelity records"`

	// Derived fields (excluded from schema and YAML serialization - these are runtime/internal fields)
	Filename     string      `yaml:"-" json:"filename,omitempty" jsonschema:"-"`
	FilePath     string      `yaml:"-" json:"file_path,omitempty" jsonschema:"-"`
	PromptBody   string      `yaml:"-" json:"-" jsonschema:"-"`
	Dependencies []*Job      `yaml:"-" json:"-" jsonschema:"-"`
	StartTime    time.Time   `yaml:"-" json:"start_time,omitempty" jsonschema:"-"`
	EndTime      time.Time   `yaml:"-" json:"end_time,omitempty" jsonschema:"-"`
	Metadata     JobMetadata `yaml:"-" json:"metadata,omitempty" jsonschema:"-"`
}

// JobMetadata holds additional job metadata.
type JobMetadata struct {
	ExecutionTime time.Duration `yaml:"execution_time"`
	RetryCount    int           `yaml:"retry_count"`
	LastError     string        `yaml:"last_error"`
}

// JobOptions contains options for creating a new job.
type JobOptions struct {
	DependsOn           []string
	Include             []string
	Worktree            string
	Prompt              string
	Inline              InlineConfig // New field: controls which file types are inlined
	PrependDependencies bool         // Deprecated: use Inline instead
}

// IsMemoryEnabled determines whether this job should have related memories injected
// into its prompt. Memory injection is enabled by default unless explicitly opted-out.
func (j *Job) IsMemoryEnabled() bool {
	return j.Memory == nil || *j.Memory
}

// IsAgentResponded reports whether this chat job's response turns are authored
// directly into the artifact by a fresh agent session per turn rather than
// produced by dispatching to an LLM API. Agent-responded chats (responder:
// agent) must NEVER be sent to an LLM provider and never inherit the plan's
// default chat model. An absent responder field and responder: oracle are
// equivalent: today's oracle behavior (stateless API model over inlined,
// comment-stripped context).
func (j *Job) IsAgentResponded() bool {
	return j.Type == JobTypeChat && j.Responder == "agent"
}

// IsStripCommentsEnabled reports whether code comments should be stripped from
// this job's generated repository context. Enabled by default unless the job
// explicitly sets strip_comments: false.
func (j *Job) IsStripCommentsEnabled() bool {
	return j.StripComments == nil || *j.StripComments
}

// ShouldInline checks if a specific category should be inlined in the prompt.
// It first checks the new Inline field, then falls back to PrependDependencies for backwards compatibility.
func (j *Job) ShouldInline(category InlineCategory) bool {
	// Check new inline field first
	for _, v := range j.Inline.Categories {
		if v == category {
			return true
		}
	}
	// Backwards compat: prepend_dependencies maps to inline: [dependencies]
	if category == InlineDependencies && j.PrependDependencies {
		return true
	}
	return false
}

// IsRunnable checks if a job can be executed.
func (j *Job) IsRunnable() bool {
	// File jobs are never runnable - they're just for context/reference
	if j.Type == JobTypeFile {
		return false
	}

	// A job is runnable if its own status is valid for starting...
	isReadyToStart := (j.Status == JobStatusPending) ||
		(j.Type == JobTypeChat && j.Status == JobStatusPendingUser)

	if !isReadyToStart {
		return false
	}

	// ...and all of its dependencies are met.
	for _, dep := range j.Dependencies {
		if dep == nil { // A missing/unresolved dependency is not met.
			return false
		}

		dependencyMet := false
		if dep.Status == JobStatusCompleted || dep.Status == JobStatusAbandoned {
			dependencyMet = true
		} else if (j.Type == JobTypeInteractiveAgent || j.Type == JobTypeIsolatedAgent || j.Type == JobTypeAgent) && dep.Type == JobTypeChat && dep.Status == JobStatusPendingUser {
			// Special case: an interactive agent can run if its chat dependency is pending user input.
			dependencyMet = true
		}

		if !dependencyMet {
			return false
		}
	}

	return true
}

// CanBeRetried checks if a failed job can be manually retried.
// This is used when a user explicitly targets a failed job for re-execution.
func (j *Job) CanBeRetried() bool {
	// Only failed jobs can be retried
	if j.Status != JobStatusFailed {
		return false
	}

	// Check if all dependencies are met
	for _, dep := range j.Dependencies {
		if dep == nil {
			return false
		}

		dependencyMet := false
		if dep.Status == JobStatusCompleted || dep.Status == JobStatusAbandoned {
			dependencyMet = true
		} else if (j.Type == JobTypeInteractiveAgent || j.Type == JobTypeIsolatedAgent || j.Type == JobTypeAgent) && dep.Type == JobTypeChat && dep.Status == JobStatusPendingUser {
			// Special case: an interactive agent can run if its chat dependency is pending user input.
			dependencyMet = true
		}

		if !dependencyMet {
			return false
		}
	}

	return true
}

// UpdateStatus updates the job status using the state persister.
func (j *Job) UpdateStatus(sp *StatePersister, newStatus JobStatus) error {
	return sp.UpdateJobStatus(j, newStatus)
}

// AppendOutput appends output to the job file.
func (j *Job) AppendOutput(sp *StatePersister, output string) error {
	return sp.AppendJobOutput(j, output)
}
