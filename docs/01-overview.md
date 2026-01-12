Grove Flow is a command-line tool for managing local, Markdown-based development workflows. All state is stored in plain text files: plans are directories of `.md` files, and job status is defined by YAML frontmatter. The tool is designed for terminal-native use, with text-based interfaces for management and `tmux` for execution environments. It does not use a database or a cloud service, and all workflow definitions are git-diffable.

## Key Features

*   **Job Types**: Defines several job types, each with a specific execution model:
    *   `chat`: An interactive, multi-turn conversation with an LLM, stored in a single Markdown file.
    *   `oneshot`: A single-turn request to an LLM for tasks like code generation or summarization.
    *   `interactive_agent`: An interactive coding agent session that runs in a dedicated `tmux` window.
    *   `headless_agent`: An interactive coding agent that carries out a job, then exits.
    *   `shell`: A command executed in the system's default shell.
    *   `file`: A non-executable job used to store reference content.

*   **Worktree Isolation**: Creates git worktrees in a `.grove-worktrees/` directory at the repository root. This provides filesystem isolation for parallel development tasks associated with different plans.

*   **Dependency Orchestration**: Jobs declare dependencies on other jobs via the `depends_on` field in their frontmatter. The orchestrator reads these declarations to build a dependency graph and execute jobs in the correct order.

*   **Lifecycle Management**: Provides commands to manage a plan's lifecycle from creation (`flow init`), through review (`flow review`), to cleanup (`flow finish`). The `finish` command guides the user through merging work and safely removing the associated worktree and git branches.

## How It Works

A "plan" is a directory containing numbered Markdown files (e.g., `01-setup.md`, `02-implement.md`). Each file represents a "job" and contains YAML frontmatter that defines its properties, including `type`, `status`, and `depends_on`.

When a command like `flow run` is executed, an orchestrator reads all job files in the plan directory, builds an in-memory dependency graph, and identifies jobs whose dependencies are met. It then executes these runnable jobs using the appropriate executor for their type. Agent jobs can be launched as subprocesses within a `tmux` session, scoped to the plan's associated git worktree.

## Ecosystem Integration

Grove Flow executes other command-line tools as part of its operation:

*   **`nb`**: Plan initialization hooks can execute `nb` commands to move notes between directories, linking a note to a plan.
*   **`grove cx`**: Before executing `oneshot` or `shell` jobs, `grove cx generate` is run to create a context file based on `.grove/rules`.
*   **`grove-hooks`**: Running agent sessions are registered with the `grove-hooks` session registry, which tracks PID and other metadata in `~/.grove/hooks/sessions/`.
*   **Agent CLIs**: Interactive agent jobs launch `claude`, `codex`, or `opencode` as subprocesses within a `tmux` window.

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
