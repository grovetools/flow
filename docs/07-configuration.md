# Configuration

Grove Flow is configured through YAML files at both the project and plan level, allowing for a flexible and layered approach to setting defaults and managing workflows.

## Project-Level Configuration (`grove.yml`)

The primary configuration for Grove Flow resides under the `flow` key in your project's `grove.yml` file. This file sets global defaults for all plans within the repository.

```yaml
# ./.grove/config.yml or ./grove.yml
flow:
  # Directory where plans are stored. Supports variables like ${REPO}.
  # Default: ./plans
  plans_directory: ./plans
  
  # Directory for chat-based jobs.
  # Default: ./chats
  chat_directory: ./chats

  # Default model for oneshot and chat jobs.
  # No default, must be set here or at the plan/job level.
  oneshot_model: gemini-2.5-pro
  
  # Default container image for agent jobs.
  # No default, must be set.
  target_agent_container: grove-agent-ide

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
    # These can be overridden by the --recipe-vars flag.
    docgen-customize:
      vars:
        model: "gemini-2.5-pro"
        rules_file: "docs/docs.rules"
```

### Core Settings

-   `plans_directory`: Specifies the directory where plan subdirectories are stored. It supports path variables like `${REPO}`. Defaults to `./plans`.
-   `chat_directory`: Defines the location for standalone chat files managed by the `flow chat` command. Defaults to `./chats`.
-   `oneshot_model`: Sets the default LLM model for `oneshot` and `chat` jobs. This setting is highly recommended.
-   `target_agent_container`: Specifies the default Docker container image to use for `agent` and `interactive_agent` jobs.
-   `summarize_on_complete`: A boolean (`true` or `false`) that enables or disables automatic job summarization when a job is marked as completed.
-   `summary_model`: The LLM model to use for generating job summaries.
-   `summary_prompt`: A custom prompt template for summarization. Must include a `%s` placeholder for the job content.
-   `summary_max_chars`: The maximum character length for a generated summary.

### Recipe Configuration

Under the `recipes` key, you can configure how plan recipes are handled:

-   `get_recipe_cmd`: An optional command that, when executed, outputs a JSON definition of available recipes. This allows for dynamic loading of recipes from external sources.
-   `vars`: A sub-key for defining default variables for a specific, named recipe. These variables are accessible within the recipe's templates and can be overridden at initialization time with the `--recipe-vars` flag.

## Plan-Level Configuration (`.grove-plan.yml`)

For more granular control, you can place a `.grove-plan.yml` file inside a plan's directory. This file overrides project-level settings for that specific plan, making plans self-contained and portable.

```yaml
# ./plans/my-feature-plan/.grove-plan.yml

# Override the default model for all jobs in this plan.
model: gemini-2.5-pro

# Set a default worktree for all agent jobs in this plan.
worktree: feature/my-new-api

# For ecosystem worktrees, specify which repositories to include.
# If omitted, all submodules are included.
repos:
  - grove-core
  - grove-flow

# Can be used to filter finished plans from commands like `flow plan list`.
status: finished
```

### Plan-Specific Settings

-   `model`: Overrides the default LLM model for all jobs within this plan.
-   `worktree`: Sets a default `worktree` for all `agent`, `interactive_agent`, and `shell` jobs in the plan. This is automatically set by `flow plan init --worktree`.
-   `target_agent_container`: Overrides the default container for agent jobs in the plan.
-   `status`: Can be set to `finished` to mark the plan as complete. Commands like `flow plan list` and `flow plan tui` will hide finished plans by default.
-   `repos`: For "ecosystem worktrees" in projects with submodules, this key specifies which repositories to include when creating the worktree.

## Managing Plan Configuration

The `flow plan config` command provides a convenient way to read and write values to a plan's `.grove-plan.yml` file.

-   **View current configuration:**
    ```bash
    flow plan config my-feature-plan
    ```
-   **Get a specific value:**
    ```bash
    flow plan config my-feature-plan --get model
    ```
-   **Set one or more values:**
    ```bash
    flow plan config my-feature-plan --set model=gemini-2.5-pro --set worktree=feature/new
    ```
    This command also propagates the new values to existing job files within the plan that do not already have that value explicitly set in their frontmatter.

## Configuration Inheritance

Grove Flow uses a clear hierarchy to determine which configuration value to use, providing predictability and control. Settings are resolved in the following order of precedence (from highest to lowest):

1.  **Job Frontmatter**: Values set directly in a job's markdown file (e.g., `model: gemini-2.5-pro`).
2.  **Plan-Level (`.grove-plan.yml`)**: Values set in the plan's configuration file.
3.  **Project-Level (`grove.yml`)**: Global values set in the `flow` section of the project's configuration file.
4.  **System Defaults**: Hardcoded fallbacks within the application.

## Environment Variables

-   `GROVE_CONFIG`: Specifies a path to a custom `grove.yml` configuration file, overriding the default search behavior.
-   `GROVE_ECOSYSTEM_ROOT`: A path to the root of a Grove ecosystem checkout, used to locate other Grove tools and shared resources.
-   LLM provider API keys (e.g., `GEMINI_API_KEY`) are managed by their respective tools (`grove-gemini`, `grove-openai`) but are essential for `grove-flow` to function.

## Best Practices

-   **Use Project Config for Common Defaults**: Set your most-used models and container images in the project `grove.yml` to avoid repetition.
-   **Use Plan Config for Self-Contained Workflows**: Define plan-specific models or worktrees in `.grove-plan.yml` to make your plans portable and easy to understand.
-   **Manage API Keys Securely**: Store LLM API keys in your environment or a secure keychain, not in configuration files.
-   **Leverage Active Plan State**: Use `flow plan set` to make a plan active, which simplifies commands by removing the need to specify the plan directory repeatedly. This state is local to your checkout or worktree.