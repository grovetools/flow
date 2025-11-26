# Examples

This document demonstrates a real-world `grove-flow` workflow, showing how ideas evolve from notes in `grove-notebook` into structured, executable plans using the terminal user interface (TUI).

## A Real-World Workflow: From Note to Finished Plan

This example covers the complete lifecycle of a plan, from capturing an idea to cleaning up after completion. It illustrates the modern, TUI-centric workflow that integrates `grove-notebook`, `grove-flow`, and `grove-hooks`.

### 1. Start with an Idea in `grove-notebook`

The workflow begins in the `grove-notebook` TUI, where you manage ideas, issues, and tasks.

```bash
# Open the notebook TUI
nb tui
```

The notebook interface organizes notes by workspace and status:

```
nb > grove-flow

  ▶ global
  ▼ grove-flow
    ├─ ▼ inbox (6)
    │  ├─ failing-tests
    │  ├─ review-workflow
    │  ├─ ecosystem-refactoring
    │  ├─ flow-plan-status-features
    │  ├─ consolidate-tui
    │  └─ notes-for-docs
    ├─ ▼ issues (6)
    │  ├─ interactive-agent-promptfile
    │  ├─ worktree-siblings-v2
    │  ├─ launching-multi-jobs
    │  ├─ improve-jobs-cli
    │  ├─ no-plan-dir-exists
    │  └─ flow-test-config-migrations
    └─ ▼ in_progress (1)
```

### 2. Promote the Note to a Plan

When you're ready to work on an issue, navigate to it in the TUI and press `P` to promote it to a plan.

```
╭────────────────────────────────────────╮
│                                        │
│  Create a git worktree for this plan?  │
│                                        │
╰────────────────────────────────────────╯

                (y/n)
```

Choose whether to create a dedicated git worktree for filesystem isolation. A worktree creates a separate working directory and branch, allowing you to work on the plan without affecting your main branch.

After promotion, the notebook TUI shows the bidirectional link between the note and the plan:

```
nb > grove-flow

  ▼ grove-flow
    ├─ ▼ in_progress (1)
    │  └─ ghost-jobs [plan: → ghost-jobs]
    ├─ ▼ plans (1)
    │  └─ ▶ ghost-jobs [note: ← ghost-jobs] (1)
    └─ ▼ completed (1)
```

The promotion automatically:
- Creates a plan directory in the configured `plans_directory`
- Adds a `.grove-plan.yml` configuration file
- Creates an initial `01-chat.md` job file
- Adds a `note_ref` field to link back to the source note
- Optionally creates a git worktree in `.grove-worktrees/`

### 3. Set Up the Development Environment

Open the plan in a dedicated development environment:

```bash
flow plan open ghost-jobs
```

This command:
- Creates or attaches to a `tmux` session named after the plan
- Changes to the worktree directory (if one exists)
- Launches the plan's interactive status TUI

The plan status TUI displays:

```
Plan Status: ghost-jobs

  ╭─────┬────────────┬──────┬──────────────╮
  │ SEL │ JOB        │ TYPE │ STATUS       │
  ├─────┼────────────┼──────┼──────────────┤
  │ [ ] │ 01-chat.md │ chat │ pending_user │
  ╰─────┴────────────┴──────┴──────────────╯

  Press ? for help • q/ctrl+c • quit [table]
```

Before running the job, define what context should be included. Open `.grove/rules` in the worktree:

```
**/*.go
Makefile
go.mod
grove.yml

!tests/**
```

This file tells `grove-context` which files to include when providing codebase context to the LLM.

### 4. Iterate with the LLM using a Chat Job

The initial chat job file looks like this:

```markdown
---
id: chat-about-ghost-jobs-0151a038
note_ref: /Users/solom4/notebooks/nb/workspaces/grove-hooks/in_progress/20251115-ghost-jobs.md
repository: grove-hooks
status: pending_user
template: chat
title: Chat about ghost-jobs
type: chat
worktree: ghost-jobs
---

# ghost-jobs

[Describe the problem or feature here...]
```

Edit the file to describe your task, then submit it to the LLM by pressing `r` (run) in the status TUI or using the CLI:

```bash
flow chat run ghost-jobs
```

The job is executed:

