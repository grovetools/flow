package status

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/cli"
	"github.com/grovetools/core/config"
	markdown "github.com/grovetools/core/tui/components/markdown"
	"github.com/grovetools/core/tui/embed"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/flow/pkg/orchestration"
)

// Every agent writes into its job's artifact directory as a scratch area, but
// the only way to read those files back was to leave the TUI. This pane browses
// `<planDir>/.artifacts/<jobID>` in place: a cursor-driven tree of the whole
// subtree, with the selected file previewed beneath it.

// artifactPaneMaxNodes bounds the tree so a job that wrote thousands of files
// (workflow transcripts, per-agent dumps) cannot stall a render. Anything past
// the cap is reported in the summary rather than silently dropped.
const artifactPaneMaxNodes = 2000

// artifactPreviewMaxLines caps how much of a selected file is rendered into the
// preview. Big JSONL traces are common here.
const artifactPreviewMaxLines = 500

// ArtifactPaneNode is one row of the artifacts tree: a directory or a file
// inside the job's artifact directory.
type ArtifactPaneNode struct {
	Name    string // basename
	RelPath string // path relative to the job's artifact dir
	IsDir   bool
	Depth   int
	Size    int64
	ModTime time.Time
	// Children counts entries directly inside a directory node.
	Children int
}

// jobArtifactDir returns the job's artifact directory.
func jobArtifactDir(plan *orchestration.Plan, job *orchestration.Job) string {
	if plan == nil || job == nil {
		return ""
	}
	return filepath.Join(plan.Directory, ".artifacts", job.ID)
}

// resolveArtifactPaneNodes walks the job's artifact dir depth-first, listing
// directories before files at each level and sorting each group by name, so the
// flattened slice reads as an indented tree. Returns the nodes and whether the
// walk hit artifactPaneMaxNodes.
func resolveArtifactPaneNodes(artifactDir string) ([]*ArtifactPaneNode, bool) {
	if artifactDir == "" {
		return nil, false
	}
	var nodes []*ArtifactPaneNode
	truncated := false

	var walk func(dir, rel string, depth int)
	walk = func(dir, rel string, depth int) {
		if truncated {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		dirs := make([]os.DirEntry, 0, len(entries))
		files := make([]os.DirEntry, 0, len(entries))
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue // scratch dotfiles are noise, not artifacts
			}
			if e.IsDir() {
				dirs = append(dirs, e)
			} else {
				files = append(files, e)
			}
		}
		byName := func(s []os.DirEntry) {
			sort.Slice(s, func(i, j int) bool { return s[i].Name() < s[j].Name() })
		}
		byName(dirs)
		byName(files)

		for _, d := range dirs {
			if len(nodes) >= artifactPaneMaxNodes {
				truncated = true
				return
			}
			childRel := filepath.Join(rel, d.Name())
			childPath := filepath.Join(dir, d.Name())
			count := 0
			if sub, err := os.ReadDir(childPath); err == nil {
				count = len(sub)
			}
			node := &ArtifactPaneNode{
				Name:     d.Name(),
				RelPath:  childRel,
				IsDir:    true,
				Depth:    depth,
				Children: count,
			}
			if info, err := d.Info(); err == nil {
				node.ModTime = info.ModTime()
			}
			nodes = append(nodes, node)
			walk(childPath, childRel, depth+1)
		}
		for _, f := range files {
			if len(nodes) >= artifactPaneMaxNodes {
				truncated = true
				return
			}
			node := &ArtifactPaneNode{
				Name:    f.Name(),
				RelPath: filepath.Join(rel, f.Name()),
				Depth:   depth,
			}
			if info, err := f.Info(); err == nil {
				node.Size = info.Size()
				node.ModTime = info.ModTime()
			}
			nodes = append(nodes, node)
		}
	}
	walk(artifactDir, "", 0)
	return nodes, truncated
}

// artifactsPaneResult mirrors skillPaneResult: the tree half, the detail half,
// the flat node list the cursor indexes, and the cursor's 1-based line within
// the tree content (used as EnsureVisible by the promoted split).
type artifactsPaneResult struct {
	treeContent   string
	detailContent string
	nodes         []*ArtifactPaneNode
	cursorLine    int
}

// artifactTreeHeaderLines is what renderInteractiveArtifactsPane writes above
// the first node line: the heading plus a blank line.
const artifactTreeHeaderLines = 2

