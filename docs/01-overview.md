# Grove Flow

Grove Flow is a command-line tool for executing multi-step development workflows, deeply integrated with `grove-notebook` for idea management and featuring interactive terminal interfaces (TUIs) for plan management and execution.

<!-- placeholder for animated gif -->

## Key Features

*   **Notebook-Integrated Workflow**: Plans are created by promoting notes from `grove-notebook`, maintaining bidirectional links between ideas and implementations. Press `P` in the notebook TUI to transform any note into an executable plan with a dedicated worktree.

*   **TUI-First Interface**: Interactive terminal interfaces for every stage of development:
    - `nb tui` - Browse and promote notes to plans
    - `flow plan tui` - Overview of all plans with git status and lifecycle management
    - `flow plan status -t` - Detailed job management with keyboard-driven workflow
    - `hooks b` - Monitor all running agent sessions across your ecosystem

*   **Job Orchestration**: Execute workflows as dependency graphs of jobs defined in Markdown files. Jobs can be chat conversations, oneshot LLM calls, interactive coding agents, or shell commands. Dependencies are managed automatically through the TUI.

*   **Git Worktree Integration**: Plans can create isolated git worktrees in `.grove-worktrees/`, providing complete filesystem isolation for development work. The `flow plan open` command creates a dedicated `tmux` session for each plan's worktree.

*   **Plan Lifecycle Management**: Track plans from creation through review to completion, with automatic git status tracking, merge state visualization, and guided cleanup workflows.

*   **Chat-Driven Development**: Start with conversational exploration using chat jobs, then extract structured implementation plans using simple keyboard shortcuts (`x` for XML plan, `i` for interactive agent).

## How It Works

### The Complete Workflow

1. **Start in grove-notebook** (`nb tui`): Capture ideas, issues, and tasks
2. **Promote to Plan**: Press `P` to create a plan directory with an initial chat job and optional worktree
3. **Explore with Chat**: Discuss the problem with an LLM to get a detailed implementation plan
4. **Structure Work**: Use the plan status TUI to extract chat content into structured jobs with dependencies
5. **Execute**: Run jobs in dependency order, with interactive agents launching in dedicated tmux windows
6. **Monitor**: Track all running work across plans using the hooks session browser
7. **Review and Finish**: Use lifecycle commands to review work, merge changes, and clean up resources

### Technical Architecture

A "Plan" is a directory containing Markdown files, where each file is a "Job" with YAML frontmatter defining its type, status, and dependencies. The orchestrator builds a dependency graph and executes jobs when their dependencies are complete. Jobs of type `interactive_agent` launch in isolated git worktrees within tmux sessions managed by `grove-hooks`.

## Ecosystem Integration

Grove Flow integrates deeply with the Grove ecosystem:

*   **Grove Notebook (`nb`)**: The primary starting point for all work. Notes are promoted to plans with automatic linking, maintaining traceability from idea to implementation. The notebook TUI provides the `P` (promote) action that creates plans.

*   **Grove Context (`cx`)**: Automatically generates file context based on `.grove/rules` files in worktrees. This context is provided to LLMs for all job types, ensuring the AI has relevant codebase information.

*   **Grove Hooks (`hooks`)**: Manages all interactive agent sessions across the ecosystem. The `hooks b` command shows a unified view of all running jobs. Hooks also provide lifecycle management, triggering actions on plan events (e.g., creating PRs on review).

*   **Agent Tools** (`claude`, `gemini`, etc.): For `interactive_agent` jobs, `flow` launches agent CLIs in isolated tmux windows with worktree context. These agents have full access to the codebase and can make changes interactively.

*   **Grove Meta** (`grove`): The meta-tool manages binary installation and provides ecosystem-wide commands. Use `grove list` to see all active binaries.

## Installation

Install via the Grove meta-CLI:
```bash
grove install flow
```

Verify installation:
```bash
flow version
```

Requires the `grove` meta-CLI. See the [Grove Installation Guide](https://github.com/mattsolo1/grove-meta/blob/main/docs/02-installation.md) if you don't have it installed.