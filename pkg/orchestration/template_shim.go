package orchestration

import (
	"context"
	"strings"

	grovelogging "github.com/grovetools/core/logging"
)

// legacyTemplateShim maps deleted template names to their replacements.
// Values are encoded as "<kind>:<name>" where kind is either "skill" or
// "template". Skill-like templates (e.g. cx-builder) were converted into
// proper skills under .claude/skills; rarely-used generic oneshot
// templates fall back to the default "chat" template. See
// plans/groveterm-pt3/08-fixup-plan.md §2 and 05-playbook-design.md Part 2.
var legacyTemplateShim = map[string]string{
	// Former grove/agent/ templates that are now skills.
	"cx-builder":            "skill:cx-builder",
	"tend-tester":           "skill:tend-tester",
	"tui-explorer":          "skill:tui-explorer",
	"logging-guide":         "skill:logging-guide",
	"from-note-planner":     "skill:from-note-planner",
	"flow-qb":               "skill:flow-qb",
	"test-writer-tend":      "skill:test-writer-tend",
	"recipe-writer":         "skill:recipe-writer",
	"workspace-init-writer": "skill:workspace-init-writer",

	// Former generic/oneshot/ templates that fall back to the default chat template.
	"api-design":            "template:chat",
	"architecture-overview": "template:chat",
	"deployment-runbook":    "template:chat",
	"documentation":         "template:chat",
	"incident-postmortem":   "template:chat",
	"initial-plan":          "template:chat",
	"learning-guide":        "template:chat",
	"learning-lang":         "template:chat",
	"migration-plan":        "template:chat",
	"performance-analysis":  "template:chat",
	"refactoring-plan":      "template:chat",
	"refine-plan-generic":   "template:chat",
	"security-audit":        "template:chat",
	"tech-debt-assessment":  "template:chat",
	"test-strategy":         "template:chat",
	"concept-planner":       "template:chat",
}

// shimUlog is a package-level logger used to emit deprecation warnings
// from the legacy template shim.
var shimUlog = grovelogging.NewUnifiedLogger("grove-flow.template-shim")

// applyTemplateShim rewrites a job's Template field in place if it
// references a deleted template, either promoting it to a Skill or
// falling back to the default chat template. It emits a deprecation
// warning for every rewrite. Jobs whose template is unset or not in the
// shim map are left unchanged.
func applyTemplateShim(job *Job) {
	if job == nil || job.Template == "" {
		return
	}
	mapped, ok := legacyTemplateShim[job.Template]
	if !ok {
		return
	}
	kind, name, _ := strings.Cut(mapped, ":")
	ctx := context.Background()
	switch kind {
	case "skill":
		shimUlog.Warn("Job uses deprecated template; upgrading to skill").
			Field("job_id", job.ID).
			Field("old_template", job.Template).
			Field("new_skill", name).
			Log(ctx)
		// Only set the skill if the job does not already declare one,
		// so an explicit `skill:` in the frontmatter wins over the shim.
		if job.Skill == "" {
			job.Skill = name
		}
		job.Template = ""
	case "template":
		shimUlog.Warn("Job uses deleted template; falling back to default").
			Field("job_id", job.ID).
			Field("old_template", job.Template).
			Field("new_template", name).
			Log(ctx)
		job.Template = name
	}
}
