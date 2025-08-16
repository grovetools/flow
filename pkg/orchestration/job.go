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
)

// JobType represents the type of job execution.
type JobType string

const (
	JobTypeOneshot          JobType = "oneshot"
	JobTypeAgent            JobType = "agent"
	JobTypeShell            JobType = "shell"
	JobTypeChat             JobType = "chat"
	JobTypeInteractiveAgent JobType = "interactive_agent"
)

// OutputConfig defines how job output should be handled.
type OutputConfig struct {
	Type    string       `yaml:"type"`    // "file", "commit", "none"
	Message string       `yaml:"message"` // For commit type
	Path    string       `yaml:"path"`    // For file type
	Commit  CommitConfig `yaml:"commit"`  // For commit type
}

// CommitConfig defines commit creation settings.
type CommitConfig struct {
	Enabled bool   `yaml:"enabled"`
	Message string `yaml:"message"`
}

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
	Template             string       `yaml:"template,omitempty" json:"template,omitempty"`
	Worktree             string       `yaml:"worktree" json:"worktree,omitempty"`
	TargetAgentContainer string       `yaml:"target_agent_container,omitempty" json:"target_agent_container,omitempty"`
	Output               OutputConfig `yaml:"output" json:"output,omitempty"`
	OnCompleteStatus     string       `yaml:"on_complete_status,omitempty" json:"on_complete_status,omitempty"`
	CreatedAt            time.Time    `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	UpdatedAt            time.Time    `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`

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
	DependsOn    []string
	PromptSource []string
	Worktree     string
	OutputType   string
	Prompt       string
}

// IsRunnable checks if a job can be executed.
func (j *Job) IsRunnable() bool {
	// Chat jobs have special status handling
	if j.Type == JobTypeChat {
		// Chat jobs are runnable when pending or pending_user (waiting for user input)
		// pending_llm means waiting for LLM response, which happens during execution
		if j.Status != JobStatusPending && j.Status != JobStatusPendingUser {
			return false
		}
	} else {
		// Non-chat jobs must be pending
		if j.Status != JobStatusPending {
			return false
		}
	}

	// All dependencies must be completed
	for _, dep := range j.Dependencies {
		if dep.Status != JobStatusCompleted {
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