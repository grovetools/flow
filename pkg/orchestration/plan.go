package orchestration

// Plan represents a collection of orchestration jobs.
type Plan struct {
	Name          string            // Name of the plan (directory name)
	Directory     string            // Root directory of the plan
	Jobs          []*Job            // List of all jobs
	JobsByID      map[string]*Job   // Keyed by job ID
	SpecFile      string            // Path to spec.md if exists
	Orchestration *Config           // Orchestration configuration
	Context       *ExecutionContext // Execution context for the plan
}