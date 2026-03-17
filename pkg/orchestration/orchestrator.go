package orchestration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/grovetools/core/command"
	grovelogging "github.com/grovetools/core/logging"
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
	ModelOverride       string           // Override model for all jobs
	MaxConsecutiveSteps int              // Maximum consecutive steps before halting
	SkipInteractive     bool             // Skip interactive agent jobs
	CommandExecutor     command.Executor // For dependency injection
	Runtime             Runtime          // Defines how jobs are executed; defaults to LocalRuntime
}

// Orchestrator coordinates job execution and manages state.
type Orchestrator struct {
	Plan            *Plan
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

	// Use injected executor or default
	if config.CommandExecutor == nil {
		config.CommandExecutor = &command.RealExecutor{}
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
		dependencyGraph: graph,
		config:          config,
		logger:          NewDefaultLogger(),
		stateManager:    stateManager,
	}

	// Initialize LocalRuntime if no runtime is provided
	if orch.config.Runtime == nil {
		execConfig := &ExecutorConfig{
			MaxPromptLength: 1000000,
			Timeout:         30 * time.Minute,
			RetryCount:      2,
			Model:           "default",
			ModelOverride:   orch.config.ModelOverride,
			SkipInteractive: orch.config.SkipInteractive,
		}
		orch.config.Runtime = NewLocalRuntime(execConfig, orch.config.CommandExecutor, orch, orch.logger)
	}

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

// RunJob executes a specific job by filename.
func (o *Orchestrator) RunJob(ctx context.Context, jobFile string) error {
	// Find job by filename or full path
	var job *Job
	for _, j := range o.Plan.Jobs {
		if j.FilePath == jobFile || j.Filename == jobFile {
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
	// Attach the provided writer (e.g., TUI writer) to the context
	ctx = grovelogging.WithWriter(ctx, output)

	// Delegate entirely to the runtime
	return o.config.Runtime.ExecuteJob(ctx, job, o.Plan)
}

// executeJob runs a single job using standard output.
func (o *Orchestrator) executeJob(ctx context.Context, job *Job) error {
	// Attach a writer to the context for the runtime's MultiWriter.
	// If no writer is already on the context, use os.Stdout (CLI mode).
	// When running inside the daemon's JobRunner, the caller should set
	// io.Discard on the context to prevent output leaking to the daemon's terminal.
	if grovelogging.GetWriter(ctx) == nil {
		ctx = grovelogging.WithWriter(ctx, os.Stdout)
	}

	// Delegate entirely to the runtime
	return o.config.Runtime.ExecuteJob(ctx, job, o.Plan)
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

// Logger returns the orchestrator's logger.
func (o *Orchestrator) Logger() Logger {
	return o.logger
}

// SetLogger sets a custom logger.
func (o *Orchestrator) SetLogger(logger Logger) {
	o.logger = logger
}

// defaultLogger provides a simple logger implementation using grove-core unified logging.
type defaultLogger struct{
	ulog *grovelogging.UnifiedLogger
}

func NewDefaultLogger() Logger {
	return &defaultLogger{
		ulog: grovelogging.NewUnifiedLogger("grove-flow"),
	}
}

func (l *defaultLogger) Info(msg string, keysAndValues ...interface{}) {
	entry := l.ulog.Info(msg)
	for i := 0; i < len(keysAndValues); i += 2 {
		if i+1 < len(keysAndValues) {
			entry = entry.Field(fmt.Sprint(keysAndValues[i]), keysAndValues[i+1])
		}
	}
	entry.Emit()
}

func (l *defaultLogger) Error(msg string, keysAndValues ...interface{}) {
	entry := l.ulog.Error(msg)
	for i := 0; i < len(keysAndValues); i += 2 {
		if i+1 < len(keysAndValues) {
			entry = entry.Field(fmt.Sprint(keysAndValues[i]), keysAndValues[i+1])
		}
	}
	entry.Emit()
}

func (l *defaultLogger) Debug(msg string, keysAndValues ...interface{}) {
	entry := l.ulog.Debug(msg)
	for i := 0; i < len(keysAndValues); i += 2 {
		if i+1 < len(keysAndValues) {
			entry = entry.Field(fmt.Sprint(keysAndValues[i]), keysAndValues[i+1])
		}
	}
	entry.Emit()
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