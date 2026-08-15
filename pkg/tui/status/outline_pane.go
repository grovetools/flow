package status

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/agentlogs/pkg/toc"
	"github.com/grovetools/core/pkg/sessions"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/flow/pkg/orchestration"
)

// The Outline pane (vo) renders a table-of-contents of the selected job's
// agent transcript — prompts, assistant response summaries with their Markdown
// headings, and per-tool activity rows — using the shared agentlogs/pkg/toc
// builder + styled renderer. It is built LIVE from the transcript on every
// load so a running job's outline grows as the agent works; the completion
// artifact (.artifacts/<job>/toc.ansi, written when a job finishes) is only a
// fallback for jobs whose session binding is no longer resolvable.

// OutlineLoadedMsg carries the rendered transcript outline for the detail
// pane. Width records the render width the content was produced at, so the
// refresh tick can detect a stale-width render after a layout change.
type OutlineLoadedMsg struct {
	JobID   string
	Content string
	Width   int
	Err     error
}

// outlineTocArtifactName is the completion artifact the outline pane falls
// back to (phase 2 writes it on job completion). Path convention only — this
// pane does not depend on the writer.
const outlineTocArtifactName = "toc.ansi"

// outlineSessionLookup resolves a job's session binding through the hooks
// session registry: the transcript path, the recorded provider, and the
// session's working directory (the $WT path marker). Package-level var so
// tests can stub the registry away. Empty strings mean "unresolved".
var outlineSessionLookup = func(jobID string) (transcriptPath, provider, workDir string) {
	registry, err := sessions.NewFileSystemRegistry()
	if err != nil {
		return "", "", ""
	}
	metadata, err := registry.Find(jobID)
	if err != nil || metadata == nil {
		return "", "", ""
	}
	return metadata.TranscriptPath, metadata.Provider, metadata.WorkingDirectory
}

// loadOutlineCmd builds the styled outline for a job off the event loop.
// Preference order: live transcript (always fresh, resolved via the hooks
// session registry exactly like the log streamer), then the completion
// artifact toc.ansi. An empty Content with a nil Err means "nothing found" —
// renderOutlinePaneContent shows the placeholder.
func loadOutlineCmd(plan *orchestration.Plan, job *orchestration.Job, width int) tea.Cmd {
	jobID := job.ID
	jobProvider := job.Provider
	artifactDir := jobArtifactDir(plan, job)
	return func() tea.Msg {
		transcriptPath, provider, workDir := outlineSessionLookup(jobID)
		if provider == "" {
			provider = jobProvider
		}
		if transcriptPath != "" {
			if _, err := os.Stat(transcriptPath); err == nil {
				entries, err := normalizeTranscriptFile(transcriptPath, provider)
				if err != nil {
					return OutlineLoadedMsg{JobID: jobID, Width: width, Err: err}
				}
				items := toc.BuildAgentItems(entries, toc.BuildOptions{
					Provider: provider,
					Markers: toc.PathMarkers{
						Worktree:  workDir,
						Artifacts: artifactDir,
					},
				})
				content := toc.RenderStyled(items, toc.DefaultRenderOptions(width, provider))
				return OutlineLoadedMsg{JobID: jobID, Width: width, Content: content}
			}
		}
		// Fallback: the completion artifact, rendered at completion time (its
		// width is whatever the writer used — still readable in a viewport).
		if artifactDir != "" {
			if data, err := os.ReadFile(filepath.Join(artifactDir, outlineTocArtifactName)); err == nil {
				return OutlineLoadedMsg{JobID: jobID, Width: width, Content: strings.TrimRight(string(data), "\n")}
			}
		}
		return OutlineLoadedMsg{JobID: jobID, Width: width}
	}
}

// renderOutlinePaneContent wraps the loader's result for display: errors and
// the empty case get styled text, real content passes through untouched (it is
// pre-rendered ANSI at the pane's exact width — never re-wrap it).
func renderOutlinePaneContent(content string, err error) string {
	t := theme.DefaultTheme
	if err != nil {
		return t.Error.Render(fmt.Sprintf("Error building outline: %v", err))
	}
	if strings.TrimSpace(content) == "" {
		return t.Muted.Render("No transcript found for this job.\n\nThe outline is built from the agent's session transcript once the\nhooks registry records the session, or from .artifacts/<job>/toc.ansi\nafter completion.")
	}
	return content
}

// outlineRenderWidth is the exact width the outline is rendered at: the detail
// pane's content width minus the scrollbar column. RenderStyled does its own
// ANSI-aware width math, so rendering at the right width replaces wrapping.
func (m Model) outlineRenderWidth() int {
	w := m.LogViewerWidth - 1
	if w < 20 {
		w = 80
	}
	return w
}
