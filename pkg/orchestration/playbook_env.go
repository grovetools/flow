package orchestration

import (
	"fmt"

	"github.com/grovetools/skills/pkg/skills"
)

// resolvePlaybookRootForJob determines which playbook is active for the given
// job. It consults the per-job override first, then falls back to the plan's
// .grove-plan.yml manifest (plan.Config.Playbook). Returns the active name
// and the absolute path to that playbook, or the empty strings if no
// playbook is active or the playbook cannot be resolved.
func resolvePlaybookRootForJob(job *Job, plan *Plan) (name, root string) {
	if job != nil && job.Playbook != "" {
		name = job.Playbook
	} else if plan != nil && plan.Config != nil && plan.Config.Playbook != "" {
		name = plan.Config.Playbook
	}
	if name == "" {
		return "", ""
	}
	path, err := skills.ResolvePlaybookPath(name)
	if err != nil {
		return name, ""
	}
	return name, path
}

// playbookEnvExports returns a shell fragment of `export KEY='VALUE'`
// statements that configure PLAYBOOK_ROOT (and friends) for a job's agent
// subprocess. Returns an empty string if no playbook is active.
func playbookEnvExports(job *Job, plan *Plan) string {
	name, root := resolvePlaybookRootForJob(job, plan)
	if root == "" {
		return ""
	}
	return fmt.Sprintf("; export PLAYBOOK_ROOT='%s'; export PLAYBOOK_NAME='%s'", root, name)
}
