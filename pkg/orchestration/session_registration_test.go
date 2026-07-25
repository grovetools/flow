package orchestration

import (
	"testing"

	"github.com/grovetools/core/pkg/models"
)

func TestNewAgentSessionIntentCarriesParentJobID(t *testing.T) {
	job := &Job{
		ID:           "child-job",
		ParentJobID:  "parent-job",
		FilePath:     "/plan/02-child.md",
		Title:        "Child",
		Channels:     []string{"signal"},
		SignalTarget: "team",
	}
	plan := &Plan{Name: "plan"}

	got := newAgentSessionIntent(job, plan, "pi", "/worktree", models.MuxNone)
	if got.ParentJobID != job.ParentJobID {
		t.Errorf("ParentJobID = %q, want %q", got.ParentJobID, job.ParentJobID)
	}
	if got.JobID != job.ID || got.Provider != "pi" || got.PlanName != plan.Name || got.Mux != models.MuxNone {
		t.Errorf("unexpected session intent: %+v", got)
	}
}
