package status

import (
	"os"
	"path/filepath"
	"slices"
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

// artifactCursorOn points the artifacts cursor at a named node.
func artifactCursorOn(t *testing.T, m Model, name string) Model {
	t.Helper()
	for i, n := range m.artifactPaneNodes {
		if n.Name == name {
			m.artifactPaneCursor = i
			return m
		}
	}
	t.Fatalf("no artifact node named %q in %d nodes", name, len(m.artifactPaneNodes))
	return m
}

// ctrl+y in the artifacts tree yanks the selected artifact's absolute path —
// the pane takes CopyPath over from the global job-note yank while it is
// focused.
func TestArtifactsPaneYankCopiesArtifactPath(t *testing.T) {
	m, artifactDir := newArtifactJobModel(t, false)
	m, _ = press(t, m, "vj")
	m.Focus = FocusDetailPrimary
	m = artifactCursorOn(t, m, "briefing.xml")

	oldWriteClipboard := writeClipboard
	defer func() { writeClipboard = oldWriteClipboard }()
	var copied string
	writeClipboard = func(path string) error {
		copied = path
		return nil
	}

	mdl, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m = mdl.(Model)

	want := filepath.Join(artifactDir, "briefing.xml")
	if copied != want {
		t.Errorf("copied = %q, want %q", copied, want)
	}
	if !strings.Contains(m.StatusSummary, want) {
		t.Errorf("status summary = %q, want it to name the copied path", m.StatusSummary)
	}
}

// With the artifacts pane open but the jobs pane focused, ctrl+y still yanks
// the job note: the pane only claims the key while it has focus.
func TestArtifactsPaneYankLeavesJobsPaneCopyPathAlone(t *testing.T) {
	m, _ := newArtifactJobModel(t, false)
	m, _ = press(t, m, "vj")
	m.Focus = FocusJobs

	oldWriteClipboard := writeClipboard
	defer func() { writeClipboard = oldWriteClipboard }()
	var copied string
	writeClipboard = func(path string) error {
		copied = path
		return nil
	}

	mdl, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m = mdl.(Model)

	if want := m.Jobs[0].FilePath; copied != want {
		t.Errorf("copied = %q, want the job note path %q", copied, want)
	}
}

// `o` on an ordinary artifact asks the host for a DEDICATED editor pane, so the
// file gets pinned in the rail under its own name instead of replacing whatever
// the singleton editor is showing.
func TestArtifactsPaneOpenAsksForDedicatedEditor(t *testing.T) {
	m, artifactDir := newArtifactJobModel(t, false)
	m, _ = press(t, m, "vj")
	m.Focus = FocusDetailPrimary
	m = artifactCursorOn(t, m, "notes.md")

	m, cmd := press(t, m, "o")

	var req *embed.EditRequestMsg
	for _, msg := range collectMsgs(cmd) {
		if r, ok := msg.(embed.EditRequestMsg); ok {
			req = &r
		}
	}
	if req == nil {
		t.Fatalf("no EditRequestMsg emitted for o on notes.md")
	}
	if want := filepath.Join(artifactDir, "notes.md"); req.Path != want {
		t.Errorf("edit path = %q, want %q", req.Path, want)
	}
	if !req.Dedicated {
		t.Error("o asked for a quick open; want a dedicated per-file pane")
	}
}

// `o` on an .html artifact hands it to the desktop opener with the configured
// argv instead of to an editor no browser engine lives in.
func TestArtifactsPaneOpenSendsHTMLToTheBrowser(t *testing.T) {
	m, artifactDir := newArtifactJobModel(t, false)
	if err := os.WriteFile(filepath.Join(artifactDir, "report.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m, _ = press(t, m, "vj")
	m.Focus = FocusDetailPrimary
	m.openCommand = []string{"open", "-a", "Firefox"}
	m = artifactCursorOn(t, m, "report.html")

	oldOpen := openArtifactExternally
	defer func() { openArtifactExternally = oldOpen }()
	var gotArgv []string
	var gotTarget string
	openArtifactExternally = func(argv []string, target string) error {
		gotArgv, gotTarget = argv, target
		return nil
	}

	m, cmd := press(t, m, "o")

	if want := filepath.Join(artifactDir, "report.html"); gotTarget != want {
		t.Errorf("opened %q, want %q", gotTarget, want)
	}
	if strings.Join(gotArgv, " ") != "open -a Firefox" {
		t.Errorf("opener argv = %v, want the configured open_command", gotArgv)
	}
	for _, msg := range collectMsgs(cmd) {
		if _, ok := msg.(embed.EditRequestMsg); ok {
			t.Error("html artifact also went to an editor pane")
		}
	}
	if !strings.Contains(m.StatusSummary, "report.html") {
		t.Errorf("status summary = %q, want it to name the opened file", m.StatusSummary)
	}
}

// Promoted into the host's split, the pane must claim `o` and ctrl+y too — a
// key the split is not told to forward never reaches the tree handler.
func TestArtifactsPaneForwardsItsActionKeys(t *testing.T) {
	m, _ := newArtifactJobModel(t, true)
	_, cmd := press(t, m, "vj")

	var forwarded []string
	for _, msg := range collectMsgs(cmd) {
		if req, ok := msg.(embed.SplitViewportRequestMsg); ok {
			forwarded = req.ForwardKeys
		}
	}
	if forwarded == nil {
		t.Fatal("no SplitViewportRequestMsg emitted for the artifacts pane")
	}
	for _, want := range []string{"o", "ctrl+y", "e", "j", "k"} {
		if !slices.Contains(forwarded, want) {
			t.Errorf("ForwardKeys = %v, missing %q", forwarded, want)
		}
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