// renderInteractiveArtifactsPane renders the job's artifact directory as a tree
// plus a preview of the node under the cursor.
func renderInteractiveArtifactsPane(plan *orchestration.Plan, job *orchestration.Job, cursor int) artifactsPaneResult {
	t := theme.DefaultTheme
	dir := jobArtifactDir(plan, job)
	if dir == "" {
		return artifactsPaneResult{treeContent: t.Muted.Render("No plan or job selected.")}
	}

	nodes, truncated := resolveArtifactPaneNodes(dir)
	if len(nodes) == 0 {
		return artifactsPaneResult{
			treeContent: t.Muted.Render("No artifacts for this job yet.\n\nAgents write scratch files (briefings, notes, logs) to\n" + dir),
		}
	}

	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(nodes) {
		cursor = len(nodes) - 1
	}

	var tree strings.Builder
	tree.WriteString(t.Info.Bold(true).Render("Job Artifacts"))
	tree.WriteString("  ")
	tree.WriteString(t.Muted.Render(dir))
	tree.WriteString("\n\n")

	for i, node := range nodes {
		renderArtifactNodeLine(&tree, node, i == cursor)
	}

	files, dirs := 0, 0
	for _, n := range nodes {
		if n.IsDir {
			dirs++
		} else {
			files++
		}
	}
	tree.WriteString("\n")
	summary := fmt.Sprintf("%d files", files)
	if dirs > 0 {
		summary += fmt.Sprintf(" in %d dirs", dirs)
	}
	if truncated {
		summary += fmt.Sprintf(" (listing capped at %d entries)", artifactPaneMaxNodes)
	}
	summary += " • e: editor • o: open (rail editor / browser) • ctrl+y: yank path"
	tree.WriteString(t.Muted.Render(summary))
	tree.WriteString("\n")

	var detail strings.Builder
	renderArtifactNodeDetail(&detail, dir, nodes[cursor])

	return artifactsPaneResult{
		treeContent:   tree.String(),
		detailContent: detail.String(),
		nodes:         nodes,
		cursorLine:    artifactTreeHeaderLines + 1 + cursor,
	}
}

// renderArtifactNodeLine renders one tree row: cursor, indent, icon, name, and
// (for files) size and age.
func renderArtifactNodeLine(b *strings.Builder, node *ArtifactPaneNode, isCursor bool) {
	t := theme.DefaultTheme

	cursorStr := "  "
	if isCursor {
		cursorStr = t.Highlight.Render(theme.IconArrowRightBold + " ")
	}
	indent := strings.Repeat("  ", node.Depth)

	if node.IsDir {
		line := fmt.Sprintf("%s%s%s %s", cursorStr, indent, t.Info.Render(theme.IconFolder), t.Info.Render(node.Name+"/"))
		if node.Children > 0 {
			line += "  " + t.Muted.Render(fmt.Sprintf("(%d)", node.Children))
		}
		b.WriteString(line + "\n")
		return
	}

	line := fmt.Sprintf("%s%s%s %s", cursorStr, indent, t.Muted.Render(theme.IconFile), node.Name)
	meta := formatArtifactSize(node.Size)
	if !node.ModTime.IsZero() {
		meta += " · " + formatArtifactAge(node.ModTime)
	}
	line += "  " + t.Muted.Render(meta)
	b.WriteString(line + "\n")
}

// renderArtifactNodeDetail previews the selected node: a file's content, or a
// directory's immediate children.
func renderArtifactNodeDetail(b *strings.Builder, artifactDir string, node *ArtifactPaneNode) {
	t := theme.DefaultTheme
	path := filepath.Join(artifactDir, node.RelPath)

	if node.IsDir {
		b.WriteString(t.Info.Bold(true).Render(node.Name + "/"))
		b.WriteString("\n\n")
		entries, err := os.ReadDir(path)
		if err != nil {
			b.WriteString(t.Error.Render(fmt.Sprintf("  cannot read directory: %v", err)))
			b.WriteString("\n")
			return
		}
		if len(entries) == 0 {
			b.WriteString(t.Muted.Render("  (empty directory)"))
			b.WriteString("\n")
			return
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			b.WriteString("  " + t.Muted.Render(name) + "\n")
		}
		return
	}

	b.WriteString(t.Info.Bold(true).Render(node.Name))
	b.WriteString("  ")
	b.WriteString(t.Muted.Render(formatArtifactSize(node.Size)))
	b.WriteString("\n\n")

	data, err := os.ReadFile(path)
	if err != nil {
		b.WriteString(t.Error.Render(fmt.Sprintf("  cannot read file: %v", err)))
		b.WriteString("\n")
		return
	}
	content := strings.TrimRight(string(data), "\n")
	if strings.TrimSpace(content) == "" {
		b.WriteString(t.Muted.Render("  (empty file)"))
		b.WriteString("\n")
		return
	}
	if !isTextArtifact(data) {
		b.WriteString(t.Muted.Render("  (binary file — press e to open it in $EDITOR)"))
		b.WriteString("\n")
		return
	}

	lines := strings.Split(content, "\n")
	truncated := false
	if len(lines) > artifactPreviewMaxLines {
		lines = lines[:artifactPreviewMaxLines]
		truncated = true
	}
	body := strings.Join(lines, "\n")

	if strings.EqualFold(filepath.Ext(node.Name), ".md") {
		b.WriteString(markdown.Render(body, t))
	} else {
		b.WriteString(body)
	}
	b.WriteString("\n")
	if truncated {
		b.WriteString(t.Muted.Render(fmt.Sprintf("… %d more lines — press e to open it in $EDITOR", len(strings.Split(content, "\n"))-artifactPreviewMaxLines)))
		b.WriteString("\n")
	}
}

