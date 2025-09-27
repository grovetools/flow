This document provides a comprehensive reference for the `flow` command-line interface, covering all subcommands and their options.

## `flow plan`

Manages multi-step orchestration plans.

### `flow plan init`

Initializes a new plan directory.

**Syntax**

```bash
flow plan init <directory> [flags]
```

**Description**

Creates a new plan in the specified directory, including a `.grove-plan.yml` file for default configuration. Running the command without a directory name or with the `-t` flag launches an interactive TUI to guide plan creation.

**Flags**

| Flag                       | Shorthand | Description                                                                   | Default          |
| -------------------------- | --------- | ----------------------------------------------------------------------------- | ---------------- |
| `--extract-all-from`       |           | Path to a markdown file to extract all content into an initial job.             |                  |
| `--force`                  | `-f`      | Overwrite the destination directory if it already exists.                       | `false`          |
| `--model`                  |           | Default model for jobs in this plan (e.g., `gemini-2.5-pro`).                 | (none)           |
| `--open-session`           |           | Immediately open a tmux session for the plan's worktree.                        | `false`          |
| `--recipe`                 |           | Initialize the plan from a pre-defined recipe template.                         | `chat-workflow`  |
| `--recipe-vars`            |           | Variables for recipe templates (`key=value`). Can be used multiple times.     | (none)           |
| `--target-agent-container` |           | Default container for agent jobs in the plan.                                 | (none)           |
| `--tui`                    | `-t`      | Launch an interactive TUI to create a new plan.                                 | `false`          |
| `--worktree`               |           | Set a default worktree. If no name is provided, uses the plan directory name. | (none)           |

**Examples**

```bash
# Initialize a new plan in the 'new-feature' directory
flow plan init new-feature

# Initialize a plan with a dedicated git worktree named 'feature-branch'
flow plan init new-feature --worktree=feature-branch

# Initialize a plan and extract content from an existing document
flow plan init refactor-plan --extract-all-from=docs/refactor-notes.md

# Initialize a plan from the 'standard-feature' recipe
flow plan init user-auth --recipe standard-feature
```

### `flow plan add`

Adds a new job to a plan.

**Syntax**

```bash
flow plan add [directory] [flags]
```

**Description**

Adds a new job file to a plan directory. If no directory is specified, it uses the active plan. The command can be run interactively or non-interactively with flags.

**Flags**

| Flag               | Shorthand | Description                                                                        | Default         |
| ------------------ | --------- | ---------------------------------------------------------------------------------- | --------------- |
| `--agent-continue` |           | Continue the last agent session (adds `--continue` flag to the agent command).     | `false`         |
| `--depends-on`     | `-d`      | List of job filenames this job depends on.                                         | (none)          |
| `--interactive`    | `-i`      | Launch an interactive TUI to create the new job.                                   | `false`         |
| `--output-type`    |           | Output type: `file`, `commit`, `none`, or `generate_jobs`.                         | `file`          |
| `--prompt`         | `-p`      | Inline prompt text for the job.                                                    | (from stdin)    |
| `--prompt-file`    | `-f`      | Path to a file containing the prompt.                                              | (none)          |
| `--source-files`   |           | Comma-separated list of source files for context.                                  | (none)          |
| `--template`       |           | Name of a job template to use.                                                     | (none)          |
| `--title`          |           | Title of the job.                                                                  | (required)      |
| `--type`           | `-t`      | Job type: `agent`, `interactive_agent`, `headless_agent`, `oneshot`, `shell`, `chat`. | `agent`         |
| `--worktree`       |           | Explicitly set the worktree for this job.                                          | (plan default)  |

**Examples**

```bash
# Add a new agent job to the active plan interactively
flow plan add -i

# Add a shell job to 'my-plan' that depends on a previous job
flow plan add my-plan -t shell -p "npm install" -d "01-setup.md" --title "Install Dependencies"

# Add a job using a template and source files for context
flow plan add --template code-review --source-files src/main.go --title "Review Main Logic"
```

### `flow plan list`

Lists all plans in the configured directory.

