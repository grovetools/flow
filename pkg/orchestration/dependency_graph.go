package orchestration

import (
	"fmt"
	"strings"
)

// DependencyGraph represents the job dependency relationships.
type DependencyGraph struct {
	nodes map[string]*Job
	edges map[string][]string // job -> dependencies
}

// ExecutionPlan contains stages of jobs that can run in parallel.
type ExecutionPlan struct {
	Stages [][]string // Each stage contains jobs that can run in parallel
}

// BuildDependencyGraph creates a dependency graph from all jobs in a plan.
func BuildDependencyGraph(plan *Plan) (*DependencyGraph, error) {
	graph := &DependencyGraph{
		nodes: make(map[string]*Job),
		edges: make(map[string][]string),
	}

	// Add all jobs as nodes
	for _, job := range plan.Jobs {
		graph.nodes[job.ID] = job
		
		// Use resolved dependencies instead of raw DependsOn
		depIDs := make([]string, 0, len(job.Dependencies))
		for _, dep := range job.Dependencies {
			if dep != nil {
				depIDs = append(depIDs, dep.ID)
			}
		}
		graph.edges[job.ID] = depIDs
	}

	// Validate dependencies
	if err := graph.ValidateDependencies(); err != nil {
		return nil, err
	}

	return graph, nil
}

// GetExecutionPlan performs topological sort and groups jobs into parallel stages.
func (dg *DependencyGraph) GetExecutionPlan() (*ExecutionPlan, error) {
	// Perform topological sort
	sorted, err := dg.topologicalSort()
	if err != nil {
		return nil, err
	}

	// Group into stages based on dependencies
	stages := [][]string{}
	processed := make(map[string]bool)

	for len(processed) < len(sorted) {
		stage := []string{}
		
		for _, jobID := range sorted {
			if processed[jobID] {
				continue
			}

			// Check if all dependencies are processed
			canRun := true
			for _, dep := range dg.edges[jobID] {
				if !processed[dep] {
					canRun = false
					break
				}
			}

			if canRun {
				// Skip completed jobs
				job := dg.nodes[jobID]
				if job.Status != JobStatusCompleted {
					stage = append(stage, jobID)
				}
			}
		}

		if len(stage) == 0 && len(processed) < len(sorted) {
			// We have unprocessed jobs but can't make progress
			return nil, fmt.Errorf("unable to create execution plan: circular dependency or invalid state")
		}

		if len(stage) > 0 {
			stages = append(stages, stage)
		}

		// Mark stage jobs as processed
		for _, jobID := range stage {
			processed[jobID] = true
		}
	}

	return &ExecutionPlan{Stages: stages}, nil
}

// GetRunnableJobs finds all jobs that can be run immediately.
func (dg *DependencyGraph) GetRunnableJobs() []*Job {
	runnable := []*Job{}

	for jobID, job := range dg.nodes {
		// Skip non-pending jobs (except chat jobs in pending_user status)
		if job.Status != JobStatusPending && !(job.Type == JobTypeChat && job.Status == JobStatusPendingUser) {
			continue
		}

		// Check all dependencies are completed
		canRun := true
		for _, depID := range dg.edges[jobID] {
			dep, exists := dg.nodes[depID]
			if !exists || dep.Status != JobStatusCompleted {
				canRun = false
				break
			}
		}

		if canRun {
			runnable = append(runnable, job)
		}
	}

	return runnable
}

// ValidateDependencies checks for circular dependencies and missing references.
func (dg *DependencyGraph) ValidateDependencies() error {
	// Check for missing dependencies
	for jobID, deps := range dg.edges {
		for _, depID := range deps {
			if _, exists := dg.nodes[depID]; !exists {
				return fmt.Errorf("unknown dependency '%s' in job '%s'", depID, jobID)
			}
		}
	}

	// Check for self-dependencies
	for jobID, deps := range dg.edges {
		for _, depID := range deps {
			if jobID == depID {
				return fmt.Errorf("job '%s' depends on itself", jobID)
			}
		}
	}

	// Check for circular dependencies
	cycles, err := dg.DetectCycles()
	if err != nil {
		return err
	}
	if len(cycles) > 0 {
		return fmt.Errorf("circular dependency detected: %s", strings.Join(cycles, " → "))
	}

	return nil
}

// DetectCycles uses DFS to detect circular dependencies.
func (dg *DependencyGraph) DetectCycles() ([]string, error) {
	visited := make(map[string]bool)
	recursionStack := make(map[string]bool)
	path := []string{}

	var detectCycle func(node string) ([]string, bool)
	detectCycle = func(node string) ([]string, bool) {
		visited[node] = true
		recursionStack[node] = true
		path = append(path, node)

		// Check all dependencies
		for _, dep := range dg.edges[node] {
			if !visited[dep] {
				if cycle, found := detectCycle(dep); found {
					return cycle, true
				}
			} else if recursionStack[dep] {
				// Found a cycle - build the cycle path
				cycleStart := -1
				for i, n := range path {
					if n == dep {
						cycleStart = i
						break
					}
				}
				if cycleStart >= 0 {
					cyclePath := append(path[cycleStart:], dep)
					return cyclePath, true
				}
			}
		}

		// Backtrack
		recursionStack[node] = false
		path = path[:len(path)-1]
		return nil, false
	}

	// Check from each unvisited node
	for node := range dg.nodes {
		if !visited[node] {
			if cycle, found := detectCycle(node); found {
				return cycle, nil
			}
		}
	}

	return nil, nil
}

// topologicalSort performs a topological sort of the dependency graph.
func (dg *DependencyGraph) topologicalSort() ([]string, error) {
	visited := make(map[string]bool)
	temp := make(map[string]bool)
	result := []string{}

	var visit func(string) error
	visit = func(node string) error {
		if temp[node] {
			return fmt.Errorf("circular dependency detected involving job '%s'", node)
		}
		if visited[node] {
			return nil
		}

		temp[node] = true
		for _, dep := range dg.edges[node] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		temp[node] = false
		visited[node] = true
		result = append([]string{node}, result...)
		return nil
	}

	// Visit all nodes
	for node := range dg.nodes {
		if err := visit(node); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// ToMermaid generates a Mermaid diagram representation of the graph.
func (dg *DependencyGraph) ToMermaid() string {
	var lines []string
	lines = append(lines, "graph TD")

	// Add nodes with status
	for jobID, job := range dg.nodes {
		status := string(job.Status)
		nodeStyle := ""
		switch job.Status {
		case JobStatusCompleted:
			nodeStyle = ":::completed"
		case JobStatusRunning:
			nodeStyle = ":::running"
		case JobStatusFailed:
			nodeStyle = ":::failed"
		case JobStatusPending:
			nodeStyle = ":::pending"
		}
		lines = append(lines, fmt.Sprintf("  %s[\"%s (%s)\"]%s", jobID, jobID, status, nodeStyle))
	}

	// Add edges
	for job, deps := range dg.edges {
		for _, dep := range deps {
			lines = append(lines, fmt.Sprintf("  %s --> %s", dep, job))
		}
	}

	// Add styles
	lines = append(lines, "  classDef completed fill:#90EE90,stroke:#333,stroke-width:2px;")
	lines = append(lines, "  classDef running fill:#87CEEB,stroke:#333,stroke-width:2px;")
	lines = append(lines, "  classDef failed fill:#FFB6C1,stroke:#333,stroke-width:2px;")
	lines = append(lines, "  classDef pending fill:#FFF,stroke:#333,stroke-width:2px;")

	return strings.Join(lines, "\n")
}