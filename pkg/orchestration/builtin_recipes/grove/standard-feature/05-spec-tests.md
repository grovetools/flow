---
id: spec-tests-{{ .PlanName }}
title: Specification for Tests
type: oneshot
{{- if .Vars.model }}
model: '{{ .Vars.model }}'
{{- else }}
model: 'gemini-2.5-pro'
{{- end }}
depends_on:
  - 02-spec.md
  - 03-generate-plan.md
  - 04-implement.md
{{- if .Vars.repository }}
repository: '{{ .Vars.repository }}'
{{- end }}
branch: '{{ .PlanName }}'
worktree: '{{ .PlanName }}'
prepend_dependencies: true
---

Based on the feature specification and the implementation plan, write a detailed test plan.

Describe the end-to-end tests needed to validate the feature. Use the `tend` testing framework syntax where applicable.

Focus on the 'what' to test, not the 'how' to implement the test. The implementation test agent will use this specification as its primary guide.

The test spec should be comprehensive and cover all acceptance criteria mentioned in the original feature specification.
