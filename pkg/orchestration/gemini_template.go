package orchestration

import "strings"

// GeminiPlanTemplate is optimized for Gemini models to reliably generate job definitions
const GeminiPlanTemplate = `---
id: {{ .ID }}
title: "Create High-Level Implementation Plan"
status: pending
type: oneshot
prompt_source:
  - spec.md
output:
  type: generate_jobs
---

You are generating job definitions for Grove orchestration. Each job must be a complete markdown file.

INSTRUCTIONS:
1. Output ONLY job definitions, no other text
2. Start immediately with first job (no introduction)
3. Separate each job with exactly: ===
4. Follow the format precisely

REQUIRED FORMAT for each job:
---
id: [lowercase-with-hyphens]
title: "[Title in Quotes]"
status: pending
type: [agent|shell|oneshot]
depends_on:
  - [previous-job-id]
worktree: [branch-name]  # only for agent type - defaults to plan directory name if not specified
---
[Job instructions here]

RULES:
- First job: depends_on: []
- After each agent job: add a shell job with body "grove cx update && grove cx generate"
- agent = writes code, shell = runs commands, oneshot = review/plan

EXAMPLE OUTPUT:
---
id: project-setup
title: "Setup Project"
status: pending
type: agent
depends_on: []
worktree: feature-todo
---
Create package.json and initial structure.

===
---
id: update-context-1
title: "Update Context"
status: pending
type: shell
depends_on:
  - project-setup
---
grove cx update && grove cx generate

Now generate jobs for the specification. Output starts on next line:
`

// UpdateTemplateForGemini checks if using Gemini and returns appropriate template
func GetPlanTemplate(model string) string {
	if strings.Contains(strings.ToLower(model), "gemini") {
		return GeminiPlanTemplate
	}
	return InitialPlanTemplate
}