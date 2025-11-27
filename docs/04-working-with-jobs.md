# Working with Jobs

Jobs are the fundamental units of work in a Grove Flow plan. Each job is a single, executable step defined in a Markdown file with YAML frontmatter. Most job creation and management happens through the plan status TUI, though jobs can also be managed via CLI commands.

## Creating Jobs

Jobs are typically created through the plan status TUI's keyboard shortcuts, though they can also be created via the CLI.

### From the Plan Status TUI (Recommended)

The plan status TUI (`flow plan status -t`) provides keyboard shortcuts for creating jobs with proper dependencies:

- **`A`** - Add a new job to the plan
- **`x`** - Extract XML plan from selected chat job (creates a oneshot job)
- **`i`** - Create interactive agent implementation job (depends on selected job)

These actions automatically set up dependencies based on the currently selected job, ensuring proper workflow structure.

**Example workflow in TUI:**
1. Complete a chat job to explore the problem
2. Select the chat job and press `x` to create an XML plan extraction
3. Select the XML plan job and press `i` to create an implementation job
4. The dependency tree is automatically created: chat → xml-plan → implementation

### Using the CLI

For scripting or direct creation, jobs can be added via command-line flags:

```bash
# Set 'my-feature' as the active plan
flow plan set my-feature

# Add an agent job that depends on a preceding job file
flow plan add \
  --title "Implement API Endpoints" \
  --type "agent" \
  --depends-on "01-spec.md" \
  --prompt "Implement the user API endpoints as defined in the spec."
```

This creates a new Markdown file (e.g., `02-implement-api-endpoints.md`) in the active plan directory with the specified frontmatter and prompt body.

For an interactive CLI experience, use the `-i` flag to launch a guided TUI for job creation:

```bash
flow plan add -i
```

**Key `plan add` Flags:**

| Flag | Alias | Description |
| --- | --- | --- |
| `--title` | | The title of the job. |
| `--type` | `-t` | The job type (e.g., `agent`, `oneshot`, `shell`). |
| `--depends-on` | `-d` | A job filename this new job depends on. Can be used multiple times. |
| `--prompt` | `-p` | The main prompt or command for the job, provided inline. |
| `--prompt-file` | `-f` | Path to a file containing the prompt content. |
| `--source-files` | | Comma-separated list of files to include as context. |
| `--template` | | The name of a job template to use. |
| `--worktree` | | The git worktree for the job to run in. |
| `--agent-continue` | | For `agent` jobs, continue the previous agent session in the same worktree. |
| `--interactive` | `-i` | Force the interactive TUI mode. |

### Writing Effective Prompts

-   **State the Goal and Constraints**: Provide specific file paths, function names, and expected outcomes.
-   **Provide Context**: Use `--source-files` or define dependencies on previous jobs to supply the LLM with necessary information.
-   **Structure for the Job Type**: An `agent` prompt should be a high-level task description. A `oneshot` prompt can be a more specific question or instruction. A `shell` prompt is the command to be executed.

## Job Types Explained

Grove Flow supports several job types, each executed by a specific mechanism.

| Type | Description | Use Case |
| --- | --- | --- |
| `agent` | An alias for `interactive_agent`. | Implementing features, large-scale refactoring, debugging. |
| `interactive_agent` | An agent session in `tmux` that allows for human interaction. | The primary type for complex, multi-step coding tasks. |
| `headless_agent` | A non-interactive agent that runs in the background. | Fully automated code generation tasks, often in CI environments. |
| `oneshot` | A single-shot LLM prompt for analysis, review, or generation. | Code reviews, generating documentation, or creating test plans. |
| `shell` | Executes a shell command in the job's worktree or main repo. | Running tests, linting, building code, or file management. |
| `chat` | A multi-turn conversational job for ideation and refinement. | Brainstorming a feature or breaking down a task before creating a formal plan. |
| `generate-recipe` | Generates a reusable plan recipe from an existing plan. | Automating the creation of standardized development workflows. |

## Dependencies Between Jobs

