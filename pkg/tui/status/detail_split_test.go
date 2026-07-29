package status

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/tuimux/embed"

	"github.com/grovetools/flow/pkg/orchestration"
)

// newAgentJobModel builds a status Model over a single interactive_agent job,
// sized so the pane Manager has real dimensions.
func newAgentJobModel(t *testing.T, hosted bool) Model {
	t.Helper()
	job := &orchestration.Job{
		ID:       "j1",
		Filename: "j1.md",
		Title:    "job one",
		Type:     orchestration.JobTypeInteractiveAgent,
		Status:   orchestration.JobStatusCompleted,
	}
	plan := &orchestration.Plan{
		Name:     "t",
		Jobs:     []*orchestration.Job{job},
		JobsByID: map[string]*orchestration.Job{job.ID: job},
	}
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("BuildDependencyGraph: %v", err)
	}
	m := New(Config{Plan: plan, Graph: graph, Hosted: hosted})
	mdl, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	return mdl.(Model)
}

// press sends a chord one rune at a time and returns the final model plus the
// last command produced.
func press(t *testing.T, m Model, keys string) (Model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, r := range keys {
		var mdl tea.Model
		mdl, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mdl.(Model)
	}
	return m, cmd
}

// collectMsgs drains a (possibly batched) tea.Cmd into the messages it emits.
func collectMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch v := msg.(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range v {
			out = append(out, collectMsgs(c)...)
		}
		return out
	default:
		return []tea.Msg{msg}
	}
}

// TestHostedDetailPanesPromoteToSplit: every read-only detail pane opens as a
// host BSP split when hosted, not as the built-in bubbletea detail pane. The
// token (vt) and accessed-files (vy) panes used to render inline while
// frontmatter/briefing/logs got splits — an inconsistent UX.
func TestHostedDetailPanesPromoteToSplit(t *testing.T) {
	cases := []struct {
		keys string
		pane DetailPane
	}{
		{"vf", FrontmatterPane},
		{"vb", BriefingPane},
		{"vt", TokenPaneDetail},
		{"vy", AccessedFilesPaneDetail},
	}
	for _, tc := range cases {
		m := newAgentJobModel(t, true)
		m, cmd := press(t, m, tc.keys)

		if m.ActiveDetailPane != tc.pane {
			t.Errorf("%q: ActiveDetailPane = %d, want %d", tc.keys, m.ActiveDetailPane, tc.pane)
		}
		if !m.Manager.IsPromoted("detail") {
			t.Errorf("%q: detail slot not promoted — pane rendered inline instead of a BSP split", tc.keys)
		}
		if !m.viewportActive {
			t.Errorf("%q: viewportActive = false, want true", tc.keys)
		}
		if m.ShowLogs {
			t.Errorf("%q: ShowLogs = true — the internal pane should stay hidden when promoted", tc.keys)
		}

		var sawSplit bool
		for _, msg := range collectMsgs(cmd) {
			if req, ok := msg.(embed.SplitViewportRequestMsg); ok {
				sawSplit = true
				if req.Title == "" {
					t.Errorf("%q: SplitViewportRequestMsg has an empty title", tc.keys)
				}
			}
		}
		if !sawSplit {
			t.Errorf("%q: no SplitViewportRequestMsg emitted", tc.keys)
		}
	}
}

// TestUnhostedDetailPanesStayInline: without a host there is nothing to split
// into, so the same panes must still render in the built-in detail pane.
func TestUnhostedDetailPanesStayInline(t *testing.T) {
	for _, tc := range []struct {
		keys string
		pane DetailPane
	}{
		{"vt", TokenPaneDetail},
		{"vy", AccessedFilesPaneDetail},
	} {
		m := newAgentJobModel(t, false)
		m, _ = press(t, m, tc.keys)

		if m.ActiveDetailPane != tc.pane {
			t.Errorf("%q: ActiveDetailPane = %d, want %d", tc.keys, m.ActiveDetailPane, tc.pane)
		}
		if m.Manager.IsPromoted("detail") {
			t.Errorf("%q: detail slot promoted with no host", tc.keys)
		}
		if !m.ShowLogs {
			t.Errorf("%q: ShowLogs = false — the internal pane should be visible when unhosted", tc.keys)
		}
	}
}

// TestEscClosesNonLogDetailPaneOnAgentJob: esc must close an inert detail pane
// even when the job is an agent. The double-esc "interrupt the agent" gesture
// belongs to the log pane alone; when it applied to every pane, the token /
// accessed-files / skill panes were unclosable on agent jobs — the first esc
// only armed the hint and the second interrupted the agent.
func TestEscClosesNonLogDetailPaneOnAgentJob(t *testing.T) {
	for _, tc := range []struct {
		keys string
		pane DetailPane
	}{
		{"vt", TokenPaneDetail},
		{"vy", AccessedFilesPaneDetail},
		{"vs", SkillPane},
	} {
		// Unhosted: these render inline, which is the path the double-esc
		// guard used to swallow.
		m := newAgentJobModel(t, false)
		m, _ = press(t, m, tc.keys)
		if m.ActiveDetailPane != tc.pane {
			t.Fatalf("%q: precondition failed, ActiveDetailPane = %d", tc.keys, m.ActiveDetailPane)
		}

		mdl, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = mdl.(Model)

		if m.ActiveDetailPane != NoPane {
			t.Errorf("%q: esc left ActiveDetailPane = %d, want NoPane", tc.keys, m.ActiveDetailPane)
		}
		if m.ShowLogs {
			t.Errorf("%q: esc left the detail pane visible", tc.keys)
		}
	}
}

// TestEscOnLogPaneStillArmsAgentInterrupt: the double-esc interrupt survives
// on the pane that owns it.
func TestEscOnLogPaneStillArmsAgentInterrupt(t *testing.T) {
	m := newAgentJobModel(t, false)
	m, _ = press(t, m, "vl")
	if m.ActiveDetailPane != LogsPaneDetail {
		t.Fatalf("precondition failed: ActiveDetailPane = %d", m.ActiveDetailPane)
	}

	mdl, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mdl.(Model)

	if m.ActiveDetailPane != LogsPaneDetail {
		t.Errorf("first esc on the log pane closed it; it should arm the interrupt hint")
	}
	if m.LastEscPress.IsZero() {
		t.Errorf("first esc on the log pane did not arm the double-esc interrupt")
	}
}
