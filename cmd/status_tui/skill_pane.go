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

// SkillDisplayNode represents a skill in the fidelity tree for rendering.
type SkillDisplayNode struct {
	Name     string
	Depth    int
	Children []*SkillDisplayNode
}

// buildDisplayTree converts SkillSequenceNodes to a flat display tree with depth info.
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

// renderSkillFidelityContent builds the skill fidelity pane content string
// by reading status.json files from the job's artifact directory.
func renderSkillFidelityContent(plan *orchestration.Plan, job *orchestration.Job, width int) string {
	if len(job.SkillSequence) == 0 {
		return theme.DefaultTheme.Muted.Render("No skill sequence defined for this job.")
	}

	artifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)

	// Read all status files
	stateMap := make(map[string]orchestration.SkillFidelityState)
	files, err := os.ReadDir(artifactDir)
	if err == nil {
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
	}

	// Resolve skill sequence to get tree structure
	workDir := plan.Directory
	if plan.Config != nil && plan.Config.Worktree != "" {
		// Try to use the worktree path if available
		if wd, wdErr := os.Getwd(); wdErr == nil {
			workDir = wd
		}
	}

	sequenceNodes, resolveErr := orchestration.ResolveSkillSequenceMetadata(job.SkillSequence, workDir)
	if resolveErr != nil {
		// Fall back to a flat list from the skill_sequence field
		return renderFlatFidelity(job.SkillSequence, stateMap)
	}

	displayNodes := buildDisplayTree(sequenceNodes, 0)

	var b strings.Builder
	b.WriteString(theme.DefaultTheme.Info.Bold(true).Render("Skill Sequence Fidelity"))
	b.WriteString("\n\n")

	for _, node := range displayNodes {
		renderFidelityNode(&b, node, stateMap)
	}

	// Summary
	b.WriteString("\n")
	total := countAllNodes(displayNodes)
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

	return b.String()
}

// renderFidelityNode renders a single node with its children in the fidelity tree.
func renderFidelityNode(b *strings.Builder, node *SkillDisplayNode, stateMap map[string]orchestration.SkillFidelityState) {
	state, exists := stateMap[node.Name]
	if !exists {
		state = orchestration.SkillFidelityState{Status: "pending"}
	}

	indent := strings.Repeat("  ", node.Depth)

	// Icon and color based on status
	icon, color := fidelityStatusStyle(state.Status)
	styledIcon := lipgloss.NewStyle().Foreground(color).Render(icon)

	// Status label
	statusLabel := fmt.Sprintf("[%s]", capitalizeFirst(state.Status))
	styledStatus := lipgloss.NewStyle().Foreground(color).Render(statusLabel)

	// Base line
	line := fmt.Sprintf("  %s%s %-18s %s", indent, styledIcon, node.Name, styledStatus)

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

	// Recurse children
	for _, child := range node.Children {
		renderFidelityNode(b, child, stateMap)
	}
}

// fidelityStatusStyle returns the icon and color for a skill fidelity status.
func fidelityStatusStyle(status string) (string, lipgloss.Color) {
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

// countAllNodes counts all nodes in the display tree recursively.
func countAllNodes(nodes []*SkillDisplayNode) int {
	count := 0
	for _, node := range nodes {
		count++
		count += countAllNodes(node.Children)
	}
	return count
}

// renderFlatFidelity renders a simple flat list when skill resolution fails.
func renderFlatFidelity(skillNames []string, stateMap map[string]orchestration.SkillFidelityState) string {
	var b strings.Builder
	b.WriteString(theme.DefaultTheme.Info.Bold(true).Render("Skill Sequence Fidelity"))
	b.WriteString("\n\n")

	for _, name := range skillNames {
		state, exists := stateMap[name]
		if !exists {
			state = orchestration.SkillFidelityState{Status: "pending"}
		}
		icon, color := fidelityStatusStyle(state.Status)
		styledIcon := lipgloss.NewStyle().Foreground(color).Render(icon)
		statusLabel := fmt.Sprintf("[%s]", capitalizeFirst(state.Status))
		styledStatus := lipgloss.NewStyle().Foreground(color).Render(statusLabel)
		b.WriteString(fmt.Sprintf("  %s %-18s %s\n", styledIcon, name, styledStatus))
	}

	return b.String()
}
