package status

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/agentlogs/pkg/usage"
	"github.com/grovetools/core/pkg/sessions"
	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/flow/pkg/orchestration"
)

// TokenUsageLoadedMsg carries the result of resolving a job's token usage for
// the detail pane. Summary is valid only when Err is nil and Found is true; a
// found=false result means the job has no recorded usage (e.g. no session yet).
type TokenUsageLoadedMsg struct {
	JobID   string
	Summary usage.Summary
	Found   bool
	Err     error
}

// loadTokenUsageCmd resolves a job's token usage for the detail pane. A
// completed job reads its persisted .artifacts/<job>/token-usage.json
// artifact; a running (or otherwise non-completed) agent job summarizes live
// from the Claude session via the hooks registry. Non-agent jobs and jobs
// with no resolvable session yield Found=false.
func loadTokenUsageCmd(plan *orchestration.Plan, job *orchestration.Job) tea.Cmd {
	jobID := job.ID
	planDir := plan.Directory
	jobType := job.Type
	completed := job.Status == orchestration.JobStatusCompleted
	return func() tea.Msg {
		// Completed jobs: prefer the persisted artifact.
		if completed {
			if s, ok := orchestration.ReadTokenUsageArtifact(planDir, jobID); ok {
				return TokenUsageLoadedMsg{JobID: jobID, Summary: s, Found: true}
			}
		}

		// Only agent jobs have a Claude session worth summarizing live.
		if jobType != orchestration.JobTypeInteractiveAgent &&
			jobType != orchestration.JobTypeHeadlessAgent &&
			jobType != orchestration.JobTypeIsolatedAgent {
			return TokenUsageLoadedMsg{JobID: jobID, Found: false}
		}

		registry, err := sessions.NewFileSystemRegistry()
		if err != nil {
			return TokenUsageLoadedMsg{JobID: jobID, Err: err}
		}
		metadata, err := registry.Find(jobID)
		if err != nil || metadata.ClaudeSessionID == "" {
			return TokenUsageLoadedMsg{JobID: jobID, Found: false}
		}

		s, err := orchestration.SummarizeJobTokenUsage(metadata.ClaudeSessionID, metadata.TranscriptPath)
		if err != nil {
			return TokenUsageLoadedMsg{JobID: jobID, Err: err}
		}
		if s.Usage.Total() == 0 {
			return TokenUsageLoadedMsg{JobID: jobID, Found: false}
		}
		return TokenUsageLoadedMsg{JobID: jobID, Summary: s, Found: true}
	}
}

// renderTokenColumnCell renders the compact TOKENS table cell for a job:
// "<total> ($<cost>)" for jobs with recorded usage, "-" otherwise. Only
// completed jobs are read synchronously here (from the cheap cached artifact);
// running jobs show a muted "·" to avoid a live summarize on every frame. The
// result is memoized per job ID in tokenColumnCache.
func (m *Model) renderTokenColumnCell(job *orchestration.Job) string {
	t := theme.DefaultTheme
	if job == nil {
		return t.Muted.Render("-")
	}
	if cached, ok := m.tokenColumnCache[job.ID]; ok {
		return cached
	}

	var cell string
	if job.Status == orchestration.JobStatusCompleted {
		if s, ok := orchestration.ReadTokenUsageArtifact(m.PlanDir, job.ID); ok && s.Usage.Total() > 0 {
			cell = t.Muted.Render(formatTokenCell(s))
		} else {
			cell = t.Muted.Render("-")
		}
	} else {
		// Running/other jobs: a placeholder; the detail pane summarizes live.
		cell = t.Muted.Render("·")
	}

	m.tokenColumnCache[job.ID] = cell
	return cell
}

// formatTokenCell renders the compact TOKENS column value, cost-forward with
// the peak context size: "$0.31 · 27.2k ctx". Cost is the one honest magnitude
// (token classes price 0.1×–5×) and context size matches Claude Code's
// /context, unlike the cache-read-inflated cumulative total. Legacy artifacts
// lack ContextSize, so fall back to the cumulative total there.
func formatTokenCell(s usage.Summary) string {
	cost := orchestration.FormatCostUSD(s.CostUSD, s.MissingPricing)
	if s.ContextSize > 0 {
		return fmt.Sprintf("%s · %s ctx", cost, orchestration.FormatTokenCount(s.ContextSize))
	}
	return fmt.Sprintf("%s · %s", cost, orchestration.FormatTokenCount(s.Usage.Total()))
}

