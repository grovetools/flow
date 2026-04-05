package status_tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/flow/pkg/orchestration"
)

// SkillDisplayNode represents a skill in the skill pane tree for rendering.
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

// renderInteractiveSkillPane builds the skill pane with cursor highlight and artifact detail.
func renderInteractiveSkillPane(plan *orchestration.Plan, job *orchestration.Job, cursor int, width, height int) (string, []*SkillDisplayNode, map[string]orchestration.SkillFidelityState) {
	if len(job.SkillSequence) == 0 {
		return theme.DefaultTheme.Muted.Render("No skill sequence defined for this job."), nil, nil
	}

	artifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
	stateMap := readSkillStateMap(artifactDir)

	_, flatNodes := resolveSkillDisplayNodes(plan, job)

	if len(flatNodes) == 0 {
		return theme.DefaultTheme.Muted.Render("No skills resolved."), nil, stateMap
	}

	// Clamp cursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(flatNodes) {
		cursor = len(flatNodes) - 1
	}

	detailHeight := height - len(flatNodes) - 6 // Reserve space for header, summary, separator
	if detailHeight < 5 {
		detailHeight = 5
	}

	var b strings.Builder

	// Header
	b.WriteString(theme.DefaultTheme.Info.Bold(true).Render("Skill Sequence"))
	b.WriteString("\n\n")

	// Render tree with cursor
	for i, node := range flatNodes {
		renderSkillNodeLine(&b, node, stateMap, i == cursor)
	}

	// Summary
	b.WriteString("\n")
	renderSkillSummary(&b, flatNodes, stateMap)

	// Separator
	b.WriteString("\n")
	sepWidth := width - 4
	if sepWidth < 10 {
		sepWidth = 10
	}
	b.WriteString(theme.DefaultTheme.Muted.Render(strings.Repeat("─", sepWidth)))
	b.WriteString("\n")

	// Artifact detail for selected skill
	selectedNode := flatNodes[cursor]
	renderSkillArtifactDetail(&b, selectedNode, stateMap, plan, job, detailHeight)

	return b.String(), flatNodes, stateMap
}

// renderSkillNodeLine renders a single skill node line with optional cursor highlight.
func renderSkillNodeLine(b *strings.Builder, node *SkillDisplayNode, stateMap map[string]orchestration.SkillFidelityState, isCursor bool) {
	state, exists := stateMap[node.Name]
	if !exists {
		state = orchestration.SkillFidelityState{Status: "pending"}
	}

	indent := strings.Repeat("  ", node.Depth)

	// Icon and color based on status
	icon, color := skillStatusStyle(state.Status)
	styledIcon := lipgloss.NewStyle().Foreground(color).Render(icon)

	// Status label
	statusLabel := fmt.Sprintf("[%s]", capitalizeFirst(state.Status))
	styledStatus := lipgloss.NewStyle().Foreground(color).Render(statusLabel)

	// Cursor indicator
	cursorStr := "  "
	if isCursor {
		cursorStr = theme.DefaultTheme.Highlight.Render(theme.IconArrowRightBold + " ")
	}

	// Base line
	line := fmt.Sprintf("%s%s%s %-18s %s", cursorStr, indent, styledIcon, node.Name, styledStatus)

	// Supplementary info
	switch state.Status {
	case "completed", "running":
		if len(state.ArtifactsExpected) > 0 {
			artifactInfo := fmt.Sprintf("Artifacts: %d/%d", len(state.ArtifactsProduced), len(state.ArtifactsExpected))
			if len(state.ArtifactsProduced) == len(state.ArtifactsExpected) {
				line += "  " + theme.DefaultTheme.Success.Render(artifactInfo)
			} else {
				line += "  " + theme.DefaultTheme.Warning.Render(artifactInfo)
			}
		}
	case "failed":
		if state.Error != nil && *state.Error != "" {
			line += "  " + theme.DefaultTheme.Error.Render(fmt.Sprintf("Error: %s", *state.Error))
		}
		if state.DiagnosticPath != nil && *state.DiagnosticPath != "" {
			line += "  " + theme.DefaultTheme.Muted.Render(fmt.Sprintf("Diag: %s", filepath.Base(*state.DiagnosticPath)))
		}
	}

	b.WriteString(line + "\n")

	// Show feedback if present
	if state.Feedback != nil && *state.Feedback != "" {
		feedbackLine := fmt.Sprintf("  %s  %s", indent, theme.DefaultTheme.Muted.Render(fmt.Sprintf("Feedback: %s", *state.Feedback)))
		b.WriteString(feedbackLine + "\n")
	}
}

// renderSkillSummary renders the progress summary line.
func renderSkillSummary(b *strings.Builder, flatNodes []*SkillDisplayNode, stateMap map[string]orchestration.SkillFidelityState) {
	total := len(flatNodes)
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
func renderSkillArtifactDetail(b *strings.Builder, node *SkillDisplayNode, stateMap map[string]orchestration.SkillFidelityState, plan *orchestration.Plan, job *orchestration.Job, maxHeight int) {
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
				// Show artifact content preview
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

		// Try to read and show diagnostic content preview
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

// renderArtifactPreview reads an artifact file and renders a content preview.
func renderArtifactPreview(b *strings.Builder, path string, t *theme.Theme, maxLines int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return
	}

	lines := strings.Split(content, "\n")
	previewLimit := maxLines / 2
	if previewLimit < 5 {
		previewLimit = 5
	}
	if previewLimit > 20 {
		previewLimit = 20
	}

	b.WriteString("\n")
	truncated := false
	if len(lines) > previewLimit {
		lines = lines[:previewLimit]
		truncated = true
	}
	for _, line := range lines {
		b.WriteString("    " + t.Muted.Render(line) + "\n")
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
