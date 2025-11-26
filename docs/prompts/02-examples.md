# Examples Documentation for Grove Flow

Generate comprehensive examples showing the modern TUI-first, notebook-integrated workflow.

## Primary Example: Real-World Workflow from Note to Finished Plan

Create one comprehensive example covering the complete lifecycle:

### 1. Start with an Idea in grove-notebook
- Show `nb tui` interface
- Display note organization by workspace and status

### 2. Promote the Note to a Plan
- Show pressing `P` to promote
- Worktree creation dialog
- Bidirectional linking between note and plan

### 3. Set Up the Development Environment
- Using `flow plan open` to enter plan workspace
- Creating tmux session
- Defining context in `.grove/rules`

### 4. Iterate with Chat Job
- Initial chat job structure
- Running with `flow chat run` or `r` in TUI
- LLM response with unique block IDs

### 5. Structure Work by Extracting Jobs
- Using `x` to create XML plan job
- Using `i` to create interactive agent job
- Dependency tree visualization

### 6. Execute and Monitor
- Running jobs with `r`
- Monitoring in flow plan status TUI
- Viewing in hooks session browser
- Plan TUI showing git and lifecycle status

### 7. Review and Finalize
- Using `flow plan review`
- Plan TUI showing review state
- Available actions (diffs, merge, PR creation)

### 8. Clean Up
- Using `flow plan finish`
- Cleanup wizard steps
- Note updates in grove-notebook

## CLI-Based Workflow Section

Include a condensed section showing CLI equivalents:
- Direct plan initialization
- Adding jobs with dependencies via flags
- Running and monitoring via command line
- Note: Useful for scripting and automation, but TUI recommended for day-to-day work
