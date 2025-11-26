# Grove Flow

Grove Flow is a command-line tool that executes multi-step workflows defined in Markdown files. It is intended for formalizing development workflows that involve code generation or analysis by LLMs, using Git worktrees for filesystem isolation.

<!-- placeholder for animated gif -->

## Key Features

*   **Job Orchestration**: Executes a sequence of jobs defined in Markdown files. The execution order is determined by dependencies specified in each file's YAML frontmatter. It supports `shell`, `oneshot`, and `agent` job types.

*   **Plan Management**: A "Plan" is a directory of Markdown job files representing a task. The `flow plan` command provides subcommands to `init`, `add`, `run`, `status`, `graph`, and `finish` these plans. A terminal interface (`flow plan status -t`) is available for monitoring plan progress.

*   **Chat Integration**: The `flow chat` command manages conversational logs with an LLM, stored as a single Markdown file. The `flow plan extract` command can then be used to convert sections of the conversation into executable jobs within a plan.

*   **Recipes and Templates**: The `flow plan init --recipe` command creates a new plan from a predefined directory structure of job files. The `flow plan add --template` command creates a new job from a single template file.

## How It Works

A "Plan" is a directory containing numbered Markdown files, where each file represents a "Job". Each job file contains YAML frontmatter that defines its `type` (`agent`, `oneshot`, `shell`), `status` (`pending`, `completed`), and dependencies via a `depends_on` key.

The orchestrator reads all job files in a plan directory, builds a dependency graph, and executes jobs whose dependencies have a `completed` status. Jobs of type `agent` or `interactive_agent` are executed within a Git worktree specified in the job's frontmatter, providing filesystem isolation.

## Ecosystem Integration

Grove Flow functions as a component of the Grove tool suite and executes other tools in the ecosystem as subprocesses.

*   **Grove Context (`cx`)**: Before executing a job, `grove-flow` can call `grove-context` to read `.grove/rules` files and generate a file-based context. This context is then provided to the LLM.

*   **Agent Tools (`claude`, `codex`)**: For `agent` or `interactive_agent` jobs, `flow` launches interactive agent tools as subprocesses within a dedicated tmux session, inheriting the specified worktree and context.

*   **Grove Hooks (`grove-hooks`)**: The tool is designed to emit events for job lifecycle stages (e.g., job start, completion). These events can be tracked by `grove-hooks` for monitoring and logging purposes.

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