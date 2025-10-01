# Managing Plans Documentation

Generate comprehensive documentation for all aspects of managing plans in Grove Flow.

## Content to Cover:

### Plan Initialization
- Explain `flow plan init` with all its variations:
  - Basic initialization
  - Using `--worktree` (with and without custom branch names)
    - Show that it's created in `.grove-worktrees/branch-name`
  - Using `--recipe` with built-in recipes
  - Using `--extract-all-from` to convert chats to plans
  - Combining options for complex workflows
  - Show an example with the TUI (animated GIF)

### Listing and Browsing Plans
- Detail `flow plan list` command and its output
- Explain the interactive `flow plan tui` for visual browsing
- How to navigate and interact with the TUI

### Active Plan Management
- Explain the concept of an "active plan" and its benefits
- Cover `flow plan set` to set the active plan
- Cover `flow plan current` to see the current active plan
- Cover `flow plan unset` to clear the active plan
- How active plans affect other commands

### Status and Visualization
- Detailed coverage of `flow plan status` command:
  - Regular output vs TUI mode with `-t`
  - Understanding job states and progress
  - Interpreting the status display
- Explain `flow plan graph` for visualizing dependencies
- How to read and interpret dependency graphs

### Interaction with Development Environment
- Cover `flow plan open` for opening plan directories
- Explain `flow plan launch` for Tmux integration:
  - How it creates Tmux sessions
  - Working with worktrees in Tmux
  - Benefits for development workflow

### Plan Cleanup and Completion
- Detail the `flow plan finish` workflow:
  - What happens during cleanup
  - Worktree removal
  - Archiving completed plans
  - Best practices for plan lifecycle

Include practical examples for each command and explain common use cases and workflows.
