Grove Flow is a command-line tool for managing local, Markdown-based development workflows. It is designed for developers who use terminal-native tools, combining `tmux` for process isolation, `git` for observing changes, a text editor for authoring plans, and `flow` for orchestration. This approach provides a structure for LLM-assisted development that moves beyond a single chat window, enabling deliberate planning and context management. The goal is to create a sustainable practice where specifications, context, and outcomes are captured in version-controlled files, forming a git-diffable audit trail that does not rely on a database or a cloud service.

## Key Features

*   **Job Types**: Defines several job types, each with a specific execution model:
    *   `chat`: An interactive, multi-turn conversation with an LLM, stored in a single Markdown file.
    *   `oneshot`: A single-turn request to an LLM for tasks like code generation or summarization.
    *   `interactive_agent`: An agent session that runs in a dedicated `tmux` window for user interaction.
    *   `headless_agent`: A non-interactive agent that carries out a job and then exits.
    *   `shell`: A command executed in the system's default shell.
    *   `file`: A non-executable job used to store reference content.
*   **Worktree Isolation**: Creates git worktrees in a `.grove-worktrees/` directory at the repository root for filesystem isolation. It can also create multi-repo worktrees to enable isolated changes across interdependent repositories.
*   **Dependency Orchestration**: Jobs declare dependencies on other jobs via the `depends_on` field in their frontmatter. The orchestrator reads these declarations to build a dependency graph and execute jobs in the correct order, enabling controlled context sharing between steps.
*   **Lifecycle Management**: Provides commands to manage a plan's lifecycle from creation (`flow init`), through review (`flow review`), to cleanup (`flow finish`).

## How It Works

A "plan" is a directory containing numbered Markdown files (e.g., `01-setup.md`, `02-implement.md`). Each file represents a "job" and contains YAML frontmatter that defines its properties, including `type`, `status`, and `depends_on`. Before execution, `flow` assembles a structured XML prompt, known as a **briefing file**, which includes system instructions, dependency outputs, and context files. This briefing is then passed to the appropriate executor.

When a command like `flow run` is executed, an orchestrator reads all job files in the plan directory, builds an in-memory dependency graph, and identifies runnable jobs. It then executes these jobs using the appropriate executor for their type. This process creates a version-controllable audit trail for a plan's execution:
*   Specifications and context definitions are captured in plain text.
*   Outputs, such as LLM responses and agent transcripts (from Claude Code, Codex, and OpenCode), are appended back to the corresponding `.md` job file. This creates a persistent, reviewable record of development history.

## Templates and Recipes

*   **Job Templates**: Reusable job definitions stored in `.md` files that can be applied when creating new jobs. They provide a way to standardize frontmatter and prompt instructions for common tasks. Templates are discoverable from project, user, and notebook directories.
*   **Plan Recipes**: Scaffolding for entire plans. A recipe is a directory containing a set of job templates and a `recipe.yml` file defining metadata. The `flow plan init --recipe <name>` command uses a recipe to generate a new plan with a pre-defined structure and jobs.

## Terminal & Editor Integration

*   **Terminal User Interfaces (TUIs)**: `flow` includes several TUIs for managing plans:
    *   `flow status`: The primary interface for monitoring a plan. It displays a dependency tree of all jobs, allows inspection of job files (markdown content, frontmatter), and streams live logs from running agents.
    *   `flow plan init --tui`: An interactive form for creating a new plan, including options for worktrees and recipes.
    *   `flow plan tui`: A TUI for browsing plans, merging worktrees, and managing plan lifecycle.
*   **Starship**: Includes a prompt module (`flow starship`) that displays the active `flow` plan and a summary of job statuses (e.g., `my-plan [✓3 ●1 ○2]`) in the shell prompt.
*   **Neovim**: Chat jobs can be executed from within Neovim, facilitating interactive sessions with large context models.

## LLM Provider & Context Support

Grove Flow integrates with `grove-anthropic` and `grove-gemini` to support models from both providers.

*   **File Uploads**: Instead of inlining large amounts of context into the prompt, the tool uses provider-native file upload APIs (Anthropic's Beta Files API and Google's Files API). This allows jobs to use large context windows by attaching files such as repository context (`.grove/context`), the outputs of dependency jobs, and other curated contexts.
*   **Model Configuration**: The `model` can be configured at multiple levels with the following precedence: job frontmatter, plan configuration (`.grove-plan.yml`), and global user settings (`grove.yml`).

## Ecosystem Integration

Grove Flow executes other command-line tools and uses library code from other parts of the Grove ecosystem as part of its operation:

*   **`grove-notebook` (`nb`)**: Plan initialization hooks can execute `nb` commands to create plans from notes, treating the notebook as a separate artifact store independent of the main project repository. `flow` can also be configured to automatically preserve markdown files generated by Claude Code's "Plan Mode" into an executable job graph.
*   **`grove-context` (`cx`)**: Before executing `oneshot` or `chat` jobs, `grove cx generate` is used to create a context file based on `.grove/rules`.
*   **`grove-hooks`**: Running agent sessions are registered with the `grove-hooks` session registry (`~/.grove/hooks/sessions/`), which independently tracks process IDs and agent status (`idle`, `running`). The `grove hooks sessions browse` TUI provides a unified view of all sessions, running jobs, and pending chat jobs.
*   **`grove-tmux` (`gmux`)**: This tool provides convenience features for `tmux` environments.
    *   `gmux sz`: An interactive project picker for creating or switching to tmux sessions for projects and worktrees.
    *   `gmux key manage`: A TUI for binding repositories and worktrees to single-key hotkeys, inspired by Harpoon. It shows project details like git status, plan stats, and context status.
    *   `gmux history`: A TUI listing recently accessed sessions for quick navigation.
    *   `gmux windows`: A TUI for managing multiple agent windows within a plan's session.
*   **Agent CLIs**: Interactive agent jobs launch `claude`, `codex`, or `opencode` as subprocesses in named `tmux` windows. Transcripts are standardized and streamed via the `grove-agent-logs` (`aglogs`) package.

## Advanced Usage & Automation

As a command-line tool, `flow` can be executed by other processes, including agents. An agent, guided by a "skill," can use `flow` to construct and execute its own development pipelines. This enables workflows such as using multi-step `oneshot` jobs with repository context for planning before carrying out an implementation.

Using Grove's "ecosystem" model, `flow` can create worktrees that span multiple repositories, allowing agents to perform coordinated and isolated changes across a set of interdependent projects.

## Installation

Install via the Grove meta-CLI:
```bash
grove install flow
```

Verify installation:
```bash
flow version
```

This requires the `grove` meta-CLI. See the Grove installation guide if it is not installed.
