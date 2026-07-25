package view

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/tui/embed"
	planinit "github.com/grovetools/flow/pkg/tui/wizards/init"
)

// creationInFlightModel drives a Model to the exact state the Add Plan tab is
// in while `flow plan init` runs: the wizard model has been discarded by the
// embed.DoneMsg handler and initProgress is set. The returned tea.Cmd batch is
// deliberately NOT executed — it would shell out to the real `flow` binary.
func creationInFlightModel(t *testing.T, width, height int) Model {
	t.Helper()
	m := New(Config{PlansDir: t.TempDir()})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = updated.(Model)
	m.mode = modeInitWizard
	m.pager, _ = m.pager.Update(embed.SwitchTabMsg{TabIndex: tabAddPlan})

	updated, cmd := m.Update(embed.DoneMsg{Result: &planinit.Request{Dir: "probe"}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("submitting the init wizard did not launch the creation subprocess")
	}
	if m.s.initWizardModel != nil || m.s.initProgress == "" {
		t.Fatalf("fixture did not reach the in-flight state: wizard=%v progress=%q", m.s.initWizardModel, m.s.initProgress)
	}
	if m.pager.ActiveIndex() != tabAddPlan {
		t.Fatalf("creation left the pager on tab %d, want Add Plan (%d)", m.pager.ActiveIndex(), tabAddPlan)
	}
	return m
}

// TestAddPlanRendersLiveCreationOutputThroughThePager asserts through
// Model.View() rather than addPlanPage.View(), so the render actually passes
// the pager's Ready() gate. A test that calls addPlanPage.View() directly
// cannot see this defect: the placeholder is substituted by the pager instead
// of ever calling the page's View.
func TestAddPlanRendersLiveCreationOutputThroughThePager(t *testing.T) {
	m := creationInFlightModel(t, 120, 40)

	frame := m.View()
	for _, want := range []string{"Creating plan probe", "Creation Output", "Waiting for flow plan init output"} {
		if !strings.Contains(frame, want) {
			t.Errorf("pre-output frame is missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "Loading wizard") {
		t.Errorf("pre-output frame still shows the wizard placeholder:\n%s", frame)
	}

	// Real subprocess stdout: these lines cannot be produced by the pre-confirm
	// review screen, by a spinner, or by any "something changed" heuristic.
	updated, _ := m.Update(initOutputTickMsg{
		path: m.s.initOutputPath,
		content: "relay: creating linked worktree\n" +
			"\uf0133 Applied default context rules to: /tmp/x/probe/beacon\n" +
			"* Created worktree: probe\n",
	})
	m = updated.(Model)

	frame = m.View()
	for _, want := range []string{"creating linked worktree", "Applied default context rules to", "Created worktree: probe"} {
		if !strings.Contains(frame, want) {
			t.Errorf("live creation output is missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "Loading wizard") {
		t.Errorf("live creation frame still shows the wizard placeholder:\n%s", frame)
	}
}

// TestAddPlanPageIsReadyWhileCreationIsInFlight guards the invariant directly:
// the page has a legitimate second surface that exists only when the wizard
// model is nil, so Ready() must not key off the wizard model alone.
func TestAddPlanPageIsReadyWhileCreationIsInFlight(t *testing.T) {
	s := &viewState{initProgress: "Creating plan probe…"}
	if ready, _ := (&addPlanPage{s: s}).Ready(); !ready {
		t.Fatal("Add Plan page reports not-ready while a creation is in flight; the pager will hide the progress surface")
	}
	// The genuine wizard-rebuild window must still gate.
	if ready, msg := (&addPlanPage{s: &viewState{}}).Ready(); ready || msg == "" {
		t.Fatalf("idle page must still gate on the wizard build: ready=%v msg=%q", ready, msg)
	}
}

// TestAddPlanCreationOutputFitsANarrowPane covers the one sizing risk in the
// progress surface: View() floors the output box at 6 lines / 40 columns, which
// in a narrow BSP split could exceed the body the pager forces. Measured
// against the pager's SubSize rather than the whole frame, so the pager's own
// tab-row chrome (which already overflows narrow panes independently of this
// page) does not mask or fake the result.
func TestAddPlanCreationOutputFitsANarrowPane(t *testing.T) {
	for _, pane := range [][2]int{{60, 14}, {36, 12}, {120, 40}} {
		width, height := pane[0], pane[1]
		m := creationInFlightModel(t, width, height)
		updated, _ := m.Update(initOutputTickMsg{
			path:    m.s.initOutputPath,
			content: strings.Repeat("relay: creating linked worktree\n", 40),
		})
		m = updated.(Model)

		sub := m.pager.SubSize(width, height)
		body := (&addPlanPage{s: m.s, width: sub.Width, height: sub.Height}).View()
		if !strings.Contains(body, "Creation Output") {
			t.Fatalf("%dx%d pane did not render the progress surface:\n%s", width, height, body)
		}
		if h := lipgloss.Height(body); h > sub.Height {
			t.Errorf("creation surface is %d lines tall in a %dx%d body (overflow, pane %dx%d)", h, sub.Width, sub.Height, width, height)
		}
		if w := lipgloss.Width(body); w > sub.Width {
			t.Errorf("creation surface is %d columns wide in a %dx%d body (overflow, pane %dx%d)", w, sub.Width, sub.Height, width, height)
		}
	}
}
