package orchestration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	grovelogging "github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-gemini/pkg/gemini"
	"github.com/sirupsen/logrus"
)

// Logger defines the logging interface.
type Logger interface {
	Info(msg string, keysAndValues ...interface{})
	Error(msg string, keysAndValues ...interface{})
	Debug(msg string, keysAndValues ...interface{})
}

// OrchestratorConfig holds configuration for the orchestrator.
type OrchestratorConfig struct {
	MaxParallelJobs     int
	CheckInterval       time.Duration
	StateFile           string
	ModelOverride       string // Override model for all jobs
	MaxConsecutiveSteps int    // Maximum consecutive steps before halting
	SkipInteractive     bool   // Skip interactive agent jobs
	SummaryConfig       *SummaryConfig // Configuration for job summarization
}

// Orchestrator coordinates job execution and manages state.
type Orchestrator struct {
	Plan            *Plan
	executors       map[JobType]Executor
	dependencyGraph *DependencyGraph
	config          *OrchestratorConfig
	logger          Logger
	stateManager    *StateManager
	mu              sync.Mutex
}

// PlanStatus provides comprehensive status information.
type PlanStatus struct {
	Total      int
	Pending    int
	Running    int
	Completed  int
	Failed     int
	Blocked    int
	Progress   float64
}

// NewOrchestrator creates a new orchestrator instance.
func NewOrchestrator(plan *Plan, config *OrchestratorConfig) (*Orchestrator, error) {
	if config == nil {
		config = &OrchestratorConfig{
			MaxParallelJobs: 3,
			CheckInterval:   5 * time.Second,
			StateFile:       "orchestrator.state",
		}
	}

	// Build dependency graph
	graph, err := BuildDependencyGraph(plan)
	if err != nil {
		return nil, fmt.Errorf("build dependency graph: %w", err)
	}

	// Create state manager
	stateManager := NewStateManager(plan.Directory)

	orch := &Orchestrator{
		Plan:            plan,
		executors:       make(map[JobType]Executor),
		dependencyGraph: graph,
		config:          config,
		logger:          NewDefaultLogger(),
		stateManager:    stateManager,
	}

	// Register executors
	orch.registerExecutors()

	// Validate prerequisites
	if err := orch.ValidatePrerequisites(); err != nil {
		return nil, fmt.Errorf("validate prerequisites: %w", err)
	}

	return orch, nil
}

// ValidatePrerequisites ensures all requirements are met before running jobs.
func (o *Orchestrator) ValidatePrerequisites() error {
	// Agent jobs now run directly on the host without Docker dependencies
	return nil
}

// registerExecutors sets up the available executors.
func (o *Orchestrator) registerExecutors() {
	// Create shared config for executors
	execConfig := &ExecutorConfig{
		MaxPromptLength: 1000000,
		Timeout:         30 * time.Minute,
		RetryCount:      2,
		Model:           "default",
		ModelOverride:   o.config.ModelOverride,
		SkipInteractive: o.config.SkipInteractive,
	}

	// Create shared LLM clients for executors
	var llmClient LLMClient
	if os.Getenv("GROVE_MOCK_LLM_RESPONSE_FILE") != "" {
		llmClient = NewMockLLMClient()
	} else {
		llmClient = NewCommandLLMClient()
	}
	geminiRunner := gemini.NewRequestRunner()

	// Register oneshot executor (also handles chat jobs)
	oneshotExecutor := NewOneShotExecutor(llmClient, execConfig)
	o.executors[JobTypeOneshot] = oneshotExecutor
	o.executors[JobTypeChat] = oneshotExecutor

	// Register headless agent executor
	o.executors[JobTypeHeadlessAgent] = NewHeadlessAgentExecutor(llmClient, execConfig)

	// Register interactive agent executor for both agent and interactive_agent types
	interactiveExecutor := NewInteractiveAgentExecutor(llmClient, geminiRunner, o.config.SkipInteractive)
	o.executors[JobTypeAgent] = interactiveExecutor
	o.executors[JobTypeInteractiveAgent] = interactiveExecutor

	// Register shell executor
	o.executors[JobTypeShell] = NewShellExecutor()

	// Register generate-recipe executor
	o.executors[JobTypeGenerateRecipe] = NewGenerateRecipeExecutor(execConfig)
}

