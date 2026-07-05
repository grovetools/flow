package status

import (
	"strings"

	"github.com/grovetools/core/tui/theme"

	"github.com/grovetools/flow/pkg/orchestration"
)

// resolveJobDisplayModel resolves the model name to show in the MODEL column,
// provider-neutrally. The chain fixes the historically-blank non-claude cell
// without mutating any committed plan files:
//  1. job.Model frontmatter — the claude path (backfilled post-run), unchanged;
//  2. the completed job's token-usage.json artifact (the cost-dominant model,
//     ModelBreakdown[0], which agentlogs sorts by cost descending);
//  3. the live running-ctx Summary for an in-progress agent job;
//  4. "" when the model is still unknown.
func (m *Model) resolveJobDisplayModel(job *orchestration.Job) string {
	if job == nil {
		return ""
	}
	if job.Model != "" {
		return job.Model
	}
	if s, ok := orchestration.ReadTokenUsageArtifact(m.PlanDir, job.ID); ok && len(s.ModelBreakdown) > 0 {
		if md := s.ModelBreakdown[0].Model; md != "" {
			return md
		}
	}
	if s, ok := m.runningTokenCell[job.ID]; ok && len(s.ModelBreakdown) > 0 {
		if md := s.ModelBreakdown[0].Model; md != "" {
			return md
		}
	}
	return ""
}

// resolveJobDisplayProvider returns the provider name effective for a job: the
// per-job `provider:` frontmatter when set, else the config default computed
// once in New() (m.defaultProviderName).
func (m *Model) resolveJobDisplayProvider(job *orchestration.Job) string {
	if job != nil && job.Provider != "" {
		return job.Provider
	}
	return m.defaultProviderName
}

// renderModelColumnCell renders the MODEL table cell for a job. Claude rows are
// byte-identical to the pre-change rendering (bare muted model, or "-" when
// unknown); non-claude rows fold the provider into the cell as
// "provider · model" (or just the provider when the model is still unknown).
// The formatted string is memoized per job ID in modelColumnCache to avoid
// re-reading the artifact on every frame (invalidated alongside the TOKENS
// cache via evictJobRenderCaches / clearTokenColumnCache).
func (m *Model) renderModelColumnCell(job *orchestration.Job) string {
	t := theme.DefaultTheme
	if job == nil {
		return t.Muted.Render("-")
	}
	if m.modelColumnCache != nil {
		if cached, ok := m.modelColumnCache[job.ID]; ok {
			return cached
		}
	}

	provider := m.resolveJobDisplayProvider(job)
	model := m.resolveJobDisplayModel(job)

	var cell string
	if provider == "claude" {
		// Claude rows render exactly as they did before this change.
		if model == "" {
			cell = t.Muted.Render("-")
		} else {
			cell = t.Muted.Render(model)
		}
	} else if model == "" {
		cell = t.Muted.Render(provider)
	} else {
		cell = t.Muted.Render(provider + " · " + model)
	}

	if m.modelColumnCache != nil {
		m.modelColumnCache[job.ID] = cell
	}
	return cell
}

// inlineCellText returns the plain (unstyled) text for the INLINE column: the
// job's inline categories joined by ",", or "deps" when only the legacy
// prepend_dependencies bool is set, or "-" when neither is present. The caller
// applies the muted style.
func inlineCellText(job *orchestration.Job) string {
	if job == nil {
		return "-"
	}
	if len(job.Inline.Categories) > 0 {
		parts := make([]string, len(job.Inline.Categories))
		for i, c := range job.Inline.Categories {
			parts[i] = string(c)
		}
		return strings.Join(parts, ",")
	}
	if job.PrependDependencies {
		return "deps"
	}
	return "-"
}
