package status

import (
	"fmt"
	"strings"
	"time"

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
	return func() tea.Msg {
		// Prefer the persisted artifact regardless of status: completed agent
		// jobs and chat/oneshot jobs (which accumulate one after every turn, even
		// while pending_user) are both served from it.
		if s, ok := orchestration.ReadTokenUsageArtifact(planDir, jobID); ok {
			return TokenUsageLoadedMsg{JobID: jobID, Summary: s, Found: true}
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

// runningTokenRefreshInterval throttles the running-ctx refresher: the refresh
// tick fires every 2s, but re-summarizing large transcripts that often is
// wasteful, so the refresher runs at most this often.
const runningTokenRefreshInterval = 4 * time.Second

// maybeRefreshRunningTokenCells returns the running-ctx refresh command when
// enough time has elapsed since the last run, else nil. Called from the
// refresh tick.
func (m *Model) maybeRefreshRunningTokenCells(now time.Time) tea.Cmd {
	if now.Sub(m.lastRunningTokenRefresh) < runningTokenRefreshInterval {
		return nil
	}
	m.lastRunningTokenRefresh = now

	// Evict memoized TOKENS cells for live chat/oneshot jobs so the next render
	// re-reads their token-usage artifact (rewritten after each turn, Phase 2).
	// Scoped to live non-agent jobs only: completed jobs' cells stay cached, so a
	// plan with many finished jobs re-reads nothing per tick. Agent live cells
	// refresh via runningTokenCell (below), not the artifact, so they're left
	// alone.
	for _, j := range m.Jobs {
		if isLiveAPIDirectJob(j) {
			delete(m.tokenColumnCache, j.ID)
		}
	}

	return refreshRunningTokenCellsCmd(m.Jobs)
}

// isLiveAPIDirectJob reports whether a job is an in-progress chat/oneshot job
// whose token-usage artifact is written incrementally (Phase 2), so its
// memoized TOKENS cell must be evicted each tick to pick up the newest turn.
func isLiveAPIDirectJob(job *orchestration.Job) bool {
	switch job.Type {
	case orchestration.JobTypeChat, orchestration.JobTypeOneshot:
	default:
		return false
	}
	switch job.Status {
	case orchestration.JobStatusRunning,
		orchestration.JobStatusPendingUser,
		orchestration.JobStatusPendingLLM:
		return true
	}
	return false
}

// runningTokenUsageMsg carries live token summaries for in-progress agent
// jobs, produced off the event loop by refreshRunningTokenCellsCmd. Keyed by
// job ID; jobs with no resolvable session or no usage are omitted.
type runningTokenUsageMsg struct {
	summaries map[string]usage.Summary
}

// isLiveAgentJob reports whether a job is an in-progress agent job worth
// summarizing live for the TOKENS column (a Claude session that may still be
// growing). Completed jobs use the persisted artifact instead.
func isLiveAgentJob(job *orchestration.Job) bool {
	switch job.Type {
	case orchestration.JobTypeInteractiveAgent,
		orchestration.JobTypeHeadlessAgent,
		orchestration.JobTypeIsolatedAgent:
	default:
		return false
	}
	switch job.Status {
	case orchestration.JobStatusRunning,
		orchestration.JobStatusIdle,
		orchestration.JobStatusPendingUser,
		orchestration.JobStatusPendingLLM:
		return true
	}
	return false
}

// refreshRunningTokenCellsCmd summarizes the live Claude session of each
// in-progress agent job off the event loop, so the TOKENS column can show a
// live "$cost · NNk ctx" for running jobs (peak context window) and their
// subagent rows their cost/total. It reuses the exact summarizer the detail
// pane and the persisted artifact use, so live and recorded numbers match.
// The job list is filtered on the event loop; only IDs cross into the
// goroutine (never *Job).
func refreshRunningTokenCellsCmd(jobs []*orchestration.Job) tea.Cmd {
	var targets []string
	for _, j := range jobs {
		if isLiveAgentJob(j) {
			targets = append(targets, j.ID)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	return func() tea.Msg {
		registry, err := sessions.NewFileSystemRegistry()
		if err != nil {
			return runningTokenUsageMsg{}
		}
		out := make(map[string]usage.Summary, len(targets))
		for _, id := range targets {
			metadata, err := registry.Find(id)
			if err != nil || metadata.ClaudeSessionID == "" {
				continue
			}
			s, err := orchestration.SummarizeJobTokenUsage(metadata.ClaudeSessionID, metadata.TranscriptPath)
			if err != nil || s.Usage.Total() == 0 {
				continue
			}
			out[id] = s
		}
		return runningTokenUsageMsg{summaries: out}
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
	} else if s, ok := orchestration.ReadTokenUsageArtifact(m.PlanDir, job.ID); ok && s.Usage.Total() > 0 {
		// Non-completed job with an incrementally-written artifact: chat/oneshot
		// jobs get one after every turn (Phase 2), so this lights up the cell for
		// a pending_user chat. Harmless for agent jobs — they have no artifact
		// until completion, so this falls through to the live path below.
		cell = t.Muted.Render(formatTokenCell(s))
	} else if s, ok := m.runningTokenCell[job.ID]; ok && s.Usage.Total() > 0 {
		// Running agent jobs: the live "$cost · NNk ctx" from the background
		// running-ctx refresher (peak context window, ≈ Claude Code /context).
		cell = t.Muted.Render(formatTokenCell(s))
	} else {
		// No live summary yet (session not registered, or refresher hasn't
		// run): a placeholder; the detail pane summarizes live on demand.
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
// next render re-reads artifacts. Called on refresh. The per-agent maps are
// deliberately NOT cleared here: tokenAgentArtifact is immutable once read,
// and tokenAgentLive is overwritten as fresher summaries arrive (and ignored
// once a job completes), so clearing them would only blank running subagent
// cells between the 2s refresh and the next live summary.
func (m *Model) clearTokenColumnCache() {
	m.tokenColumnCache = make(map[string]string)
}

// renderAgentTokenCell renders the TOKENS cell for a RowTypeAgent (subagent)
// row: "$cost · <tokens>" when usage is known, "" otherwise (matching the
// blank cells of other virtual rows). Completed jobs read the immutable
// artifact; running jobs use whatever live Summary the detail pane or the
// running-ctx refresher last produced.
func (m *Model) renderAgentTokenCell(dr *DisplayRow) string {
	if dr == nil || dr.Agent == nil || dr.Job == nil {
		return ""
	}
	mp := m.agentUsageMapForJob(dr.Job)
	au, ok := mp[dr.Agent.ID]
	if !ok {
		return ""
	}
	return theme.DefaultTheme.Muted.Render(formatAgentTokenCell(au))
}

// agentUsageMapForJob returns the per-subagent usage map for a job (agentID →
// AgentUsage). Completed jobs are served from the immutable artifact (read
// once, then cached); running jobs from the live cache. Returns nil when no
// per-agent data is available yet.
func (m *Model) agentUsageMapForJob(job *orchestration.Job) map[string]usage.AgentUsage {
	if job.Status == orchestration.JobStatusCompleted {
		if mp, ok := m.tokenAgentArtifact[job.ID]; ok {
			return mp
		}
		var mp map[string]usage.AgentUsage
		if s, ok := orchestration.ReadTokenUsageArtifact(m.PlanDir, job.ID); ok {
			mp = agentUsageMap(s)
		}
		m.tokenAgentArtifact[job.ID] = mp // cache even nil to avoid re-reading
		return mp
	}
	return m.tokenAgentLive[job.ID]
}

// agentUsageMap indexes a Summary's per-agent breakdown by agent ID, skipping
// the synthetic "parent" entry (the owning job, not a subagent). Returns nil
// when there are no real subagents.
func agentUsageMap(s usage.Summary) map[string]usage.AgentUsage {
	if len(s.Agents) == 0 {
		return nil
	}
	mp := make(map[string]usage.AgentUsage, len(s.Agents))
	for _, a := range s.Agents {
		if a.AgentID == "" || a.AgentID == "parent" {
			continue
		}
		mp[a.AgentID] = a
	}
	if len(mp) == 0 {
		return nil
	}
	return mp
}

// formatAgentTokenCell renders a subagent's TOKENS cell, cost-forward with the
// cumulative token total: "$0.42 · 1.2M". Unlike the job cell there is no
// per-agent context size (AgentUsage carries no ContextSize), so the total
// stands in for the magnitude.
func formatAgentTokenCell(au usage.AgentUsage) string {
	cost := orchestration.FormatCostUSD(au.CostUSD, au.MissingPricing)
	return fmt.Sprintf("%s · %s", cost, orchestration.FormatTokenCount(au.Usage.Total()))
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
