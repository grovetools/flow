package status

import (
	"context"
	"fmt"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/flow/pkg/orchestration"
)

// AccessedFilesLoadedMsg carries a job's deduped accessed-files trace
// (.artifacts/<job>/accessed_files.jsonl) for the detail pane. Display holds
// the workspace-rooted form of each path, index-aligned with Files; it falls
// back to the absolute path when workspace resolution fails.
type AccessedFilesLoadedMsg struct {
	JobID   string
	Files   []orchestration.AccessedFile
	Display []string
	Err     error
}

// loadAccessedFilesCmd reads a job's accessed-files trace and resolves the
// workspace-rooted display names off the event loop (the workspace provider
// may talk to the daemon or run discovery).
func loadAccessedFilesCmd(plan *orchestration.Plan, job *orchestration.Job) tea.Cmd {
	return func() tea.Msg {
		files, err := orchestration.ReadJobAccessedFiles(plan, job)
		if err != nil {
			return AccessedFilesLoadedMsg{JobID: job.ID, Err: err}
		}
		display := make([]string, len(files))
		provider, provErr := orchestration.NewDisplayWorkspaceProvider(context.Background())
		for i, f := range files {
			if provErr == nil {
				display[i] = orchestration.WorkspaceRootedPath(provider, f.Path)
			} else {
				display[i] = f.Path
			}
		}
		return AccessedFilesLoadedMsg{JobID: job.ID, Files: files, Display: display}
	}
}

// renderAccessedFilesPaneContent renders the accessed-files detail pane body:
// one workspace-rooted path per line with its last action and access count.
func renderAccessedFilesPaneContent(files []orchestration.AccessedFile, display []string, loadErr error) string {
	t := theme.DefaultTheme
	if loadErr != nil {
		return t.Error.Render(fmt.Sprintf("Error reading accessed files: %v", loadErr))
	}
	if len(files) == 0 {
		return t.Muted.Render("No accessed-files trace for this job.\n\nAgent sessions record file access to .artifacts/<job>/accessed_files.jsonl as they run.")
	}

	var b strings.Builder
	b.WriteString(t.Muted.Render(fmt.Sprintf("%d files • y: copy absolute • Y: copy workspace-rooted", len(files))))
	b.WriteString("\n\n")
	for i, f := range files {
		marker := t.Muted.Render("r")
		if f.Action == "modified" {
			marker = t.Warning.Render("m")
		}
		count := ""
		if f.Count > 1 {
			count = t.Muted.Render(fmt.Sprintf(" ×%d", f.Count))
		}
		b.WriteString(fmt.Sprintf("%s %s%s\n", marker, display[i], count))
	}
	return b.String()
}

// copyAccessedFilesToClipboard copies the current pane's file list to the
// clipboard, one path per line: workspace-rooted when rooted is true,
// absolute otherwise.
func (m Model) copyAccessedFilesToClipboard(rooted bool) (tea.Model, tea.Cmd) {
	if len(m.accessedFiles) == 0 {
		m.StatusSummary = theme.DefaultTheme.Muted.Render("No accessed files to copy")
		return m, nil
	}
	lines := make([]string, len(m.accessedFiles))
	for i, f := range m.accessedFiles {
		if rooted && i < len(m.accessedFilesDisplay) {
			lines[i] = m.accessedFilesDisplay[i]
		} else {
			lines[i] = f.Path
		}
	}
	label := "absolute"
	if rooted {
		label = "workspace-rooted"
	}
	if err := clipboard.WriteAll(strings.Join(lines, "\n") + "\n"); err != nil {
		m.StatusSummary = fmt.Sprintf("Error copying accessed files: %v", err)
	} else {
		m.StatusSummary = fmt.Sprintf("Copied %d %s paths", len(lines), label)
	}
	return m, nil
}
