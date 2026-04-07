package status

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	markdown "github.com/grovetools/core/tui/components/markdown"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/flow/pkg/orchestration"
)

// SkillPaneNode represents either a skill or an artifact in the skill pane tree.
type SkillPaneNode struct {
	Name           string
	IsArtifact     bool
	ParentSkill    string // Skill name this artifact belongs to
	FilePath       string // Relative file path for artifacts
	ArtifactStatus string // "expected", "produced", "extra"
	Depth          int
}

// SkillDisplayNode represents a skill in the skill pane tree for rendering (internal).
type SkillDisplayNode struct {
	Name     string
	Depth    int
	Children []*SkillDisplayNode
}

// buildDisplayTree converts SkillSequenceNodes to a display tree with depth info.
func buildDisplayTree(nodes []orchestration.SkillSequenceNode, depth int) []*SkillDisplayNode {
	var result []*SkillDisplayNode
	for _, node := range nodes {
		dn := &SkillDisplayNode{
			Name:  node.Metadata.Name,
			Depth: depth,
		}
		if len(node.Children) > 0 {
			dn.Children = buildDisplayTree(node.Children, depth+1)
		}
		result = append(result, dn)
	}
	return result
}

// flattenDisplayNodes recursively flattens the display tree into an ordered slice for cursor navigation.
func flattenDisplayNodes(nodes []*SkillDisplayNode) []*SkillDisplayNode {
	var flat []*SkillDisplayNode
	for _, node := range nodes {
		flat = append(flat, node)
		if len(node.Children) > 0 {
			flat = append(flat, flattenDisplayNodes(node.Children)...)
		}
	}
	return flat
}

// readSkillStateMap reads all status.json files from a job's artifact directory.
func readSkillStateMap(artifactDir string) map[string]orchestration.SkillFidelityState {
	stateMap := make(map[string]orchestration.SkillFidelityState)
	files, err := os.ReadDir(artifactDir)
	if err != nil {
		return stateMap
	}
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), "-status.json") {
			continue
		}
		path := filepath.Join(artifactDir, file.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var state orchestration.SkillFidelityState
		if json.Unmarshal(data, &state) == nil && state.Skill != "" {
			stateMap[state.Skill] = state
		}
	}
	return stateMap
}

// resolveSkillDisplayNodes resolves job skill sequence into display nodes and flat list.
func resolveSkillDisplayNodes(plan *orchestration.Plan, job *orchestration.Job) ([]*SkillDisplayNode, []*SkillDisplayNode) {
	workDir := plan.Directory
	if plan.Config != nil && plan.Config.Worktree != "" {
		if wd, err := os.Getwd(); err == nil {
			workDir = wd
		}
	}

	sequenceNodes, err := orchestration.ResolveSkillSequenceMetadata(job.SkillSequence, workDir)
	if err != nil {
		// Fall back to flat nodes
		var nodes []*SkillDisplayNode
		for _, name := range job.SkillSequence {
			nodes = append(nodes, &SkillDisplayNode{Name: name, Depth: 0})
		}
		return nodes, nodes
	}

	tree := buildDisplayTree(sequenceNodes, 0)
	flat := flattenDisplayNodes(tree)
	return tree, flat
}