**Syntax**

```bash
flow plan list [flags]
```

**Flags**

| Flag                 | Shorthand | Description                              | Default |
| -------------------- | --------- | ---------------------------------------- | ------- |
| `--include-finished` |           | Include plans marked as "finished".      | `false` |
| `--verbose`          | `-v`      | Show detailed information for each plan. | `false` |

### `flow plan tui`

Launches an interactive TUI for browsing and managing plans.

**Syntax**

```bash
flow plan tui
```

### `flow plan set`, `current`, `unset`

Manage the active plan for the current context.

**Syntax**

```bash
flow plan set <plan-directory>
flow plan current
flow plan unset
```

### `flow plan status`

Shows the status of all jobs in a plan.

**Syntax**

```bash
flow plan status [directory] [flags]
```

**Flags**

| Flag      | Shorthand | Description                                | Default |
| --------- | --------- | ------------------------------------------ | ------- |
| `--format`| `-f`      | Output format: `tree`, `list`, `json`.     | `tree`  |
| `--graph` | `-g`      | Show a dependency graph in Mermaid syntax. | `false` |
| `--tui`   | `-t`      | Launch an interactive TUI status view.     | `false` |
| `--verbose`| `-v`      | Show detailed job information.             | `false` |

### `flow plan graph`

Visualizes the job dependency graph.

**Syntax**

```bash
flow plan graph [directory] [flags]
```

**Flags**

| Flag       | Shorthand | Description                                     | Default   |
| ---------- | --------- | ----------------------------------------------- | --------- |
| `--format` | `-f`      | Output format: `mermaid`, `dot`, `ascii`.       | `mermaid` |
| `--output` | `-o`      | Output file path (defaults to stdout).          | (stdout)  |
| `--port`   | `-p`      | Port for the web server when using `--serve`.   | `8080`    |
| `--serve`  | `-s`      | Serve an interactive HTML visualization.        | `false`   |

### `flow plan run`

Runs jobs in a plan.

**Syntax**

```bash
flow plan run [job-file] [flags]
```

**Description**

Executes jobs in an orchestration plan. Without arguments, it runs the next available jobs based on dependencies.

**Flags**

| Flag                 | Shorthand | Description                                            | Default |
| -------------------- | --------- | ------------------------------------------------------ | ------- |
| `--all`              | `-a`      | Run all pending jobs in the plan sequentially.         | `false` |
| `--model`            |           | Override the LLM model for this run.                   | (none)  |
| `--next`             | `-n`      | Run the next available jobs. (This is the default)     | `false` |
| `--parallel`         | `-p`      | Maximum number of jobs to run in parallel.             | `3`     |
| `--skip-interactive` |           | Skip any interactive agent jobs.                       | `false` |
| `--watch`            | `-w`      | Watch plan progress in real-time.                      | `false` |
| `--yes`              | `-y`      | Skip all confirmation prompts.                         | `false` |

### `flow plan complete`

Marks a job as completed.

**Syntax**

```bash
flow plan complete <job-file>
```

**Description**

Manually marks a job's status as `completed`. This is useful for interactive jobs or when an external process has finished a task.

### `flow plan open`

Opens a plan's worktree in a dedicated tmux session.

**Syntax**

```bash
flow plan open [directory]
```

### `flow plan launch`

Launches an interactive agent job in a tmux session.

**Syntax**

```bash
flow plan launch <job-file> [flags]
```

**Flags**

| Flag     | Shorthand | Description                                                  | Default |
| -------- | --------- | ------------------------------------------------------------ | ------- |
| `--host` |           | Launch the agent on the host machine instead of in a container. | `false` |

### `flow plan finish`

Guides through the process of finishing and cleaning up a plan.

**Syntax**

```bash
flow plan finish [directory] [flags]
```

**Flags**

