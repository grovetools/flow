# Managing Plans

Grove Flow uses "Plans" to organize and execute multi-step workflows. A plan is a directory that contains job files, configuration, and related artifacts. This document covers the commands used to create, manage, inspect, and clean up plans.

## Plan Initialization

The `flow plan init` command creates a plan directory and a default configuration file, `.grove-plan.yml`.

### Basic Initialization

To create a new plan, provide a directory name. The base name of the directory is used as the plan's identifier.

```bash
flow plan init new-feature-development
```

This command creates a directory at `./plans/new-feature-development` (if `plans_directory` is configured), creates a `.grove-plan.yml` file with default settings, and sets `new-feature-development` as the active plan.

### Using Worktrees

For jobs that modify code, the `--worktree` flag is used to create an isolated Git worktree.

**Auto-named Worktree:**
Using `--worktree` without a value creates a worktree and a branch with the same name as the plan.

```bash
# Creates a worktree and branch named "api-refactor"
flow plan init api-refactor --worktree
```

**Custom-named Worktree:**
To specify a different branch name for the worktree, provide a value to the flag.

```bash
# Creates a worktree and branch named "feature/user-auth"
flow plan init user-authentication --worktree=feature/user-auth
```

### Using Recipes

Recipes are plan templates. The `--recipe` flag initializes a plan from a built-in or custom recipe, populating it with pre-defined jobs.

```bash
# Initializes a plan using the 'standard-feature' recipe
flow plan init implement-caching --recipe standard-feature
```

### Extracting from Files

A plan can be initialized from the content of an existing file using `--extract-all-from`. This creates an initial job containing the body of the source file.

```bash
# Creates a plan with one job containing the content of notes.md
flow plan init plan-from-notes --extract-all-from ./chats/notes.md
```

### Combined Initialization

The `init` flags can be combined. For example, a plan can be created from a recipe, have content extracted into its first job, and be configured with a worktree in a single command.

```bash
flow plan init user-profile-api \
  --recipe standard-feature \
  --extract-all-from ./chats/api-discussion.md \
  --worktree
```

## Listing and Browsing Plans

Plans can be listed from the command line or browsed in a terminal interface.

-   **`flow plan list`**: Displays a table of all plans in the configured `plans_directory`, showing the number of jobs and a summary of their statuses.
-   **`flow plan tui`**: Launches a terminal user interface for navigating plans. This interface allows viewing plan details, opening their status view, and managing their lifecycle.

## Active Plan Management

Grove Flow uses the concept of an "active plan" to determine the target for commands like `flow plan add` or `flow plan status` when a directory is not specified. The active plan is stored in a local `.grove/state.yml` file.

-   **`flow plan set <plan-directory>`**: Sets the specified plan as active.
    ```bash
    flow plan set user-profile-api
    ```
-   **`flow plan current`**: Displays the name of the currently active plan.
-   **`flow plan unset`**: Clears the active plan setting.

## Status and Visualization

The state of a plan and the relationships between its jobs can be inspected through status and graph commands.

### Checking Status

The `flow plan status` command provides a view of a plan's jobs.

-   **Default Tree View**: The default output shows a dependency tree with icons indicating the status of each job (`✓` Completed, `⚡` Running, `⏳` Pending, `✗` Failed).

-   **Interactive TUI Mode**: For a more detailed view, use the `-t` flag.
    ```bash
    flow plan status -t
    ```
    This launches a terminal UI where you can navigate the job graph, view job summaries, and execute actions like running, editing, or completing jobs.

### Visualizing the Dependency Graph

The `flow plan graph` command generates a visual representation of the workflow's dependency chain.

```bash
# Generate a Mermaid-syntax graph
flow plan graph my-complex-plan

# Generate an ASCII graph
flow plan graph my-complex-plan --format ascii
```

Supported formats include `mermaid`, `dot`, and `ascii`.

## Interaction with Development Environment

Grove Flow provides commands for interacting with a plan's development environment, primarily through `tmux`.

-   **`flow plan open [directory]`**: This command finds a plan's configured worktree, creates or switches to a `tmux` session for it, and opens the interactive status TUI (`flow plan status -t`).

-   **`flow plan launch <job-file>`**: This command is used to start an `interactive_agent` job. It launches the agent in a dedicated `tmux` session for tasks that may require user interaction.

## Plan Cleanup and Completion

The `flow plan finish` command provides a guided workflow to clean up resources associated with a completed plan.

```bash
flow plan finish user-profile-api
```

This command launches a terminal interface with a checklist of cleanup actions:
-   Marking the plan as `finished` in its `.grove-plan.yml`.
-   Pruning the Git worktree directory.
-   Deleting the local and remote Git branches associated with the worktree.
-   Closing any associated `tmux` sessions.
-   Archiving the plan directory.