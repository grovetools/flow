---
id: git-changes
title: "Show git changes for {{ .PlanName }}"
status: pending
type: shell
depends_on:
  - 02-implement.md
worktree: {{ .PlanName }}
---

git diff --name-status