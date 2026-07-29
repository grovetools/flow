package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/grovetools/tuimux/embed"

	"github.com/grovetools/flow/pkg/orchestration"
)

// newArtifactJobModel builds a status Model over one job whose plan directory
// holds a real artifact tree on disk:
//
//	.artifacts/j1/briefing.xml
//	.artifacts/j1/notes.md
//	.artifacts/j1/workflows/run.md
func newArtifactJobModel(t *testing.T, hosted bool) (Model, string) {
	t.Helper()
	planDir := t.TempDir()
	artifactDir := filepath.Join(planDir, ".artifacts", "j1")
	if err := os.MkdirAll(filepath.Join(artifactDir, "workflows"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(artifactDir, rel), []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", rel, err)
		}
	}
	write("briefing.xml", "<prompt>do the thing</prompt>")
	write("notes.md", "# Notes\n\nscratch space\n")
	write(filepath.Join("workflows", "run.md"), "workflow transcript\n")
	// A dotfile is scratch noise, not an artifact.
	write(".hidden", "ignore me")

	job := &orchestration.Job{
		ID:       "j1",
		Filename: "j1.md",
		Title:    "job one",
		Type:     orchestration.JobTypeInteractiveAgent,
		Status:   orchestration.JobStatusCompleted,
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
	m := New(Config{Plan: plan, Graph: graph, Hosted: hosted})
	mdl, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	return mdl.(Model), artifactDir
}

// The artifact directory reads back as an indented tree: dirs before files,
// nested files carried along, dotfiles skipped.
func TestResolveArtifactPaneNodes(t *testing.T) {
	_, artifactDir := newArtifactJobModel(t, false)

	nodes, truncated := resolveArtifactPaneNodes(artifactDir)
	if truncated {
		t.Error("small artifact dir reported as truncated")
	}
	var got []string
	for _, n := range nodes {
		got = append(got, n.RelPath)
	}
	want := []string{"workflows", filepath.Join("workflows", "run.md"), "briefing.xml", "notes.md"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("nodes = %v, want %v", got, want)
	}
	if !nodes[0].IsDir || nodes[0].Depth != 0 {
		t.Errorf("workflows node = %+v, want a depth-0 dir", nodes[0])
	}
	if nodes[1].Depth != 1 {
		t.Errorf("nested file depth = %d, want 1", nodes[1].Depth)
	}
	for _, n := range nodes {
		if strings.HasPrefix(n.Name, ".") {
			t.Errorf("dotfile %q leaked into the artifacts tree", n.Name)
		}
	}
}

// "vj" opens the artifacts pane, and the selected file's contents land in the
// preview half.
func TestViewArtifactsChordOpensPane(t *testing.T) {
	m, _ := newArtifactJobModel(t, false)
	m, _ = press(t, m, "vj")

	if m.ActiveDetailPane != ArtifactsPaneDetail {
		t.Fatalf("ActiveDetailPane = %d, want ArtifactsPaneDetail (%d)", m.ActiveDetailPane, ArtifactsPaneDetail)
	}
	if len(m.artifactPaneNodes) != 4 {
		t.Fatalf("artifactPaneNodes = %d, want 4", len(m.artifactPaneNodes))
	}
	if !m.ShowLogs {
		t.Error("unhosted artifacts pane should render in the internal detail pane")
	}
	if !strings.Contains(m.artifactPaneRawContent, "briefing.xml") {
		t.Errorf("tree content missing a known artifact:\n%s", m.artifactPaneRawContent)
	}

	// Cursor starts on the workflows/ dir; two rows down is briefing.xml.
	m.Focus = FocusDetailPrimary
	m, _ = press(t, m, "jj")
	node := m.artifactPaneNodes[m.artifactPaneCursor]
	if node.Name != "briefing.xml" {
		t.Fatalf("cursor on %q after jj, want briefing.xml", node.Name)
	}
	if !strings.Contains(m.artifactPreviewViewport.View(), "do the thing") {
		t.Errorf("preview does not show the selected file's contents:\n%s", m.artifactPreviewViewport.View())
	}
}

// Pressing "vj" again closes the pane, like every other detail pane toggle.
func TestViewArtifactsChordTogglesClosed(t *testing.T) {
	m, _ := newArtifactJobModel(t, false)
	m, _ = press(t, m, "vj")
	m, _ = press(t, m, "vj")
	if m.ActiveDetailPane == ArtifactsPaneDetail {
		t.Error("second vj did not close the artifacts pane")
	}
}

// Hosted, the pane is promoted into the host's BSP viewport split like the
// other detail panes, carrying its body and claiming its navigation keys.
func TestArtifactsPanePromotesWhenHosted(t *testing.T) {
	m, _ := newArtifactJobModel(t, true)
	m, cmd := press(t, m, "vj")

	if m.ActiveDetailPane != ArtifactsPaneDetail {
		t.Fatalf("ActiveDetailPane = %d, want ArtifactsPaneDetail", m.ActiveDetailPane)
	}
	if !m.Manager.IsPromoted("detail") {
		t.Error("detail slot not promoted — pane rendered inline instead of a BSP split")
	}
	var sawSplit bool
	for _, msg := range collectMsgs(cmd) {
		if req, ok := msg.(embed.SplitViewportRequestMsg); ok {
			sawSplit = true
			if req.Title == "" {
				t.Error("SplitViewportRequestMsg has an empty title")
			}
		}
	}
	if !sawSplit {
		t.Error("no SplitViewportRequestMsg emitted for the artifacts pane")
	}
	if !strings.Contains(m.artifactPaneBody, "briefing.xml") {
		t.Errorf("promoted body missing the artifact listing:\n%s", m.artifactPaneBody)
	}
}

// A binary artifact is described rather than dumped into the preview.
func TestBinaryArtifactPreviewIsDescribed(t *testing.T) {
	_, artifactDir := newArtifactJobModel(t, false)
	if err := os.WriteFile(filepath.Join(artifactDir, "blob.bin"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	var b strings.Builder
	renderArtifactNodeDetail(&b, artifactDir, &ArtifactPaneNode{Name: "blob.bin", RelPath: "blob.bin", Size: 3})
	if !strings.Contains(b.String(), "binary file") {
		t.Errorf("binary preview = %q, want a binary-file note", b.String())
	}
}

// A job that has written nothing yet opens to an explanatory pane, and the
// tree keys are inert rather than panicking on an empty node list.
func TestArtifactsPaneWithNoArtifacts(t *testing.T) {
	m := newAgentJobModel(t, false) // plan has no directory, so no artifacts
	m, _ = press(t, m, "vj")

	if m.ActiveDetailPane != ArtifactsPaneDetail {
		t.Fatalf("ActiveDetailPane = %d, want ArtifactsPaneDetail", m.ActiveDetailPane)
	}
	if len(m.artifactPaneNodes) != 0 {
		t.Fatalf("artifactPaneNodes = %d, want 0", len(m.artifactPaneNodes))
	}
	if !strings.Contains(m.artifactPaneRawContent, "No artifacts") {
		t.Errorf("empty pane body = %q, want a no-artifacts note", m.artifactPaneRawContent)
	}
	m.Focus = FocusDetailPrimary
	m, _ = press(t, m, "jjke")
	if m.artifactPaneCursor != 0 {
		t.Errorf("cursor = %d on an empty tree, want 0", m.artifactPaneCursor)
	}
}
