---
id: git-status
title: "Check git status for {{ .PlanName }}"
status: pending
type: shell
depends_on:
  - 02-implement.md
worktree: {{ .PlanName }}
---

git status --porcelain