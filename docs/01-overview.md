# Overview

`grove-flow` is a command-line tool for orchestrating multi-step development workflows using Large Language Models (LLMs). It formalizes complex processes into structured **Plans** composed of individual **Jobs**, all defined in plain Markdown files. This tool is a core component of the experimental Grove ecosystem, designed to make it easier to direct coding agents.

The central assumption is that agents like Claude Code are highly effective at executing well-defined, scoped tasks but can "go off the rails" during long, open-ended sessions. `grove-flow` mitigates this by transforming development into a more measured process: a detailed plan is created (often with a planning-focused LLM like Gemini), and then an execution-focused agent is tasked with completing discrete jobs from that plan. This "Plan -> Agent -> Review" cycle creates a traceable, repeatable, and more predictable workflow, all managed through editor-independent, version-controlled text files.

## Core Concept: Plans and Jobs

Grove Flow organizes work into **Plans** - structured workflows composed of individual **Jobs**. Each job represents a discrete task that can be executed by different types of executors:

- **Agent Jobs**: Complex, autonomous tasks using LLM agents with full development tool access
- **Oneshot Jobs**: Simple, single-response tasks for analysis or generation
- **Chat Jobs**: Interactive, conversational workflows for exploration and refinement
- **Shell Jobs**: Direct command execution for builds, tests, and deployments

Plans define the relationships and dependencies between these jobs, allowing Grove Flow to execute them in the correct order while maximizing parallelism where possible.

## Key Features

### Structured Workflow Definition
Define complex development processes as declarative plans with clear job dependencies and execution order.

### LLM Integration
Leverage multiple LLM providers (Anthropic Claude, OpenAI GPT, Google Gemini) with automatic model selection and prompt optimization.

### Git Worktree Isolation
Execute agent jobs in isolated Git worktrees, keeping your main branch clean while enabling safe experimentation and parallel development.

### Conversational Development
Start with free-form chat sessions to explore problems, then extract structured plans for implementation.

### Template and Recipe System
Standardize common workflows using built-in templates and recipes, or create custom ones for your team.

### Development Environment Integration
Seamless integration with tmux for persistent development sessions and your existing Git workflow.

## Who Should Use Grove Flow

### Individual Developers
- **Solo developers** who want to automate repetitive development tasks
- **Open source maintainers** managing multiple projects and contributions
- **Consultants** who need consistent workflows across different client projects

### Development Teams
- **Startup teams** looking to standardize development processes without heavy overhead
- **Remote teams** needing structured async collaboration workflows
- **Platform teams** building internal development tooling and workflows

### Use Cases

**Feature Development**: Structure the entire lifecycle from specification through implementation, testing, and review.

**Code Reviews**: Automate comprehensive code quality assessments with consistent criteria.

**System Migrations**: Plan and execute complex migrations with clear dependencies and checkpoints.

**Documentation Generation**: Automatically generate and maintain technical documentation from code and specifications.

**Technical Debt Management**: Systematically assess and address technical debt with structured workflows.

## Getting Started

Grove Flow is designed to be approachable for developers already familiar with Git, command-line tools, and markdown. The basic workflow is:

1. **Initialize a Plan**: Create a new workflow directory
2. **Add Jobs**: Define individual tasks with their dependencies
3. **Execute**: Run jobs automatically in the correct order
4. **Iterate**: Refine and extend your workflows over time

The tool integrates naturally with existing development practices while adding powerful automation and orchestration capabilities.

## Design Philosophy

Grove Flow is built on several core principles:

**Code as Documentation**: Plans and jobs are defined in markdown files that serve as both executable workflows and human-readable documentation.

**Git-Native**: Full integration with Git workflows, worktrees, and branching strategies.

**LLM-Augmented**: Leverage AI capabilities while maintaining developer control over the process and outputs.

**Incremental Adoption**: Start small with simple workflows and gradually build more complex automation.

**Team Collaboration**: Share, version, and collaborate on workflows as easily as code.

Grove Flow bridges the gap between ad-hoc manual processes and heavy workflow automation, providing the structure and automation benefits without sacrificing the flexibility and control that developers need.
