package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovepm/grove-flow/pkg/orchestration"
)

func TestRunPlanAddStep(t *testing.T) {
	tests := []struct {
		name      string
		setupPlan func(t *testing.T, dir string)
		cmd       *JobsAddStepCmd
		wantErr   bool
		checkJob  func(t *testing.T, dir string)
	}{
		{
			name: "add oneshot job",
			setupPlan: func(t *testing.T, dir string) {
				// Create initial plan
				plan := &orchestration.Plan{
					Name: "test-plan",
					Jobs: []*orchestration.Job{
						{
							ID:       "initial-plan",
							Title:    "initial-plan",
							Filename: "01-initial-plan.md",
							Type:     "oneshot",
							Status:   "completed",
						},
					},
				}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &JobsAddStepCmd{
				Type:       "oneshot",
				Title:      "API Design",
				DependsOn:  []string{"01-initial-plan.md"},
				PromptFile: createTempFile(t, "Design the API"),
			},
			wantErr: false,
			checkJob: func(t *testing.T, dir string) {
				// Verify job was created
				files, err := filepath.Glob(filepath.Join(dir, "*.md"))
				if err != nil {
					t.Fatal(err)
				}
				if len(files) != 2 {
					t.Errorf("expected 2 job files, got %d", len(files))
				}
			},
		},
		{
			name: "add agent job",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{
					Name: "test-plan",
					Jobs: []*orchestration.Job{},
				}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &JobsAddStepCmd{
				Type:       "agent",
				Title:      "Implementation",
				PromptFile: createTempFile(t, "Implement the feature"),
			},
			wantErr: false,
		},
		{
			name: "missing title",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan"}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &JobsAddStepCmd{
				Type:       "oneshot",
				PromptFile: createTempFile(t, "Some prompt"),
			},
			wantErr: true,
		},
		{
			name: "invalid job type",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan"}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &JobsAddStepCmd{
				Type:       "invalid",
				Title:      "Test Job",
				PromptFile: createTempFile(t, "Some prompt"),
			},
			wantErr: true,
		},
		{
			name: "invalid dependency",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan"}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &JobsAddStepCmd{
				Type:       "oneshot",
				Title:      "Test Job",
				DependsOn:  []string{"nonexistent.md"},
				PromptFile: createTempFile(t, "Some prompt"),
			},
			wantErr: true,
		},
		{
			name: "missing prompt",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan"}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &JobsAddStepCmd{
				Type:  "oneshot",
				Title: "Test Job",
			},
			wantErr: true,
		},
		{
			name: "reference-based prompt with template and source files",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan"}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
				// Create test source files
				os.WriteFile(filepath.Join(dir, "source1.txt"), []byte("Source 1 content"), 0644)
				os.WriteFile(filepath.Join(dir, "source2.txt"), []byte("Source 2 content"), 0644)
			},
			cmd: &JobsAddStepCmd{
				Type:        "oneshot",
				Title:       "Reference-based Step",
				Template:    "agent-run", // Assuming this built-in template exists
				SourceFiles: []string{"source1.txt", "source2.txt"},
			},
			wantErr: false,
			checkJob: func(t *testing.T, dir string) {
				// Load the created job
				plan, err := orchestration.LoadPlan(dir)
				if err != nil {
					t.Fatal(err)
				}
				
				// Find the created job
				var job *orchestration.Job
				for _, j := range plan.Jobs {
					if j.Title == "Reference-based Step" {
						job = j
						break
					}
				}
				
				if job == nil {
					t.Fatal("Created job not found")
				}
				
				// Verify job has template and prompt sources
				if job.Template != "agent-run" {
					t.Errorf("Expected template 'agent-run', got '%s'", job.Template)
				}
				
				if len(job.PromptSource) != 2 {
					t.Errorf("Expected 2 prompt sources, got %d", len(job.PromptSource))
				}
				
				// Check that prompt body contains reference comment
				if !strings.Contains(job.PromptBody, "Template will be resolved at execution time") {
					t.Error("Expected reference comment in prompt body")
				}
			},
		},
		{
			name: "legacy prompt-file flag converts to source files",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan"}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &JobsAddStepCmd{
				Type:       "oneshot",
				Title:      "Legacy Conversion",
				Template:   "agent-run",
				PromptFile: createTempFile(t, "Legacy prompt content"),
			},
			wantErr: false,
			checkJob: func(t *testing.T, dir string) {
				// Load the created job
				plan, err := orchestration.LoadPlan(dir)
				if err != nil {
					t.Fatal(err)
				}
				
				// Find the created job
				var job *orchestration.Job
				for _, j := range plan.Jobs {
					if j.Title == "Legacy Conversion" {
						job = j
						break
					}
				}
				
				if job == nil {
					t.Fatal("Created job not found")
				}
				
				// Verify job has template and one prompt source
				if job.Template != "agent-run" {
					t.Errorf("Expected template 'agent-run', got '%s'", job.Template)
				}
				
				if len(job.PromptSource) != 1 {
					t.Errorf("Expected 1 prompt source, got %d", len(job.PromptSource))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp directory
			dir := t.TempDir()

			// Setup plan
			tt.setupPlan(t, dir)

			// Set directory in command
			tt.cmd.Dir = dir

			// Run command
			err := RunPlanAddStep(tt.cmd)

			// Check error
			if (err != nil) != tt.wantErr {
				t.Errorf("RunPlanAddStep() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Additional checks
			if !tt.wantErr && tt.checkJob != nil {
				tt.checkJob(t, dir)
			}
		})
	}
}

