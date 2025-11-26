# Managing Plans

Grove Flow uses "Plans" to organize and execute multi-step workflows. A plan is a directory that contains job files, configuration, and related artifacts. Plans are typically created by promoting notes from `grove-notebook`, though they can also be initialized directly via the CLI.

## Plan Initialization

Plans are most commonly created by promoting a note in the `grove-notebook` TUI, but can also be initialized directly using `flow plan init`.

### From grove-notebook (Recommended)

The primary workflow for creating plans starts in `grove-notebook`:

1. Open the notebook TUI: `nb tui`
2. Navigate to a note or issue
3. Press `P` to promote it to a plan
4. Choose whether to create a worktree

This automatically:
- Creates a plan directory in the configured `plans_directory`
- Adds a `.grove-plan.yml` configuration file
- Creates an initial `01-chat.md` job file
- Links the plan to the source note via the `note_ref` field
- Optionally creates a git worktree in `.grove-worktrees/`

The bidirectional link between the note and plan is maintained throughout the plan's lifecycle, allowing easy navigation between the original idea and the implementation.

### Direct Initialization via CLI

To create a new plan directly without using `grove-notebook`:

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

The primary interface for browsing plans is the `flow plan tui` command, which launches a comprehensive terminal interface for plan management.

### Plan TUI (`flow plan tui`)

The plan TUI provides a high-level overview of all plans with their status, git state, and lifecycle stage:

```
╭──────────────┬──────────────────────────┬──────────────┬─────────┬──────────┬─────────────┬───────┬────────────────╮
│ PLAN         │ STATUS                   │ WORKTREE     │ GIT     │ MERGE    │ REVIEW      │ NOTES │ UPDATED        │
├──────────────┼──────────────────────────┼──────────────┼─────────┼──────────┼─────────────┼───────┼────────────────┤
│ ghost-jobs   │ 2 completed, 1 running   │ ghost-jobs   │ Clean   │ Synced   │ Not Started │ -     │ 1 minute ago   │
│ user-auth    │ 3 completed              │ user-auth    │ Clean   │ Synced   │ Ready       │ -     │ 2 hours ago    │
╰──────────────┴──────────────────────────┴──────────────┴─────────┴──────────┴─────────────┴───────┴────────────────╯
```

From this TUI, you can:
- View the status of all jobs in each plan
- See git status and merge state relative to the main branch
- Open a plan's detailed status view
- Review diffs and manage the plan lifecycle
- Navigate to the plan's worktree

### List Command (`flow plan list`)

For a simple table view or scripting, use `flow plan list`:

```bash
flow plan list
```

This displays a table of all plans found in the configured `plans_directory`, including the number of jobs and their statuses.

## Active Plan Management

An "active plan" is a reference stored in a local `.grove/state.yml` file. It allows commands like `flow plan status` or `flow plan add` to operate on a plan without requiring the directory path to be specified in each command.

-   **`flow plan set <plan-directory>`**: Sets the specified plan as active.
    ```bash
    flow plan set user-profile-api
    ```
-   **`flow plan current`**: Displays the name of the currently active plan.
-   **`flow plan unset`**: Clears the active plan setting.

## Status and Visualization

The state of a plan and the relationships between its jobs can be inspected through the status TUI and graph commands.

### Plan Status TUI (Primary Interface)

The interactive status TUI (`flow plan status -t`) is the primary interface for working with a plan. It provides a complete view of all jobs, their dependencies, and available actions:

```bash
flow plan status -t
```

The TUI displays:

```
Plan Status: ghost-jobs

  ╭─────┬───────────────────────────────────────────┬──────────────────┬───────────╮
  │ SEL │ JOB                                       │ TYPE             │ STATUS    │
  ├─────┼───────────────────────────────────────────┼──────────────────┼───────────┤
  │ [x] │ 01-chat.md                                │ chat             │ completed │
  │ [ ] │ └─ 02-xml-plan-chat-about-ghost-jobs.md   │ oneshot          │ pending   │
  │ [ ] │    └─ 03-impl-chat-about-ghost-jobs.md    │ interactive_agent│ pending   │
  ╰─────┴───────────────────────────────────────────┴──────────────────┴───────────╯

  Press ? for help • q/ctrl+c • quit [table]
```

Available TUI actions:
- `r` - Run selected job(s)
- `A` - Add a new job to the plan
- `x` - Extract XML plan from selected chat job
- `i` - Create interactive agent implementation job
- `e` - Edit the selected job file
- `d` - Delete the selected job
- `c` - Mark the selected job as completed
- `↑/↓` - Navigate between jobs
- `space` - Toggle job selection
- `?` - Show help

This TUI is the recommended way to work with plans, as it provides immediate visual feedback and keyboard-driven workflow.

### Command-Line Status

For scripting or quick checks, use the command-line status view:

```bash
# Default tree view
flow plan status

# Verbose output with job details
flow plan status -v

# JSON output for scripting
flow plan status --format json
```

The default output shows a dependency tree with status indicators for each job (Completed, Running, Pending, Failed).

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

### Opening a Plan (`flow plan open`)

The `flow plan open` command is the primary entrypoint into a plan's development environment:

```bash
flow plan open ghost-jobs
```

This command:
- Finds the plan's configured worktree
- Creates or attaches to a `tmux` session named after the plan
- Changes the working directory to the worktree
- Launches the plan's interactive status TUI (`flow plan status -t`)

This provides a dedicated, isolated workspace for the plan, separate from your main development environment.

### Running Interactive Agents

When you run a job of type `interactive_agent`, it launches in a dedicated `tmux` window within the plan's session:

```bash
flow plan run 03-impl-chat-about-ghost-jobs.md
```

This provides an isolated environment for the agent to work, with access to the worktree and the ability to interact with you through the terminal.

You can monitor running agents using the `grove-hooks` session browser:

```bash
hooks b
```

This shows all active agent sessions across your entire ecosystem, making it easy to track multiple concurrent plans.

## Plan Lifecycle

Plans follow a lifecycle from creation through completion. Understanding this lifecycle helps manage plans effectively throughout their development.

### Plan States

- **Active**: The plan is currently being worked on, with jobs in progress or pending
- **Review**: Work is complete and ready for review (`flow plan review`)
- **Finished**: The plan has been completed and archived (`flow plan finish`)
- **Hold**: The plan is temporarily paused (`flow plan hold`)

### Reviewing a Plan

When all jobs are complete, mark the plan as ready for review:

```bash
flow plan review user-profile-api
```

This command:
- Updates the plan's status to `review`
- Triggers configured `on_review` hooks
- Can automatically create pull requests (if configured)
- Updates the linked note in `grove-notebook`

The plan TUI shows review status, merge state with main, and git cleanliness.

### Finishing and Cleanup

The `flow plan finish` command initiates a guided workflow to clean up resources associated with a completed plan:

```bash
flow plan finish user-profile-api
```

This launches a terminal interface with a checklist of cleanup actions:
- Mark the plan as `finished` in `.grove-plan.yml`
- Prune the Git worktree directory from `.grove-worktrees/`
- Delete the local and remote Git branches
- Close any associated `tmux` sessions
- Archive the plan directory to `.archive/`
- Update the source note in `grove-notebook` to reflect completion

### Holding a Plan

To temporarily pause work on a plan without archiving it:

```bash
flow plan hold user-profile-api
```

Held plans are hidden from default list views but can be shown with `--show-hold`. Resume work with `flow plan unhold`.