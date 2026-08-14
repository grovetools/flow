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
				Type:       "headless_agent",
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
			name: "parent job ID persists as lineage without dependency",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan", Jobs: []*orchestration.Job{{
					ID: "parent-id", Title: "Parent", Filename: "01-parent.md", Type: "interactive_agent", Status: "running",
				}}}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanAddStepCmd{
				Type: "interactive_agent", Title: "Child", ParentJobID: "parent-id", Provider: "pi",
				PromptFile: createTempFile(t, "Do child work"),
			},
			wantErr: false,
			checkJob: func(t *testing.T, dir string) {
				job := findJobByTitle(t, dir, "Child")
				if job.ParentJobID != "parent-id" {
					t.Errorf("ParentJobID = %q, want parent-id", job.ParentJobID)
				}
				if len(job.DependsOn) != 0 {
					t.Errorf("parent lineage changed dependencies: %v", job.DependsOn)
				}
				if !job.IsRunnable() {
					t.Error("child with a running parent must remain runnable")
				}
			},
		},
		{
			// flow_subjob may hand a child any allowlisted provider plus an
			// optional model. Ownership lineage is provider-agnostic: a claude
			// child records provider/model and parent_job_id exactly like a Pi
			// one, without gaining a dependency.
			name: "claude subjob child persists provider, model, and lineage",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan", Jobs: []*orchestration.Job{{
					ID: "parent-id", Title: "Parent", Filename: "01-parent.md", Type: "interactive_agent", Status: "running",
				}}}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanAddStepCmd{
				Type: "interactive_agent", Title: "Claude Child", ParentJobID: "parent-id",
				Provider: "claude", Model: "claude-opus-4-8",
				PromptFile: createTempFile(t, "Do child work"),
			},
			wantErr: false,
			checkJob: func(t *testing.T, dir string) {
				job := findJobByTitle(t, dir, "Claude Child")
				if job.Provider != "claude" {
					t.Errorf("Provider = %q, want claude", job.Provider)
				}
				if job.Model != "claude-opus-4-8" {
					t.Errorf("Model = %q, want claude-opus-4-8", job.Model)
				}
				if job.ParentJobID != "parent-id" {
					t.Errorf("ParentJobID = %q, want parent-id", job.ParentJobID)
				}
				if len(job.DependsOn) != 0 {
					t.Errorf("parent lineage changed dependencies: %v", job.DependsOn)
				}
				if !job.IsRunnable() {
					t.Error("child with a running parent must remain runnable")
				}
			},
		},
		{
			// The pi provider takes any model its CLI accepts (no
			// ValidateJobModel hook), so a non-claude model must survive.
			name: "pi subjob child accepts a provider-native model",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan", Jobs: []*orchestration.Job{{
					ID: "parent-id", Title: "Parent", Filename: "01-parent.md", Type: "interactive_agent", Status: "running",
				}}}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanAddStepCmd{
				Type: "interactive_agent", Title: "Pi Child", ParentJobID: "parent-id",
				Provider: "pi", Model: "gpt-5.6-sol",
				PromptFile: createTempFile(t, "Do child work"),
			},
			wantErr: false,
			checkJob: func(t *testing.T, dir string) {
				job := findJobByTitle(t, dir, "Pi Child")
				if job.Provider != "pi" || job.Model != "gpt-5.6-sol" {
					t.Errorf("provider/model = %q/%q, want pi/gpt-5.6-sol", job.Provider, job.Model)
				}
			},
		},
		{
			// Submit-time model validation is provider-aware: the claude CLI
			// cannot run a Pi-family model, so the mismatch must fail at add.
			name: "provider/model mismatch on a subjob child errors",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan", Jobs: []*orchestration.Job{{
					ID: "parent-id", Title: "Parent", Filename: "01-parent.md", Type: "interactive_agent", Status: "running",
				}}}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanAddStepCmd{
				Type: "interactive_agent", Title: "Mismatched Child", ParentJobID: "parent-id",
				Provider: "claude", Model: "gpt-5.6-sol",
				PromptFile: createTempFile(t, "Do child work"),
			},
			wantErr: true,
		},
		{
			name: "unknown provider on a subjob child errors",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan", Jobs: []*orchestration.Job{{
					ID: "parent-id", Title: "Parent", Filename: "01-parent.md", Type: "interactive_agent", Status: "running",
				}}}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanAddStepCmd{
				Type: "interactive_agent", Title: "Unknown Provider Child", ParentJobID: "parent-id",
				Provider:   "not-a-provider",
				PromptFile: createTempFile(t, "Do child work"),
			},
			wantErr: true,
		},
		{
			name: "invalid parent job ID",
			setupPlan: func(t *testing.T, dir string) {
				if err := orchestration.SavePlan(dir, &orchestration.Plan{Name: "test-plan"}); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanAddStepCmd{
				Type: "interactive_agent", Title: "Child", ParentJobID: "missing", Provider: "pi",
				PromptFile: createTempFile(t, "Do child work"),
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
			name: "responder agent with non-chat type errors",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan"}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanAddStepCmd{
				Type:       "oneshot",
				Title:      "Agent Responded Oneshot",
				Responder:  "agent",
				PromptFile: createTempFile(t, "Some prompt"),
			},
			wantErr: true,
		},
		{
			name: "invalid responder value errors",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan"}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanAddStepCmd{
				Type:       "chat",
				Title:      "Bad Responder",
				Responder:  "human",
				PromptFile: createTempFile(t, "Some prompt"),
			},
			wantErr: true,
		},
		{
			name: "responder agent with chat type creates agent-responded chat with chat-agent template",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan"}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanAddStepCmd{
				Type:       "chat",
				Title:      "Design Chat",
				Responder:  "agent",
				PromptFile: createTempFile(t, "Design the feature"),
			},
			wantErr: false,
			checkJob: func(t *testing.T, dir string) {
				plan, err := orchestration.LoadPlan(dir)
				if err != nil {
					t.Fatal(err)
				}
				var job *orchestration.Job
				for _, j := range plan.Jobs {
					if j.Title == "Design Chat" {
						job = j
						break
					}
				}
				if job == nil {
					t.Fatal("Created job not found")
				}
				if job.Responder != "agent" {
					t.Errorf("Expected responder 'agent', got %q", job.Responder)
				}
				if !job.IsAgentResponded() {
					t.Error("Expected job to be agent-responded")
				}
				if job.Template != "chat-agent" {
					t.Errorf("Expected default template 'chat-agent' for --responder agent, got %q", job.Template)
				}
			},
		},
		{
			name: "responder oracle with chat type succeeds and behaves like absent responder",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan"}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanAddStepCmd{
				Type:       "chat",
				Title:      "Oracle Chat",
				Responder:  "oracle",
				PromptFile: createTempFile(t, "Discuss the feature"),
			},
			wantErr: false,
			checkJob: func(t *testing.T, dir string) {
				plan, err := orchestration.LoadPlan(dir)
				if err != nil {
					t.Fatal(err)
				}
				var job *orchestration.Job
				for _, j := range plan.Jobs {
					if j.Title == "Oracle Chat" {
						job = j
						break
					}
				}
				if job == nil {
					t.Fatal("Created job not found")
				}
				// Either no responder frontmatter or an explicit
				// "responder: oracle" is acceptable; the dispatch guard
				// must treat both identically to an absent field.
				if job.Responder != "" && job.Responder != "oracle" {
					t.Errorf("Expected responder empty or 'oracle', got %q", job.Responder)
				}
				if job.IsAgentResponded() {
					t.Error("responder: oracle must not be treated as agent-responded (same dispatch path as absent field)")
				}
			},
		},
		{
			// Oracle chats (responder oracle/absent) with a dependency default
			// inline: dependencies ON so the tool-less LLM actually sees them.
			name: "oracle chat with deps defaults inline dependencies",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{
					Name: "test-plan",
					Jobs: []*orchestration.Job{
						{
							ID:       "spec",
							Title:    "spec",
							Filename: "01-spec.md",
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
				Type:       "chat",
				Title:      "Oracle Chat With Deps",
				DependsOn:  []string{"01-spec.md"},
				PromptFile: createTempFile(t, "Discuss the spec"),
			},
			wantErr: false,
			checkJob: func(t *testing.T, dir string) {
				job := findJobByTitle(t, dir, "Oracle Chat With Deps")
				if !hasInlineCategory(job, orchestration.InlineDependencies) {
					t.Errorf("expected inline: dependencies to be defaulted, got %+v", job.Inline.Categories)
				}
			},
		},
		{
			// responder: agent chats read files from disk — they must NOT get
			// the inline default (the lint warns the other direction).
			name: "agent chat with deps does not default inline",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{
					Name: "test-plan",
					Jobs: []*orchestration.Job{
						{
							ID:       "spec",
							Title:    "spec",
							Filename: "01-spec.md",
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
				Type:       "chat",
				Title:      "Agent Chat With Deps",
				Responder:  "agent",
				DependsOn:  []string{"01-spec.md"},
				PromptFile: createTempFile(t, "Design the feature"),
			},
			wantErr: false,
			checkJob: func(t *testing.T, dir string) {
				job := findJobByTitle(t, dir, "Agent Chat With Deps")
				if hasInlineCategory(job, orchestration.InlineDependencies) {
					t.Errorf("responder: agent chat must not default inline dependencies, got %+v", job.Inline.Categories)
				}
			},
		},
		{
			// An explicit --inline value always wins over the oracle default.
			name: "explicit inline none on oracle chat wins",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{
					Name: "test-plan",
					Jobs: []*orchestration.Job{
						{
							ID:       "spec",
							Title:    "spec",
							Filename: "01-spec.md",
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
				Type:       "chat",
				Title:      "Explicit None Chat",
				DependsOn:  []string{"01-spec.md"},
				Inline:     []string{"none"},
				PromptFile: createTempFile(t, "Discuss the spec"),
			},
			wantErr: false,
			checkJob: func(t *testing.T, dir string) {
				job := findJobByTitle(t, dir, "Explicit None Chat")
				if hasInlineCategory(job, orchestration.InlineDependencies) {
					t.Errorf("explicit --inline none must win over the default, got %+v", job.Inline.Categories)
				}
			},
		},
		{
			// A dependency-free oracle chat has nothing to inline.
			name: "oracle chat without deps does not default inline",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan"}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanAddStepCmd{
				Type:       "chat",
				Title:      "Depless Chat",
				PromptFile: createTempFile(t, "Just chat"),
			},
			wantErr: false,
			checkJob: func(t *testing.T, dir string) {
				job := findJobByTitle(t, dir, "Depless Chat")
				if hasInlineCategory(job, orchestration.InlineDependencies) {
					t.Errorf("dependency-free chat must not default inline, got %+v", job.Inline.Categories)
				}
			},
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
				_ = os.WriteFile(filepath.Join(dir, "source1.txt"), []byte("Source 1 content"), 0o600)
				_ = os.WriteFile(filepath.Join(dir, "source2.txt"), []byte("Source 2 content"), 0o600)
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
		{
			// A self-contained oneshot in a caller-owned plan directory: with
			// --no-context nothing stamps a rules_file, so nothing can send the
			// run into the unauthored-rules funnel at execution time.
			name: "no-context oneshot stamps no rules file",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan"}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanAddStepCmd{
				Type:      "oneshot",
				Title:     "Self Contained",
				NoContext: true,
				Prompt:    "Summarize this sealed work block.",
			},
			wantErr: false,
			checkJob: func(t *testing.T, dir string) {
				job := findJobByTitle(t, dir, "Self Contained")
				if job.RulesFile != "" {
					t.Errorf("RulesFile = %q, want empty for a no_context job", job.RulesFile)
				}
				if !job.NoContext {
					t.Error("NoContext = false; want the flag persisted to frontmatter")
				}
				if _, err := os.Stat(filepath.Join(dir, "rules")); !os.IsNotExist(err) {
					t.Errorf("rules dir stat = %v; want absent for a no_context job", err)
				}
			},
		},
		{
			// The two flags contradict each other; one would have to be
			// silently dropped at run time, so the add is refused instead.
			name: "no-context with an explicit rules file errors",
			setupPlan: func(t *testing.T, dir string) {
				plan := &orchestration.Plan{Name: "test-plan"}
				if err := orchestration.SavePlan(dir, plan); err != nil {
					t.Fatal(err)
				}
			},
			cmd: &PlanAddStepCmd{
				Type:      "oneshot",
				Title:     "Contradiction",
				NoContext: true,
				RulesFile: "custom.rules",
				Prompt:    "Do the thing.",
			},
			wantErr: true,
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
				Type:       "headless_agent",
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
		{
			// oneshot jobs DO inherit the plan-level default model.
			name: "oneshot inherits plan default model",
			cmd: &PlanAddStepCmd{
				Title:      "Oneshot Job",
				Type:       "oneshot",
				PromptFile: createTempFile(t, "Test prompt"),
			},
			plan: &orchestration.Plan{
				Config: &orchestration.PlanConfig{Model: "gemini-3.1-pro-preview"},
			},
			wantErr: false,
			check: func(t *testing.T, job *orchestration.Job) {
				if job.Model != "gemini-3.1-pro-preview" {
					t.Errorf("expected oneshot to inherit plan model, got %q", job.Model)
				}
			},
		},
		{
			// Agent jobs must NOT be stamped with the chat/oneshot default —
			// they run a CLI agent whose model is backfilled at launch.
			name: "agent job does not inherit plan default model",
			cmd: &PlanAddStepCmd{
				Title:      "Agent Job",
				Type:       "headless_agent",
				PromptFile: createTempFile(t, "Test prompt"),
			},
			plan: &orchestration.Plan{
				Config: &orchestration.PlanConfig{Model: "gemini-3.1-pro-preview"},
			},
			wantErr: false,
			check: func(t *testing.T, job *orchestration.Job) {
				if job.Model != "" {
					t.Errorf("expected agent job model to stay empty, got %q", job.Model)
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

// findJobByTitle loads the plan in dir and returns the job with the given
// title, failing the test if it is not found.
func findJobByTitle(t *testing.T, dir, title string) *orchestration.Job {
	t.Helper()
	plan, err := orchestration.LoadPlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range plan.Jobs {
		if j.Title == title {
			return j
		}
	}
	t.Fatalf("job %q not found", title)
	return nil
}

// hasInlineCategory reports whether the job's inline config includes cat.
func hasInlineCategory(job *orchestration.Job, cat orchestration.InlineCategory) bool {
	for _, c := range job.Inline.Categories {
		if c == cat {
			return true
		}
	}
	return false
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
