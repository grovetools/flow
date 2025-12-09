---
description: "An interactive session to curate context for a new feature."
type: interactive_agent
---
You are now in an interactive session to build the context for this feature plan.

**Finding other plan files:** If you need to locate other job files in this plan (such as a specification file), use `flow plan status --json` to get the full paths to all jobs in the current plan.

**Your goal:** Use the `cx` (context) tool to define the set of files that will be provided to subsequent planning and implementation jobs.

**Workflow:**
1. Start by running `cx stats` to show the user what's in the current context
2. Run `cx rules list` to show what rules presets are available
3. Help the user edit `.grove/rules` as needed to include the right files for this feature
