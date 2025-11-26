# Grove Flow

Grove Flow is a command-line tool for executing multi-step development workflows defined in Markdown files. It features interactive terminal interfaces (TUIs) for plan management and execution, and integrates with the Grove ecosystem for a complete development workflow.

<!-- placeholder for animated gif -->

## Key Features

*   **Job Orchestration**: Execute workflows as dependency graphs of jobs defined in Markdown files. Jobs can be chat conversations, oneshot LLM calls, interactive coding agents, or shell commands. Dependencies are managed automatically.

*   **Interactive Interfaces**: Terminal UIs provide visual feedback and keyboard-driven workflows for browsing plans, managing jobs, and monitoring execution across your entire workspace.

*   **Git Worktree Integration**: Plans can create isolated git worktrees, providing complete filesystem isolation for development work. Each plan can have a dedicated tmux session in its own worktree.

*   **Plan Lifecycle Management**: Track plans from creation through review to completion, with automatic git status tracking, merge state visualization, and guided cleanup workflows.

*   **Chat-Driven Development**: Start with conversational exploration using chat jobs, then extract structured implementation plans using keyboard shortcuts.

*   **Notebook Integration**: Optionally integrates with grove-notebook for idea management. Plans can be created by promoting notes, maintaining bidirectional links between ideas and implementations.

## How It Works

### The Complete Workflow

1. **Initialize a Plan**: Create a plan directory and define your initial approach
2. **Explore with Chat**: Discuss the problem with an LLM using chat jobs to get a detailed implementation plan
3. **Structure Work**: Extract chat content into structured jobs with dependencies
4. **Execute**: Run jobs in dependency order, with interactive agents launching in isolated environments
5. **Monitor**: Track running work across plans using interactive interfaces
6. **Review and Finish**: Review work, merge changes, and clean up resources with guided workflows

### Technical Architecture

A "Plan" is a directory containing Markdown files, where each file is a "Job" with YAML frontmatter defining its type, status, and dependencies. The orchestrator builds a dependency graph and executes jobs when their dependencies are complete. Interactive agent jobs launch in isolated git worktrees within tmux sessions for a clean development environment.

## Ecosystem Integration

Grove Flow integrates deeply with the Grove ecosystem:

*   **Grove Notebook**: Optional integration for idea management. Notes can be promoted to plans with automatic linking, maintaining traceability from idea to implementation.

*   **Grove Context**: Automatically generates file context based on rules files in worktrees. This context is provided to LLMs for all job types, ensuring the AI has relevant codebase information.

*   **Grove Hooks**: Manages all interactive agent sessions across the ecosystem, showing a unified view of all running jobs. Also provides lifecycle management, triggering actions on plan events like review or completion.

*   **Agent Tools**: For interactive agent jobs, grove-flow launches agent CLIs in isolated tmux windows with worktree context. These agents have full access to the codebase and can make changes interactively.

*   **Grove Meta**: The meta-tool manages binary installation and provides ecosystem-wide commands for all Grove tools.

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