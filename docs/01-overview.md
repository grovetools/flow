Grove Flow is a command-line tool for managing local, Markdown-based development workflows. State is stored in version-controlled plain text files: plans are directories of `.md` jobs, and status is defined by YAML frontmatter. It is designed for terminal-native use with `tmux` for execution environments and does not use a database or a cloud service.

## Key Features

*   **Job Types**: Defines several job types, each with a specific execution model:
    *   `chat`: An interactive, multi-turn conversation with an LLM, stored in a single Markdown file.
    *   `oneshot`: A single-turn request to an LLM for tasks like code generation or summarization.
    *   `interactive_agent`: An interactive coding agent session that runs in a dedicated `tmux` window.
    *   `headless_agent`: A non-interactive agent that carries out a job and then exits.
    *   `shell`: A command executed in the system's default shell.
    *   `file`: A non-executable job used to store reference content.
*   **Worktree Isolation**: Creates git worktrees in a `.grove-worktrees/` directory at the repository root. This provides filesystem isolation for development tasks. It can also create multi-repo worktrees to enable isolated changes across interdependent repositories.
*   **Dependency Orchestration**: Jobs declare dependencies on other jobs via the `depends_on` field in their frontmatter. The orchestrator reads these declarations to build a dependency graph and execute jobs in the correct order, enabling controlled context sharing.
*   **Auditable History**: Captures a traceable history of development activities. Context definitions, specifications, LLM outputs, and agent transcripts (from Claude Code, Codex, and OpenCode) can be preserved in version-controlled Markdown files or viewed in tools like Obsidian.
*   **Lifecycle Management**: Provides commands to manage a plan's lifecycle from creation (`flow init`), through review (`flow review`), to cleanup (`flow finish`).

## How It Works

A "plan" is a directory containing numbered Markdown files (e.g., `01-setup.md`, `02-implement.md`). Each file represents a "job" and contains YAML frontmatter that defines its properties, including `type`, `status`, and `depends_on`.

When a command like `flow run` is executed, an orchestrator reads all job files in the plan directory, builds an in-memory dependency graph, and identifies runnable jobs. It then executes these jobs using the appropriate executor for their type. Outputs, such as LLM responses and agent transcripts, are appended back to the corresponding `.md` job file, creating a persistent record.

## Ecosystem Integration

Grove Flow executes other command-line tools and uses library code from other parts of the grove ecosystem as part of its operation:

*   **`grove-notebook` (`nb`)**: Plan initialization hooks can execute `nb` commands to create plans from notes. The notebook system is used for context engineering.
*   **`grove-context` (`cx`)**: Before executing `oneshot` or `chat` jobs, this tool creates a context file based on `.grove/rules`, enabling quick chatting about large chunks of a codebase or multiple codebases for planning (requires API keys from Gemini or Anthropic).
*   **`grove-hooks`**: Running agent sessions are registered with the `grove-hooks` session registry, which independently tracks PID and agent status (e.g., `idle`, `running`).
*   **`grove-tmux`**: Worktrees created by `flow` can be navigated using `grove-tmux`, which can dynamically bind workspaces to `tmux` hotkeys. Agents within a worktree are launched in new, named windows within a `tmux` session for management. When jobs complete, transcripts are finalized and appened to the markdown file. Agent associated with paritcular jobs can be revised in tmux windows if needed.
*   **Agent CLIs**: Interactive agent jobs launch `claude`, `codex`, or `opencode` as subprocesses.

## Advanced Usage & Automation

As a command-line tool, `flow` can be executed by other processes, including agents. An agent, guided by a "skills", can use `flow` to construct and execute its own development pipelines. This enables workflows such as using multi-step `oneshot` jobs with large repository context for planning, potentially with an ensemble of different models, before carrying out the implementation.

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