// resolveSkillPaneNodes builds a mixed tree of SkillPaneNodes (skills + artifact children).
func resolveSkillPaneNodes(plan *orchestration.Plan, job *orchestration.Job, stateMap map[string]orchestration.SkillFidelityState) []*SkillPaneNode {
	_, flatDisplayNodes := resolveSkillDisplayNodes(plan, job)

	var paneNodes []*SkillPaneNode
	for _, dn := range flatDisplayNodes {
		// Add the skill node
		paneNodes = append(paneNodes, &SkillPaneNode{
			Name:  dn.Name,
			Depth: dn.Depth,
		})

		// Add artifact children from state map
		state, exists := stateMap[dn.Name]
		if !exists {
			continue
		}

		producedSet := make(map[string]bool, len(state.ArtifactsProduced))
		for _, p := range state.ArtifactsProduced {
			producedSet[p] = true
		}

		// Expected artifacts
		for _, art := range state.ArtifactsExpected {
			status := "expected"
			if producedSet[art] {
				status = "produced"
			}
			paneNodes = append(paneNodes, &SkillPaneNode{
				Name:           art,
				IsArtifact:     true,
				ParentSkill:    dn.Name,
				FilePath:       art,
				ArtifactStatus: status,
				Depth:          dn.Depth + 1,
			})
		}

		// Extra artifacts (produced but not expected)
		for _, art := range state.ArtifactsProduced {
			if !contains(state.ArtifactsExpected, art) {
				paneNodes = append(paneNodes, &SkillPaneNode{
					Name:           art,
					IsArtifact:     true,
					ParentSkill:    dn.Name,
					FilePath:       art,
					ArtifactStatus: "extra",
					Depth:          dn.Depth + 1,
				})
			}
		}
	}

	return paneNodes
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// skillPaneResult holds the output of renderInteractiveSkillPane.
type skillPaneResult struct {
	treeContent   string                                      // Tree view (top half)
	detailContent string                                      // Detail view (bottom half)
	nodes         []*SkillPaneNode                            // Flat node list
	stateMap      map[string]orchestration.SkillFidelityState // Cached state
}

// renderInteractiveSkillPane builds the skill pane tree view and detail content separately.
func renderInteractiveSkillPane(plan *orchestration.Plan, job *orchestration.Job, cursor int, _, height int) skillPaneResult {
	empty := skillPaneResult{}
	if len(job.SkillSequence) == 0 {
		empty.treeContent = theme.DefaultTheme.Muted.Render("No skill sequence defined for this job.")
		return empty
	}

	artifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
	stateMap := readSkillStateMap(artifactDir)

	paneNodes := resolveSkillPaneNodes(plan, job, stateMap)

	if len(paneNodes) == 0 {
		empty.treeContent = theme.DefaultTheme.Muted.Render("No skills resolved.")
		empty.stateMap = stateMap
		return empty
	}

	// Clamp cursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(paneNodes) {
		cursor = len(paneNodes) - 1
	}

	// === Build tree content (top half) ===
	var tree strings.Builder

	// Header
	tree.WriteString(theme.DefaultTheme.Info.Bold(true).Render("Skill Sequence"))
	tree.WriteString("\n\n")

	// Render tree with cursor
	for i, node := range paneNodes {
		isLastChild := isLastArtifactChild(paneNodes, i)
		renderPaneNodeLine(&tree, node, stateMap, i == cursor, isLastChild)
	}

	// Summary (count only skill nodes)
	tree.WriteString("\n")
	renderPaneSkillSummary(&tree, paneNodes, stateMap)

	tree.WriteString("\n")

	// === Build detail content (bottom half) ===
	var detail strings.Builder

	selectedNode := paneNodes[cursor]
	if selectedNode.IsArtifact {
		artPath := filepath.Join(artifactDir, selectedNode.FilePath)
		renderArtifactDetailContent(&detail, artPath, selectedNode)
	} else {
		renderSkillArtifactDetail(&detail, selectedNode, stateMap, plan, job, height)
	}

	return skillPaneResult{
		treeContent:   tree.String(),
		detailContent: detail.String(),
		nodes:         paneNodes,
		stateMap:      stateMap,
	}
}

// isLastArtifactChild checks if the node at index i is the last artifact child of its parent skill.
func isLastArtifactChild(nodes []*SkillPaneNode, i int) bool {
	node := nodes[i]
	if !node.IsArtifact {
		return false
	}
	// Check if the next node is NOT an artifact of the same parent
	if i+1 >= len(nodes) {
		return true
	}
	next := nodes[i+1]
	return !next.IsArtifact || next.ParentSkill != node.ParentSkill
}

// renderPaneNodeLine renders a single node line (skill or artifact) with optional cursor highlight.
func renderPaneNodeLine(b *strings.Builder, node *SkillPaneNode, stateMap map[string]orchestration.SkillFidelityState, isCursor bool, isLastChild bool) {
	t := theme.DefaultTheme

	// Cursor indicator
	cursorStr := "  "
	if isCursor {
		cursorStr = t.Highlight.Render(theme.IconArrowRightBold + " ")
	}

	if node.IsArtifact {
		// Render artifact node with tree connector
		parentIndent := strings.Repeat("  ", node.Depth-1)
		connector := t.Muted.Render("├─")
		if isLastChild {
			connector = t.Muted.Render("└─")
		}

		var icon string
		var style lipgloss.Style
		switch node.ArtifactStatus {
		case "produced":
			icon = theme.IconStatusCompleted
			style = t.Success
		case "extra":
			icon = theme.IconStatusCompleted
			style = t.Info
		default: // expected but not produced
			icon = theme.IconPending
			style = t.Muted
		}
		line := fmt.Sprintf("%s%s  %s %s %s", cursorStr, parentIndent, connector, style.Render(icon), t.Muted.Render(node.Name))
		b.WriteString(line + "\n")
		return
	}

	// Render skill node
	indent := strings.Repeat("  ", node.Depth)
	state, exists := stateMap[node.Name]
	if !exists {
		state = orchestration.SkillFidelityState{Status: "pending"}
	}

	icon, color := skillStatusStyle(state.Status)
	styledIcon := lipgloss.NewStyle().Foreground(color).Render(icon)
	statusLabel := fmt.Sprintf("[%s]", capitalizeFirst(state.Status))
	styledStatus := lipgloss.NewStyle().Foreground(color).Render(statusLabel)

	line := fmt.Sprintf("%s%s%s %-18s %s", cursorStr, indent, styledIcon, node.Name, styledStatus)

	// Supplementary info — compact, no feedback inline
	switch state.Status {
	case "completed", "running":
		if len(state.ArtifactsExpected) > 0 {
			artifactInfo := fmt.Sprintf("%d/%d", len(state.ArtifactsProduced), len(state.ArtifactsExpected))
			if len(state.ArtifactsProduced) == len(state.ArtifactsExpected) {
				line += "  " + t.Success.Render(artifactInfo)
			} else {
				line += "  " + t.Warning.Render(artifactInfo)
			}
		}
	case "failed":
		if state.Error != nil && *state.Error != "" {
			line += "  " + t.Error.Render(*state.Error)
		}
	}

	b.WriteString(line + "\n")
}

