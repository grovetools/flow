package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/agentlogs/pkg/usage"
	coresessions "github.com/grovetools/core/pkg/sessions"
)

// tokenUsageSectionHeader is the job-.md section holding the per-job token
// usage + cost summary. Like "# Workflow Runs" and "# Subagents" it is
// rebuilt wholesale on every completion and inserted before the agent chat
// transcript section. It is a level-2 heading (per the spec) so it nests
// visually under the higher-level archival sections.
const tokenUsageSectionHeader = "## Token Usage"

// tokenUsageArtifactName is the per-job artifact holding the serialized
// usage.Summary, read back by the status TUI for completed jobs.
const tokenUsageArtifactName = "token-usage.json"

// ArchiveTokenUsage resolves the job's Claude session, summarizes its token
// usage + cost across the parent transcript plus all ad-hoc and workflow
// subagents (via agentlogs/pkg/usage), writes the Summary to
// .artifacts/<job-id>/token-usage.json, and rebuilds the job .md's
// "## Token Usage" section. Every failure path warns and returns nil: token
// accounting must never fail job completion. An unverified session binding
// (no registry entry or no recorded Claude session) is a silent no-op.
func ArchiveTokenUsage(job *Job, plan *Plan) error {
	ctx := context.Background()

	if job.Type != JobTypeInteractiveAgent && job.Type != JobTypeHeadlessAgent && job.Type != JobTypeIsolatedAgent {
		return nil
	}

	registry, err := coresessions.NewFileSystemRegistry()
	if err != nil {
		ulog.Warn("[TOKENS] Failed to create session registry; skipping token usage").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
		return nil
	}

	metadata, err := registry.Find(job.ID)
	if err != nil || metadata.ClaudeSessionID == "" {
		// Unverified binding: nothing trustworthy to summarize.
		ulog.Debug("[TOKENS] Session binding unverified; skipping token usage").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
		return nil
	}

	summary, err := SummarizeJobTokenUsage(metadata.ClaudeSessionID, metadata.TranscriptPath)
	if err != nil {
		ulog.Warn("[TOKENS] Failed to summarize token usage; skipping").
			Field("job_id", job.ID).
			Field("claude_session_id", metadata.ClaudeSessionID).
			Err(err).
			Log(ctx)
		return nil
	}

	// A session with no billable tokens (e.g. the transcript hasn't been
	// flushed, or resolution found nothing) leaves the job file untouched
	// rather than writing an empty/misleading section.
	if summary.Usage.Total() == 0 {
		ulog.Debug("[TOKENS] No token usage found for session; skipping").
			Field("job_id", job.ID).
			Field("claude_session_id", metadata.ClaudeSessionID).
			Log(ctx)
		return nil
	}

	// Write the artifact JSON (the TUI reads this back for completed jobs).
	destArtifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
	if err := os.MkdirAll(destArtifactDir, 0o755); err != nil {
		ulog.Warn("[TOKENS] Failed to create artifact directory; skipping token usage").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
		return nil
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		ulog.Warn("[TOKENS] Failed to marshal token usage summary; skipping").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
		return nil
	}
	if err := os.WriteFile(filepath.Join(destArtifactDir, tokenUsageArtifactName), data, 0o600); err != nil {
		ulog.Warn("[TOKENS] Failed to write token-usage.json").
			Field("job_id", job.ID).
			Err(err).
			Log(ctx)
		// Still attempt the markdown section below.
	}

	// Rebuild the job .md's "## Token Usage" section via the locked
	// StatePersister, mirroring the "# Workflow Runs" rebuild.
	if job.FilePath != "" {
		content := renderTokenUsageSection(summary)
		if _, err := NewStatePersister().UpdateJobSection(job, tokenUsageSectionHeader, content, transcriptSectionHeader, false); err != nil {
			ulog.Warn("[TOKENS] Failed to update token usage section in job file").
				Field("job_id", job.ID).
				Field("filepath", job.FilePath).
				Err(err).
				Log(ctx)
		}
	}

	return nil
}

// TokenUsageArtifactPath returns the path to a job's token-usage.json artifact
// under the plan's .artifacts dir.
func TokenUsageArtifactPath(planDir, jobID string) string {
	return filepath.Join(planDir, ".artifacts", jobID, tokenUsageArtifactName)
}

// ReadTokenUsageArtifact loads a completed job's persisted token usage summary
// from .artifacts/<job-id>/token-usage.json. The bool reports whether the
// artifact was present and parseable.
func ReadTokenUsageArtifact(planDir, jobID string) (usage.Summary, bool) {
	data, err := os.ReadFile(TokenUsageArtifactPath(planDir, jobID))
	if err != nil {
		return usage.Summary{}, false
	}
	var s usage.Summary
	if err := json.Unmarshal(data, &s); err != nil {
		return usage.Summary{}, false
	}
	return s, true
}

// FormatTokenCount renders an integer token count with thousands separators
// for compact display (exported for the status TUI's TOKENS column/pane).
func FormatTokenCount(n int64) string {
	return formatTokenCount(n)
}

