package orchestration

// InitialPlanTemplate is the template for the initial planning job.
const InitialPlanTemplate = `---
id: {{ .ID }}
title: "Create High-Level Implementation Plan"
status: pending
type: oneshot
prompt_source:
  - spec.md
output:
  type: file
---

You are a senior software architect AI responsible for creating detailed, multi-step implementation plans.
You are operating within the Grove orchestration framework.

Your task is to analyze the feature request in spec.md and generate a comprehensive implementation plan in markdown format.

**GUIDELINES:**
- Break the work down into 3-5 focused steps (jobs).
- Each step should be completable in a single session by an LLM agent.
- For each step, provide a clear title and detailed instructions.
- Use direct, technical language. Be specific about file paths and outcomes.
- Split large features into multiple jobs.
- Focus on the single-responsibility principle for each job.
- Always include a final step for reviewing the implementation.
`

// AgentJobTemplate is the template for agent jobs.
const AgentJobTemplate = `---
id: {{ .ID }}
title: "{{ .Title }}"
status: pending
type: {{ .Type }}
plan_type: {{ .PlanType }}{{ if .DependsOn }}
depends_on:{{ range .DependsOn }}
  - {{ . }}{{ end }}{{ end }}{{ if .PromptSource }}
prompt_source:{{ range .PromptSource }}
  - {{ . }}{{ end }}{{ end }}{{ if .Repository }}
repository: {{ .Repository }}{{ end }}{{ if .Branch }}
branch: {{ .Branch }}{{ end }}{{ if .Worktree }}
worktree: {{ .Worktree }}{{ end }}{{ if .AgentContinue }}
agent_continue: true{{ end }}{{ if .PrependDependencies }}
prepend_dependencies: true{{ end }}
output:
  type: {{ .OutputType }}
---

{{ .Prompt }}
`

// InitialJobContent is the template for the initial job file.
const InitialJobContent = `---
id: %s
title: "Create High-Level Implementation Plan"
status: pending
type: oneshot%s
prompt_source:
  - spec.md
output:
  type: file
---

You are a senior software architect AI responsible for creating detailed, multi-step implementation plans.
You are operating within the Grove orchestration framework.

Your task is to analyze the feature request in spec.md and generate a comprehensive implementation plan in markdown format.

**GUIDELINES:**
- Break the work down into 3-5 focused steps (jobs).
- Each step should be completable in a single session by an LLM agent.
- For each step, provide a clear title and detailed instructions.
- Use direct, technical language. Be specific about file paths and outcomes.
- Split large features into multiple jobs.
- Focus on the single-responsibility principle for each job.
- Always include a final step for reviewing the implementation.
`