// renderPaneSkillSummary renders the progress summary line (only counting skill nodes).
func renderPaneSkillSummary(b *strings.Builder, paneNodes []*SkillPaneNode, stateMap map[string]orchestration.SkillFidelityState) {
	total := 0
	for _, n := range paneNodes {
		if !n.IsArtifact {
			total++
		}
	}
	completed := 0
	failed := 0
	for _, state := range stateMap {
		switch state.Status {
		case "completed":
			completed++
		case "failed":
			failed++
		}
	}
	summaryStyle := theme.DefaultTheme.Muted
	b.WriteString(summaryStyle.Render(fmt.Sprintf("Progress: %d/%d skills completed", completed, total)))
	if failed > 0 {
		b.WriteString("  ")
		b.WriteString(theme.DefaultTheme.Error.Render(fmt.Sprintf("(%d failed)", failed)))
	}
	b.WriteString("\n")
}

// renderSkillArtifactDetail renders the artifact detail for the selected skill.
func renderSkillArtifactDetail(b *strings.Builder, node *SkillPaneNode, stateMap map[string]orchestration.SkillFidelityState, plan *orchestration.Plan, job *orchestration.Job, maxHeight int) {
	state, exists := stateMap[node.Name]
	if !exists {
		b.WriteString(theme.DefaultTheme.Muted.Render(fmt.Sprintf("  %s — no status data yet", node.Name)))
		b.WriteString("\n")
		return
	}

	t := theme.DefaultTheme
	labelStyle := t.Muted.Italic(true)

	// Skill name header
	_, color := skillStatusStyle(state.Status)
	b.WriteString(lipgloss.NewStyle().Foreground(color).Bold(true).Render(node.Name))
	b.WriteString("  ")
	b.WriteString(lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("[%s]", capitalizeFirst(state.Status))))
	b.WriteString("\n\n")

	artifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)

	// Expected artifacts with content preview
	if len(state.ArtifactsExpected) > 0 {
		b.WriteString(labelStyle.Render("Expected Artifacts:"))
		b.WriteString("\n")
		for _, art := range state.ArtifactsExpected {
			produced := false
			for _, p := range state.ArtifactsProduced {
				if p == art {
					produced = true
					break
				}
			}
			if produced {
				b.WriteString(fmt.Sprintf("  %s %s\n", t.Success.Render(theme.IconStatusCompleted), art))
				renderArtifactPreview(b, filepath.Join(artifactDir, art), t, maxHeight)
			} else {
				b.WriteString(fmt.Sprintf("  %s %s\n", t.Muted.Render(theme.IconPending), art))
			}
		}
	}

	// Produced artifacts (extras not in expected) with content preview
	extras := extraArtifacts(state.ArtifactsProduced, state.ArtifactsExpected)
	if len(extras) > 0 {
		b.WriteString("\n")
		b.WriteString(labelStyle.Render("Additional Artifacts:"))
		b.WriteString("\n")
		for _, art := range extras {
			b.WriteString(fmt.Sprintf("  %s %s\n", t.Info.Render(theme.IconStatusCompleted), art))
			renderArtifactPreview(b, filepath.Join(artifactDir, art), t, maxHeight)
		}
	}

	// Error details
	if state.Error != nil && *state.Error != "" {
		b.WriteString("\n")
		b.WriteString(labelStyle.Render("Error:"))
		b.WriteString("\n")
		b.WriteString("  " + t.Error.Render(*state.Error) + "\n")
	}

	// Diagnostic path
	if state.DiagnosticPath != nil && *state.DiagnosticPath != "" {
		b.WriteString("\n")
		b.WriteString(labelStyle.Render("Diagnostic:"))
		b.WriteString("\n")
		b.WriteString("  " + t.Muted.Render(*state.DiagnosticPath) + "\n")

		artifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
		diagPath := filepath.Join(artifactDir, *state.DiagnosticPath)
		if data, err := os.ReadFile(diagPath); err == nil {
			lines := strings.Split(string(data), "\n")
			previewLines := maxHeight - 10
			if previewLines < 3 {
				previewLines = 3
			}
			if len(lines) > previewLines {
				lines = lines[:previewLines]
				lines = append(lines, t.Muted.Render("..."))
			}
			b.WriteString("\n")
			for _, line := range lines {
				b.WriteString("  " + t.Muted.Render(line) + "\n")
			}
		}
	}

	// Feedback
	if state.Feedback != nil && *state.Feedback != "" {
		b.WriteString("\n")
		b.WriteString(labelStyle.Render("Feedback:"))
		b.WriteString("\n")
		b.WriteString("  " + t.Muted.Render(*state.Feedback) + "\n")
	}
}

