# Configuration

Grove Flow configuration is managed through YAML files at the project level (`grove.yml`) and the plan level (`.grove-plan.yml`). This allows for project-wide defaults with plan-specific overrides.

## Project-Level Configuration (`grove.yml`)

Global defaults for Grove Flow are set under the `flow` key in the project's `grove.yml` file.

```yaml
# Example grove.yml
flow:
  # Default model for oneshot and chat jobs.
  oneshot_model: gemini-2.5-pro
  
  # Default container image for agent jobs.
  target_agent_container: grove-agent-ide

  # Maximum consecutive non-interactive steps before the orchestrator halts.
  # This acts as a safeguard against potential infinite loops. Default: 20
  max_consecutive_steps: 50

  # Configuration for job summarization on completion.
  summarize_on_complete: true
  summary_model: gemini-2.5-flash
  summary_prompt: "Provide a one-sentence summary of the outcome from the following job log, under 150 characters:\n---\n%s"
  summary_max_chars: 150

  # Configuration for plan recipes.
  recipes:
    # (Optional) Command to execute for discovering dynamic recipes.
    # The command should output a JSON object where keys are recipe names.
    get_recipe_cmd: "grove-recipes list --json"
    
    # (Optional) Default variables for specific recipes.
    # These can be overridden by the --recipe-vars flag during plan creation.
    docgen-customize:
      vars:
        model: "gemini-2.5-pro"
```

### Core Settings

-   `plans_directory` **(Deprecated)**: Previously specified the storage directory for plans. This setting is now superseded by the `notebooks` configuration in `grove.yml`, which provides a more unified way to manage plans, chats, and notes.
-   `chat_directory` **(Deprecated)**: Previously defined the location for standalone chat files. This is also now managed by the `notebooks` configuration.
-   `oneshot_model`: Sets the default LLM model for `oneshot` and `chat` jobs. This value is used if no model is specified in the plan or job frontmatter.
-   `target_agent_container`: Specifies the default Docker container image for `agent` and `interactive_agent` jobs.
-   `max_consecutive_steps`: An integer that sets the maximum number of non-interactive jobs the orchestrator will run sequentially before halting. This prevents runaway executions. Defaults to 20.

### Summarization Settings

-   `summarize_on_complete`: A boolean (`true` or `false`) that enables or disables automatic job summarization when a job is marked as completed via `flow plan complete`.
-   `summary_model`: The LLM model used to generate job summaries.
-   `summary_prompt`: A custom prompt template for summarization. It must include a `%s` placeholder where the job content will be inserted.
-   `summary_max_chars`: An integer defining the maximum character length for a generated summary.

### Recipe Configuration

The `recipes` section configures how `flow plan init --recipe` behaves:

-   `get_recipe_cmd`: An optional command that outputs a JSON definition of available recipes, allowing for dynamic recipe loading from external sources.
-   `vars`: Under a specific recipe name (e.g., `docgen-customize`), this key defines default variables for that recipe's templates. These can be overridden at initialization time with the `--recipe-vars` flag.

## Plan-Level Configuration (`.grove-plan.yml`)

Each plan directory can contain a `.grove-plan.yml` file to specify settings that apply only to that plan, overriding any project-level defaults.

```yaml
# ./plans/my-feature-plan/.grove-plan.yml

# Override the default model for all jobs in this plan.
model: gemini-2.5-pro

# Set a default worktree for all agent and shell jobs in this plan.
worktree: feature/my-new-api

# For ecosystem worktrees, specify which repositories to include.
# If omitted, all submodules are included.
repos:
  - grove-core
  - grove-flow

# Set a status to control visibility in commands like `flow plan list`.
# Valid statuses: "hold", "review", "finished".
status: hold

# Default setting for whether dependency content should be inlined into prompts.
prepend_dependencies: true

# Plan-specific user notes, visible in the `flow plan tui`.
notes: "API implementation for the new user profile service."

# Hooks to execute at different lifecycle stages.
hooks:
  on_start: 'echo "Plan {{.PlanName}} starting..."'
  on_review: 'nb move "{{.NoteRef}}" review --force'
```

### Plan-Specific Settings

-   `model`: Overrides the `oneshot_model` from `grove.yml` for all jobs within this plan.
-   `worktree`: Sets a default `worktree` for `agent`, `interactive_agent`, and `shell` jobs. This is automatically set by `flow plan init --worktree`.
-   `target_agent_container`: Overrides the default container for agent jobs.
-   `status`: Sets a lifecycle status for the plan.
    -   `hold`: Hides the plan from default list views (`flow plan list`, `flow plan tui`).
    -   `review`: Marks the plan as ready for final review before cleanup. Set via `flow plan review`.
    -   `finished`: Marks the plan as complete. Set via `flow plan finish`.
-   `repos`: For "ecosystem worktrees" in projects with submodules, this specifies which repositories to include when creating the worktree. If omitted, all submodules are included.
-   `notes`: A string for user-defined notes about the plan, visible in the TUI.
-   `prepend_dependencies`: A boolean (`true` or `false`) that sets the default for whether dependency content is inlined into a job's prompt. Can be overridden in individual jobs.
-   `hooks`: Defines shell commands to run at specific plan lifecycle events (`on_start`, `on_review`, `on_finish`). Supports Go template variables like `{{.PlanName}}` and `{{.NoteRef}}`.

## Managing Plan Configuration

The `flow plan config` command reads and writes values to a plan's `.grove-plan.yml` file.

-   **View current configuration:**
    ```bash
    flow plan config my-feature-plan
    ```
-   **Get a specific value:**
    ```bash
    flow plan config my-feature-plan --get model
    ```
-   **Set or update values:**
    ```bash
    flow plan config my-feature-plan --set model=gemini-2.5-pro --set worktree=feature/new
    ```
    When a value is set, this command also propagates the new value to any existing job files within the plan that do not already have that key defined in their frontmatter.

## Configuration Inheritance

Settings are resolved in the following order of precedence (highest to lowest):

1.  **Job Frontmatter**: Values in a job's markdown file (`.md`).
2.  **Plan-Level (`.grove-plan.yml`)**: Configuration in the plan's directory.
3.  **Project-Level (`grove.yml`)**: Global configuration for the project.
4.  **System Defaults**: Hardcoded fallbacks within the application.

## Environment Variables

-   `GROVE_CONFIG`: Specifies a path to a custom `grove.yml` file, overriding the default search behavior up the directory tree.
-   `GROVE_ECOSYSTEM_ROOT`: A path to the root of a Grove ecosystem checkout, used to locate other Grove tools and shared resources.
-   LLM provider API keys (e.g., `GEMINI_API_KEY`) are required for `grove-flow` to function but are managed by their respective tool configurations (e.g., `grove-gemini`).

## Best Practices

-   Set project-wide defaults for `oneshot_model` and `target_agent_container` in the root `grove.yml` file.
-   Use `.grove-plan.yml` to define a plan-specific `worktree` to make the plan self-contained and portable.
-   Store LLM API keys in your shell environment or a secure keychain, not in version-controlled configuration files.
-   Use `flow plan set <plan-name>` to make a plan active for the current repository or worktree. This avoids the need to specify the plan directory in every subsequent command. The active plan is stored locally in `.grove/state.yml`.