package orchestration

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/grovetools/skills/pkg/skills"
)

// planWorkDir returns the directory to use as the workDir argument when
// resolving a playbook for a plan. Falls back to the empty string when
// the plan is nil, which causes the resolver to skip the project and
// ecosystem tiers and fall back to user-scoped and globally-registered
// dirs — the correct behavior for ad-hoc callers without workspace
// context.
func planWorkDir(plan *Plan) string {
	if plan == nil {
		return ""
	}
	return plan.Directory
}

// renderPlaybookOverview builds the <playbook_overview> XML block that is
// injected at the top of a briefing when the job belongs to a playbook-scoped
// plan. Returns the empty string when no playbook is active or the playbook
// cannot be loaded (the briefing continues without it).
func renderPlaybookOverview(job *Job, plan *Plan) string {
	name, _ := resolvePlaybookRootForJob(job, plan)
	if name == "" {
		return ""
	}
	pb, err := skills.LoadPlaybook(planWorkDir(plan), name)
	if err != nil || pb == nil {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "    <playbook_overview name=%q version=%q path=%q>\n",
		pb.Manifest.Name, pb.Manifest.Version, pb.Path)
	if desc := strings.TrimSpace(pb.Manifest.Description); desc != "" {
		fmt.Fprintf(&b, "        <description>%s</description>\n", escapeXML(desc))
	}

	if len(pb.Skills) > 0 {
		b.WriteString("        <skills>\n")
		for _, s := range pb.Skills {
			fmt.Fprintf(&b, "            <skill name=%q description=%q/>\n",
				s.Name, truncateForXML(s.Description, 240))
		}
		b.WriteString("        </skills>\n")
	}
	if len(pb.Prompts) > 0 {
		b.WriteString("        <prompts>\n")
		for _, p := range pb.Prompts {
			fmt.Fprintf(&b, "            <prompt file=%q purpose=%q/>\n",
				p.File, truncateForXML(p.Purpose, 240))
		}
		b.WriteString("        </prompts>\n")
	}
	if len(pb.Recipes) > 0 {
		b.WriteString("        <recipes>\n")
		for _, r := range pb.Recipes {
			fmt.Fprintf(&b, "            <recipe file=%q description=%q/>\n",
				r.File, truncateForXML(r.Description, 240))
		}
		b.WriteString("        </recipes>\n")
	}
	b.WriteString("        <references_note>Worked examples and invariant checklists are available in $PLAYBOOK_ROOT/references/</references_note>\n")
	b.WriteString("    </playbook_overview>\n")
	return b.String()
}

// escapeXML escapes a raw string for inclusion in XML element content.
func escapeXML(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// truncateForXML collapses whitespace and truncates long strings before
// embedding them in an XML attribute.
func truncateForXML(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}

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
	path, err := skills.ResolvePlaybookPath(planWorkDir(plan), name)
	if err != nil {
		return name, ""
	}
	return name, path
}

// playbookEnvInline returns a shell fragment of `KEY='VALUE' ` assignments
// suitable for prefixing a single command (e.g. `FOO=bar baz`), scoping the
// variables to the agent process without exporting them into the surrounding
// shell. Returns an empty string if no playbook is active.
func playbookEnvInline(job *Job, plan *Plan) string {
	name, root := resolvePlaybookRootForJob(job, plan)
	if root == "" {
		return ""
	}
	return fmt.Sprintf("PLAYBOOK_ROOT='%s' PLAYBOOK_NAME='%s' ", root, name)
}
