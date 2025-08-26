package orchestration

import (
	"strings"
	"testing"
)

// Helper function to create a plan with resolved dependencies
func createTestPlan(jobs []*Job) *Plan {
	plan := &Plan{
		Name:     "test-plan",
		Jobs:     jobs,
		JobsByID: make(map[string]*Job),
	}
	
	// Populate JobsByID
	for _, job := range plan.Jobs {
		plan.JobsByID[job.ID] = job
	}
	
	// Resolve dependencies
	if err := plan.ResolveDependencies(); err != nil {
		panic(err) // In tests, we can panic on setup errors
	}
	
	return plan
}

// Helper function to create a plan without resolving dependencies (for error testing)
func createTestPlanUnresolved(jobs []*Job) *Plan {
	plan := &Plan{
		Name:     "test-plan",
		Jobs:     jobs,
		JobsByID: make(map[string]*Job),
	}
	
	// Populate JobsByID
	for _, job := range plan.Jobs {
		plan.JobsByID[job.ID] = job
	}
	
	// Don't resolve dependencies - leave that to the test
	return plan
}

func TestBuildDependencyGraph(t *testing.T) {
	plan := createTestPlan([]*Job{
		{ID: "job1", Status: JobStatusPending, DependsOn: []string{}},
		{ID: "job2", Status: JobStatusPending, DependsOn: []string{"job1"}},
		{ID: "job3", Status: JobStatusPending, DependsOn: []string{"job1", "job2"}},
	})

	graph, err := BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("Failed to build dependency graph: %v", err)
	}

	// Verify nodes
	if len(graph.nodes) != 3 {
		t.Errorf("Expected 3 nodes, got %d", len(graph.nodes))
	}

	// Verify edges
	if len(graph.edges["job3"]) != 2 {
		t.Errorf("Expected job3 to have 2 dependencies, got %d", len(graph.edges["job3"]))
	}
}

func TestDependencyGraph_ValidateDependencies(t *testing.T) {
	tests := []struct {
		name      string
		jobs      []*Job
		wantError bool
		errorMsg  string
		setupError bool // Whether error should occur during setup
	}{
		{
			name: "valid dependencies",
			jobs: []*Job{
				{ID: "job1", DependsOn: []string{}},
				{ID: "job2", DependsOn: []string{"job1"}},
			},
			wantError: false,
		},
		{
			name: "missing dependency",
			jobs: []*Job{
				{ID: "job1", DependsOn: []string{"job2"}},
			},
			wantError: true,
			setupError: true,
			errorMsg:  "non-existent job",
		},
		{
			name: "self dependency",
			jobs: []*Job{
				{ID: "job1", DependsOn: []string{"job1"}},
			},
			wantError: true,
			errorMsg:  "depends on itself",
		},
		{
			name: "circular dependency",
			jobs: []*Job{
				{ID: "job1", DependsOn: []string{"job2"}},
				{ID: "job2", DependsOn: []string{"job1"}},
			},
			wantError: true,
			errorMsg:  "circular dependency",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupError {
				// Test cases that should fail during plan setup
				plan := &Plan{
					Name:     "test-plan",
					Jobs:     tt.jobs,
					JobsByID: make(map[string]*Job),
				}
				for _, job := range plan.Jobs {
					plan.JobsByID[job.ID] = job
				}
				err := plan.ResolveDependencies()
				if err == nil {
					t.Errorf("Expected error during setup but got none")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error containing '%s', got '%s'", tt.errorMsg, err.Error())
				}
				return
			}
			
			// For tests that expect graph validation errors (not setup errors)
			// we need to bypass dependency resolution to test the graph validation
			plan := createTestPlanUnresolved(tt.jobs)
			
			// Manually set up dependencies for graph validation tests
			// This bypasses ResolveDependencies which would catch circular deps
			for _, job := range plan.Jobs {
				job.Dependencies = make([]*Job, 0)
				for _, depID := range job.DependsOn {
					if dep, exists := plan.JobsByID[depID]; exists {
						job.Dependencies = append(job.Dependencies, dep)
					}
				}
			}
			
			graph, err := BuildDependencyGraph(plan)
			if tt.wantError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error containing '%s', got '%s'", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if graph == nil {
					t.Errorf("Expected non-nil graph")
				}
			}
		})
	}
}

