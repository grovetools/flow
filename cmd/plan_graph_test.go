package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/flow/pkg/orchestration"
)

func TestRunPlanGraph(t *testing.T) {
	tests := []struct {
		name      string
		setupPlan func(t *testing.T, dir string)
		cmd       *PlanGraphCmd
		wantErr   bool
		checkOut  func(t *testing.T, output string)
	}{
		{
			name: "mermaid format",
			setupPlan: func(t *testing.T, dir string) {
				plan := createTestPlan()
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanGraphCmd{
				Format: "mermaid",
			},
			wantErr: false,
			checkOut: func(t *testing.T, output string) {
				if !strings.Contains(output, "graph TD") {
					t.Error("expected mermaid graph to start with 'graph TD'")
				}
				if !strings.Contains(output, "classDef completed") {
					t.Error("expected mermaid graph to contain style definitions")
				}
			},
		},
		{
			name: "dot format",
			setupPlan: func(t *testing.T, dir string) {
				plan := createTestPlan()
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanGraphCmd{
				Format: "dot",
			},
			wantErr: false,
			checkOut: func(t *testing.T, output string) {
				if !strings.Contains(output, "digraph jobs") {
					t.Error("expected dot graph to start with 'digraph jobs'")
				}
				if !strings.Contains(output, "->") {
					t.Error("expected dot graph to contain edges")
				}
			},
		},
		{
			name: "ascii format",
			setupPlan: func(t *testing.T, dir string) {
				plan := createTestPlan()
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanGraphCmd{
				Format: "ascii",
			},
			wantErr: false,
			checkOut: func(t *testing.T, output string) {
				if !strings.Contains(output, "Job Dependency Graph") {
					t.Error("expected ASCII graph to have title")
				}
				if !strings.Contains(output, "Legend:") {
					t.Error("expected ASCII graph to have legend")
				}
			},
		},
		{
			name: "invalid format",
			setupPlan: func(t *testing.T, dir string) {
				plan := createTestPlan()
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanGraphCmd{
				Format: "invalid",
			},
			wantErr: true,
		},
		{
			name: "empty plan",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{
					Name: "empty-plan",
					Jobs: []*orchestration.Job{},
				}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanGraphCmd{
				Format: "mermaid",
			},
			wantErr: true,
		},
		{
			name: "output to file",
			setupPlan: func(t *testing.T, dir string) {
				plan := createTestPlan()
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanGraphCmd{
				Format: "mermaid",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp directory
			dir := t.TempDir()

			// Setup plan
			tt.setupPlan(t, dir)

			// Set directory in command
			tt.cmd.Directory = dir

			// Set output file if needed
			if tt.name == "output to file" {
				tt.cmd.Output = filepath.Join(dir, "graph.mermaid")
			}

			// Capture output
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			// Run command
			err := RunPlanGraph(tt.cmd)

			// Restore stdout
			w.Close()
			os.Stdout = oldStdout

			// Read output
			buf := make([]byte, 4096)
			n, _ := r.Read(buf)
			output := string(buf[:n])

			// Check error
			if (err != nil) != tt.wantErr {
				t.Errorf("RunPlanGraph() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Check output
			if !tt.wantErr && tt.checkOut != nil {
				if tt.cmd.Output != "" {
					// Read from file
					content, err := os.ReadFile(tt.cmd.Output)
					if err != nil {
						t.Fatal(err)
					}
					tt.checkOut(t, string(content))
				} else {
					tt.checkOut(t, output)
				}
			}
		})
	}
}

func TestBuildDependencyGraph(t *testing.T) {
	plan := createTestPlan()
	graph := buildDependencyGraph(plan)

	// Check nodes
	if len(graph.Nodes) != 4 {
		t.Errorf("expected 4 nodes, got %d", len(graph.Nodes))
	}

	// Check roots
	if len(graph.Roots) != 1 || graph.Roots[0] != "job1" {
		t.Errorf("expected one root 'job1', got %v", graph.Roots)
	}

	// Check edges
	if edges, ok := graph.Edges["job1"]; !ok || len(edges) != 1 {
		t.Errorf("expected job1 to have 1 outgoing edge")
	}
	if edges, ok := graph.Edges["job2"]; !ok || len(edges) != 2 {
		t.Errorf("expected job2 to have 2 outgoing edges")
	}
}

func TestComputeJobLevels(t *testing.T) {
	plan := createTestPlan()
	graph := buildDependencyGraph(plan)
	levels := computeJobLevels(plan, graph)

	expected := map[string]int{
		"job1": 0,
		"job2": 1,
		"job3": 2,
		"job4": 2,
	}

	for jobID, expectedLevel := range expected {
		if level, ok := levels[jobID]; !ok || level != expectedLevel {
			t.Errorf("expected job %s at level %d, got %d", jobID, expectedLevel, level)
		}
	}
}

func TestGetStatusSymbol(t *testing.T) {
	tests := []struct {
		status   orchestration.JobStatus
		expected string
	}{
		{orchestration.JobStatusCompleted, "* Completed"},
		{orchestration.JobStatusRunning, "⚡ Running"},
		{orchestration.JobStatusPending, "[...] Pending"},
		{orchestration.JobStatusFailed, "x Failed"},
		{orchestration.JobStatusBlocked, "⊘ Blocked"},
		{orchestration.JobStatusNeedsReview, "? Needs Review"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			got := getStatusSymbol(tt.status)
			if got != tt.expected {
				t.Errorf("getStatusSymbol(%s) = %s, want %s", tt.status, got, tt.expected)
			}
		})
	}
}

func createTestPlan() *orchestration.Plan {
	return &orchestration.Plan{
		Name: "test-plan",
		Jobs: []*orchestration.Job{
			{
				ID:       "job1",
				Title:    "First Job",
				Filename: "01-first-job.md",
				Status:   orchestration.JobStatusCompleted,
				Type:     orchestration.JobTypeOneshot,
			},
			{
				ID:        "job2",
				Title:     "Second Job",
				Filename:  "02-second-job.md",
				Status:    orchestration.JobStatusRunning,
				Type:      orchestration.JobTypeAgent,
				DependsOn: []string{"job1"},
			},
			{
				ID:        "job3",
				Title:     "Third Job",
				Filename:  "03-third-job.md",
				Status:    orchestration.JobStatusPending,
				Type:      orchestration.JobTypeOneshot,
				DependsOn: []string{"job2"},
			},
			{
				ID:        "job4",
				Title:     "Fourth Job",
				Filename:  "04-fourth-job.md",
				Status:    orchestration.JobStatusPending,
				Type:      orchestration.JobTypeAgent,
				DependsOn: []string{"job2"},
			},
		},
		JobsByID: map[string]*orchestration.Job{
			"job1": nil, // Will be set by SavePlan
			"job2": nil,
			"job3": nil,
			"job4": nil,
		},
	}
}
