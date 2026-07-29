package status

import (
	"slices"
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

// skillJobModel builds a model over a job with a skill sequence, so the skill
// pane has nodes to navigate.
func skillJobModel(t *testing.T, hosted bool) Model {
	t.Helper()
	job := &orchestration.Job{
		ID:            "j1",
		Filename:      "j1.md",
		Title:         "job one",
		Type:          orchestration.JobTypeInteractiveAgent,
		Status:        orchestration.JobStatusCompleted,
		SkillSequence: []string{"alpha", "beta", "gamma"},
	}
	plan := &orchestration.Plan{
		Name:      "t",
		Directory: t.TempDir(),
		Jobs:      []*orchestration.Job{job},
		JobsByID:  map[string]*orchestration.Job{job.ID: job},
	}
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("BuildDependencyGraph: %v", err)
	}
	m := New(Config{Plan: plan, Graph: graph, Hosted: hosted})
	mdl, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	return mdl.(Model)
}

// TestSkillPanePromotesWithForwardedKeys: the skill pane opens as a BSP split
// like every other detail pane. It can only do that because it claims its
// cursor keys — an unclaimed pane would have no way to move a selection that
// lives in this model, not in the panel.
func TestSkillPanePromotesWithForwardedKeys(t *testing.T) {
	m := skillJobModel(t, true)
	m, cmd := press(t, m, "vs")

	if m.ActiveDetailPane != SkillPane {
		t.Fatalf("ActiveDetailPane = %d, want SkillPane", m.ActiveDetailPane)
	}
	if !m.Manager.IsPromoted("detail") {
		t.Error("skill pane should be promoted into a BSP split")
	}

	var req *embed.SplitViewportRequestMsg
	for _, msg := range collectMsgs(cmd) {
		if r, ok := msg.(embed.SplitViewportRequestMsg); ok {
			req = &r
		}
	}
	if req == nil {
		t.Fatal("no SplitViewportRequestMsg emitted")
	}
	if len(req.ForwardKeys) == 0 {
		t.Error("the skill pane must claim its cursor keys")
	}
	for _, k := range []string{"j", "k", "enter"} {
		if !slices.Contains(req.ForwardKeys, k) {
			t.Errorf("claimed keys missing %q: %v", k, req.ForwardKeys)
		}
	}
	// Scrolling stays with the panel — it owns the offset.
	for _, k := range []string{"ctrl+d", "ctrl+u", "g", "G"} {
		if slices.Contains(req.ForwardKeys, k) {
			t.Errorf("%q must stay with the panel so the body can be scrolled", k)
		}
	}
	if !req.Focus {
		t.Error("the skill pane must open focused; an unfocused pane is sent no keys to forward")
	}
	if req.Content == "" {
		t.Error("the body is rendered synchronously and should travel with the request")
	}
}

// TestSkillPaneForwardedKeyMovesCursor: a forwarded key drives the tree and
// answers with a re-rendered body that tells the panel to follow the cursor.
func TestSkillPaneForwardedKeyMovesCursor(t *testing.T) {
	m := skillJobModel(t, true)
	m, _ = press(t, m, "vs")
	if len(m.skillPaneNodes) < 2 {
		t.Skipf("need at least 2 skill nodes, got %d", len(m.skillPaneNodes))
	}
	before := m.skillPaneCursor

	mdl, cmd := m.Update(embed.ViewportKeyMsg{Key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}})
	m = mdl.(Model)

	if m.skillPaneCursor != before+1 {
		t.Errorf("forwarded j should move the tree cursor; %d -> %d", before, m.skillPaneCursor)
	}
	var push *embed.UpdateViewportContentMsg
	for _, msg := range collectMsgs(cmd) {
		if p, ok := msg.(embed.UpdateViewportContentMsg); ok {
			push = &p
		}
	}
	if push == nil {
		t.Fatal("moving the cursor must push a re-rendered body")
	}
	if push.EnsureVisible <= 0 {
		t.Errorf("the push must ask the panel to follow the cursor; EnsureVisible = %d", push.EnsureVisible)
	}
	if push.EnsureVisible != m.skillPaneCursorLine {
		t.Errorf("EnsureVisible = %d, want the cursor's line %d", push.EnsureVisible, m.skillPaneCursorLine)
	}
}

// TestSkillPaneForwardedEscCloses: the split holds focus, so esc only reaches
// this model as a forwarded key — and it still has to mean "close the pane".
func TestSkillPaneForwardedEscCloses(t *testing.T) {
	m := skillJobModel(t, true)
	m, _ = press(t, m, "vs")

	mdl, _ := m.Update(embed.ViewportKeyMsg{Key: tea.KeyMsg{Type: tea.KeyEsc}})
	m = mdl.(Model)

	if m.ActiveDetailPane != NoPane {
		t.Errorf("forwarded esc should close the skill pane; ActiveDetailPane = %d", m.ActiveDetailPane)
	}
	if m.Manager.IsPromoted("detail") {
		t.Error("closing must demote the split")
	}
}

// TestForwardedKeyIgnoredForOtherPanes: only the skill pane claims keys, so a
// forward arriving after the slot changed hands must not drive anything.
func TestForwardedKeyIgnoredForOtherPanes(t *testing.T) {
	m := skillJobModel(t, true)
	m, _ = press(t, m, "vt")
	if m.ActiveDetailPane != TokenPaneDetail {
		t.Fatalf("precondition: ActiveDetailPane = %d", m.ActiveDetailPane)
	}
	before := m.skillPaneCursor

	mdl, cmd := m.Update(embed.ViewportKeyMsg{Key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}})
	m = mdl.(Model)

	if m.skillPaneCursor != before {
		t.Error("a stale forward must not move the skill cursor")
	}
	if cmd != nil {
		t.Error("a stale forward should produce no command")
	}
}

// TestSkillPaneUnhostedStaysInline: with no host there is no split to claim
// keys from, so the internal two-viewport pane must still work.
func TestSkillPaneUnhostedStaysInline(t *testing.T) {
	m := skillJobModel(t, false)
	m, _ = press(t, m, "vs")

	if m.ActiveDetailPane != SkillPane {
		t.Fatalf("ActiveDetailPane = %d, want SkillPane", m.ActiveDetailPane)
	}
	if m.Manager.IsPromoted("detail") {
		t.Error("nothing to promote into when unhosted")
	}
	if !m.ShowLogs {
		t.Error("the internal skill pane should be visible")
	}
}
