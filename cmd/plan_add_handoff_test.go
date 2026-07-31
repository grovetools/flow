package cmd

import (
	"testing"

	"github.com/grovetools/flow/pkg/orchestration"
)

// The successor of a coordinator handoff is created by shelling out to
// `flow plan add`, so these cover the exact argument shapes the grove-pi
// flow_handoff tool emits.
func TestRunPlanAddStepHandoff(t *testing.T) {
	predecessor := func(t *testing.T, dir string) {
		t.Helper()
		plan := &orchestration.Plan{Name: "test-plan", Jobs: []*orchestration.Job{{
			ID: "coord-1", Title: "Coordinator", Filename: "01-coordinator.md",
			Type: "interactive_agent", Status: "running", Provider: "pi",
			CoordMode: orchestration.CoordModeAutonomous, HandoffMax: 3,
		}}}
		if err := orchestration.SavePlan(dir, plan); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name     string
		cmd      *PlanAddStepCmd
		wantErr  bool
		checkJob func(t *testing.T, dir string)
	}{
		{
			name: "successor records lineage, depth and budget",
			cmd: &PlanAddStepCmd{
				Type: "interactive_agent", Title: "Coordinator (handoff 1)", Provider: "pi",
				CoordMode: "autonomous", HandoffFrom: "coord-1", HandoffDepth: 1, HandoffMax: 3,
				PromptFile: createTempFile(t, "Continue the work"),
			},
			checkJob: func(t *testing.T, dir string) {
				job := findJobByTitle(t, dir, "Coordinator (handoff 1)")
				if job.HandoffFrom != "coord-1" || job.HandoffDepth != 1 || job.HandoffMax != 3 {
					t.Errorf("handoff frontmatter = %q/%d/%d, want coord-1/1/3", job.HandoffFrom, job.HandoffDepth, job.HandoffMax)
				}
				if !job.IsAutonomousCoordinator() {
					t.Error("successor of an autonomous coordinator must stay autonomous")
				}
				if len(job.DependsOn) != 0 {
					t.Errorf("handoff lineage must not create dependencies: %v", job.DependsOn)
				}
			},
		},
		{
			name: "autonomous coordinator materializes the default bound",
			cmd: &PlanAddStepCmd{
				Type: "interactive_agent", Title: "Fresh Coordinator", Provider: "pi",
				CoordMode: "autonomous", PromptFile: createTempFile(t, "Coordinate"),
			},
			checkJob: func(t *testing.T, dir string) {
				job := findJobByTitle(t, dir, "Fresh Coordinator")
				if job.HandoffMax == 0 {
					t.Error("an autonomous coordinator must carry an explicit handoff_max")
				}
			},
		},
		{
			name: "manual coordinator stays clean",
			cmd: &PlanAddStepCmd{
				Type: "interactive_agent", Title: "Manual Coordinator", Provider: "pi",
				PromptFile: createTempFile(t, "Coordinate"),
			},
			checkJob: func(t *testing.T, dir string) {
				job := findJobByTitle(t, dir, "Manual Coordinator")
				if job.CoordMode != "" || job.HandoffMax != 0 || job.HandoffDepth != 0 {
					t.Errorf("unrequested handoff frontmatter written: %+v", job)
				}
				if job.IsAutonomousCoordinator() {
					t.Error("a job must never become autonomous implicitly")
				}
			},
		},
		{
			name: "handoff past the budget is refused",
			cmd: &PlanAddStepCmd{
				Type: "interactive_agent", Title: "Runaway", Provider: "pi",
				CoordMode: "autonomous", HandoffFrom: "coord-1", HandoffDepth: 4, HandoffMax: 3,
				PromptFile: createTempFile(t, "Continue forever"),
			},
			wantErr: true,
		},
		{
			name: "unknown predecessor is refused",
			cmd: &PlanAddStepCmd{
				Type: "interactive_agent", Title: "Orphan", Provider: "pi",
				HandoffFrom: "not-a-job", HandoffDepth: 1,
				PromptFile: createTempFile(t, "Continue the work"),
			},
			wantErr: true,
		},
		{
			name: "unknown coord mode is refused",
			cmd: &PlanAddStepCmd{
				Type: "interactive_agent", Title: "Bad Mode", Provider: "pi",
				CoordMode: "semi", PromptFile: createTempFile(t, "Coordinate"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			predecessor(t, dir)
			tt.cmd.Dir = dir
			err := RunPlanAddStep(tt.cmd)
			if (err != nil) != tt.wantErr {
				t.Fatalf("RunPlanAddStep() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && tt.checkJob != nil {
				tt.checkJob(t, dir)
			}
		})
	}
}
