package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/flow/pkg/orchestration"
)

func TestRunPlanAddStep(t *testing.T) {
	tests := []struct {
		name      string
		setupPlan func(t *testing.T, dir string)
		cmd       *PlanAddStepCmd
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
			cmd: &PlanAddStepCmd{
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
			cmd: &PlanAddStepCmd{
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
			cmd: &PlanAddStepCmd{
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
			cmd: &PlanAddStepCmd{
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
			cmd: &PlanAddStepCmd{
				Type:       "oneshot",
				Title:      "Test Job",
				DependsOn:  []string{"nonexistent.md"},
				PromptFile: createTempFile(t, "Some prompt"),
			},
			wantErr: true,
		},
		{
			name: "missing prompt and template",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan"}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanAddStepCmd{
				Type:  "oneshot",
				Title: "Test Job",
			},
			wantErr: true,
		},
		{
			name: "auto-create plan directory",
			setupPlan: func(t *testing.T, dir string) {
				// Don't create the plan directory - it should be auto-created
				// Remove the directory if it exists
				os.RemoveAll(dir)
			},
			cmd: &PlanAddStepCmd{
				Type:       "oneshot",
				Title:      "Test Auto-Create",
				PromptFile: createTempFile(t, "Test prompt"),
			},
			wantErr: false,
			checkJob: func(t *testing.T, dir string) {
				// Verify the directory was created
				if _, err := os.Stat(dir); os.IsNotExist(err) {
					t.Error("Plan directory was not created")
				}

				// Verify job was created
				files, err := filepath.Glob(filepath.Join(dir, "*.md"))
				if err != nil {
					t.Fatal(err)
				}
				if len(files) != 1 {
					t.Errorf("expected 1 job file, got %d", len(files))
				}
			},
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
			cmd: &PlanAddStepCmd{
				Type:         "oneshot",
				Title:        "Reference-based Step",
				Template:     "agent-run", // Assuming this built-in template exists
				IncludeFiles: []string{"source1.txt", "source2.txt"},
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

				// Verify job has template and include files
				if job.Template != "agent-run" {
					t.Errorf("Expected template 'agent-run', got '%s'", job.Template)
				}

				if len(job.Include) != 2 {
					t.Errorf("Expected 2 include files, got %d", len(job.Include))
				}

				// Check that prompt body contains template content
				if !strings.Contains(job.PromptBody, "Given a high level plan") {
					t.Error("Expected template content in prompt body")
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
			cmd: &PlanAddStepCmd{
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

				// Verify job has template
				if job.Template != "agent-run" {
					t.Errorf("Expected template 'agent-run', got '%s'", job.Template)
				}

				// Verify prompt body contains both template content and additional prompt
				if !strings.Contains(job.PromptBody, "Given a high level plan") {
					t.Error("Expected template content in prompt body")
				}
				if !strings.Contains(job.PromptBody, "Legacy prompt content") {
					t.Error("Expected additional prompt content in prompt body")
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
	t.Skip("Test uses removed generateJobIDFromTitle function")
}

func TestCollectJobDetails(t *testing.T) {
	tests := []struct {
		name    string
		cmd     *PlanAddStepCmd
		plan    *orchestration.Plan
		wantErr bool
		check   func(t *testing.T, job *orchestration.Job)
	}{
		{
			name: "valid non-interactive",
			cmd: &PlanAddStepCmd{
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
			cmd: &PlanAddStepCmd{
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
			job, err := collectJobDetails(tt.cmd, tt.plan, "")

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