// RunJob executes a specific job by filename.
func (o *Orchestrator) RunJob(ctx context.Context, jobFile string) error {
	// Find job by filename
	var job *Job
	for _, j := range o.Plan.Jobs {
		if j.FilePath == jobFile {
			job = j
			break
		}
	}

	if job == nil {
		return fmt.Errorf("job not found: %s", jobFile)
	}

	// Check if job is already completed
	if job.Status == JobStatusCompleted {
		return fmt.Errorf("job already completed: %s", jobFile)
	}

	// Validate job is runnable or can be retried
	runnable := o.dependencyGraph.GetRunnableJobs()
	isRunnable := false
	for _, r := range runnable {
		if r.ID == job.ID {
			isRunnable = true
			break
		}
	}

	// If not in the runnable list, check if it's a failed job that can be retried
	if !isRunnable {
		if job.CanBeRetried() {
			isRunnable = true
		} else {
			return fmt.Errorf("job %s is not runnable (dependencies not met or in wrong status)", job.ID)
		}
	}

	// Execute job
	return o.executeJob(ctx, job)
}

// RunNext executes all currently runnable jobs.
func (o *Orchestrator) RunNext(ctx context.Context) error {
	// Get all runnable jobs
	runnable := o.dependencyGraph.GetRunnableJobs()
	if len(runnable) == 0 {
		return fmt.Errorf("no runnable jobs found")
	}

	// Limit to max parallel jobs
	if len(runnable) > o.config.MaxParallelJobs {
		runnable = runnable[:o.config.MaxParallelJobs]
	}

	// Run jobs concurrently
	return o.runJobsConcurrently(ctx, runnable)
}

// RunAll executes all jobs in the plan.
func (o *Orchestrator) RunAll(ctx context.Context) error {
	o.logger.Info("Starting orchestration", "plan", o.Plan.Name)

	stepCount := 0
	limit := o.config.MaxConsecutiveSteps
	if limit <= 0 {
		limit = 20 // Default if not configured
	}

	for {
		// Check if we're done
		status := o.GetStatus()
		if status.Pending == 0 && status.Running == 0 {
			if status.Failed > 0 {
				return fmt.Errorf("orchestration completed with %d failed jobs", status.Failed)
			}
			o.logger.Info("Orchestration completed successfully", 
				"total", status.Total,
				"completed", status.Completed)
			return nil
		}

		// Reload job statuses from disk to detect external changes
		// This allows 'flow plan complete' to work while orchestrator is running
		if err := o.reloadJobStatusesFromDisk(); err != nil {
			o.logger.Error("Failed to reload job statuses", "error", err)
		}
		
		// Get runnable jobs
		runnable := o.dependencyGraph.GetRunnableJobs()
		
		if len(runnable) == 0 {
			if status.Running > 0 {
				// Wait for running jobs to complete
				o.logger.Debug("No runnable jobs, waiting for running jobs to complete",
					"running", status.Running)
				time.Sleep(o.config.CheckInterval)
				continue
			} else {
				// No running jobs and no runnable jobs - we're blocked
				return fmt.Errorf("no runnable jobs and no jobs running - possible circular dependency or all remaining jobs depend on failed jobs")
			}
		}

		// Limit to max parallel jobs
		if len(runnable) > o.config.MaxParallelJobs {
			runnable = runnable[:o.config.MaxParallelJobs]
		}

		// Run jobs
		if err := o.runJobsConcurrently(ctx, runnable); err != nil {
			o.logger.Error("Error running jobs", "error", err)
			// Continue to allow other jobs to run
		}

		// Increment step counter and check limit
		stepCount++
		if stepCount >= limit {
			return fmt.Errorf("execution halted: maximum consecutive step limit (%d) reached. This is a safeguard against potential infinite loops", limit)
		}

		// Small delay before next iteration
		time.Sleep(1 * time.Second)
	}
}

// reloadJobStatusesFromDisk reloads job statuses from their files
// This allows the orchestrator to detect external changes (e.g., from 'flow plan complete')
func (o *Orchestrator) reloadJobStatusesFromDisk() error {
	o.mu.Lock()
	defer o.mu.Unlock()

	for _, job := range o.Plan.Jobs {
		// Load the job file to get the current status
		diskJob, err := LoadJob(job.FilePath)
		if err != nil {
			o.logger.Error("Failed to reload job from disk", "job", job.ID, "error", err)
			continue // Skip this job but continue with others
		}
		
		// Update status if it changed externally
		if diskJob.Status != job.Status {
			o.logger.Info("Job status changed externally", 
				"job", job.ID, 
				"old_status", job.Status,
				"new_status", diskJob.Status)
			
			// Update in-memory status
			job.Status = diskJob.Status
			job.StartTime = diskJob.StartTime
			job.EndTime = diskJob.EndTime
			
			// Update dependency graph
			o.dependencyGraph.UpdateJobStatus(job.ID, job.Status)
		}
	}
	
	return nil
}

