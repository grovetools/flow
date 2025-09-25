# CLI Reference

This document provides comprehensive reference information for all Grove Flow commands, flags, and options.
## flow

The `flow` command is the main entry point for Grove Flow, providing access to plan and chat management, model information, version details, and more.

### Usage

```bash
flow [command] [flags]
```

### Subcommands

*   `plan`: Manage multi-step orchestration plans.
*   `chat`: Start or manage chat-based jobs.
*   `models`: List available LLM models.
*   `version`: Print version information.
*   `starship`: Manage Starship prompt integration.

## flow plan

The `flow plan` command manages multi-step orchestration plans, allowing users to define, run, and manage workflows as a series of jobs.

### Usage

```bash
flow plan [subcommand] [flags] [arguments]
```

### Subcommands

#### `flow plan init <directory>`

Initializes a new plan directory with a default configuration file. If no directory is specified, an interactive TUI will be launched.

*   **Usage:** `flow plan init <directory> [flags]`
*   **Description:** Creates a new plan directory and a `.grove-plan.yml` file with default configuration options.
*   **Arguments:**
    *   `<directory>`: (Optional) The name of the directory to initialize. If not provided, an interactive TUI is launched.
*   **Flags:**
    *   `-f, --force`: Overwrite existing directory (boolean, default: false)
    *   `--model`: Default model for jobs (string, e.g., claude-3-5-sonnet-20241022, gpt-4)
    *   `--worktree`: Set default worktree (string, uses plan name if no value provided)
    *   `--target-agent-container`: Default container for agent jobs (string)
    *   `--extract-all-from`: Path to a markdown file to extract all content from into an initial job (string)
    *   `--open-session`: Immediately open a tmux session for the plan (boolean, default: false)
    *   `--recipe`: Name of a plan recipe to initialize from (string, e.g., standard-feature)
    *   `--recipe-vars`: Variables to pass to recipe templates (string array, e.g., `--recipe-vars model=gpt-4 --recipe-vars rules_file=docs.rules`)
    *   `--recipe-cmd`: Command that outputs JSON recipe definitions (string, overrides grove.yml's get_recipe_cmd)
    *   `-t, --tui`: Launch interactive TUI to create a new plan (boolean, default: false)
*   **Examples:**
    *   `flow plan init my-new-plan`: Initializes a new plan in the `my-new-plan` directory.
    *   `flow plan init my-new-plan --worktree`: Initializes a new plan with a default worktree.
    *   `flow plan init my-new-plan --recipe standard-feature`: Initializes a new plan using the `standard-feature` recipe.

#### `flow plan add [directory]`

Adds a new job to an existing plan. Can be used interactively or with command-line arguments.

*   **Usage:** `flow plan add [directory] [flags]`
*   **Description:** Adds a new job to an existing plan directory. If no directory is specified, uses the active plan if set.
*   **Arguments:**
    *   `[directory]`: (Optional) The plan directory. If not specified, uses the active plan.
*   **Flags:**
    *   `--template`: Name of the job template to use (string)
    *   `-t, --type`: Job type: oneshot, agent, chat, interactive_agent, headless_agent, or shell (string, default: agent)
    *   `--title`: Job title (string)
    *   `-d, --depends-on`: Dependencies (job filenames, string array)
    *   `-f, --prompt-file`: File containing the prompt (string)
    *   `-p, --prompt`: Inline prompt text (string)
    *   `--output-type`: Output type: file, commit, none, or generate_jobs (string, default: file)
    *   `-i, --interactive`: Interactive mode (boolean, default: false)
    *   `--source-files`: Comma-separated list of source files for reference-based prompts (string array)
    *   `--worktree`: Explicitly set the worktree name (overrides automatic inference) (string)
    *   `--agent-continue`: Continue the last agent session (adds --continue flag) (boolean, default: false)
*   **Examples:**
    *   `flow plan add myplan \
          -t agent \
          --title "Implementation" \
          -d 01-plan.md \
          -p "Implement the user authentication feature"`: Adds a job with an inline prompt.
    *   `flow plan add myplan \
          -t agent \
          --title "Implementation" \
          -d 01-plan.md \
          -f prompt.md`: Adds a job with a prompt from a file.
    *   `echo "Implement feature X" | flow plan add myplan \
          -t agent \
          --title "Implementation" \
          -d 01-plan.md`: Adds a job with a prompt from stdin.

#### `flow plan list`

Lists all plans in the configured plans directory.

*   **Usage:** `flow plan list [flags]`
*   **Description:** Scans the directory specified in `flow.plans_directory` from your `grove.yml` and lists all orchestration plans found.
*   **Flags:**
    *   `-v, --verbose`: Show detailed information including jobs in each plan (boolean, default: false)
    *   `--include-finished`: Include finished plans in the output (boolean, default: false)
*   **Examples:**
    *   `flow plan list`: Lists all plans.
    *   `flow plan list -v`: Lists all plans with verbose output.

#### `flow plan tui`

Launches an interactive TUI for browsing and managing plans.

*   **Usage:** `flow plan tui`
*   **Description:** Provides a navigable view of all plans in your plans directory with interactive features.
*   **Examples:**
    *   `flow plan tui`: Launches the interactive TUI.

#### `flow plan set <plan-directory>`

Sets the active job plan directory.

*   **Usage:** `flow plan set <plan-directory>`
*   **Description:** Sets the active job plan directory to avoid specifying it in every command.
*   **Arguments:**
    *   `<plan-directory>`: The plan directory to set as active.
*   **Examples:**
    *   `flow plan set user-profile-api`: Sets the active job to the `user-profile-api` directory.

#### `flow plan current`

Shows the current active job plan directory.

*   **Usage:** `flow plan current`
*   **Description:** Shows the current active job plan directory.
*   **Examples:**
    *   `flow plan current`: Displays the current active job, if any.

#### `flow plan unset`

Clears the active job plan directory.

*   **Usage:** `flow plan unset`
*   **Description:** Clears the active job plan directory.
*   **Examples:**
    *   `flow plan unset`: Clears the currently set active job.

#### `flow plan status [directory]`

Shows the status of all jobs in an orchestration plan.

*   **Usage:** `flow plan status [directory] [flags]`
*   **Description:** Displays the status of all jobs in a plan. If no directory is specified, uses the active job if set.
*   **Arguments:**
    *   `[directory]`: (Optional) The plan directory. If not specified, uses the active plan.
*   **Flags:**
    *   `-v, --verbose`: Show detailed job information (boolean, default: false)
    *   `-g, --graph`: Visualize job dependency graph (boolean, default: false)
    *   `-f, --format`: Output format: mermaid, dot, ascii (string, default: mermaid)
    *   `-t, --tui`: Launch interactive TUI (boolean, default: false)
*   **Examples:**
    *   `flow plan status my-plan`: Shows the status of the `my-plan` plan.
    *   `flow plan status -t`: Launches an interactive TUI to display the plan status.

#### `flow plan graph [directory]`

Visualizes the job dependency graph.

*   **Usage:** `flow plan graph [directory] [flags]`
*   **Description:** Generates a visualization of the job dependency graph. Supports multiple output formats including Mermaid, DOT, and ASCII. If no directory is specified, uses the active job if set.
*   **Arguments:**
    *   `[directory]`: (Optional) The plan directory. If not specified, uses the active plan.
*   **Flags:**
    *   `-f, --format`: Output format: mermaid, dot, ascii (string, default: mermaid)
    *   `-s, --serve`: Serve interactive HTML visualization (boolean, default: false)
    *   `-p, --port`: Port for web server (integer, default: 8080)
    *   `-o, --output`: Output file (string, stdout if not specified)
*   **Examples:**
    *   `flow plan graph my-plan -f mermaid`: Generates a Mermaid diagram of the plan.
    *   `flow plan graph -s`: Serves an interactive HTML visualization of the plan.

#### `flow plan run [job-file]`

Executes jobs in an orchestration plan.

*   **Usage:** `flow plan run [job-file] [flags]`
*   **Description:** Runs jobs in a plan. Without arguments, runs the next available jobs. With a job file argument, runs that specific job.
*   **Arguments:**
    *   `[job-file]`: (Optional) The specific job file to run.
*   **Flags:**
    *   `-d, --dir`: Plan directory (string, default: ".")
    *   `-a, --all`: Run all pending jobs (boolean, default: false)
    *   `-n, --next`: Run next available jobs (boolean, default: false)
    *   `-p, --parallel`: Max parallel jobs (integer, default: 3)
    *   `-w, --watch`: Watch progress in real-time (boolean, default: false)
    *   `-y, --yes`: Skip confirmation prompts (boolean, default: false)
    *   `--model`: Override model for jobs (string, e.g., claude-3-5-sonnet-20240620, gpt-4)
	*   `--skip-interactive`: Skip interactive agent jobs (boolean, default: false)
*   **Examples:**
    *   `flow plan run`: Runs the next available jobs in the active plan.
    *   `flow plan run 01-my-job.md`: Runs a specific job file.

#### `flow plan complete <job-file>`

Marks a job as completed.

*   **Usage:** `flow plan complete <job-file>`
*   **Description:** Marks a job as completed, especially useful for chat jobs.
*   **Arguments:**
    *   `<job-file>`: The job file to mark as completed.
*   **Examples:**
    *   `flow plan complete my-project/plan.md`: Completes a chat job.
    *   `flow plan complete my-project/01-design-api.md`: Completes a job by its filename.

#### `flow plan launch <job-file>`

Launches an interactive agent session for a job.

*   **Usage:** `flow plan launch <job-file> [flags]`
*   **Description:** Launches a job in a new detached tmux session, pre-filling the agent prompt.
*   **Arguments:**
    *   `<job-file>`: The job file to launch.
*   **Flags:**
    *   `--host`: Launch agent on the host in the main git repo, not in a container worktree (boolean, default: false)
*   **Examples:**
    *   `flow plan launch my-project/01-design-api.md`: Launches an agent job in a tmux session.
    *   `flow plan launch my-project/01-design-api.md --host`: Launches an agent job in the main git repo.

#### `flow plan finish [directory]`

Completes and cleans up a plan and its associated worktree.

*   **Usage:** `flow plan finish [directory] [flags]`
*   **Description:** Guides through the process of cleaning up a completed plan, including removing the git worktree, deleting the branch, closing tmux sessions, and archiving the plan.
*   **Arguments:**
    *   `[directory]`: (Optional) The plan directory. If not specified, uses the active plan.
*   **Flags:**
    *   `-y, --yes`: Automatically confirm all cleanup actions (boolean, default: false)
    *   `--delete-branch`: Delete the local git branch (boolean, default: false)
    *   `--delete-remote`: Delete the remote git branch (boolean, default: false)
    *   `--prune-worktree`: Remove the git worktree directory (boolean, default: false)
    *   `--close-session`: Close the associated tmux session (boolean, default: false)
    *   `--clean-dev-links`: Clean up development binary links from the worktree (boolean, default: false)
    *   `--archive`: Archive the plan directory using 'nb archive' (boolean, default: false)
    *   `--force`: Force git operations (use with caution) (boolean, default: false)
*   **Examples:**
    *   `flow plan finish my-plan`: Finishes the `my-plan` plan and prompts for cleanup actions.
    *   `flow plan finish --yes`: Finishes the active plan and automatically performs all cleanup actions.

#### `flow plan config [directory]`

Gets or sets plan configuration values.

*   **Usage:** `flow plan config [directory] [flags]`
*   **Description:** Get or set configuration values in the plan's `.grove-plan.yml` file.
*   **Arguments:**
    *   `[directory]`: (Optional) The plan directory. If not specified, uses the active plan.
*   **Flags:**
    *   `--set`: Set a configuration value (format: key=value, string array)
    *   `--get`: Get a configuration value (string)
    *   `--json`: Output in JSON format (boolean, default: false)
*   **Examples:**
    *   `flow plan config myplan --set model=gemini-2.0-flash`: Sets the model configuration value.
    *   `flow plan config myplan --get model`: Gets the model configuration value.

#### `flow plan extract <block-id-1> [block-id-2...] | all | list`

Extracts chat blocks into a new chat job or lists available blocks.

*   **Usage:** `flow plan extract <block-id-1> [block-id-2...] | all | list [flags]`
*   **Description:** Extracts specific LLM responses from a chat into a new chat job, or lists available blocks in a file. The special argument "all" extracts all content below the frontmatter. The special argument "list" shows all available block IDs in the file.
*   **Arguments:**
    *   `<block-id-1> [block-id-2...] | all | list`: One or more block IDs to extract, the keyword "all", or the keyword "list".
*   **Flags:**
    *   `--title`: Title for the new chat job (string)
    *   `--file`: Chat file to extract from (string, default: plan.md)
    *   `-d, --depends-on`: Dependencies (job filenames, string array)
    *   `--worktree`: Explicitly set the worktree name (string, overrides automatic inference)
    *   `--model`: LLM model to use for this job (string)
    *   `--output`: Output type: file, commit, none, or generate_jobs (string, default: file)
    *   `--json`: Output in JSON format (boolean, default: false)
*   **Examples:**
    *   `flow plan extract --title "Database Schema Refinement" f3b9a2 a1c2d4`: Extracts two specific blocks.
    *   `flow plan extract all --file doc.md --title "Full Document"`: Extracts all content below the frontmatter.
    *   `flow plan extract list --file chat-session.md`: Lists available blocks in a file.

#### `flow plan templates list`

Lists available job templates.

*   **Usage:** `flow plan templates list [flags]`
*   **Description:** Lists available job templates for use in new plans and jobs.
*   **Flags:**
    *   `--json`: Output in JSON format (boolean, default: false)
*   **Examples:**
    *   `flow plan templates list`: Lists all available templates.

## flow chat

The `flow chat` command is used to start and manage chat-based jobs, facilitating interactive conversations with LLMs.

### Usage

```bash
flow chat [subcommand] [flags]
```

### Subcommands

#### `flow chat -s <file.md>`

Initializes a markdown file as a runnable chat job.

*   **Usage:** `flow chat -s <file.md> [flags]`
*   **Description:** Converts an existing markdown file into a chat job by adding the necessary frontmatter.
*   **Flags:**
    *   `-s, --spec-file`: Path to an existing markdown file to convert into a chat job (string, required)
    *   `-t, --title`: Title for the chat job (string, defaults to the filename)
    *   `-m, --model`: LLM model to use for the chat (string, defaults to flow.oneshot_model from config)
*   **Examples:**
    *   `flow chat -s /path/to/my-notes/new-feature.md`: Initializes a chat job from the specified file.

#### `flow chat list`

Lists all chat jobs in the configured chat directory.

*   **Usage:** `flow chat list [flags]`
*   **Description:** Lists all chat jobs in the configured chat directory.
*   **Flags:**
    *   `--status`: Filter chats by status (e.g., pending_user, completed, running, string)
*   **Examples:**
    *   `flow chat list`: Lists all chat jobs.
    *   `flow chat list --status pending_user`: Lists all chat jobs with the `pending_user` status.

#### `flow chat run [title...]`

Runs outstanding chat jobs that are waiting for an LLM response.

*   **Usage:** `flow chat run [title...]`
*   **Description:** Scans the configured chat directory for chats where the last turn is from a user and executes them sequentially to generate the next LLM response. You can optionally specify chat titles to run only specific chats.
*   **Arguments:**
    *   `[title...]`: (Optional) One or more chat titles to run.
*   **Examples:**
    *   `flow chat run`: Runs all pending chats.
    *   `flow chat run testing-situation`: Runs only the chat titled "testing-situation".
    *   `flow chat run chat1 chat2`: Runs multiple specific chats.

#### `flow chat launch [title-or-file]`

Launches an interactive agent session from a chat file.

*   **Usage:** `flow chat launch [title-or-file]`
*   **Description:** Launches a chat in a new detached tmux session, pre-filling the agent prompt with the chat content. Allows you to quickly jump from an idea in a markdown file into an interactive session.
*   **Arguments:**
    *   `[title-or-file]`: The title or file path of the chat to launch.
*   **Flags:**
    *    `--host`: Launch agent on the host in the main git repo, not in a container worktree (boolean, default: false)
*   **Examples:**
    *   `flow chat launch issue123`: Launches the chat with the title "issue123".
    *   `flow chat launch /path/to/issue.md`: Launches the chat from the specified file path.

## flow models

The `flow models` command provides information about available LLM models.

### Usage

```bash
flow models [subcommand]
```

### Subcommands

#### `flow models list`

Lists available LLM models.

*   **Usage:** `flow models list`
*   **Description:** Lists recommended LLM models that can be used in job and chat frontmatter.
*   **Flags:**
    *  `--json`: Output in JSON format (boolean, default: false)

## flow version

The `flow version` command displays version and build information for the Grove Flow CLI.

### Usage

```bash
flow version [flags]
```

### Flags

*   `--json`: Output version information in JSON format (boolean, default: false)

### Examples

```bash
flow version
```

Output:

```text
Version: v0.2.0
Commit: 1234567
Branch: main
BuildDate: 2024-01-01T00:00:00Z
```

or

```json
{
  "Version": "v0.2.0",
  "Commit": "1234567",
  "Branch": "main",
  "BuildDate": "2024-01-01T00:00:00Z"
}
```

## flow starship

The `flow starship` command is used to manage integration with the Starship prompt.

### Usage

```bash
flow starship [subcommand]
```

### Subcommands

#### `flow starship install`

Installs the Grove Flow module to your starship.toml.

*   **Usage:** `flow starship install`
*   **Description:** Appends a custom module to your `starship.toml` configuration file to display the current active plan, model, and worktree in your shell prompt.

#### `flow starship status`

Prints status for the Starship prompt. This is a hidden command for internal use.

## Global Options

The following global options can be used with any `flow` command:

*   `--config`: Specify config file (string)
*   `--verbose`: Verbose output (boolean)
*   `--quiet`: Suppress output (boolean)
*   `--format`: Output format options (string)

## Exit Codes

| Code | Meaning                                     |
|------|---------------------------------------------|
| 0    | Success                                     |
| 1    | General error or command failure           |
| 2    | Configuration error                         |
| 3    | Invalid arguments or usage                  |

## Environment Variables

| Variable                | Description                                                                 |
|-------------------------|-----------------------------------------------------------------------------|
| `GROVE_HOME`            | Overrides the Grove home directory.                                       |
| `GROVE_CONFIG`          | Specifies a custom config file location.                                   |
| Model API keys          | Environment variables specific to each LLM provider for authentication.    |

## Configuration Files

Grove Flow uses `grove.yml` and `.grove-plan.yml` for configuration. See the documentation for details.

### `grove.yml`
-   Located in the project root directory.
-   Defines global configurations for Grove Flow.

### `.grove-plan.yml`
-   Located in each plan directory.
-   Overrides global configurations for a specific plan.