// renderArtifactDetailContent renders a full artifact content preview when an artifact node is selected.
func renderArtifactDetailContent(b *strings.Builder, path string, node *SkillPaneNode) {
	t := theme.DefaultTheme

	// Artifact header
	var statusIcon string
	var statusStyle lipgloss.Style
	switch node.ArtifactStatus {
	case "produced":
		statusIcon = theme.IconStatusCompleted
		statusStyle = t.Success
	case "extra":
		statusIcon = theme.IconStatusCompleted
		statusStyle = t.Info
	default:
		statusIcon = theme.IconPending
		statusStyle = t.Muted
	}

	b.WriteString(statusStyle.Render(statusIcon + " " + node.Name))
	b.WriteString("  ")
	b.WriteString(t.Muted.Render(fmt.Sprintf("(from %s)", node.ParentSkill)))
	b.WriteString("\n\n")

	data, err := os.ReadFile(path)
	if err != nil {
		b.WriteString(t.Muted.Render("  File not yet produced."))
		b.WriteString("\n")
		return
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		b.WriteString(t.Muted.Render("  (empty file)"))
		b.WriteString("\n")
		return
	}

	// Apply markdown rendering to the content
	b.WriteString(markdown.Render(content, t))
}

// renderArtifactPreview reads an artifact file and renders a content preview with markdown styling.
func renderArtifactPreview(b *strings.Builder, path string, t *theme.Theme, maxLines int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return
	}

	// Truncate to preview limit
	lines := strings.Split(content, "\n")
	previewLimit := maxLines / 2
	if previewLimit < 5 {
		previewLimit = 5
	}
	if previewLimit > 20 {
		previewLimit = 20
	}

	truncated := false
	if len(lines) > previewLimit {
		lines = lines[:previewLimit]
		truncated = true
	}
	previewContent := strings.Join(lines, "\n")

	b.WriteString("\n")
	// Apply markdown rendering
	rendered := markdown.Render(previewContent, t)
	// Indent the rendered content
	for _, line := range strings.Split(rendered, "\n") {
		if line != "" {
			b.WriteString("    " + line + "\n")
		} else {
			b.WriteString("\n")
		}
	}
	if truncated {
		b.WriteString("    " + t.Muted.Render(fmt.Sprintf("... (%d more lines)", len(strings.Split(content, "\n"))-previewLimit)) + "\n")
	}
	b.WriteString("\n")
}

// extraArtifacts returns artifacts in produced that are not in expected.
func extraArtifacts(produced, expected []string) []string {
	expectedSet := make(map[string]bool, len(expected))
	for _, e := range expected {
		expectedSet[e] = true
	}
	var extras []string
	for _, p := range produced {
		if !expectedSet[p] {
			extras = append(extras, p)
		}
	}
	return extras
}

// skillStatusStyle returns the icon and color for a skill status.
func skillStatusStyle(status string) (string, lipgloss.Color) {
	switch status {
	case "completed":
		return theme.IconStatusCompleted, lipgloss.Color("#00cc00")
	case "running":
		return theme.IconStatusRunning, lipgloss.Color("#cccc00")
	case "failed":
		return theme.IconStatusFailed, lipgloss.Color("#cc0000")
	case "skipped":
		return "-", lipgloss.Color("#888888")
	default: // pending
		return theme.IconPending, lipgloss.Color("#888888")
	}
}

// capitalizeFirst capitalizes the first letter of a string.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
