---
id: git-status
title: "Check git status for {{ .PlanName }}"
status: pending
type: shell
depends_on:
  - 03-implement.md
---

echo "=== Committed changes (diff from main) ==="
git diff main...HEAD

printf "\n=== Uncommitted changes (working directory) ===\n"
git diff

printf "\n=== Staged changes (index) ===\n"
git diff --cached

printf "\n=== New/untracked files (full content) ===\n"
for file in $(git ls-files --others --exclude-standard); do
  echo "--- $file ---"
  cat "$file"
done