```
Found 1 runnable chat(s). Executing sequentially...

--- Running Chat: Chat about ghost-jobs ---
Executing job [id=chat-about-ghost-jobs-0151a038 type=chat]
Job status updated [job=chat-about-ghost-jobs-0151a038 from=pending_user to=running]
Checking context in worktree for chat job...
Generated .grove/context with 25 files

────────────────────────────────────────────────────────────
Context Summary
Total Files: 25
Total Tokens: 57.1k
Total Size: 223.0 KB

Language Distribution:
.go:      96.9% (55.3k tokens, 22 files)
Makefile:  1.7% (945 tokens, 1 files)
.mod:      1.4% (790 tokens, 1 files)
.yml:      0.1% (46 tokens, 1 files)

Context available for chat job.
────────────────────────────────────────────────────────────

Calling Gemini API with model: gemini-2.5-pro
```

The LLM's response is appended to the chat file with a unique block ID:

```markdown
---
<!-- grove: {"id": "f2eb25"} -->
## LLM Response (2025-11-26 12:23:48)

Excellent, I understand the problem of "ghost jobs" appearing in the session browser...

Here is the implementation plan:

### Project-Level Context
...

### Phase 1: Implement Orphaned Session Reaper
...
```

You can continue the conversation by adding more content to the file and running `flow chat run` again. Each LLM response receives a unique ID for later extraction.

### 5. Structure the Work by Extracting Jobs

Once the LLM provides a detailed plan, convert it into structured jobs using the TUI:

**Create an XML Plan Job:**

In the status TUI, select the chat job and press `x` to create an XML plan extraction job. This uses the LLM's response to generate a detailed, structured plan.

```
╭─────────────────────────────────────────────────────────╮
│                                                         │
│  Create XML Plan Job                                    │
│                                                         │
│  Enter job title:                                       │
│  > xml-plan-Chat about ghost-jobs                       │
│                                                         │
│  Press Enter to create, Esc to cancel                   │
╰─────────────────────────────────────────────────────────╯
```

**Create an Implementation Job:**

Next, press `i` to create an interactive agent job that will execute the plan. The status TUI now shows:

```
Plan Status: ghost-jobs

  ╭─────┬───────────────────────────────────────────┬──────────────────┬───────────╮
  │ SEL │ JOB                                       │ TYPE             │ STATUS    │
  ├─────┼───────────────────────────────────────────┼──────────────────┼───────────┤
  │ [x] │ 01-chat.md                                │ chat             │ completed │
  │ [ ] │ └─ 02-xml-plan-chat-about-ghost-jobs.md   │ oneshot          │ pending   │
  │ [ ] │    └─ 03-impl-chat-about-ghost-jobs.md    │ interactive_agent│ pending   │
  ╰─────┴───────────────────────────────────────────┴──────────────────┴───────────╯
```

The dependency tree shows that job 03 depends on job 02, which depends on job 01.

### 6. Execute and Monitor the Plan

Select the jobs and press `r` to run them. The XML plan job runs first (as a `oneshot`), followed by the implementation job.

Monitor execution across different interfaces:

**In the flow plan status TUI:**

```
Plan Status: ghost-jobs

  ╭─────┬─────────────────────────────────────────┬──────────────────┬───────────╮
  │ SEL │ JOB                                     │ TYPE             │ STATUS    │
  ├─────┼─────────────────────────────────────────┼──────────────────┼───────────┤
  │ [x] │ 01-chat.md                              │ chat             │ completed │
  │ [x] │ └─ 02-xml-plan-chat-about-ghost-jobs.md │ oneshot          │ completed │
  │ [ ] │    └─ 03-impl-chat-about-ghost-jobs.md  │ interactive_agent│ running   │
  ╰─────┴─────────────────────────────────────────┴──────────────────┴───────────╯
```

**In the grove-hooks session browser (`hooks b`):**

```
╭──────────────────────────────────────────────────────┬──────────────────┬──────────────────────┬─────╮
│ WORKSPACE / JOB                                      │ TYPE             │ STATUS               │ AGE │
├──────────────────────────────────────────────────────┼──────────────────┼──────────────────────┼─────┤
│ grove-ecosystem                                      │                  │                      │     │
│ ├─(1) grove-flow                                     │                  │                      │     │
│ │   └─(2) test-modernization                         │                  │                      │     │
│ │        └─ Plan: test-modernization (1 jobs)        │                  │                      │     │
│ │             └─ 01-chat.md                          │ [chat]           │ pending_user         │     │
│ ├─(3) grove-hooks                                    │                  │                      │     │
│ │   └─(4) ghost-jobs                                 │                  │                      │     │
│ │        └─ Plan: ghost-jobs (1 jobs)                │                  │                      │     │
│ │             └─ 03-impl-chat-about-ghost-jobs.md    │ [interactive_agent] │ running (claude_code) │ 9s  │
╰──────────────────────────────────────────────────────┴──────────────────┴──────────────────────┴─────╯
```

