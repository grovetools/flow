# Working with Jobs 

Jobs are the fundamental units of work in a Grove Flow plan. Each job is a single, executable step defined in a Markdown file. This document covers how to create, configure, and manage these jobs to build effective workflows.

## Creating Jobs

The primary command for adding a new job to a plan is `flow plan add`. It can be used interactively through a Terminal User Interface (TUI) or non-interactively with flags.

### Interactive Mode (TUI)

Running `flow plan add` or `flow plan add -i` launches an interactive form to guide you through creating a new job. This interface provides prompts and lists available options like job types, dependencies, and models.

```bash
# If an active plan is set
flow plan add

# Or specify the plan directory
flow plan add ./plans/my-feature -i
```

The TUI allows you to set the job's title, type, prompt, dependencies, and other configurations in a structured way.

### Flag-Based Mode

For scripting or quick additions, you can provide all job details using flags.

**Example:**
```bash
# Set 'my-feature' as the active plan
flow plan set my-feature

# Add an agent job that depends on the initial spec
flow plan add \
  --title "Implement API Endpoints" \
  --type "agent" \
  --depends-on "01-spec.md" \
  --prompt "Implement the user API endpoints as defined in the spec."
```

This creates a new Markdown file (e.g., `02-implement-api-endpoints.md`) in the active plan directory with the specified frontmatter and prompt.

**Key `plan add` Flags:**

| Flag             | Alias | Description                                                                |
| ---------------- | ----- | -------------------------------------------------------------------------- |
| `--title`        |       | The title of the job.                                                      |
| `--type`         | `-t`  | The job type (e.g., `agent`, `oneshot`, `shell`).                           |
| `--depends-on`   | `-d`  | A job filename this new job depends on. Can be used multiple times.        |
| `--prompt`       | `-p`  | The main prompt or command for the job, provided inline.                   |
| `--prompt-file`  | `-f`  | Path to a file containing the prompt content.                              |
| `--source-files` |       | Comma-separated list of files to include as context.                       |
| `--template`     |       | The name of a job template to use.                                         |
| `--worktree`     |       | The git worktree for the job to run in.                                    |
| `--agent-continue` |     | For `agent` jobs, continue the previous agent session in the same worktree.|
| `--interactive`  | `-i`  | Force the interactive TUI mode.                                            |

### Writing Effective Prompts

-   **Be Specific**: State the goal and constraints. Provide file paths, function names, and expected outcomes.
-   **Provide Context**: Use `--source-files` or reference outputs from previous jobs to give the LLM the context it needs.
-   **Structure for the Job Type**: An `agent` prompt should be a high-level task, while a `oneshot` prompt can be a more specific question. A `shell` prompt is simply the command to be executed.

## Job Types Explained

Grove Flow supports several job types, each with a specific purpose and executor.

| Type                | Description                                                          | Use Case                                                              |
| ------------------- | -------------------------------------------------------------------- | --------------------------------------------------------------------- |
| `agent`             | An `interactive_agent` session for complex, multi-step coding tasks. | Implementing features, large-scale refactoring, debugging.            |
| `interactive_agent` | The primary agent type; a human-in-the-loop session in `tmux`.       | Same as `agent`. `agent` is an alias for this type.                   |
| `headless_agent`    | A non-interactive agent that runs in the background.                 | Fully automated code generation tasks in a CI/CD pipeline.            |
| `oneshot`           | A single-shot LLM prompt for analysis, review, or generation.        | Code reviews, generating documentation, creating test plans.          |
| `shell`             | Executes a shell command in the job's worktree or main repo.         | Running tests, linting, building code, or managing files.             |
| `chat`              | A multi-turn conversational job for ideation and refinement.         | Brainstorming a feature before creating a formal plan.                |
| `generate-recipe`   | Generates a reusable plan recipe from an existing plan.              | Automating the creation of standardized development workflows.        |

## Dependencies Between Jobs

You can create workflows by defining dependencies between jobs. A job will not run until all of its dependencies have the status `completed`.

Dependencies are defined using the `depends_on` key in the job's frontmatter, which contains a list of the filenames of the jobs it depends on.

**Example Frontmatter:**
```yaml
---
id: review-code
title: "Review Code"
status: pending
type: oneshot
depends_on:
  - 01-implement-feature.md
  - 02-run-tests.md
---
```

When using the CLI, you can specify dependencies with the `-d` or `--depends-on` flag:
```bash
flow plan add --title "Review Code" -d 01-implement-feature.md -d 02-run-tests.md
```

## Models and LLM Configuration

You can control which LLM is used for a job at multiple levels, following a clear inheritance hierarchy:

1.  **Job Level**: The `model` key in a job's frontmatter (`.md` file). This has the highest precedence.
2.  **Plan Level**: The `model` key in the plan's `.grove-plan.yml` file. This sets the default for all jobs in that plan. You can manage this with `flow plan config set model <model-id>`.
3.  **Project Level**: The `oneshot_model` key in your project's `grove.yml` file. This serves as the global default.

Use the `flow models` command to see a list of recommended models.

## Job Templates

Templates allow you to reuse common job structures and prompts.

-   **List Templates**: To see all available templates (project-local, user-global, and built-in), run:
    ```bash
    flow plan templates list
    ```

-   **Use a Template**: Apply a template when adding a new job using the `--template` flag.
    ```bash
    flow plan add --title "Review API" --template code-review
    ```

This will create a new job with the pre-defined prompt and configuration from the `code-review` template. You can add your own instructions, which will be appended to the template's prompt.

-   **Creating Custom Templates**: You can create your own templates by adding `.md` files to a `.grove/job-templates` directory in your project root.

## Job Completion and Management

-   **Manual Completion**: For jobs that require manual intervention or verification (like `interactive_agent` or `chat`), you can mark them as complete with:
    ```bash
    flow plan complete <job-file>
    ```
    When an `interactive_agent` job is completed, Grove Flow will automatically find the `clogs` transcript from the session and append it to the job file for a complete record.

-   **Automatic Summarization**: If `summarize_on_complete: true` is set in your `grove.yml`, Grove Flow will automatically generate a one-sentence summary of what the job accomplished and add it to the `summary` field in the job's frontmatter upon completion.

-   **Job Output**: The output from `oneshot` and `shell` jobs is appended to the job's Markdown file under an `## Output` heading, providing a persistent log of its execution.

-   **Debugging**: If a job fails, its status will be set to `failed`. The output and logs within the job file provide the necessary information to debug the issue. Once fixed, you can re-run the job with `flow plan run <job-file>`.

## Advanced Topics

-   **Job Frontmatter**: The YAML frontmatter in each job file is the source of truth for its configuration. You can directly edit these files to modify any aspect of a job, such as its type, dependencies, or prompt.
-   **Worktree Integration**: For `agent` and `shell` jobs, you can specify a `worktree` in the frontmatter. The job will execute within that git worktree, providing isolation from your main branch. If a dependent job is added, it will automatically inherit the worktree from its dependency unless specified otherwise.