func TestGenerateJobIDFromTitle(t *testing.T) {
	tests := []struct {
		name     string
		plan     *orchestration.Plan
		title    string
		expected string
	}{
		{
			name:     "simple title",
			plan:     &orchestration.Plan{},
			title:    "API Design",
			expected: "api-design",
		},
		{
			name:     "title with underscores",
			plan:     &orchestration.Plan{},
			title:    "api_design_v2",
			expected: "api-design-v2",
		},
		{
			name:     "title with special chars",
			plan:     &orchestration.Plan{},
			title:    "API Design (v2.0)",
			expected: "api-design-v20",
		},
		{
			name: "duplicate ID",
			plan: &orchestration.Plan{
				Jobs: []*orchestration.Job{
					{ID: "api-design"},
				},
			},
			title:    "API Design",
			expected: "api-design-2",
		},
		{
			name: "multiple duplicates",
			plan: &orchestration.Plan{
				Jobs: []*orchestration.Job{
					{ID: "api-design"},
					{ID: "api-design-2"},
				},
			},
			title:    "API Design",
			expected: "api-design-3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateJobIDFromTitle(tt.plan, tt.title)
			if got != tt.expected {
				t.Errorf("generateJobID() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestCollectJobDetails(t *testing.T) {
	tests := []struct {
		name    string
		cmd     *JobsAddStepCmd
		plan    *orchestration.Plan
		wantErr bool
		check   func(t *testing.T, job *orchestration.Job)
	}{
		{
			name: "valid non-interactive",
			cmd: &JobsAddStepCmd{
				Title:      "Test Job",
				Type:       "oneshot",
				PromptFile: createTempFile(t, "Test prompt"),
			},
			plan:    &orchestration.Plan{},
			wantErr: false,
			check: func(t *testing.T, job *orchestration.Job) {
				if job.Title != "Test Job" {
					t.Errorf("expected title 'Test Job', got %s", job.Title)
				}
				if job.Type != "oneshot" {
					t.Errorf("expected type 'oneshot', got %s", job.Type)
				}
				if !strings.Contains(job.PromptBody, "Test prompt") {
					t.Errorf("expected prompt to contain 'Test prompt'")
				}
			},
		},
		{
			name: "with dependencies",
			cmd: &JobsAddStepCmd{
				Title:      "Test Job",
				Type:       "agent",
				DependsOn:  []string{"01-initial.md"},
				PromptFile: createTempFile(t, "Test prompt"),
			},
			plan: &orchestration.Plan{
				Jobs: []*orchestration.Job{
					{
						ID:       "initial",
						Filename: "01-initial.md",
					},
				},
			},
			wantErr: false,
			check: func(t *testing.T, job *orchestration.Job) {
				if len(job.DependsOn) != 1 || job.DependsOn[0] != "01-initial.md" {
					t.Errorf("expected dependency on 01-initial.md")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job, err := collectJobDetails(tt.cmd, tt.plan)

			if (err != nil) != tt.wantErr {
				t.Errorf("collectJobDetails() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr && tt.check != nil {
				tt.check(t, job)
			}
		})
	}
}

func createTempFile(t *testing.T, content string) string {
	t.Helper()

	f, err := os.CreateTemp("", "prompt-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}

	return f.Name()
}

