---
id: generate-plan
title: "Generate Implementation Plan for {{ .PlanName }}"
status: pending
type: oneshot
template: agent-xml
depends_on:
  - 01-spec.md
prepend_dependencies: true
output:
  type: file
---

Generate a detailed, step-by-step implementation plan based on the provided specification. The plan should be suitable for an AI agent to execute and should include specific tasks, file changes, and any architectural decisions needed.