| Flag                | Shorthand | Description                                           | Default |
| ------------------- | --------- | ----------------------------------------------------- | ------- |
| `--archive`         |           | Archive the plan directory using `nb archive`.        | `false` |
| `--clean-dev-links` |           | Clean up development binary links from the worktree.  | `false` |
| `--close-session`   |           | Close the associated tmux session.                    | `false` |
| `--delete-branch`   |           | Delete the local git branch.                          | `false` |
| `--delete-remote`   |           | Delete the remote git branch.                         | `false` |
| `--force`           |           | Force git operations (e.g., deleting unmerged branches). | `false` |
| `--prune-worktree`  |           | Remove the git worktree directory.                    | `false` |
| `--yes`             | `-y`      | Automatically confirm all cleanup actions.            | `false` |

### `flow plan config`

Gets or sets configuration values in a plan's `.grove-plan.yml`.

**Syntax**

```bash
flow plan config [directory] [flags]
```

**Flags**

| Flag     | Description                                      |
| -------- | ------------------------------------------------ |
| `--get`  | Get a specific configuration value by key.       |
| `--set`  | Set a configuration value (e.g., `key=value`).   |
| `--json` | Output the configuration in JSON format.         |

### `flow plan extract`

Extracts content from a chat or markdown file into a new job.

**Syntax**

```bash
flow plan extract <block-id... | all | list> --file <source-file> --title <new-job-title> [flags]
```

**Description**

`list`: Lists all extractable block IDs in the source file.
`all`: Extracts all content below the frontmatter into a single new job.
`<block-id...>`: Extracts one or more specific blocks into a new job.

**Flags**

| Flag           | Shorthand | Description                         |
| -------------- | --------- | ----------------------------------- |
| `--depends-on` | `-d`      | Dependencies for the new job.       |
| `--file`       |           | Source markdown file to extract from. |
| `--model`      |           | LLM model for the new job.          |
| `--output`     |           | Output type for the new job.        |
| `--title`      |           | Title for the new job (required).   |
| `--worktree`   |           | Worktree for the new job.           |
| `--json`       |           | Output block list in JSON format (for `list` command). |

### `flow plan templates list`

Lists available job templates.

**Syntax**

```bash
flow plan templates list
```

---

## `flow chat`

Manages conversational, multi-turn AI interactions.

**Syntax**

```bash
# Initialize a markdown file as a runnable chat
flow chat -s <file.md> [flags]

# List all chats
flow chat list [flags]

# Run pending chats
flow chat run [title...]
```

**Description**

The `chat` subcommand is used for ideation and refinement. You can start with a markdown file, turn it into a `chat` job, and have a conversation with an LLM. The results can later be extracted into a formal plan.

---

## `flow models`

Lists available LLM models.

**Syntax**

```bash
flow models [flags]
```

**Description**

Displays a list of recommended LLM models that can be used in job and chat frontmatter.

**Flags**

| Flag     | Description                         |
| -------- | ----------------------------------- |
| `--json` | Output the list in JSON format.     |

---

## `flow version`

Prints version information for the `flow` binary.

**Syntax**

```bash
flow version [flags]
```

**Flags**

| Flag     | Description                         |
| -------- | ----------------------------------- |
| `--json` | Output version info in JSON format. |

---

## Global Options

| Flag       | Description                                  |
| ---------- | -------------------------------------------- |
| `--config` | Path to a custom `grove.yml` configuration file. |
| `--json`   | Output command results in JSON format.       |
| `--verbose`| Enable verbose logging output.               |
| `--quiet`  | Suppress all non-essential output.           |
| `--help`   | Display help for any command.                |

---

## Environment Variables

-   `GROVE_ECOSYSTEM_ROOT`: Specifies the root directory of the Grove ecosystem repositories, used to locate shared resources.
-   `GROVE_FLOW_SKIP_DOCKER_CHECK`: If set to `true`, skips pre-flight checks for the Docker daemon (used in testing).

---

## Configuration Files

-   **`grove.yml`**: The main project-level configuration file. `flow` settings are placed under the `flow:` key.
-   **`.grove-plan.yml`**: A plan-specific configuration file located inside a plan directory. Values here override the project-level `grove.yml`.
-   **`.grove/state.yml`**: A local file that stores the active plan for the current directory or worktree context. This file should not be committed to version control.