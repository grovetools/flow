package orchestration

// ApplyPlanDefaults stamps plan-level configuration defaults onto a newly
// created job. It is the single source of truth for "inherit from .grove-plan.yml",
// replacing the per-site copies that previously lived in cmd/plan_add_step.go,
// cmd/plan_extract.go, pkg/orchestration/recipes.go, and pkg/tui/view/model.go —
// copies that had drifted (different field sets) and let an unguarded model
// default stamp gemini-* onto agent jobs.
//
// Contract: call this AFTER all explicit inputs (CLI flags, template frontmatter,
// interactive wizard choices) have been written onto the job, but BEFORE the job
// is persisted. Every field is applied only when the job has not already set it,
// so explicit overrides always win. It is a no-op when the plan carries no config.
func ApplyPlanDefaults(plan *Plan, job *Job) {
	if plan == nil || plan.Config == nil || job == nil {
		return
	}
	cfg := plan.Config

	// Model: only oneshot/chat jobs inherit the plan default (typically a
	// gemini-* chat model). Agent jobs select their own model at launch.
	if job.Model == "" && job.Type.InheritsPlanModel() && cfg.Model != "" {
		job.Model = cfg.Model
	}
	if job.Worktree == "" && cfg.Worktree != "" {
		job.Worktree = cfg.Worktree
	}
	if job.Inline.IsEmpty() && !cfg.Inline.IsEmpty() {
		job.Inline = cfg.Inline
	}
	// Deprecated prepend_dependencies: honored only when neither the job nor the
	// plan expresses the newer inline config (mirrors the prior per-site logic).
	if !job.PrependDependencies && job.Inline.IsEmpty() && cfg.PrependDependencies {
		job.PrependDependencies = true
	}
}
