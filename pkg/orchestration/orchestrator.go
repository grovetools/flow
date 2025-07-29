package orchestration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	
	"github.com/mattsolo1/grove-core/docker"
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
}

// Orchestrator coordinates job execution and manages state.
type Orchestrator struct {
	plan            *Plan
	executors       map[JobType]Executor
	dependencyGraph *DependencyGraph
	config          *OrchestratorConfig
	logger          Logger
	stateManager    *StateManager
	dockerClient    docker.Client
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
func NewOrchestrator(plan *Plan, config *OrchestratorConfig, dockerClient docker.Client) (*Orchestrator, error) {
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
		plan:            plan,
		executors:       make(map[JobType]Executor),
		dependencyGraph: graph,
		config:          config,
		logger:          NewDefaultLogger(),
		stateManager:    stateManager,
		dockerClient:    dockerClient,
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
	// Check if we have any agent jobs
	hasAgentJobs := false
	for _, job := range o.plan.Jobs {
		if job.Type == JobTypeAgent {
			hasAgentJobs = true
			break
		}
	}

	// If we have agent jobs, ensure target_agent_container is configured
	if hasAgentJobs {
		if o.plan.Orchestration == nil || o.plan.Orchestration.TargetAgentContainer == "" {
			return fmt.Errorf("orchestration with agent jobs requires 'orchestration.target_agent_container' to be set in grove.yml")
		}
		// TODO: Add logic to check if the container is running via Docker client
		// For now, we'll just log a warning
		o.logger.Info("Using target agent container", "container", o.plan.Orchestration.TargetAgentContainer)
	}

	return nil
}

// registerExecutors sets up the available executors.
func (o *Orchestrator) registerExecutors() {
	// Create shared config for executors
	execConfig := &Config{
		MaxPromptLength: 1000000,
		Timeout:         30 * time.Minute,
		RetryCount:      2,
		Model:           "default",
		ModelOverride:   o.config.ModelOverride,
	}

	// Register oneshot executor (also handles chat jobs)
	oneshotExecutor := NewOneShotExecutor(execConfig)
	o.executors[JobTypeOneshot] = oneshotExecutor
	o.executors[JobTypeChat] = oneshotExecutor

	// Register agent executor with mock LLM client
	o.executors[JobTypeAgent] = NewAgentExecutor(NewMockLLMClient(), execConfig, o.dockerClient)

	// Register shell executor
	o.executors[JobTypeShell] = NewShellExecutor()
}

// RunJob executes a specific job by filename.
func (o *Orchestrator) RunJob(ctx context.Context, jobFile string) error {
	// Find job by filename
	var job *Job
	for _, j := range o.plan.Jobs {
		if j.FilePath == jobFile {
			job = j
			break
		}
	}

	if job == nil {
		return fmt.Errorf("job not found: %s", jobFile)
	}

	// Validate job is runnable
	runnable := o.dependencyGraph.GetRunnableJobs()
	isRunnable := false
	for _, r := range runnable {
		if r.ID == job.ID {
			isRunnable = true
			break
		}
	}

	if !isRunnable {
		if job.Status == JobStatusCompleted {
			return fmt.Errorf("job already completed: %s", jobFile)
		}
		return fmt.Errorf("job %s is not runnable (dependencies not met or in wrong status)", job.ID)
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
	o.logger.Info("Starting orchestration", "plan", o.plan.Name)

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

// GetStatus returns the current plan status.
func (o *Orchestrator) GetStatus() *PlanStatus {
	o.mu.Lock()
	defer o.mu.Unlock()

	status := &PlanStatus{
		Total: len(o.plan.Jobs),
	}

	for _, job := range o.plan.Jobs {
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

// executeJob runs a single job with the appropriate executor.
func (o *Orchestrator) executeJob(ctx context.Context, job *Job) error {
	o.logger.Info("Executing job", "id", job.ID, "type", job.Type)

	// Update status to running
	if err := o.UpdateJobStatus(job, JobStatusRunning); err != nil {
		return fmt.Errorf("update status to running: %w", err)
	}

	// Get executor
	executor, ok := o.executors[job.Type]
	if !ok {
		return fmt.Errorf("no executor for job type: %s", job.Type)
	}

	// Execute job
	execErr := executor.Execute(ctx, job, o.plan)

	// Update final status (skip for chat jobs - they manage their own status)
	if job.Type != JobTypeChat {
		finalStatus := JobStatusCompleted
		if execErr != nil {
			finalStatus = JobStatusFailed
			o.logger.Error("Job execution failed", "id", job.ID, "error", execErr)
		}

		if err := o.UpdateJobStatus(job, finalStatus); err != nil {
			return fmt.Errorf("update final status: %w", err)
		}
	} else if execErr != nil {
		// For chat jobs, only update status on error
		o.logger.Error("Job execution failed", "id", job.ID, "error", execErr)
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

// defaultLogger provides a simple logger implementation.
type defaultLogger struct{}

func NewDefaultLogger() Logger {
	return &defaultLogger{}
}

func (l *defaultLogger) Info(msg string, keysAndValues ...interface{}) {
	fmt.Printf("[INFO] %s %v\n", msg, keysAndValues)
}

func (l *defaultLogger) Error(msg string, keysAndValues ...interface{}) {
	fmt.Printf("[ERROR] %s %v\n", msg, keysAndValues)
}

func (l *defaultLogger) Debug(msg string, keysAndValues ...interface{}) {
	fmt.Printf("[DEBUG] %s %v\n", msg, keysAndValues)
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