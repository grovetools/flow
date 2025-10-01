# Grove Flow

<img src="./images/grove-flow-readme.svg" width="60%" />

Grove Flow is a command-line tool for orchestrating multi-step LLM-assisted tasks using Markdown files as definitions. It uses Git worktrees facilitate parallel development. The tool is intended for formalizing and automating development workflows that involve code generation or analysis by LLMs. `grove-flow` is designed for stateful, local development workflows that wrap tools like Anthropic's Claude Code and the Google's Gemini API discrete, dependent steps.

<!-- placeholder for animated gif -->

## Key Features

*   **Job Orchestration**: Defines a workflow as a sequence of jobs in Markdown files with dependencies specified in YAML frontmatter. The tool manages the execution order, running jobs only after their prerequisites are met. It supports executing shell commands and running LLM-driven code generation tasks.

*   **Plan Management**: A "Plan" is a directory of Markdown files that represents a task. The `flow plan` command includes subcommands to `init`, `add`, `run`, `status`, `graph`, and `finish` these plans. A terminal interface (`flow plan status -t`) is available for managing and monitoring plan progress.

*   **Chat Integration**: The `flow chat` command manages multi-turn conversations with an LLM, stored as a single Markdown file. The `flow plan extract` command can then be used to convert sections of the conversation into executable jobs within a plan.

*   **Recipes and Templates**: The `flow plan init --recipe` command scaffolds a new plan from a predefined directory structure of job files. The `flow plan add --template` command creates a new job from a predefined Markdown file.

## How It Works

A "Plan" is a directory containing numbered Markdown files, where each file represents a "Job". Each job file contains YAML frontmatter that defines its `type` (`agent`, `oneshot`, `shell`), `status` (`pending`, `completed`), and dependencies via a `depends_on` key.

The orchestrator reads all job files in a plan directory, builds a dependency graph, and executes jobs whose dependencies have a `completed` status. Jobs of type `agent` or `interactive_agent` are executed within a Git worktree specified in the job's frontmatter, providing filesystem isolation.

## Ecosystem Integration

Grove Flow functions as a component of the Grove tool suite and executes other tools in the ecosystem as subprocesses.

*   **Grove Context (`cx`)**: Before executing a job, `grove-flow` calls `grove-context` to read `.grove/rules` files and generate a file-based context. This context is then provided to the LLM.

*   **Grove Hooks (`grove-hooks`)**: `grove-flow` emits events for job lifecycle stages (e.g., job start, completion, or failure). These events can be tracked by `grove-hooks` for monitoring and logging purposes.

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
