# Grove Flow

<img src="https://github.com/user-attachments/assets/6b0cc785-c87f-4a34-a1ac-f083b5f3f8ac" width="70%" />

### LLM job orchestration in Markdown

`grove-flow` is a CLI tool for orchestrating multi-step tasks using LLMs. It allows you to define, run, and manage workflows (called "Plans") as a series of Markdown files, leveraging isolated worktrees for safe, reproducible execution.

Whether you're scaffolding a new feature, running a series of code modifications, or building an emergent plan with AI, `grove-flow` provides the structure and tooling to automate your development process.

## Core Concepts

-   **Plan**: A directory containing a sequence of dependent jobs that define a complete workflow. It's a living document of a complex task.
-   **Job**: A single step in a plan, defined in a Markdown file. Jobs have a type, status, dependencies, and a prompt for an LLM or shell.
-   **Executor**: The engine that runs a job. `grove-flow` has different executors for different job types (e.g., `agent` for code generation, `shell` for commands, `oneshot` for analysis).
-   **Worktree Isolation**: `agent` and `interactive_agent` jobs run in dedicated Git worktrees, keeping your main branch clean until you're ready to merge changes.

## Features

-   🌳 **Multi-Step Plans**: Define complex workflows with dependencies between jobs.
-   🤖 **Diverse Job Types**:
    -   `agent`: Long-running, stateful AI agent for code generation in an isolated worktree.
    -   `interactive_agent`: Human-in-the-loop agent sessions in `tmux`.
    -   `oneshot`: Quick, single-shot LLM prompts for analysis or generation.
    -   `shell`: Execute arbitrary shell commands in a worktree or the main repo.
    -   `chat`: Manage conversational, multi-turn AI interactions as markdown files.
-   🌿 **Git Integration**: Automatic creation and management of Git worktrees for safe, isolated execution.
-   ✨ **Emergent Workflows**: Use the `generate_jobs` output type to have an AI create the next steps in your plan dynamically.
-   💻 **Interactive Tooling**: A beautiful terminal UI (`flow plan status -t`) and an interactive step-by-step wizard (`flow plan step`) for managing plans.
-   🚀 **Powerful CLI**: A comprehensive set of commands to `init`, `add`, `run`, `status`, `graph`, and `launch` your plans and jobs.
-   📄 **Templating**: Create and reuse job templates for common tasks.
-   💬 **Chat-to-Code**: Start with an idea in a markdown `chat` file, and seamlessly `extract` parts into a formal plan.

## Quick Start

1.  **Initialize a new plan:**
    ```bash
    flow plan init new-feature --with-worktree
    ```
    This creates a `plans/new-feature` directory, sets its default worktree to `new-feature`, and makes it the active plan.

2.  **Add your first job:**
    ```bash
    # Since 'new-feature' is active, we don't need to specify the directory
    flow plan add --title "Scaffold API" --type agent -p "Create a new Go API endpoint for users with basic CRUD operations."
    ```
    This creates `01-scaffold-api.md` inside your plan. The job will run in the `new-feature` worktree.

3.  **Check the plan status with the interactive TUI:**
    ```bash
    flow plan status -t
    ```
    ![Status TUI](https://raw.githubusercontent.com/mattsolo1/assets/main/grove-flow/status-tui.png)

4.  **Run the job:**
    ```bash
    flow plan run
    ```
    This runs the next available job in the active plan. The agent will spin up in its isolated worktree and begin implementing the feature.

## Usage

### `flow plan`

The `plan` subcommand is the core of `grove-flow`, used to manage multi-step workflows.

-   `flow plan init <dir>`: Create a new plan directory.
-   `flow plan add [dir]`: Add a new job to a plan.
-   `flow plan run [job-file]`: Run the next job, a specific job, or all jobs (`--all`).
-   `flow plan status [dir]`: View the plan's status. Use `-t` for the TUI.
-   `flow plan graph [dir]`: Visualize the dependency graph as Mermaid, DOT, or ASCII.
-   `flow plan launch <job-file>`: Launch an `interactive_agent` job in a `tmux` session.
-   `flow plan step [dir]`: Step through plan execution interactively.
-   `flow plan set <dir>` / `current` / `unset`: Manage the active plan for the current context.

### `flow chat`

The `chat` subcommand manages conversational workflows, perfect for ideation and refinement before creating a formal plan.

-   `flow chat -s <file.md>`: Turn a markdown file into a runnable chat job.
-   `flow chat run [title]`: Run the next LLM turn for one or more pending chats.
-   `flow chat list`: List all chat jobs in your configured `chat_directory`.

## Configuration

`grove-flow` is configured via your project's `grove.yml` file under the `flow` key.

```yaml
# .grove/config.yml or grove.yml
flow:
  # Directory where plans are stored. Supports variables like ${REPO}.
  plans_directory: ./plans
  
  # Directory for chat-based jobs.
  chat_directory: ./chats

  # Default container for agent jobs.
  target_agent_container: grove-agent-ide
  
  # Default model for oneshot jobs.
  oneshot_model: gemini-2.5-pro
```

Plan-specific defaults (like `model` or `worktree`) can be set in a `.grove-plan.yml` file inside a plan directory.
