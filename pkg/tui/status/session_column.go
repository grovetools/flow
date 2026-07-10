package status

import (
	"github.com/charmbracelet/x/ansi"
	"github.com/grovetools/core/pkg/sessions"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/flow/pkg/orchestration"
)

// renderSessionColumnCell shows the agent CLI's native session ID. SessionID
// itself is normally the Grove job ID, so it would duplicate JOB. A miss is
// cached too: chat/oneshot jobs do not have agent sessions and must not stat
// the registry on every frame.
func (m *Model) renderSessionColumnCell(job *orchestration.Job) string {
	if job == nil {
		return theme.DefaultTheme.Muted.Render("-")
	}
	if m.sessionColumnCache != nil {
		if cell, ok := m.sessionColumnCache[job.ID]; ok {
			return cell
		}
	}
	cell := theme.DefaultTheme.Muted.Render("-")
	if registry, err := sessions.NewFileSystemRegistry(); err == nil {
		if metadata, err := registry.Find(job.ID); err == nil && metadata != nil && metadata.ClaudeSessionID != "" {
			cell = theme.DefaultTheme.Muted.Render(ansi.Truncate(metadata.ClaudeSessionID, 10, "…"))
		}
	}
	if m.sessionColumnCache != nil {
		m.sessionColumnCache[job.ID] = cell
	}
	return cell
}
