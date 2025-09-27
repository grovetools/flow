# Managing Plans

Grove Flow uses "Plans" to organize and execute multi-step workflows. A plan is a directory containing a series of job files, configuration, and any related artifacts. This document covers the commands used to create, manage, inspect, and clean up these plans.

## Plan Initialization

The `flow plan init` command is the starting point for any new workflow. It creates a plan directory and its default configuration file, `.grove-plan.yml`.

### Basic Initialization

To create a new plan, provide a directory name. This name will also be used as the plan's identifier.

```bash
flow plan init new-feature-development
```

This command performs the following actions:
1.  Creates a directory at `./plans/new-feature-development` (assuming `plans_directory` is set to `./plans`).
2.  Creates a `.grove-plan.yml` file inside with default settings.
3.  Sets `new-feature-development` as the active plan for the current context.

### Using Worktrees

For jobs that modify code, it is best practice to use isolated Git worktrees. The `--worktree` flag facilitates this.

**Auto-named Worktree:**
Using `--worktree` without a value instructs Grove Flow to create a worktree and a branch with the same name as the plan.

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

Recipes are pre-defined plan templates for common workflows. The `--recipe` flag initializes a plan from a built-in or custom recipe.

```bash
# Initializes a plan using the built-in 'standard-feature' recipe
flow plan init implement-caching --recipe standard-feature
```
This creates a plan with pre-defined jobs for specification, implementation, and review.

### Extracting from Chats

You can initialize a plan directly from the content of a chat or any markdown file using `--extract-all-from`. This creates an initial job containing the body of the source file.

```bash
# Creates a plan with a single job containing the content of notes.md
flow plan init plan-from-notes --extract-all-from ./chats/notes.md
```

### Combined Initialization

The `init` flags can be combined for more advanced setups. For example, you can initialize a plan from a recipe, extract content from a chat into its first job, and configure a worktree simultaneously.

```bash
flow plan init user-profile-api \
  --recipe standard-feature \
  --extract-all-from ./chats/api-discussion.md \
  --worktree
```

## Listing and Browsing Plans

Grove Flow provides both command-line and interactive TUI options for listing and browsing plans.

-   **`flow plan list`**: Displays a table of all plans in the configured `plans_directory`, showing the number of jobs and a summary of their statuses.
-   **`flow plan tui`**: Launches a full-screen interactive TUI for navigating plans. This interface allows you to view plan details, open their status view, and manage their lifecycle.

## Active Plan Management

Grove Flow uses the concept of an "active plan" to simplify commands. When a plan is active, you do not need to specify its directory in subsequent commands like `flow plan add` or `flow plan status`. The active plan is stored locally in a `.grove/state.yml` file and is specific to your current directory or worktree.

-   **`flow plan set <plan-directory>`**: Sets the specified plan as active.
    ```bash
    flow plan set user-profile-api
    ```
-   **`flow plan current`**: Displays the name of the currently active plan.
-   **`flow plan unset`**: Clears the active plan setting.

## Status and Visualization

Understanding the state of a plan and the relationships between its jobs is essential for managing complex workflows.

### Checking Status

The `flow plan status` command provides a detailed view of a plan's jobs.

-   **Default Tree View**: The default output shows a dependency tree with color-coded icons indicating the status of each job (e.g., `✓` Completed, `⚡` Running, `⏳` Pending, `✗` Failed).

-   **Interactive TUI Mode**: For a more detailed and interactive view, use the `-t` flag.
    ```bash
    flow plan status -t
    ```
    This launches a full-screen terminal UI where you can navigate the job graph, view job summaries, and execute actions like running, editing, or completing jobs.

### Visualizing the Dependency Graph

For complex plans, a visual graph can clarify job dependencies. The `flow plan graph` command generates a visual representation of the workflow.

```bash
# Generate a Mermaid-syntax graph
flow plan graph my-complex-plan

# Generate an ASCII graph
flow plan graph my-complex-plan --format ascii
```

Supported formats include `mermaid`, `dot`, and `ascii`.

## Interaction with Development Environment

Grove Flow integrates with your development environment, particularly through `tmux`, to streamline workflows that involve code modification.

-   **`flow plan open [directory]`**: This command provides a quick entry point into a plan's development environment. It finds the plan's configured worktree, creates a `tmux` session for it (if one doesn't exist), and opens the interactive status TUI (`flow plan status -t`).

-   **`flow plan launch <job-file>`**: This command is used to start an `interactive_agent` job. It launches the agent in a dedicated `tmux` session, allowing for long-running, human-in-the-loop coding tasks. You can detach from the session and the agent will continue to run.

## Plan Cleanup and Completion

Once a plan's objectives are met, the `flow plan finish` command provides a guided workflow to clean up associated resources.

```bash
flow plan finish user-profile-api
```

Running this command launches an interactive TUI that presents a checklist of cleanup actions, including:
-   Marking the plan as finished in its `.grove-plan.yml`.
-   Pruning the Git worktree directory.
-   Deleting the local and remote Git branches associated with the worktree.
-   Closing any associated `tmux` sessions.
-   Archiving the plan directory.

This ensures that completed work is properly finalized and development artifacts are cleanly removed.