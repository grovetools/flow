package orchestration

import (
	"strings"
	"time"
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
	JobTypeGenerateRecipe   JobType = "generate-recipe"
	JobTypeFile             JobType = "file" // Non-executable job for storing context/reference content
)

// Job represents a single orchestration job.
type Job struct {
	// From frontmatter
	ID                   string       `yaml:"id" json:"id"`
	Title                string       `yaml:"title" json:"title"`
	Status               JobStatus    `yaml:"status" json:"status"`
	Type                 JobType      `yaml:"type" json:"type"`
	Model                string       `yaml:"model,omitempty" json:"model,omitempty"`
	DependsOn            []string     `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	Include              []string     `yaml:"include,omitempty" json:"include,omitempty"`
	SourceBlock          string       `yaml:"source_block,omitempty" json:"source_block,omitempty"`
	Template             string       `yaml:"template,omitempty" json:"template,omitempty"`
	Repository           string       `yaml:"repository,omitempty" json:"repository,omitempty"`
	Branch               string       `yaml:"branch,omitempty" json:"branch,omitempty"`
	Worktree             string       `yaml:"worktree" json:"worktree,omitempty"`
	TargetAgentContainer string       `yaml:"target_agent_container,omitempty" json:"target_agent_container,omitempty"`
	Inline               InlineConfig `yaml:"inline,omitempty" json:"inline,omitempty"`               // New field: controls which file types are inlined vs uploaded
	PrependDependencies  bool         `yaml:"prepend_dependencies,omitempty" json:"prepend_dependencies,omitempty"` // Deprecated: use inline: [dependencies] instead
	OnCompleteStatus     string       `yaml:"on_complete_status,omitempty" json:"on_complete_status,omitempty"`
	CreatedAt            time.Time     `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	UpdatedAt            time.Time     `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
	CompletedAt          time.Time     `yaml:"completed_at,omitempty" json:"completed_at,omitempty"`
	Duration             time.Duration `yaml:"duration,omitempty" json:"duration,omitempty"`
	Summary              string        `yaml:"summary,omitempty" json:"summary,omitempty"`
	SourcePlan           string       `yaml:"source_plan,omitempty" json:"source_plan,omitempty"`
	RecipeName           string       `yaml:"recipe_name,omitempty" json:"recipe_name,omitempty"`
	GeneratePlanFrom     bool         `yaml:"generate_plan_from,omitempty" json:"generate_plan_from,omitempty"`
	GitChanges           bool         `yaml:"git_changes,omitempty" json:"git_changes,omitempty"`
	GatherConceptNotes   bool         `yaml:"gather_concept_notes,omitempty" json:"gather_concept_notes,omitempty"`
	GatherConceptPlans   bool         `yaml:"gather_concept_plans,omitempty" json:"gather_concept_plans,omitempty"`
	RulesFile            string       `yaml:"rules_file,omitempty" json:"rules_file,omitempty"`
	NoteRef              string       `yaml:"note_ref,omitempty" json:"note_ref,omitempty"`

	// Derived fields
	Filename     string      `json:"filename,omitempty"`     // The markdown filename
	FilePath     string      `json:"file_path,omitempty"`    // Full path to the file
	PromptBody   string      `json:"-"`                       // Content after frontmatter
	Dependencies []*Job      `json:"-"`                       // Resolved job references
	StartTime    time.Time   `json:"start_time,omitempty"`   // When job started
	EndTime      time.Time   `json:"end_time,omitempty"`     // When job completed
	Metadata     JobMetadata `json:"metadata,omitempty"`
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
		} else if (j.Type == JobTypeInteractiveAgent || j.Type == JobTypeAgent) && dep.Type == JobTypeChat && dep.Status == JobStatusPendingUser {
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
		} else if (j.Type == JobTypeInteractiveAgent || j.Type == JobTypeAgent) && dep.Type == JobTypeChat && dep.Status == JobStatusPendingUser {
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