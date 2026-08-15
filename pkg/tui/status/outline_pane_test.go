package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/grovetools/tuimux/embed"

	"github.com/grovetools/flow/pkg/orchestration"
)

// outlineFixtureTranscript is a minimal Claude-format jsonl transcript: a user
// prompt, an assistant response with a Markdown heading plus a Bash tool call,
// and the matching tool result.
const outlineFixtureTranscript = `{"type":"user","message":{"role":"user","content":"Add an outline pane to the status TUI"}}
{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"## Plan\n\nI will wire the pane."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./pkg/tui/...","description":"Run tests"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}
`

// stubOutlineLookup replaces the hooks-registry session lookup for the test's
// lifetime.
func stubOutlineLookup(t *testing.T, path, provider, workDir string) {
	t.Helper()
	orig := outlineSessionLookup
	outlineSessionLookup = func(jobID string) (string, string, string) { return path, provider, workDir }
	t.Cleanup(func() { outlineSessionLookup = orig })
}

// newOutlineJobModel builds a status Model over one running agent job in a
// temp plan, with a fixture transcript on disk and the session lookup stubbed
// to resolve to it.
//
// toc.RenderStyled pins the process-global lipgloss color profile to ANSI256
// on first use (headless "styled" output would otherwise silently strip to
// plain). Tests in this package run in one process and several assert on the
// Ascii-profile rendering (e.g. the footer's plain-text scroll indicator), so
// restore whatever profile was active once the outline test is done.
func newOutlineJobModel(t *testing.T, hosted bool) (Model, string) {
	t.Helper()
	origProfile := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(origProfile) })
	planDir := t.TempDir()
	transcriptPath := filepath.Join(planDir, "session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(outlineFixtureTranscript), 0o644); err != nil {
		t.Fatalf("WriteFile transcript: %v", err)
	}

	job := &orchestration.Job{
		ID:       "j1",
		Filename: "j1.md",
		Title:    "job one",
		Type:     orchestration.JobTypeIsolatedAgent,
		Status:   orchestration.JobStatusRunning,
		Provider: "claude",
	}
	plan := &orchestration.Plan{
		Name:      "t",
		Directory: planDir,
		Jobs:      []*orchestration.Job{job},
		JobsByID:  map[string]*orchestration.Job{job.ID: job},
	}
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		t.Fatalf("BuildDependencyGraph: %v", err)
	}
	stubOutlineLookup(t, transcriptPath, "claude", "/work/tree")
	m := New(Config{Plan: plan, Graph: graph, Hosted: hosted})
	mdl, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	return mdl.(Model), transcriptPath
}

// runOutlineLoad executes a (possibly batched) cmd and returns the
// OutlineLoadedMsg it produced.
func runOutlineLoad(t *testing.T, cmd tea.Cmd) OutlineLoadedMsg {
	t.Helper()
	for _, msg := range collectMsgs(cmd) {
		if loaded, ok := msg.(OutlineLoadedMsg); ok {
			return loaded
		}
	}
	t.Fatal("no OutlineLoadedMsg produced")
	return OutlineLoadedMsg{}
}

// The loader builds the outline live from the transcript: prompt title,
// assistant response with its Markdown heading, and a tool activity row.
func TestLoadOutlineCmdBuildsFromTranscript(t *testing.T) {
	m, _ := newOutlineJobModel(t, false)

	msg := runOutlineLoad(t, loadOutlineCmd(m.Plan, m.Plan.Jobs[0], 100))
	if msg.Err != nil {
		t.Fatalf("loadOutlineCmd: %v", msg.Err)
	}
	if msg.JobID != "j1" || msg.Width != 100 {
		t.Errorf("msg = {JobID:%q Width:%d}, want {j1 100}", msg.JobID, msg.Width)
	}
	plain := ansi.Strip(msg.Content)
	for _, want := range []string{
		"Add an outline pane to the status TUI", // prompt title
		"Plan",                                  // assistant Markdown heading
		"go test",                               // Bash tool row
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("outline missing %q:\n%s", want, plain)
		}
	}
}

