# Managing Plans

Grove Flow uses "Plans" to organize and execute multi-step workflows. A plan is a directory that contains job files, configuration, and related artifacts. This document covers the commands used to create, manage, inspect, and clean up plans.

## Plan Initialization

The `flow plan init` command creates a plan directory and a default configuration file, `.grove-plan.yml`. The base name of the directory provided becomes the plan's identifier.

### Basic Initialization

To create a new plan in the configured plans directory:

```bash
flow plan init new-feature-development
```

This creates the directory, adds a default `.grove-plan.yml` configuration file, and sets `new-feature-development` as the active plan.

### Using Worktrees

For jobs that modify code, the `--worktree` flag creates an isolated Git worktree located in the repository's `.grove-worktrees/` directory.

*   **Auto-named Worktree:** Using `--worktree` without a value creates a worktree and a branch with the same name as the plan.
    ```bash
    # Creates a worktree and branch named "api-refactor"
    flow plan init api-refactor --worktree
    ```
*   **Custom-named Worktree:** To specify a different branch name, provide a value to the flag.
    ```bash
    # Creates a worktree and branch named "feature/user-auth"
    flow plan init user-authentication --worktree=feature/user-auth
    ```

### Using Recipes

Recipes are plan templates. The `--recipe` flag initializes a plan with a set of pre-defined job files from a built-in or custom recipe.

```bash
# Initializes a plan using the 'standard-feature' recipe
flow plan init implement-caching --recipe standard-feature
```

### Extracting from Files

A plan can be initialized from an existing file using `--extract-all-from`. This creates an initial job containing the body of the source file, which is useful for converting chat notes into an executable plan.

```bash
# Creates a plan with one job containing the content of notes.md
flow plan init plan-from-notes --extract-all-from ./chats/notes.md
```

### Combined Initialization

Initialization flags can be combined to create more complex starting points for a plan.

```bash
flow plan init user-profile-api \
  --recipe standard-feature \
  --extract-all-from ./chats/api-discussion.md \
  --worktree
```

### Interactive TUI

Running `flow plan init` in a terminal without a directory argument, or with the `-t` flag, launches a terminal user interface (TUI) to guide through the creation of a new plan. The TUI provides options to name the plan, select a recipe, and configure worktree settings.

## Listing and Browsing Plans

-   **`flow plan list`**: Displays a table of all plans found in the configured `plans_directory`. The output includes the number of jobs in each plan and a summary of their statuses.
-   **`flow plan tui`**: Launches a terminal user interface for navigating plans. This interface allows for viewing plan details, opening a plan's specific status view, and managing the plan lifecycle (e.g., setting active, reviewing, finishing).

## Active Plan Management

An "active plan" is a reference stored in a local `.grove/state.yml` file. It allows commands like `flow plan status` or `flow plan add` to operate on a plan without requiring the directory path to be specified in each command.

-   **`flow plan set <plan-directory>`**: Sets the specified plan as active.
    ```bash
    flow plan set user-profile-api
    ```
-   **`flow plan current`**: Displays the name of the currently active plan.
-   **`flow plan unset`**: Clears the active plan setting.

## Status and Visualization

The state of a plan and the relationships between its jobs can be inspected through status and graph commands.

### Checking Status

The `flow plan status` command provides a view of a plan's jobs and their current states.

-   **Default Tree View**: The default output shows a dependency tree with icons indicating the status of each job (e.g., `✓` Completed, `⚡` Running, `⏳` Pending, `✗` Failed).

-   **Interactive TUI Mode**: The `-t` flag launches a terminal interface for the specified plan.
    ```bash
    flow plan status -t
    ```
    This UI allows for navigating the job graph, viewing job summaries, and executing actions like running, editing, or completing jobs directly from the interface.

### Visualizing the Dependency Graph

The `flow plan graph` command generates a text-based representation of the job dependency chain. This is useful for understanding the workflow structure.

```bash
# Generate a Mermaid-syntax graph
flow plan graph my-complex-plan

# Generate an ASCII graph
flow plan graph my-complex-plan --format ascii
```

Supported formats include `mermaid`, `dot`, and `ascii`.

## Interaction with Development Environment

Grove Flow uses `tmux` to manage development environments for plans that are associated with a Git worktree.

-   **`flow plan open [directory]`**: This command is the primary entrypoint into a plan's development environment. It finds the plan's configured worktree, creates or attaches to a `tmux` session for that worktree, and opens the plan's interactive status TUI (`flow plan status -t`).

-   **`flow plan run <job-file>`**: Running a job of type `interactive_agent` will launch the agent in a dedicated `tmux` window within the plan's session. This provides an isolated environment for tasks that may require user interaction.

## Plan Cleanup and Completion

The `flow plan finish` command initiates a guided workflow to clean up resources associated with a completed plan.

```bash
flow plan finish user-profile-api
```

This command launches a terminal interface with a checklist of cleanup actions, which may include:
-   Marking the plan as `finished` in its `.grove-plan.yml` file.
-   Pruning the Git worktree directory from `.grove-worktrees/`.
-   Deleting the local and remote Git branches associated with the worktree.
-   Closing any associated `tmux` sessions.
-   Moving the plan directory to an `.archive/` subdirectory to remove it from the default list view.