// FormatCostUSD renders a USD cost; an unpriced model surfaces a warning
// rather than a misleading $0.00 (exported for the status TUI).
func FormatCostUSD(cost float64, missingPricing bool) string {
	return formatCostUSD(cost, missingPricing)
}

// SummarizeJobTokenUsage resolves every project-slug dir holding the given
// Claude session (handling slug fragmentation via ResolveClaudeSessionDirs,
// falling back to the transcript's own dir) and returns the deduped,
// cache-aware, subagent-inclusive usage.Summary for that session. It is the
// shared live summarizer used by both completion-time persistence and the
// status TUI's running-job token pane.
func SummarizeJobTokenUsage(claudeSessionID, transcriptPath string) (usage.Summary, error) {
	slugDirs := resolveTokenUsageSlugDirs(claudeSessionID, transcriptPath)
	return usage.SummarizeSession(slugDirs, claudeSessionID, usage.CostModeCalculate)
}

// resolveTokenUsageSlugDirs returns the project-slug directories to scan for a
// session's transcripts. ResolveClaudeSessionDirs returns the per-session
// subdirs (…/<slug>/<session-id>/); usage.SummarizeSession wants the parent
// SLUG dirs (…/<slug>/) so it can also find the sibling <session-id>.jsonl
// parent transcript and ad-hoc agent-*.jsonl files. So we map each resolved
// dir up to its slug parent and dedupe. The transcript's own slug dir is
// always included as a fallback.
func resolveTokenUsageSlugDirs(claudeSessionID, transcriptPath string) []string {
	seen := make(map[string]bool)
	var slugDirs []string
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		slugDirs = append(slugDirs, dir)
	}

	sessionDirs, err := coresessions.ResolveClaudeSessionDirs(claudeSessionID)
	if err == nil {
		for _, dir := range sessionDirs {
			// dir is …/<slug>/<session-id>; the slug dir is its parent.
			add(filepath.Dir(dir))
		}
	}

	// Fallback: the slug dir directly holding the parent transcript
	// (~/.claude/projects/<slug>/<session-id>.jsonl).
	if transcriptPath != "" {
		add(filepath.Dir(transcriptPath))
	}

	// When nothing resolved, returning an empty slice makes
	// usage.SummarizeSession scan all of ~/.claude/projects itself.
	return slugDirs
}

// renderTokenUsageSection renders the body of the job .md's "## Token Usage"
// section (without the heading itself): a totals line, a token-class
// breakdown, and a per-model breakdown. Costs are formatted to cents; an
// unpriced model surfaces a "⚠ unpriced" marker rather than a misleading $0.
func renderTokenUsageSection(s usage.Summary) string {
	var b strings.Builder
	b.WriteString("\n")

	fmt.Fprintf(&b, "- Total tokens: %s\n", formatTokenCount(s.Usage.Total()))
	fmt.Fprintf(&b, "- Cost: %s\n", formatCostUSD(s.CostUSD, s.MissingPricing))
	if len(s.Models) > 0 {
		fmt.Fprintf(&b, "- Models: %s\n", strings.Join(s.Models, ", "))
	}
	if s.MessageCount > 0 {
		fmt.Fprintf(&b, "- Messages: %d\n", s.MessageCount)
	}

	b.WriteString("\n### Token breakdown\n\n")
	fmt.Fprintf(&b, "- Input: %s\n", formatTokenCount(s.Usage.Input))
	fmt.Fprintf(&b, "- Output: %s\n", formatTokenCount(s.Usage.Output))
	fmt.Fprintf(&b, "- Cache read: %s\n", formatTokenCount(s.Usage.CacheRead))
	fmt.Fprintf(&b, "- Cache write (5m): %s\n", formatTokenCount(s.Usage.CacheWrite5m))
	if s.Usage.CacheWrite1h > 0 {
		fmt.Fprintf(&b, "- Cache write (1h): %s\n", formatTokenCount(s.Usage.CacheWrite1h))
	}

	if len(s.ModelBreakdown) > 0 {
		b.WriteString("\n### Per-model\n\n")
		for _, m := range s.ModelBreakdown {
			fmt.Fprintf(&b, "- `%s`: %s tokens, %s\n",
				m.Model, formatTokenCount(m.Usage.Total()), formatCostUSD(m.CostUSD, m.MissingPricing))
		}
	}

	return b.String()
}

// formatCostUSD renders a USD cost. When missingPricing is set the model
// table had no entry, so the cost is unreliable and surfaced as a warning
// rather than a misleading $0.00.
func formatCostUSD(cost float64, missingPricing bool) string {
	if missingPricing {
		return "⚠ unpriced"
	}
	return fmt.Sprintf("$%.2f", cost)
}

// formatTokenCount renders an integer token count with thousands separators
// for readability in the markdown section.
func formatTokenCount(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		return "-" + out
	}
	return out
}
