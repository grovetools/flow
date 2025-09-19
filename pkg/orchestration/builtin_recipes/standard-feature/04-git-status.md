---
id: git-status
title: "Check git status for {{ .PlanName }}"
status: pending
type: shell
depends_on:
  - 02-implement.md
---

echo "=== Uncommitted changes ==="
git status --porcelain
echo ""
echo "=== All changes since main ==="
git diff --name-status main...HEAD
