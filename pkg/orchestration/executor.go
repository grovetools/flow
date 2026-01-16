package orchestration

import (
	"context"
	"fmt"
	"os"

	grovelogging "github.com/grovetools/core/logging"
)

// Executor is the interface for job executors.
type Executor interface {
	Execute(ctx context.Context, job *Job, plan *Plan) error
	Name() string
}

// ExecutorRegistry manages available executors.
type ExecutorRegistry struct {
	executors map[JobType]Executor
}

// NewExecutorRegistry creates a new executor registry.
func NewExecutorRegistry() *ExecutorRegistry {
	return &ExecutorRegistry{
		executors: make(map[JobType]Executor),
	}
}

// Register adds an executor to the registry.
func (r *ExecutorRegistry) Register(jobType JobType, executor Executor) {
	r.executors[jobType] = executor
}

// Get returns the executor for a given job type.
func (r *ExecutorRegistry) Get(jobType JobType) (Executor, error) {
	executor, exists := r.executors[jobType]
	if !exists {
		return nil, fmt.Errorf("no executor registered for job type: %s", jobType)
	}
	return executor, nil
}

// ExecuteJob runs a job using the appropriate executor.
func (r *ExecutorRegistry) ExecuteJob(ctx context.Context, job *Job, plan *Plan) error {
	executor, err := r.Get(job.Type)
	if err != nil {
		return err
	}

	// Attach os.Stdout to the context for CLI execution
	ctx = grovelogging.WithWriter(ctx, os.Stdout)
	return executor.Execute(ctx, job, plan)
}