The `interactive_agent` job runs in a dedicated `tmux` window where you can interact with the coding agent.

**In the flow plan TUI (`flow plan tui`):**

```
╭────────────┬───────────────────────────┬────────────┬───────┬────────┬─────────────┬───────┬───────────────╮
│ PLAN       │ STATUS                    │ WORKTREE   │ GIT   │ MERGE  │ REVIEW      │ NOTES │ UPDATED       │
├────────────┼───────────────────────────┼────────────┼───────┼────────┼─────────────┼───────┼───────────────┤
│ ghost-jobs │ 2 completed, 1 running    │ ghost-jobs │ Clean │ Synced │ Not Started │ -     │ 1 minute ago  │
╰────────────┴───────────────────────────┴────────────┴───────┴────────┴─────────────┴───────┴───────────────╯

Git Repository Log

╭────────────────────────────────────────────────────────────╮
│ * 01a103b (HEAD -> ghost-jobs, origin/main, main) chore:  │
│   simplify test-e2e to use tend auto-build                │
│ * 544522c feat: integrate centralized icon system          │
│ * ec44643 feat: add recent job highlighting                │
╰────────────────────────────────────────────────────────────╯
```

This view shows the plan's git status, merge status with main, and overall progress.

### 7. Review and Finalize

When all jobs are complete, review the work:

```bash
flow plan review ghost-jobs
```

This marks the plan as ready for review and can trigger configured hooks (e.g., creating a pull request, running tests).

The plan TUI now shows:

```
╭────────────┬──────────────┬────────────┬───────┬────────┬────────┬───────┬──────────────╮
│ PLAN       │ STATUS       │ WORKTREE   │ GIT   │ MERGE  │ REVIEW │ NOTES │ UPDATED      │
├────────────┼──────────────┼────────────┼───────┼────────┼────────┼───────┼──────────────┤
│ ghost-jobs │ 3 completed  │ ghost-jobs │ Clean │ Synced │ Ready  │ -     │ 1 minute ago │
╰────────────┴──────────────┴────────────┴───────┴────────┴────────┴───────┴──────────────╯
```

You can use the TUI to:
- Review diffs between the worktree and main branch
- Rebase or merge changes
- Create pull requests
- Mark the plan as finished

### 8. Clean Up

Once the work is merged, finish the plan:

```bash
flow plan finish ghost-jobs
```

This launches a cleanup wizard that guides you through:
- Marking the plan as `finished` in `.grove-plan.yml`
- Pruning the git worktree from `.grove-worktrees/`
- Deleting the local and remote branches
- Closing the `tmux` session
- Archiving the plan directory to `.archive/`

The note in `grove-notebook` is automatically updated to reflect completion.

## CLI-Based Workflow

While the TUI-first workflow is recommended for interactive work, all operations can be performed via the CLI:

### Initialize a Plan

```bash
flow plan init new-feature --worktree
```

### Add Jobs with Dependencies

```bash
flow plan add --title "Write Specification" --type oneshot \
  -p "Create a technical spec for the new feature"

flow plan add --title "Implement Feature" --type agent \
  -d "01-write-specification.md" \
  -p "Implement the feature based on the specification"

flow plan add --title "Write Tests" --type agent \
  -d "02-implement-feature.md" \
  -p "Write comprehensive tests for the feature"
```

### Run Jobs

```bash
# Run the next available job
flow plan run

# Run all jobs in dependency order
flow plan run --all

# Run a specific job
flow plan run new-feature/02-implement-feature.md
```

### Monitor Status

```bash
# View dependency tree
flow plan status

# View in interactive TUI
flow plan status -t

# View as JSON
flow plan status --format json
```

This CLI workflow is useful for automation, scripting, and CI/CD pipelines, but the TUI provides a more intuitive interface for day-to-day development.