// "vo" opens the outline detail pane, and the loaded content lands in the
// internal viewport unwrapped.
func TestViewOutlineChordOpensPane(t *testing.T) {
	m, _ := newOutlineJobModel(t, false)
	m, cmd := press(t, m, "vo")

	if m.ActiveDetailPane != OutlinePaneDetail {
		t.Fatalf("ActiveDetailPane = %d, want OutlinePaneDetail (%d)", m.ActiveDetailPane, OutlinePaneDetail)
	}
	if !m.ShowLogs {
		t.Error("unhosted outline pane should render in the internal detail pane")
	}

	mdl, _ := m.Update(runOutlineLoad(t, cmd))
	m = mdl.(Model)
	plain := ansi.Strip(m.outlineRawContent)
	if !strings.Contains(plain, "Add an outline pane to the status TUI") {
		t.Errorf("pane content missing the prompt title:\n%s", plain)
	}
	if !strings.Contains(plain, "go test") {
		t.Errorf("pane content missing the tool row:\n%s", plain)
	}
	if m.outlineRenderedWidth == 0 {
		t.Error("outlineRenderedWidth not recorded from the loaded msg")
	}
	if !strings.Contains(ansi.Strip(m.outlineViewport.View()), "Add an outline pane") {
		t.Error("viewport does not show the outline content")
	}
}

// Pressing "vo" again closes the pane, like every other detail pane toggle.
func TestViewOutlineChordTogglesClosed(t *testing.T) {
	m, _ := newOutlineJobModel(t, false)
	m, _ = press(t, m, "vo")
	m, _ = press(t, m, "vo")
	if m.ActiveDetailPane == OutlinePaneDetail {
		t.Error("second vo did not close the outline pane")
	}
}

// Hosted, the pane is promoted into the host's BSP viewport split like the
// other read-only detail panes.
func TestOutlinePanePromotesWhenHosted(t *testing.T) {
	m, _ := newOutlineJobModel(t, true)
	m, cmd := press(t, m, "vo")

	if m.ActiveDetailPane != OutlinePaneDetail {
		t.Fatalf("ActiveDetailPane = %d, want OutlinePaneDetail", m.ActiveDetailPane)
	}
	if !m.Manager.IsPromoted("detail") {
		t.Error("detail slot not promoted — pane rendered inline instead of a BSP split")
	}
	var sawSplit bool
	for _, msg := range collectMsgs(cmd) {
		if req, ok := msg.(embed.SplitViewportRequestMsg); ok {
			sawSplit = true
			if !strings.HasPrefix(req.Title, "Outline") {
				t.Errorf("split title = %q, want an Outline title", req.Title)
			}
		}
	}
	if !sawSplit {
		t.Error("no SplitViewportRequestMsg emitted for the outline pane")
	}
}

// Without a resolvable transcript the loader falls back to the completion
// artifact .artifacts/<job>/toc.ansi (written by the job-completion path).
func TestOutlineFallsBackToTocArtifact(t *testing.T) {
	m, _ := newOutlineJobModel(t, false)
	stubOutlineLookup(t, "", "", "") // no session binding

	// No artifact either: empty content, nil error → placeholder text.
	msg := runOutlineLoad(t, loadOutlineCmd(m.Plan, m.Plan.Jobs[0], 80))
	if msg.Err != nil || msg.Content != "" {
		t.Fatalf("no-source load = {Content:%q Err:%v}, want empty/nil", msg.Content, msg.Err)
	}
	if !strings.Contains(renderOutlinePaneContent(msg.Content, msg.Err), "No transcript found") {
		t.Error("empty outline does not render the placeholder")
	}

	artifactDir := filepath.Join(m.Plan.Directory, ".artifacts", "j1")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "toc.ansi"), []byte("archived outline body\n"), 0o644); err != nil {
		t.Fatalf("WriteFile toc.ansi: %v", err)
	}
	msg = runOutlineLoad(t, loadOutlineCmd(m.Plan, m.Plan.Jobs[0], 80))
	if msg.Err != nil {
		t.Fatalf("artifact fallback: %v", msg.Err)
	}
	if msg.Content != "archived outline body" {
		t.Errorf("artifact fallback content = %q", msg.Content)
	}
}
