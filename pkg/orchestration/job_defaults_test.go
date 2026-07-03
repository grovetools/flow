package orchestration

import "testing"

func nonEmptyInline() InlineConfig {
	return InlineConfig{Categories: []InlineCategory{InlineDependencies}}
}

// TestApplyPlanDefaults is the single regression guard for plan-default
// inheritance across every job-creation site (CLI add, TUI wizard, recipe
// expansion, plan extract), which previously had hand-duplicated, drifted copies.
func TestApplyPlanDefaults(t *testing.T) {
	tests := []struct {
		name string
		plan *Plan
		job  Job
		want Job
	}{
		{
			name: "nil plan is a no-op",
			plan: nil,
			job:  Job{Type: JobTypeOneshot},
			want: Job{Type: JobTypeOneshot},
		},
		{
			name: "nil config is a no-op",
			plan: &Plan{},
			job:  Job{Type: JobTypeOneshot},
			want: Job{Type: JobTypeOneshot},
		},
		{
			name: "oneshot inherits plan model",
			plan: &Plan{Config: &PlanConfig{Model: "gemini-3.5-flash"}},
			job:  Job{Type: JobTypeOneshot},
			want: Job{Type: JobTypeOneshot, Model: "gemini-3.5-flash"},
		},
		{
			name: "chat inherits plan model",
			plan: &Plan{Config: &PlanConfig{Model: "gemini-3.5-flash"}},
			job:  Job{Type: JobTypeChat},
			want: Job{Type: JobTypeChat, Model: "gemini-3.5-flash"},
		},
		{
			name: "agent-responded chat does NOT inherit plan model",
			plan: &Plan{Config: &PlanConfig{Model: "gemini-3.5-flash"}},
			job:  Job{Type: JobTypeChat, Responder: "agent"},
			want: Job{Type: JobTypeChat, Responder: "agent"}, // Model stays empty: never dispatched to an LLM
		},
		{
			name: "oracle-responder chat still inherits plan model",
			plan: &Plan{Config: &PlanConfig{Model: "gemini-3.5-flash"}},
			job:  Job{Type: JobTypeChat, Responder: "oracle"},
			want: Job{Type: JobTypeChat, Responder: "oracle", Model: "gemini-3.5-flash"},
		},
		{
			name: "headless agent does NOT inherit plan model",
			plan: &Plan{Config: &PlanConfig{Model: "gemini-3.5-flash"}},
			job:  Job{Type: JobTypeHeadlessAgent},
			want: Job{Type: JobTypeHeadlessAgent}, // Model stays empty
		},
		{
			name: "interactive agent does NOT inherit plan model",
			plan: &Plan{Config: &PlanConfig{Model: "gemini-3.5-flash"}},
			job:  Job{Type: JobTypeInteractiveAgent},
			want: Job{Type: JobTypeInteractiveAgent},
		},
		{
			name: "explicit job model wins over plan default",
			plan: &Plan{Config: &PlanConfig{Model: "gemini-3.5-flash"}},
			job:  Job{Type: JobTypeChat, Model: "claude-opus-4-8"},
			want: Job{Type: JobTypeChat, Model: "claude-opus-4-8"},
		},
		{
			name: "explicit worktree wins over plan default",
			plan: &Plan{Config: &PlanConfig{Worktree: "default-wt"}},
			job:  Job{Type: JobTypeOneshot, Worktree: "explicit-wt"},
			want: Job{Type: JobTypeOneshot, Worktree: "explicit-wt"},
		},
		{
			name: "worktree inherited when unset (any type)",
			plan: &Plan{Config: &PlanConfig{Worktree: "default-wt"}},
			job:  Job{Type: JobTypeHeadlessAgent},
			want: Job{Type: JobTypeHeadlessAgent, Worktree: "default-wt"},
		},
		{
			name: "inline propagated when job inline empty",
			plan: &Plan{Config: &PlanConfig{Inline: nonEmptyInline()}},
			job:  Job{Type: JobTypeOneshot},
			want: Job{Type: JobTypeOneshot, Inline: nonEmptyInline()},
		},
		{
			name: "legacy prepend_dependencies applied when no inline anywhere",
			plan: &Plan{Config: &PlanConfig{PrependDependencies: true}},
			job:  Job{Type: JobTypeOneshot},
			want: Job{Type: JobTypeOneshot, PrependDependencies: true},
		},
		{
			name: "inline supersedes legacy prepend_dependencies",
			plan: &Plan{Config: &PlanConfig{Inline: nonEmptyInline(), PrependDependencies: true}},
			job:  Job{Type: JobTypeOneshot},
			want: Job{Type: JobTypeOneshot, Inline: nonEmptyInline()}, // PrependDependencies stays false
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := tt.job
			ApplyPlanDefaults(tt.plan, &job)
			if job.Model != tt.want.Model {
				t.Errorf("Model = %q, want %q", job.Model, tt.want.Model)
			}
			if job.Worktree != tt.want.Worktree {
				t.Errorf("Worktree = %q, want %q", job.Worktree, tt.want.Worktree)
			}
			if job.Inline.IsEmpty() != tt.want.Inline.IsEmpty() {
				t.Errorf("Inline.IsEmpty() = %v, want %v", job.Inline.IsEmpty(), tt.want.Inline.IsEmpty())
			}
			if job.PrependDependencies != tt.want.PrependDependencies {
				t.Errorf("PrependDependencies = %v, want %v", job.PrependDependencies, tt.want.PrependDependencies)
			}
		})
	}
}
