<!-- DOCGEN:OVERVIEW:START -->

Grove Flow is a command-line tool that executes multi-step workflows defined in Markdown files. It uses Git worktrees to facilitate parallel development and is intended for formalizing development workflows that involve code generation or analysis by LLMs.

<!-- placeholder for animated gif -->

## Key Features

*   **Job Orchestration**: Executes a sequence of jobs defined in Markdown files. The execution order is determined by dependencies specified in each file's YAML frontmatter. It supports running shell commands and LLM-driven tasks.

*   **Plan Management**: A "Plan" is a directory of Markdown files that represents a task. The `flow plan` command includes subcommands to `init`, `add`, `run`, `status`, `graph`, and `finish` these plans. A terminal interface (`flow plan status -t`) is available for monitoring plan progress.

*   **Chat Integration**: The `flow chat` command manages conversational logs with an LLM, stored as a single Markdown file. The `flow plan extract` command can then be used to convert sections of the conversation into executable jobs within a plan.

*   **Recipes and Templates**: The `flow plan init --recipe` command creates a new plan from a predefined directory structure of job files. The `flow plan add --template` command creates a new job from a predefined Markdown file.

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

<!-- DOCGEN:OVERVIEW:END -->

<!-- DOCGEN:TOC:START -->

See the [documentation](docs/) for detailed usage instructions:
- [Overview](docs/01-overview.md)
- [Examples](docs/02-examples.md)
- [Managing Plans](docs/03-managing-plans.md)
- [Working with Jobs](docs/04-working-with-jobs.md)
- [Chats](docs/05-chats.md)
- [Recipes and Templates](docs/06-recipes-and-templates.md)
- [Configuration](docs/07-configuration.md)
- [Command Reference](docs/08-command-reference.md)

<!-- DOCGEN:TOC:END -->
