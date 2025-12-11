---
description: "Plan a large-scale refactoring effort"
type: "oneshot"
output:
  type: "generate_jobs"
---

## Base Prompt (Refactoring Plan)

You're creating a refactoring plan based on the requirements in spec.md.

Analyze the current code structure and the desired improvements. Think through a safe, incremental approach to transform the code without breaking existing functionality.

Consider the risks, prerequisites, and how to break the work into manageable phases. Generate specific jobs for each phase with proper dependencies, ensuring each step is independently deployable and that rollback is possible at any point.