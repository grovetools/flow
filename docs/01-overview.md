# Grove Flow

<img src="./images/grove-flow-readme.svg" width="60%" />

Grove Flow is a command-line interface designed for orchestrating multi-step tasks that leverage Large Language Models (LLMs). It provides a structured way to define, execute, and manage complex development workflows—referred to as "Plans"—using a series of Markdown files. By integrating with Git worktrees, it ensures that each task runs in an isolated environment, keeping the main development branch clean and stable.

Whether you are scaffolding a new feature, automating a series of code modifications, or building an emergent plan with AI assistance, Grove Flow offers the tooling to formalize and automate your development process.

<!-- placeholder for animated gif -->

## Key Features

Grove Flow is built around a set of core concepts that facilitate structured, reproducible, and automated workflows.

*   **Job Orchestration**: Define complex workflows as a sequence of jobs with dependencies. Grove Flow manages the execution order, ensuring that each step runs only after its prerequisites are met. This allows for the creation of sophisticated, multi-stage tasks that can combine AI-driven code generation, shell command execution, and manual review steps.

*   **Plan Management**: Plans are living documents that capture the entire lifecycle of a complex task. They are organized in directories and defined by Markdown files, making them easy to read, version control, and share. The `flow plan` command provides a comprehensive suite of tools to `init`, `add`, `run`, `status`, `graph`, and `finish` your plans. An interactive terminal UI (`flow plan status -t`) offers a visual way to manage and monitor plan progress.

*   **Chat Integration**: Start with an idea in a conversational format and seamlessly transition to a structured plan. The `flow chat` command allows you to manage multi-turn AI interactions as Markdown files. As a conversation evolves, you can use the `flow plan extract` command to convert specific LLM responses into formal, executable jobs within a plan, bridging the gap between exploration and implementation.

*   **Recipes and Templates**: Accelerate common workflows using reusable components. Job templates provide pre-defined structures for frequent tasks, while plan recipes offer complete scaffolds for entire projects, such as implementing a new feature or generating documentation. You can use built-in recipes or create your own to standardize processes across your team.

## Ecosystem Integration

Grove Flow is a component of the larger Grove ecosystem and is designed to work in concert with other specialized tools to provide a cohesive development environment.

*   **Grove Meta-CLI (`grove`)**: The central tool for managing the entire ecosystem. `grove` handles the installation, updating, and version management of all Grove binaries, including `grove-flow`. It ensures that the correct versions of all tools are available in your `PATH`.

*   **Grove Context (`cx`)**: Manages the context provided to LLMs. `grove-flow` uses `grove-context` to automatically gather relevant source code and documentation based on predefined rules (`.grove/rules`). This ensures that AI agents have the necessary information to perform their tasks accurately, without requiring manual context gathering for each job.

*   **Grove Hooks (`grove-hooks`)**: Provides a system for tracking and responding to events within the ecosystem. `grove-flow` integrates with `grove-hooks` to emit events for job lifecycle stages, such as when a job starts, completes, or fails. This enables external systems, dashboards, or notification services to monitor the progress of orchestration plans.

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
