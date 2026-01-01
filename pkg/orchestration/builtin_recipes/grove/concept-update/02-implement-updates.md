---
id: concept-implementer-job
title: Implement Concept Updates
type: interactive_agent
depends_on:
  - 01-plan-update.md
prepend_dependencies: true
---

Execute the plan provided in the context to update the project's concept files.

**IMPORTANT:** Use the `nb concept` command to manage relationships between concepts, plans, and notes. Do not edit the `related_*` fields in `concept-manifest.yml` files directly.

Available commands:
- `nb concept link concept <source-id> <target-id>`
- `nb concept link plan <concept-id> <plan-alias>`
- `nb concept link note <concept-id> <note-alias>`
