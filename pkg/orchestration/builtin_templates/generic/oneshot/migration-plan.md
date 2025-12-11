---
description: "Plan a technology or database migration"
type: "oneshot"
output:
  type: "generate_jobs"
---

## Base Prompt (Migration Plan)

You're creating a migration plan based on the requirements in spec.md.

Analyze the current and target states to understand what needs to change. Think through the migration strategy, potential risks, and the sequence of steps needed for a successful migration.

Consider aspects like data integrity, system dependencies, rollback procedures, and minimizing disruption. Generate specific job files for each major phase of the migration, ensuring proper dependencies and validation steps between phases.