// GetStatus returns the current plan status.
func (o *Orchestrator) GetStatus() *PlanStatus {
	o.mu.Lock()
	defer o.mu.Unlock()

	status := &PlanStatus{
		Total: len(o.Plan.Jobs),
	}

	for _, job := range o.Plan.Jobs {
		switch job.Status {
		case JobStatusPending, JobStatusPendingUser, JobStatusPendingLLM:
			status.Pending++
		case JobStatusRunning:
			status.Running++
		case JobStatusCompleted:
			status.Completed++
		case JobStatusFailed:
			status.Failed++
		}
	}

	// Calculate blocked jobs (pending but not runnable)
	runnable := o.dependencyGraph.GetRunnableJobs()
	status.Blocked = status.Pending - len(runnable)

	// Calculate progress
	if status.Total > 0 {
		status.Progress = float64(status.Completed) / float64(status.Total) * 100
	}

	return status
}

// runJobsConcurrently executes multiple jobs in parallel.
func (o *Orchestrator) runJobsConcurrently(ctx context.Context, jobs []*Job) error {
	var wg sync.WaitGroup
	errChan := make(chan error, len(jobs))
	
	sem := make(chan struct{}, o.config.MaxParallelJobs)
	
	for _, job := range jobs {
		wg.Add(1)
		go func(j *Job) {
			defer wg.Done()
			
			sem <- struct{}{}
			defer func() { <-sem }()
			
			if err := o.executeJob(ctx, j); err != nil {
				errChan <- fmt.Errorf("job %s: %w", j.ID, err)
			}
		}(job)
	}
	
	wg.Wait()
	close(errChan)
	
	// Collect errors
	var errs []error
	for err := range errChan {
		errs = append(errs, err)
	}
	
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	
	return nil
}

// logFieldsToKeyVals converts a map to alternating key-value pairs for structured logging
func logFieldsToKeyVals(fields map[string]interface{}) []interface{} {
	result := make([]interface{}, 0, len(fields)*2)
	for k, v := range fields {
		result = append(result, k, v)
	}
	return result
}

// ExecuteJobWithWriter runs a single job and streams its output to the provided writer.
// This is primarily for TUI integration where output needs to be captured and displayed.
func (o *Orchestrator) ExecuteJobWithWriter(ctx context.Context, job *Job, output io.Writer) error {
	// Generate a unique request ID for tracing this execution
	requestID := "req-" + uuid.New().String()[:8]
	ctx = context.WithValue(ctx, "request_id", requestID)

	// Log job execution with full frontmatter details
	logFields := map[string]interface{}{
		"request_id": requestID,
		"job_id":     job.ID,
		"job_type":   job.Type,
		"job_title":  job.Title,
		"job_file":   job.FilePath,
		"plan_name":  o.Plan.Name,
		"plan_dir":   o.Plan.Directory,
		"status":     job.Status,
	}

	// Add optional fields if present
	if job.Model != "" {
		logFields["model"] = job.Model
	}
	if job.Template != "" {
		logFields["template"] = job.Template
	}
	if job.RulesFile != "" {
		logFields["rules_file"] = job.RulesFile
	}
	if job.Repository != "" {
		logFields["repository"] = job.Repository
	}
	if job.Worktree != "" {
		logFields["worktree"] = job.Worktree
	}
	if len(job.Dependencies) > 0 {
		depFiles := make([]string, len(job.Dependencies))
		for i, dep := range job.Dependencies {
			if dep != nil {
				depFiles[i] = dep.Filename
			}
		}
		logFields["dependencies"] = depFiles
		logFields["dependency_count"] = len(job.Dependencies)
	}
	if job.PrependDependencies {
		logFields["prepend_dependencies"] = job.PrependDependencies
	}

	// Special logging for interactive jobs
	if job.Type == JobTypeInteractiveAgent {
		o.logger.Info("Starting interactive job", logFieldsToKeyVals(logFields)...)
	} else {
		o.logger.Info("Executing job", logFieldsToKeyVals(logFields)...)
	}

	// Update status to running
	if err := o.UpdateJobStatus(job, JobStatusRunning); err != nil {
		return fmt.Errorf("update status to running: %w", err)
	}

	// Get executor
	executor, ok := o.executors[job.Type]
	if !ok {
		return fmt.Errorf("no executor for job type: %s", job.Type)
	}

	// Execute job, passing the writer
	execErr := executor.Execute(ctx, job, o.Plan, output)

	// Update final status (skip for chat and interactive agent jobs - they manage their own status)
	if job.Type != JobTypeChat && job.Type != JobTypeInteractiveAgent && job.Type != JobTypeAgent {
		finalStatus := JobStatusCompleted
		if execErr != nil {
			finalStatus = JobStatusFailed
			o.logger.Error("Job execution failed", "request_id", requestID, "id", job.ID, "error", execErr)
		}

		if err := o.UpdateJobStatus(job, finalStatus); err != nil {
			return fmt.Errorf("update final status: %w", err)
		}
	} else if execErr != nil {
		// For chat jobs, only update status on error
		o.logger.Error("Job execution failed", "request_id", requestID, "id", job.ID, "error", execErr)
		if err := o.UpdateJobStatus(job, JobStatusFailed); err != nil {
			return fmt.Errorf("update final status: %w", err)
		}
	}

	return execErr
}

