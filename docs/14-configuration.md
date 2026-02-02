## Flow Configuration

This section details the configuration properties available under the `flow` extension in a `grove.yml` file. These settings control the behavior of the Grove Flow orchestration engine, including default models, directory structures, and automation preferences.

| Property | Description |
| :--- | :--- |
| `chat_directory` | (string, optional) <br> Specifies the directory where chat-based job files are stored or looked up. This helps separate interactive chat sessions from formal orchestration plans. |
| `max_consecutive_steps` | (integer, optional) <br> Defines the safety limit for the maximum number of consecutive execution steps the orchestrator will take before pausing. This prevents infinite loops in autonomous agent workflows. |
| `oneshot_model` | (string, optional) <br> The default Language Model (LLM) to use for "oneshot" jobs (jobs that execute a single prompt without a conversational loop) if no specific model is defined in the job itself. |
| `plans_directory` | (string, optional) <br> The root directory where Grove searches for orchestration plans. When running `flow plan list` or executing a plan by name, the system looks here. |
| `recipes` | (object, optional) <br> A configuration object for defining custom plan recipes or overrides for existing ones. |
| `run_init_by_default` | (boolean, optional) <br> Controls whether the initialization actions defined in a recipe should execute automatically when a plan is created. If set to `false`, the user must manually trigger initialization. |
| `summarize_on_complete` | (boolean, optional) <br> If set to `true`, the system will automatically generate a summary of the job's output using an LLM upon successful completion and append it to the job file. |
| `summary_max_chars` | (integer, optional) <br> The maximum character length for the automatically generated summary. Useful for keeping summaries concise for display in lists. |
| `summary_model` | (string, optional) <br> The specific LLM model to use when generating summaries. This allows you to use a cheaper or faster model for summarization than the one used for the main task. |
| `summary_prompt` | (string, optional) <br> A custom prompt template used to instruct the LLM on how to summarize the job output. |
| `target_agent_container` | (string, optional) <br> Specifies the default Docker container or environment where agent jobs should be executed. Useful for isolating agent execution environments. |

```toml
[flow]
plans_directory = "./plans"
oneshot_model = "claude-3-5-sonnet-20240620"
summarize_on_complete = true
max_consecutive_steps = 25
target_agent_container = "grove-agent-v1"
```

## Job Schema

This section describes the schema for Grove Flow jobs. These properties are typically found in the YAML frontmatter of Markdown files (`.md`) within a plan directory. They define the job's identity, execution parameters, dependencies, and state.

| Property | Description |
| :--- | :--- |
| `Dependencies` | (array, optional) <br> **System Managed.** An internal list of resolved dependency objects. Users should generally configure `depends_on` instead. |
| `EndTime` | (string, optional) <br> **System Managed.** The timestamp recording when the job execution finished. |
| `Filename` | (string, optional) <br> **System Managed.** The name of the file containing this job. |
| `FilePath` | (string, optional) <br> **System Managed.** The full filesystem path to the job file. |
| `PromptBody` | (string, optional) <br> **System Managed.** The textual content of the job (everything below the frontmatter), which serves as the primary prompt or instruction. |
| `StartTime` | (string, optional) <br> **System Managed.** The timestamp recording when the job execution began. |
| `branch` | (string, optional) <br> Specifies the git branch context in which this job should operate. |
| `completed_at` | (string, optional) <br> **System Managed.** The timestamp marking successful completion. |
| `created_at` | (string, optional) <br> **System Managed.** The timestamp marking when the job was created. |
| `depends_on` | (array of strings, optional) <br> A list of job IDs or filenames that this job depends on. This job will not execute until all listed dependencies have successfully completed. |
| `duration` | (integer, optional) <br> **System Managed.** The duration of the job execution in nanoseconds. |
| `gather_concept_notes` | (boolean, optional) <br> If `true`, the job will attempt to gather related notes from the knowledge base (concepts) and include them in the context. |
| `gather_concept_plans` | (boolean, optional) <br> If `true`, the job will attempt to gather related plans from the knowledge base and include them in the context. |
| `generate_plan_from` | (boolean, optional) <br> Indicates that this job is intended to generate a new execution plan based on the output of its dependencies. |
| `git_changes` | (boolean, optional) <br> If `true`, the current git diff/changes will be included in the context provided to the agent or LLM. |
| `id` | (string, optional) <br> A unique identifier for the job. Used for dependency resolution and referencing. |
| `include` | (array of strings, optional) <br> A list of file paths to include as context for this job. |
| `model` | (string, optional) <br> The LLM model to use for this specific job, overriding any global or plan-level defaults. |
| `note_ref` | (string, optional) <br> A reference to a specific note (e.g., in a PKM system) associated with this job. |
| `on_complete_status` | (string, optional) <br> Defines a status to set or an action to take when the job completes. |
| `prepend_dependencies` | **Deprecated** (boolean, optional) <br> Formerly used to inline dependency outputs. Please use the `inline` object with `Categories: ["dependencies"]` instead. |
| `recipe_name` | (string, optional) <br> The name of the recipe used if this job was generated from one. |
| `repository` | (string, optional) <br> Specifies the target git repository for this job. |
| `rules_file` | (string, optional) <br> Path to a specific rules file that governs context inclusion for this job. |
| `source_block` | (string, optional) <br> Used to target a specific block of text from a source file (e.g., `filename.md#block-id`) to use as the prompt. |
| `source_file` | (string, optional) <br> The path to the source file if this job was generated or extracted from another document. |
| `source_plan` | (string, optional) <br> The name of the plan this job belongs to or originated from. |
| `status` | (string, optional) <br> The current state of the job. Common values include `pending`, `running`, `completed`, `failed`. |
| `summary` | (string, optional) <br> **System Managed.** An automatically generated summary of the job's execution results. |
| `target_agent_container` | (string, optional) <br> Overrides the global agent container setting for this specific job. |
| `template` | (string, optional) <br> The name of a template to use for rendering the job's prompt structure. |
| `title` | (string, optional) <br> A human-readable title for the job. |
| `type` | (string, optional) <br> The type of job (e.g., `oneshot`, `agent`, `chat`, `interactive_agent`). |
| `updated_at` | (string, optional) <br> **System Managed.** The timestamp of the last update to the job file. |
| `worktree` | (string, optional) <br> The specific git worktree directory to use for this job's execution context. |

### Metadata

This object contains execution statistics and error details, typically managed by the system.

| Property | Description |
| :--- | :--- |
| `execution_time` | (integer, required) <br> The time taken to execute the job. |
| `last_error` | (string, required) <br> The error message if the last execution failed. |
| `retry_count` | (integer, required) <br> The number of times this job has been retried. |

### inline

Configuration for what content should be directly embedded (inlined) into the prompt context.

| Property | Description |
| :--- | :--- |
| `Categories` | (array of strings, required) <br> A list of category names to inline. Common values include `"dependencies"` (outputs of upstream jobs), `"include"` (content of included files), or `"context"` (project context). |

```toml
title = "Implement User Auth"
type = "agent"
status = "pending"
depends_on = ["01-design-auth.md"]
worktree = "feat/user-auth"
model = "claude-3-5-sonnet"
[inline]
Categories = ["dependencies", "include"]
```