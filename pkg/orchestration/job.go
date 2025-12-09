package orchestration

import "time"

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
	PromptSource         []string     `yaml:"prompt_source,omitempty" json:"prompt_source,omitempty"`
	SourceBlock          string       `yaml:"source_block,omitempty" json:"source_block,omitempty"`
	Template             string       `yaml:"template,omitempty" json:"template,omitempty"`
	Repository           string       `yaml:"repository,omitempty" json:"repository,omitempty"`
	Branch               string       `yaml:"branch,omitempty" json:"branch,omitempty"`
	Worktree             string       `yaml:"worktree" json:"worktree,omitempty"`
	TargetAgentContainer string       `yaml:"target_agent_container,omitempty" json:"target_agent_container,omitempty"`
	AgentContinue        bool         `yaml:"agent_continue,omitempty" json:"agent_continue,omitempty"`
	PrependDependencies  bool         `yaml:"prepend_dependencies,omitempty" json:"prepend_dependencies,omitempty"`
	OnCompleteStatus     string       `yaml:"on_complete_status,omitempty" json:"on_complete_status,omitempty"`
	CreatedAt            time.Time    `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	UpdatedAt            time.Time    `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
	Summary              string       `yaml:"summary,omitempty" json:"summary,omitempty"`
	SourcePlan           string       `yaml:"source_plan,omitempty" json:"source_plan,omitempty"`
	RecipeName           string       `yaml:"recipe_name,omitempty" json:"recipe_name,omitempty"`
	GeneratePlanFrom     bool         `yaml:"generate_plan_from,omitempty" json:"generate_plan_from,omitempty"`
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
	PromptSource        []string
	Worktree            string
	Prompt              string
	PrependDependencies bool
}

// IsRunnable checks if a job can be executed.
func (j *Job) IsRunnable() bool {
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
		if dep.Status == JobStatusCompleted {
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