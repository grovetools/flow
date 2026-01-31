<p align="center">
  <img src="https://grovetools.ai/docs/flow/images/flow-logo-with-text-dark.svg" alt="Grove Flow">
</p>

<!-- DOCGEN:OVERVIEW:START -->

`flow` is a command-line tool that orchestrates LLM-assisted development workflows. It manages the lifecycle of "plans"—directories of Markdown files defining distinct jobs—and executes them within isolated Git worktrees.

## Core Mechanisms

**Plan Structure**: A plan is a directory containing numbered Markdown files (e.g., `01-spec.md`, `02-impl.md`). Each file represents a "job" and contains YAML frontmatter defining properties like `type`, `model`, and `depends_on`.

**Orchestration**: The tool reads the plan directory, builds an in-memory dependency graph, and executes runnable jobs.
*   **Briefing**: Before execution, it assembles an XML prompt containing system instructions, dependency outputs, and repository context.
*   **Audit Trail**: Outputs (LLM responses, agent transcripts) are appended directly to the source Markdown file, preserving the history in version control.

**Worktree Isolation**: Jobs can specify a `worktree`. `flow` creates these in `.grove-worktrees/` at the project root, allowing agents to modify code without affecting the main working directory.

## Features

### Job Types
*   **`chat`**: Interactive, multi-turn conversations stored in a single file.
*   **`oneshot`**: Single-turn requests that complete immediately.
*   **`interactive_agent`**: Launches an agent (Claude Code, OpenCode, Codex) in a dedicated `tmux` window.
*   **`headless_agent`**: Non-interactive agent execution that completes after going idle.
*   **`shell`**: Executes system shell commands.

### Dependency Management
Jobs declare dependencies via the `depends_on` field. The orchestrator ensures jobs run in topological order and passes context from dependencies to dependent jobs.

### Terminal Interfaces (TUI)
*   **`flow status`**: Displays the plan's dependency tree and streams live logs from running agents.
*   **`flow plan tui`**: A browser for managing multiple plans and merging worktrees.
*   **`flow plan init`**: An interactive wizard for creating plans.

### Templates & Recipes
*   **Templates**: Reusable Markdown files defining job structures and prompts.
*   **Recipes**: Predefined collections of jobs that scaffold entire plans (e.g., "Feature Implementation").

## Integrations

*   **`hooks`**: Registers running agent sessions to track process liveness and status.
*   **`cx`**: Used to generate repository context files based on `.grove/rules`.
*   **`nav`**: Integrates with Grove Navigation for `tmux` session management.
*   **LLM Providers**: Supports Anthropic and Gemini models via `grove-anthropic` and `grove-gemini`. Uses native file upload APIs for handling large context.

<!-- DOCGEN:OVERVIEW:END -->

<!-- DOCGEN:TOC:START -->

See the [documentation](docs/) for detailed usage instructions:
- [Overview](docs/01-overview.md)
- [Quick Start](docs/02-quick-start.md)

<!-- DOCGEN:TOC:END -->