Workflows are created by defining dependencies between jobs. A job will not run until all of its dependencies have a status of `completed`. Dependencies are defined using the `depends_on` key in a job's frontmatter, which contains a list of the filenames of the jobs it depends on.

**Example Frontmatter:**
```yaml
---
id: job-c5b1a2d3
title: "Review Code"
status: pending
type: oneshot
depends_on:
  - 01-implement-feature.md
  - 02-run-tests.md
---
```

When using the CLI, dependencies are specified with the `-d` or `--depends-on` flag:
```bash
flow plan add --title "Review Code" -d 01-implement-feature.md -d 02-run-tests.md
```

## Models and LLM Configuration

The LLM used for a job is determined by a three-level hierarchy:

1.  **Job Level**: The `model` key in a job's frontmatter (`.md` file). This has the highest precedence.
2.  **Plan Level**: The `model` key in the plan's `.grove-plan.yml` file. This sets the default for all jobs in that plan and can be managed with `flow plan config set model <model-id>`.
3.  **Project Level**: The `oneshot_model` key in your project's `grove.yml` file. This serves as the global default.

Use the `flow models` command to see a list of recommended models.

## Job Templates

Templates allow for the reuse of common job structures and prompts.

-   **List Templates**: To see all available templates (project-local, user-global, and built-in), run:
    ```bash
    flow plan templates list
    ```

-   **Use a Template**: Apply a template when adding a job using the `--template` flag.
    ```bash
    flow plan add --title "Review API" --template code-review
    ```
    This creates a new job using the pre-defined prompt and configuration from the `code-review` template. Additional instructions can be provided via the `--prompt` or `--prompt-file` flags, which will be appended to the template's prompt.

-   **Creating Custom Templates**: Custom templates can be created by adding `.md` files to a `.grove/job-templates` directory in your project root or user config directory (`~/.config/grove/job-templates`).

## Job Management

### Managing Jobs from the TUI

The plan status TUI provides keyboard shortcuts for common job management tasks:

- **`r`** - Run selected job(s)
- **`c`** - Mark selected job as completed
- **`R`** - Resume a completed interactive agent job
- **`e`** - Edit the job file
- **`d`** - Delete the job
- **`space`** - Toggle job selection (for batch operations)

### Managing Jobs from the CLI

**Manual Completion:**

For jobs that require manual intervention or verification (like `interactive_agent` or `chat`), mark them as complete:

```bash
flow plan complete <job-file>
```

When an `interactive_agent` job is completed, its session transcript is found via `grove aglogs` and appended to the job file.

**Resuming Interactive Sessions:**

Completed `interactive_agent` jobs can be resumed to continue the conversation:

```bash
flow plan resume <job-file>
```

This re-launches the agent in a new tmux window with the full conversation history. When completed again, the transcript is updated with the resumed conversation. The TUI provides a keyboard shortcut (`R`) for resuming completed interactive jobs.

**Renaming Jobs:**

To rename a job and automatically update all references to it in other jobs within the plan:

```bash
flow plan jobs rename <old-job-file> "New Job Title"
```

**Updating Dependencies:**

To replace a job's entire dependency list:

```bash
flow plan jobs update-deps <job-file> [new-dep-1.md] [new-dep-2.md]
```

Running the command without any dependency files will clear all dependencies for the specified job.

### Automatic Features

- **Automatic Summarization**: If `summarize_on_complete: true` is set in `grove.yml`, a one-sentence summary of the job's accomplishment will be generated and added to the `summary` field in the job's frontmatter upon completion.

- **Job Output**: The output from `oneshot` and `shell` jobs is appended to the job's Markdown file under an `## Output` heading.

- **Note Reference**: When a plan is created from a note in `grove-notebook`, the initial job includes a `note_ref` field that links back to the source note. This maintains traceability from idea to implementation.

## Advanced Topics

-   **Job Frontmatter**: The YAML frontmatter in each job file is the source of truth for its configuration. These files can be edited directly to modify any aspect of a job.
-   **Worktree Integration**: For `agent` and `shell` jobs, a `worktree` can be specified in the frontmatter. The job will then execute within that git worktree, providing isolation from the main branch.