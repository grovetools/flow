package add

import (
	"testing"

	"github.com/grovetools/flow/pkg/orchestration"
)

// TestInitialDepsPreselectsPicker covers the wizard half of "space a job in the
// status view, press A, the new job depends on it": the host passes the
// selection as InitialDeps (job filenames) and the dependency picker must come
// up with those jobs already checked, so submitting without touching the picker
// writes them to depends_on.
func TestInitialDepsPreselectsPicker(t *testing.T) {
	jobs := []*orchestration.Job{
		{ID: "j1", Filename: "j1.md", Title: "job one"},
		{ID: "j2", Filename: "j2.md", Title: "job two"},
		{ID: "j3", Filename: "j3.md", Title: "job three"},
	}
	plan := &orchestration.Plan{Name: "t", Jobs: jobs}

	m := New(Config{Plan: plan, InitialDeps: []string{"j2.md"}})

	if !m.selectedDeps["j2"] {
		t.Errorf("j2 not pre-selected in the dependency picker: selectedDeps = %v", m.selectedDeps)
	}
	if m.selectedDeps["j1"] || m.selectedDeps["j3"] {
		t.Errorf("unselected jobs leaked into the picker: selectedDeps = %v", m.selectedDeps)
	}

	m.extractValues()
	if len(m.jobDependencies) != 1 || m.jobDependencies[0] != "j2.md" {
		t.Errorf("jobDependencies = %v, want [j2.md]", m.jobDependencies)
	}
}