func TestDependencyGraph_GetExecutionPlan(t *testing.T) {
	plan := createTestPlan([]*Job{
		{ID: "job1", Status: JobStatusPending, DependsOn: []string{}},
		{ID: "job2", Status: JobStatusPending, DependsOn: []string{}},
		{ID: "job3", Status: JobStatusPending, DependsOn: []string{"job1"}},
		{ID: "job4", Status: JobStatusPending, DependsOn: []string{"job2", "job3"}},
	})

	graph, err := BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("Failed to build graph: %v", err)
	}

	execPlan, err := graph.GetExecutionPlan()
	if err != nil {
		t.Fatalf("Failed to get execution plan: %v", err)
	}

	// Verify stages
	if len(execPlan.Stages) == 0 {
		t.Errorf("Expected at least one stage")
	}

	// First stage should contain job1 and job2 (can run in parallel)
	firstStage := execPlan.Stages[0]
	if len(firstStage) != 2 {
		t.Errorf("Expected 2 jobs in first stage, got %d", len(firstStage))
	}

	// Verify job1 and job2 are in first stage
	hasJob1, hasJob2 := false, false
	for _, jobID := range firstStage {
		if jobID == "job1" {
			hasJob1 = true
		}
		if jobID == "job2" {
			hasJob2 = true
		}
	}
	if !hasJob1 || !hasJob2 {
		t.Errorf("First stage should contain job1 and job2")
	}
}

func TestDependencyGraph_GetRunnableJobs(t *testing.T) {
	plan := createTestPlan([]*Job{
		{ID: "job1", Status: JobStatusCompleted, DependsOn: []string{}},
		{ID: "job2", Status: JobStatusPending, DependsOn: []string{"job1"}},
		{ID: "job3", Status: JobStatusPending, DependsOn: []string{"job4"}},
		{ID: "job4", Status: JobStatusPending, DependsOn: []string{}},
	})

	graph, err := BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("Failed to build graph: %v", err)
	}

	runnable := graph.GetRunnableJobs()

	// Should have job2 (dependencies met) and job4 (no dependencies)
	if len(runnable) != 2 {
		t.Errorf("Expected 2 runnable jobs, got %d", len(runnable))
	}

	// Verify the correct jobs are runnable
	runnableMap := make(map[string]bool)
	for _, job := range runnable {
		runnableMap[job.ID] = true
	}

	if !runnableMap["job2"] {
		t.Errorf("Expected job2 to be runnable")
	}
	if !runnableMap["job4"] {
		t.Errorf("Expected job4 to be runnable")
	}
	if runnableMap["job3"] {
		t.Errorf("job3 should not be runnable (depends on pending job4)")
	}
}

func TestDependencyGraph_DetectCycles(t *testing.T) {
	// Test with a complex cycle
	// We can't use createTestPlan because it would fail on circular dependency
	plan := &Plan{
		Name: "test-plan",
		Jobs: []*Job{
			{ID: "A", DependsOn: []string{"B"}},
			{ID: "B", DependsOn: []string{"C"}},
			{ID: "C", DependsOn: []string{"D"}},
			{ID: "D", DependsOn: []string{"B"}}, // Creates cycle B->C->D->B
			{ID: "E", DependsOn: []string{}},    // Independent job
		},
		JobsByID: make(map[string]*Job),
	}
	
	// Populate JobsByID
	for _, job := range plan.Jobs {
		plan.JobsByID[job.ID] = job
	}
	
	// ResolveDependencies will fail due to circular dependency
	err := plan.ResolveDependencies()
	if err == nil {
		t.Fatal("Expected error from circular dependency during resolution")
	}
	if !strings.Contains(err.Error(), "circular dependency") {
		t.Fatalf("Expected circular dependency error, got: %v", err)
	}
}

func TestDependencyGraph_ToMermaid(t *testing.T) {
	plan := createTestPlan([]*Job{
		{ID: "job1", Status: JobStatusCompleted, DependsOn: []string{}},
		{ID: "job2", Status: JobStatusRunning, DependsOn: []string{"job1"}},
		{ID: "job3", Status: JobStatusFailed, DependsOn: []string{"job1"}},
		{ID: "job4", Status: JobStatusPending, DependsOn: []string{"job2", "job3"}},
	})

	graph, err := BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("Failed to build graph: %v", err)
	}

	mermaid := graph.ToMermaid()

	// Verify basic structure
	if !strings.Contains(mermaid, "graph TD") {
		t.Errorf("Expected Mermaid diagram to start with 'graph TD'")
	}

	// Verify nodes are present
	if !strings.Contains(mermaid, "job1") {
		t.Errorf("Expected job1 in diagram")
	}

	// Verify edges
	if !strings.Contains(mermaid, "job1 --> job2") {
		t.Errorf("Expected edge from job1 to job2")
	}

	// Verify styles
	if !strings.Contains(mermaid, "classDef completed") {
		t.Errorf("Expected style definitions")
	}
}