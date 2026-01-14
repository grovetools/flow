---
id: {{ .PlanName }}-impl-tests-{{ .Vars.uuid }}
title: "Implement e2e Tests"
status: pending
type: interactive_agent
template: tend-tester
git_changes: true
depends_on:
  - "05-spec-tests.md"
prepend_dependencies: true
---

Based on the provided test specification, implement the end-to-end tests for the new feature.

Invoke the `tend-tester` skill, and if your tests involve TUIs, use the `tui-explorer` skill.