// executeJob runs a single job with the appropriate executor.
func (o *Orchestrator) executeJob(ctx context.Context, job *Job) error {
	// Generate a unique request ID for tracing this execution
	requestID := "req-" + uuid.New().String()[:8]
	ctx = context.WithValue(ctx, "request_id", requestID)

	// Log job execution with full frontmatter details
	logFields := map[string]interface{}{
		"request_id":   requestID,
		"job_id":       job.ID,
		"job_type":     job.Type,
		"job_title":    job.Title,
		"job_file":     job.FilePath,
		"plan_name":    o.Plan.Name,
		"plan_dir":     o.Plan.Directory,
		"status":       job.Status,
	}

	// Add optional fields if present
	if job.Model != "" {
		logFields["model"] = job.Model
	}
	if job.Template != "" {
		logFields["template"] = job.Template
	}
	if job.RulesFile != "" {
		logFields["rules_file"] = job.RulesFile
	}
	if job.Repository != "" {
		logFields["repository"] = job.Repository
	}
	if job.Worktree != "" {
		logFields["worktree"] = job.Worktree
	}
	if len(job.Dependencies) > 0 {
		depFiles := make([]string, len(job.Dependencies))
		for i, dep := range job.Dependencies {
			if dep != nil {
				depFiles[i] = dep.Filename
			}
		}
		logFields["dependencies"] = depFiles
		logFields["dependency_count"] = len(job.Dependencies)
	}
	if job.PrependDependencies {
		logFields["prepend_dependencies"] = job.PrependDependencies
	}

	// Special logging for interactive jobs
	if job.Type == JobTypeInteractiveAgent {
		o.logger.Info("Starting interactive job", logFieldsToKeyVals(logFields)...)
	} else {
		o.logger.Info("Executing job", logFieldsToKeyVals(logFields)...)
	}

	// Update status to running
	if err := o.UpdateJobStatus(job, JobStatusRunning); err != nil {
		return fmt.Errorf("update status to running: %w", err)
	}

	// Get executor
	executor, ok := o.executors[job.Type]
	if !ok {
		return fmt.Errorf("no executor for job type: %s", job.Type)
	}

	// Execute job with os.Stdout as default output
	execErr := executor.Execute(ctx, job, o.Plan, os.Stdout)

	// Update final status (skip for chat and interactive agent jobs - they manage their own status)
	if job.Type != JobTypeChat && job.Type != JobTypeInteractiveAgent && job.Type != JobTypeAgent {
		finalStatus := JobStatusCompleted
		if execErr != nil {
			finalStatus = JobStatusFailed
			o.logger.Error("Job execution failed", "request_id", requestID, "id", job.ID, "error", execErr)
		}

		if err := o.UpdateJobStatus(job, finalStatus); err != nil {
			return fmt.Errorf("update final status: %w", err)
		}
	} else if execErr != nil {
		// For chat jobs, only update status on error
		o.logger.Error("Job execution failed", "request_id", requestID, "id", job.ID, "error", execErr)
		if err := o.UpdateJobStatus(job, JobStatusFailed); err != nil {
			return fmt.Errorf("update final status: %w", err)
		}
	}

	return execErr
}