// isTextArtifact reports whether a file looks like text worth previewing. A NUL
// byte in the first block is the usual give-away for binaries.
func isTextArtifact(data []byte) bool {
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	for _, b := range head {
		if b == 0 {
			return false
		}
	}
	return true
}

func formatArtifactSize(size int64) string {
	switch {
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(size)/(1<<10))
	default:
		return fmt.Sprintf("%d B", size)
	}
}

func formatArtifactAge(modTime time.Time) string {
	d := time.Since(modTime)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// editArtifactCmd emits an embed.EditRequestMsg so the host opens $EDITOR on
// the selected artifact file. Directories have nothing to open.
func editArtifactCmd(artifactDir string, node *ArtifactPaneNode) tea.Cmd {
	if node == nil || node.IsDir {
		return nil
	}
	path := filepath.Join(artifactDir, node.RelPath)
	return func() tea.Msg {
		return embed.EditRequestMsg{Path: path}
	}
}

// artifactBrowserExtensions are the artifact types a terminal editor renders
// uselessly: an agent's rendered HTML report is markup to $EDITOR and a page to
// a browser. `o` hands these to the desktop instead of to an editor pane.
var artifactBrowserExtensions = map[string]bool{
	".html": true,
	".htm":  true,
	".pdf":  true,
}

// artifactOpensInBrowser reports whether `o` should hand this file to the
// desktop opener rather than to an editor pane.
func artifactOpensInBrowser(name string) bool {
	return artifactBrowserExtensions[strings.ToLower(filepath.Ext(name))]
}

// openArtifactExternally is the desktop hand-off, indirected for tests. The
// configured argv comes from [tui] open_command in grove.toml; empty means the
// platform opener.
var openArtifactExternally = cli.OpenPath

// tuiOpenCommand reads [tui] open_command out of a possibly-absent config. A
// missing config is the norm in tests and on a fresh machine, and it means the
// same thing as an unset key: use the platform opener.
func tuiOpenCommand(cfg *config.Config) []string {
	if cfg == nil || cfg.TUI == nil {
		return nil
	}
	return cfg.TUI.OpenCommand
}

// selectedArtifact returns the node under the artifacts-tree cursor and its
// absolute path, or nil/"" when the tree is empty.
func (m Model) selectedArtifact() (*ArtifactPaneNode, string) {
	if m.artifactPaneCursor < 0 || m.artifactPaneCursor >= len(m.artifactPaneNodes) {
		return nil, ""
	}
	node := m.artifactPaneNodes[m.artifactPaneCursor]
	return node, filepath.Join(jobArtifactDir(m.Plan, m.ActiveLogJob), node.RelPath)
}

// copyArtifactPath yanks the absolute path of the artifact under the cursor.
// This is what ctrl+y means while the artifacts pane has focus — the job note's
// own path is still one ctrl+y away from the jobs pane.
func (m Model) copyArtifactPath() (tea.Model, tea.Cmd) {
	node, path := m.selectedArtifact()
	if node == nil {
		m.StatusSummary = theme.DefaultTheme.Muted.Render("No artifact selected")
		return m, nil
	}
	if err := writeClipboard(path); err != nil {
		m.StatusSummary = fmt.Sprintf("Error copying path: %v", err)
	} else {
		m.StatusSummary = fmt.Sprintf("Copied: %s", path)
	}
	return m, nil
}

// openArtifact implements `o`: a browser-only artifact (.html, .pdf) goes to
// the desktop opener, anything else to a dedicated per-file editor pane —
// Dedicated so the host pins it in the rail under its own filename instead of
// replacing whatever the singleton editor is already showing.
func (m Model) openArtifact() (tea.Model, tea.Cmd) {
	node, path := m.selectedArtifact()
	if node == nil {
		return m, nil
	}
	if node.IsDir {
		m.StatusSummary = theme.DefaultTheme.Muted.Render(node.Name + "/ is a directory")
		return m, nil
	}
	if artifactOpensInBrowser(node.Name) {
		if err := openArtifactExternally(m.openCommand, path); err != nil {
			m.StatusSummary = fmt.Sprintf("Error opening %s: %v", node.Name, err)
		} else {
			m.StatusSummary = fmt.Sprintf("Opened %s externally", node.Name)
		}
		return m, nil
	}
	return m, func() tea.Msg {
		return embed.EditRequestMsg{Path: path, Dedicated: true}
	}
}