// clearTokenColumnCache invalidates the memoized TOKENS column cells so the
// next render re-reads artifacts. Called on refresh.
func (m *Model) clearTokenColumnCache() {
	m.tokenColumnCache = make(map[string]string)
}

// renderTokenPaneContent renders the full token usage detail pane body from a
// resolved Summary: a totals block, a token-class breakdown, a per-model
// breakdown, and a per-agent (job→agent) tree built from Summary.Agents.
func renderTokenPaneContent(s usage.Summary, found bool, loadErr error, width int) string {
	t := theme.DefaultTheme
	var b strings.Builder

	if loadErr != nil {
		return t.Error.Render(fmt.Sprintf("Failed to load token usage: %v", loadErr))
	}
	if !found {
		return t.Muted.Render("No token usage recorded for this job yet.")
	}

	cost := orchestration.FormatCostUSD(s.CostUSD, s.MissingPricing)
	b.WriteString(t.Bold.Render("Totals"))
	b.WriteString("\n")
	fmt.Fprintf(&b, "  Tokens:   %s\n", orchestration.FormatTokenCount(s.Usage.Total()))
	if s.ContextSize > 0 {
		// Peak window size (≈ Claude Code /context); the cumulative Tokens above
		// is cache-read-inflated and not comparable to it.
		fmt.Fprintf(&b, "  Context:  %s\n", orchestration.FormatTokenCount(s.ContextSize))
	}
	fmt.Fprintf(&b, "  Cost:     %s\n", cost)
	if s.MessageCount > 0 {
		fmt.Fprintf(&b, "  Messages: %d\n", s.MessageCount)
	}
	if len(s.Models) > 0 {
		fmt.Fprintf(&b, "  Models:   %s\n", strings.Join(s.Models, ", "))
	}

	b.WriteString("\n")
	b.WriteString(t.Bold.Render("Token breakdown"))
	b.WriteString("\n")
	fmt.Fprintf(&b, "  Input:          %s\n", orchestration.FormatTokenCount(s.Usage.Input))
	fmt.Fprintf(&b, "  Output:         %s\n", orchestration.FormatTokenCount(s.Usage.Output))
	fmt.Fprintf(&b, "  Cache read:     %s\n", orchestration.FormatTokenCount(s.Usage.CacheRead))
	fmt.Fprintf(&b, "  Cache write 5m: %s\n", orchestration.FormatTokenCount(s.Usage.CacheWrite5m))
	if s.Usage.CacheWrite1h > 0 {
		fmt.Fprintf(&b, "  Cache write 1h: %s\n", orchestration.FormatTokenCount(s.Usage.CacheWrite1h))
	}

	if len(s.ModelBreakdown) > 0 {
		b.WriteString("\n")
		b.WriteString(t.Bold.Render("Per-model"))
		b.WriteString("\n")
		for _, mdl := range s.ModelBreakdown {
			fmt.Fprintf(&b, "  %s: %s tokens, %s\n",
				mdl.Model,
				orchestration.FormatTokenCount(mdl.Usage.Total()),
				orchestration.FormatCostUSD(mdl.CostUSD, mdl.MissingPricing))
		}
	}

	if len(s.Agents) > 0 {
		b.WriteString("\n")
		b.WriteString(t.Bold.Render("Per-agent"))
		b.WriteString("\n")
		for i, a := range s.Agents {
			prefix := "├─ "
			if i == len(s.Agents)-1 {
				prefix = "└─ "
			}
			label := a.AgentType
			if label == "" {
				label = a.AgentID
			}
			if label == "" {
				label = "(parent)"
			}
			fmt.Fprintf(&b, "  %s%s: %s tokens, %s\n",
				prefix,
				label,
				orchestration.FormatTokenCount(a.Usage.Total()),
				orchestration.FormatCostUSD(a.CostUSD, a.MissingPricing))
		}
	}

	return b.String()
}
