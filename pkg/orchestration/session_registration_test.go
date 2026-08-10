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

// The session type is what tells a host whether an agent has a terminal to
// attach or only a transcript to stream. Registering a headless job as
// interactive is what made treemux open an empty shell when you clicked it in
// the drawer, so the two builders must not agree on this field.
func TestSessionIntentTypesDistinguishHeadlessFromInteractive(t *testing.T) {
	job := &Job{ID: "phase0-member-audit", Title: "phase0-member-audit", FilePath: "/plans/p/02-phase0.md"}
	plan := &Plan{Name: "grove-build-cache"}

	interactive := newAgentSessionIntent(job, plan, "claude", "/wt", models.MuxTreemux)
	if interactive.Type != models.SessionTypeInteractiveAgent {
		t.Errorf("interactive intent Type = %q, want %q", interactive.Type, models.SessionTypeInteractiveAgent)
	}

	headless := newHeadlessSessionIntent(job, plan, "claude", "/wt")
	if headless.Type != models.SessionTypeHeadlessAgent {
		t.Errorf("headless intent Type = %q, want %q", headless.Type, models.SessionTypeHeadlessAgent)
	}
	if headless.Mux != models.MuxNone {
		t.Errorf("headless intent Mux = %q, want %q", headless.Mux, models.MuxNone)
	}
	// Everything else is the shared payload.
	if headless.JobID != job.ID || headless.PlanName != plan.Name || headless.WorkDir != "/wt" {
		t.Errorf("headless intent lost shared fields: %+v", headless)
	}
}
