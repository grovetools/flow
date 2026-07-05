package status

import (
	"strings"
	"testing"

	"github.com/grovetools/agentlogs/pkg/usage"

	"github.com/grovetools/flow/pkg/orchestration"
)

// newModelForModelCells builds a minimal Model with the caches the MODEL cell
// touches, defaulting the effective provider to claude (the config default).
func newModelForModelCells(planDir string) *Model {
	return &Model{
		PlanDir:             planDir,
		defaultProviderName: "claude",
		tokenColumnCache:    map[string]string{},
		modelColumnCache:    map[string]string{},
		runningTokenCell:    map[string]usage.Summary{},
	}
}

// TestResolveJobDisplayModel_ArtifactFallback: a job with no `model:`
// frontmatter surfaces the cost-dominant model from its token-usage.json
// artifact (ModelBreakdown[0]).
func TestResolveJobDisplayModel_ArtifactFallback(t *testing.T) {
	dir := t.TempDir()
	m := newModelForModelCells(dir)
	writeArtifact(t, dir, "codex-1", usage.Summary{
		ModelBreakdown: []usage.AgentUsage{{Model: "gpt-5.5"}},
	})
	job := &orchestration.Job{ID: "codex-1", Provider: "codex", Type: orchestration.JobTypeInteractiveAgent}

	if got := m.resolveJobDisplayModel(job); got != "gpt-5.5" {
		t.Errorf("resolveJobDisplayModel with empty frontmatter = %q, want %q from artifact", got, "gpt-5.5")
	}

	// Frontmatter still wins over the artifact when set.
	job.Model = "gpt-6"
	if got := m.resolveJobDisplayModel(job); got != "gpt-6" {
		t.Errorf("resolveJobDisplayModel = %q, want frontmatter %q to win", got, "gpt-6")
	}
}

// TestRenderModelCell_ClaudeRowsUnchanged: claude rows render byte-identically
// to the pre-change cell — a bare muted model with no " · " provider separator,
// and "-" when the model is unknown.
func TestRenderModelCell_ClaudeRowsUnchanged(t *testing.T) {
	dir := t.TempDir()
	m := newModelForModelCells(dir)

	claude := &orchestration.Job{ID: "claude-1", Provider: "claude", Model: "claude-fable-5"}
	got := m.renderModelColumnCell(claude)
	if strings.Contains(got, " · ") {
		t.Errorf("claude MODEL cell = %q, must not fold provider with a separator", got)
	}
	if !strings.Contains(stripANSI(got), "claude-fable-5") {
		t.Errorf("claude MODEL cell = %q, want bare model %q", got, "claude-fable-5")
	}

	// Empty everything (no frontmatter, no artifact, no running cell) → "-".
	empty := &orchestration.Job{ID: "claude-2", Provider: "claude"}
	if got := stripANSI(m.renderModelColumnCell(empty)); got != "-" {
		t.Errorf("claude MODEL cell with unknown model = %q, want %q", got, "-")
	}

	// Non-claude folds provider into the cell.
	codex := &orchestration.Job{ID: "codex-2", Provider: "codex", Model: "gpt-5.5"}
	if got := stripANSI(m.renderModelColumnCell(codex)); got != "codex · gpt-5.5" {
		t.Errorf("codex MODEL cell = %q, want %q", got, "codex · gpt-5.5")
	}
}

// TestInlineCell_LegacyPrependBool: a job that only sets the deprecated
// prepend_dependencies bool (empty Inline) renders "deps" in the INLINE column.
func TestInlineCell_LegacyPrependBool(t *testing.T) {
	legacy := &orchestration.Job{ID: "j1", PrependDependencies: true}
	if got := inlineCellText(legacy); got != "deps" {
		t.Errorf("inlineCellText(legacy prepend) = %q, want %q", got, "deps")
	}

	categories := &orchestration.Job{
		ID:     "j2",
		Inline: orchestration.InlineConfig{Categories: []orchestration.InlineCategory{orchestration.InlineDependencies, orchestration.InlineContext}},
	}
	if got := inlineCellText(categories); got != "dependencies,context" {
		t.Errorf("inlineCellText(categories) = %q, want %q", got, "dependencies,context")
	}

	none := &orchestration.Job{ID: "j3"}
	if got := inlineCellText(none); got != "-" {
		t.Errorf("inlineCellText(none) = %q, want %q", got, "-")
	}
}