// UpdateJobStatus updates a job's status with proper synchronization.
func (o *Orchestrator) UpdateJobStatus(job *Job, status JobStatus) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	
	// Update in-memory state
	oldStatus := job.Status
	job.Status = status
	
	// Update timestamps
	switch status {
	case JobStatusRunning:
		job.StartTime = time.Now()
	case JobStatusCompleted, JobStatusFailed:
		job.EndTime = time.Now()
	}
	
	// Persist to file
	if err := o.stateManager.UpdateJobStatus(job, status); err != nil {
		// Rollback in-memory change
		job.Status = oldStatus
		return fmt.Errorf("persist status change: %w", err)
	}
	
	// Log state transition
	o.logger.Info("Job status updated", 
		"job", job.ID,
		"from", oldStatus,
		"to", status)
	
	// If job is being marked as completed and summarization is enabled, generate summary
	if status == JobStatusCompleted && oldStatus != JobStatusCompleted && o.config.SummaryConfig != nil && o.config.SummaryConfig.Enabled {
		// Note: Running summarization in a goroutine to avoid blocking the orchestrator
		go func() {
			o.logger.Info("Generating job summary", "job", job.ID)
			ctx := context.Background()
			summary, err := SummarizeJobContent(ctx, job, o.Plan, *o.config.SummaryConfig)
			if err != nil {
				o.logger.Error("Failed to generate job summary", "job", job.ID, "error", err)
				return
			}
			if summary != "" {
				if err := AddSummaryToJobFile(job, summary); err != nil {
					o.logger.Error("Failed to add summary to job file", "job", job.ID, "error", err)
				} else {
					o.logger.Info("Added summary to job", "job", job.ID)
				}
			}
		}()
	}
	
	return nil
}

// UpdateJobMetadata updates a job's metadata.
func (o *Orchestrator) UpdateJobMetadata(job *Job, meta JobMetadata) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Update in-memory state
	oldMeta := job.Metadata
	job.Metadata = meta

	// Persist to file
	if err := o.stateManager.UpdateJobMetadata(job, meta); err != nil {
		// Rollback
		job.Metadata = oldMeta
		return fmt.Errorf("persist metadata change: %w", err)
	}

	return nil
}

// SetLogger sets a custom logger.
func (o *Orchestrator) SetLogger(logger Logger) {
	o.logger = logger
}

// defaultLogger provides a simple logger implementation using grove-core logging.
type defaultLogger struct{
	prettyLog *grovelogging.PrettyLogger
	structuredLog *logrus.Entry
}

func NewDefaultLogger() Logger {
	return &defaultLogger{
		prettyLog: grovelogging.NewPrettyLogger(),
		structuredLog: grovelogging.NewLogger("grove-flow"),
	}
}

func (l *defaultLogger) Info(msg string, keysAndValues ...interface{}) {
	// Log to structured logger
	if len(keysAndValues) > 0 {
		fields := make(logrus.Fields)
		for i := 0; i < len(keysAndValues); i += 2 {
			if i+1 < len(keysAndValues) {
				fields[fmt.Sprint(keysAndValues[i])] = keysAndValues[i+1]
			}
		}
		l.structuredLog.WithFields(fields).Info(msg)

		// Also log to pretty logger
		var parts []string
		for k, v := range fields {
			parts = append(parts, fmt.Sprintf("%v=%v", k, v))
		}
		l.prettyLog.InfoPretty(fmt.Sprintf("%s [%s]", msg, strings.Join(parts, " ")))
	} else {
		l.structuredLog.Info(msg)
		l.prettyLog.InfoPretty(msg)
	}
}

func (l *defaultLogger) Error(msg string, keysAndValues ...interface{}) {
	if len(keysAndValues) > 0 {
		l.prettyLog.ErrorPretty(fmt.Sprintf("%s %v", msg, keysAndValues), nil)
	} else {
		l.prettyLog.ErrorPretty(msg, nil)
	}
}

func (l *defaultLogger) Debug(msg string, keysAndValues ...interface{}) {
	// Use structured logger for debug messages
	if len(keysAndValues) > 0 {
		fields := make(map[string]interface{})
		for i := 0; i < len(keysAndValues); i += 2 {
			if i+1 < len(keysAndValues) {
				fields[fmt.Sprint(keysAndValues[i])] = keysAndValues[i+1]
			}
		}
		l.structuredLog.WithFields(fields).Debug(msg)
	} else {
		l.structuredLog.Debug(msg)
	}
}

// StateManager handles persistence of job states.
type StateManager struct {
	planDir   string
	persister *StatePersister
}

func NewStateManager(planDir string) *StateManager {
	return &StateManager{
		planDir:   planDir,
		persister: NewStatePersister(),
	}
}

func (sm *StateManager) UpdateJobStatus(job *Job, status JobStatus) error {
	return sm.persister.UpdateJobStatus(job, status)
}

func (sm *StateManager) UpdateJobMetadata(job *Job, meta JobMetadata) error {
	return sm.persister.UpdateJobMetadata(job, meta)
}