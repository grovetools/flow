# Core Concepts

Understanding Grove Flow's architecture helps you design effective workflows and leverage the system's capabilities. This section covers the fundamental concepts that form the foundation of Grove Flow's orchestration model.

## 📋 Plan

A Plan is a directory containing a sequence of dependent Jobs that define a complete workflow. Plans serve as living documentation of complex tasks, providing a structured approach to managing multi-step development processes.

Plans are defined in dedicated directories within your configured plans directory. Each plan directory contains multiple job files (Markdown format with YAML frontmatter) and optionally includes a `.grove-plan.yml` configuration file for plan-specific settings. Plans represent single, cohesive goals that can be broken down into manageable, executable steps.

Plans encapsulate everything needed to execute a complex workflow: job definitions, dependency relationships, configuration overrides, and execution context. They provide traceability and repeatability for development processes that might otherwise be difficult to reproduce.

## 🔧 Job

A Job is a single executable unit of work within a Plan, defined as a Markdown file with YAML frontmatter. Jobs are the atomic building blocks of Grove Flow workflows, each representing a discrete task that can be executed independently or as part of a larger dependency chain.

Every job has a type that determines how it executes, a status tracking its progress, dependencies linking it to prerequisite jobs, and a prompt or command defining the work to be performed. Jobs can run in isolated environments (such as Git worktrees for agent jobs) or in the main repository context depending on their type and configuration.

Jobs maintain their execution history, outputs, and metadata, providing a complete audit trail of work performed. They can reference other jobs, include external files, and be parameterized through templates and variables.

## ⚙️ Executor

Executors are the engines that interpret job definitions and execute the corresponding tasks. Grove Flow includes specialized executors for different job types, each optimized for specific kinds of work and execution environments.

The executor system provides a consistent interface for running diverse tasks while handling the complexities of different execution contexts. Executors manage resource allocation, environment setup, tool access, and result capture, ensuring that jobs run reliably and produce consistent outputs.

Different executors handle different aspects of the development workflow: LLM interactions, shell command execution, file manipulation, and interactive sessions. The executor architecture allows Grove Flow to seamlessly coordinate between these different types of work within a single workflow.

## 🔒 Worktree Isolation

Worktree isolation provides safe, isolated environments for code generation and experimentation using Git's worktree feature. Agent jobs automatically create dedicated Git worktrees, keeping your main branch clean while enabling parallel development and experimentation.

This isolation ensures that experimental code changes, failed attempts, or work-in-progress modifications don't affect your main development branch. Changes remain isolated until you explicitly choose to merge them back, providing a safety net for AI-generated code and exploratory development work.

Worktree isolation supports parallel workflows, allowing multiple plans or jobs to work on different features simultaneously without conflicts. Each worktree maintains its own working directory, index, and HEAD, while sharing the same Git history and object database.

## 🎯 Job Types

Grove Flow supports several job types, each designed for specific development tasks and execution patterns:

**`agent`**: Autonomous development tasks that require full tool access and multi-step reasoning. Agent jobs run in isolated worktrees and can perform complex workflows including file editing, command execution, and iterative problem-solving.

**`interactive_agent`**: Human-in-the-loop development sessions that combine AI capabilities with human oversight. These jobs launch in tmux sessions, allowing real-time interaction and collaboration between developers and AI agents.

**`headless_agent`**: Fully automated agent execution without human interaction, suitable for CI/CD integration and background automation tasks.

**`oneshot`**: Single-request LLM invocations for analysis, planning, or content generation. These jobs are fast and efficient, ideal for generating specifications, documentation, or simple analysis tasks.

**`shell`**: Direct shell command execution for builds, tests, deployments, and system operations. Shell jobs provide integration with existing tooling and automation scripts.

**`chat`**: Multi-turn conversational workflows for exploration, requirements gathering, and iterative problem-solving. Chat jobs support back-and-forth interaction and can be extracted into structured plans.

## 🔗 Relationships Between Concepts

The Grove Flow architecture creates a hierarchical relationship between these concepts:

- **Plans** compose multiple **Jobs** into structured workflows
- **Jobs** are executed by type-specific **Executors**  
- **Executors** coordinate with **Worktree Isolation** for safe code generation
- **Job Types** determine execution behavior and tool access
- **Worktree Isolation** enables parallel, safe development workflows

This architecture enables Grove Flow to handle everything from simple automation tasks to complex, multi-phase development projects while maintaining safety, traceability, and reproducibility throughout the process.