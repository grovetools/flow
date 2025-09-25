# Working with Jobs

Jobs are individual tasks within Grove Flow plans that can be executed independently or as part of complex workflows. This document covers everything you need to know about creating, configuring, and managing jobs effectively.

## Creating Jobs

### Interactive Job Creation

The default way to add jobs is through the interactive TUI:

```bash
# Launch interactive job creation
flow plan add

# Add job to specific plan
flow plan add my-plan

# Add to active plan (if set)
flow plan set my-plan
flow plan add
```

The interactive TUI guides you through:
1. **Job Type Selection**: Choose the appropriate job type for your task
2. **Title and Description**: Give your job a meaningful title
3. **Dependencies**: Select which jobs this one depends on
4. **Template Selection**: Choose from available job templates
5. **Prompt Configuration**: Write or import the job prompt

### Command-Line Job Creation

For scripting and automation, create jobs with flags:

```bash
# Basic job with inline prompt
flow plan add my-plan \
  --type agent \
  --title "Implement user authentication" \
  --depends-on "01-spec.md" \
  --prompt "Implement the user authentication feature based on the specification"

# Job with prompt from file
flow plan add my-plan \
  --type agent \
  --title "Implementation" \
  --depends-on "01-spec.md" \
  --prompt-file implementation-prompt.md

# Job with template
flow plan add my-plan \
  --type agent \
  --title "Code Review" \
  --template code-review \
  --depends-on "02-implement.md"
```

### Job Creation Options

Key flags for job creation:

- `--type`: Job type (agent, oneshot, chat, shell, etc.)
- `--title`: Human-readable job title
- `--depends-on`: List of dependency job filenames
- `--prompt`: Inline prompt text
- `--prompt-file`: File containing the job prompt
- `--template`: Job template to use
- `--output-type`: Output handling (file, commit, none)
- `--source-files`: Reference files for the job
- `--worktree`: Specify worktree for execution

## Job Types Explained

Grove Flow supports several job types, each optimized for different use cases:

### Agent Jobs (`agent` / `interactive_agent`)

**Purpose**: Complex, autonomous tasks that may require multiple steps and tool usage.

**Characteristics**:
- Full access to development tools (file editing, shell commands, etc.)
- Can perform multi-step workflows autonomously
- Interactive feedback and clarification requests
- Best for implementation, debugging, and complex analysis

**Example Use Cases**:
- Feature implementation
- Bug fixing and debugging
- Code refactoring
- Complex analysis tasks

**Example Job**:
```yaml
---
id: implement-api
title: "Implement REST API endpoints"
status: pending
type: interactive_agent
depends_on:
  - 01-spec.md
model: claude-4-sonnet
output:
  type: file
---

Implement the REST API endpoints defined in the specification. 
Create the necessary route handlers, middleware, and validation logic.
Follow RESTful conventions and include proper error handling.
```

### Headless Agent Jobs (`headless_agent`)

**Purpose**: Background automation tasks that run without user interaction.

**Characteristics**:
- Runs completely autonomously
- No interactive prompts or clarifications
- Suitable for CI/CD integration
- Fast execution for routine tasks

**Example Use Cases**:
- Automated testing
- Code formatting and linting
- Documentation generation
- Build and deployment tasks

### Oneshot Jobs (`oneshot`)

**Purpose**: Simple, single-response tasks from the LLM.

**Characteristics**:
- Single request-response cycle
- No tool usage or file modifications
- Fast execution and low resource usage
- Good for analysis, planning, and text generation

**Example Use Cases**:
- Writing specifications
- Creating documentation
- Code review and analysis
- Planning and brainstorming

**Example Job**:
```yaml
---
id: api-spec
title: "API Specification"
status: pending
type: oneshot
model: claude-4-sonnet
output:
  type: file
---

Create a detailed API specification for the user management system.
Include all endpoints, request/response formats, authentication requirements,
and error handling approaches.
```

### Shell Jobs (`shell`)

**Purpose**: Execute shell commands and scripts.

**Characteristics**:
- Direct command execution
- Environment variable support
- Exit code handling
- Output capture and logging

**Example Use Cases**:
- Running tests
- Build processes
- Deployment scripts
- System maintenance tasks

**Example Job**:
```yaml
---
id: run-tests
title: "Run Test Suite"
status: pending
type: shell
depends_on:
  - 02-implement.md
---

npm test -- --coverage --reporter=json
```

### Chat Jobs (`chat`)

**Purpose**: Multi-turn conversational workflows.

**Characteristics**:
- Back-and-forth conversation
- Persistent context across turns
- Manual progression control
- Exploratory and iterative

**Example Use Cases**:
- Requirements gathering
- Design discussions
- Problem exploration
- Code review conversations

**Example Job**:
```yaml
---
id: design-discussion
title: "Architecture Design Discussion"
status: pending_user
type: chat
model: claude-4-sonnet
---

Let's discuss the overall architecture for the user management system.
I'd like to explore different approaches and trade-offs.
```

## Dependencies Between Jobs

### Defining Dependencies

Jobs can depend on other jobs using the `depends_on` field:

```yaml
---
id: implementation
title: "Implementation"
status: pending
type: interactive_agent
depends_on:
  - 01-spec.md
  - 02-design.md
---
```

### Dependency Resolution

Grove Flow automatically:
- **Orders execution**: Dependent jobs wait for prerequisites
- **Validates dependencies**: Checks for circular dependencies
- **Propagates failures**: Failed dependencies block downstream jobs
- **Optimizes parallelism**: Independent jobs run concurrently

### Dependency Patterns

**Sequential Chain**:
```
01-spec.md → 02-implement.md → 03-test.md → 04-review.md
```

**Fan-out/Fan-in**:
```
      01-spec.md
         ├───┼───┐
         ▼   ▼   ▼
 02-api.md 03-ui.md 04-db.md
         └───┼───┘
             ▼
       05-integration.md
```

**Conditional Dependencies**:
Some jobs may depend on multiple alternatives:
```yaml
depends_on:
  - 02-implement-option-a.md  # OR
  - 02-implement-option-b.md  # Either one satisfies the dependency
```

### Best Practices for Dependencies

- **Keep dependencies minimal**: Only depend on truly necessary prerequisites
- **Use descriptive filenames**: Make dependency relationships clear
- **Avoid circular dependencies**: Structure workflows as directed acyclic graphs
- **Group related tasks**: Use fan-out patterns for parallel workstreams

## Models and LLM Configuration

### Model Selection Hierarchy

Models are selected based on this precedence order:

1. **Job-level model** (highest priority)
2. **Plan-level model**
3. **Project-level model** (grove.yml)
4. **System default** (lowest priority)

### Job-Level Model Configuration

Specify models directly in job frontmatter:

```yaml
---
id: complex-analysis
title: "Complex Code Analysis"
type: interactive_agent
model: claude-4-opus  # Use the most capable model
---
```

### Available Models

List available models:

```bash
flow models
```

Current supported models:
- **claude-4-opus**: Most capable, best for complex reasoning
- **claude-4-sonnet**: Balanced performance and capability
- **claude-3-haiku**: Fast and efficient for simple tasks
- **gemini-2.5-pro**: Google's latest pro model
- **gemini-2.5-flash**: Fast, efficient Google model

### Model Selection Guidelines

**Use Claude 4 Opus for**:
- Complex reasoning tasks
- Large codebase analysis
- Architectural decisions
- Critical bug fixes

**Use Claude 4 Sonnet for**:
- General development tasks
- Feature implementation
- Code reviews
- Documentation writing

**Use Claude 3 Haiku for**:
- Simple text generation
- Code formatting
- Quick analysis
- Batch processing

**Use Gemini models for**:
- Cost-sensitive workloads
- High-throughput scenarios
- Google ecosystem integration

## Job Templates

### Using Templates

Templates provide pre-configured job structures:

```bash
# List available templates
flow plan templates list

# Add job using template
flow plan add --template code-review --title "Review authentication module"
```

### Available Built-in Templates

Templates include pre-written prompts and configurations:

- **agent-run**: Generates a plan an LLM agent carries out
- **api-design**: Design RESTful or GraphQL APIs
- **architecture-overview**: Generate architecture documentation
- **code-review**: Perform thorough code reviews
- **deployment-runbook**: Create production deployment guides
- **documentation**: Update and create documentation
- **generate-plan**: Create multi-step plans from specifications
- **incident-postmortem**: Analyze incidents and create action items
- **learning-guide**: Create codebase learning materials
- **migration-plan**: Plan technology or database migrations
- **performance-analysis**: Analyze and optimize performance
- **refactoring-plan**: Plan large-scale refactoring efforts
- **security-audit**: Conduct security audits
- **test-strategy**: Create comprehensive test strategies
- **tech-debt-assessment**: Assess and prioritize technical debt

### Template Structure

Templates define:
- **Default job type**: Appropriate type for the task
- **Pre-written prompts**: Professional, comprehensive prompts
- **Suggested dependencies**: Common prerequisite patterns
- **Output configuration**: How results should be captured
- **Model recommendations**: Optimal model for the task

Example template usage:
```bash
# Create a comprehensive code review
flow plan add --template code-review \
  --title "Review user authentication system" \
  --depends-on "02-implement.md"
```

## Job Completion and Management

### Manual Job Completion

Mark jobs as completed manually:

```bash
# Complete specific job
flow plan complete my-plan/03-chat-discussion.md

# Complete using active plan
flow plan set my-plan
flow plan complete 03-chat-discussion.md
```

This is particularly useful for:
- Chat jobs that need manual progression
- External tasks completed outside Grove Flow
- Jobs blocked by external dependencies

### Automatic Job Summarization

When enabled, Grove Flow automatically:
- **Summarizes job outputs**: Creates concise summaries of results
- **Extracts key insights**: Highlights important findings
- **Updates job status**: Marks successful completion
- **Stores metadata**: Preserves execution information

Configuration in grove.yml:
```yaml
flow:
  summarize_on_complete: true
  summary_model: claude-3-haiku  # Use efficient model for summaries
  summary_max_chars: 1000
```

### Job Output Management

Jobs can produce different types of outputs:

**File Output** (default):
```yaml
output:
  type: file
  path: api-spec.md  # Optional custom path
```

**Git Commit**:
```yaml
output:
  type: commit
  message: "Add user authentication API"
```

**No Output**:
```yaml
output:
  type: none  # For analysis or validation jobs
```

**Generate Jobs**:
```yaml
output:
  type: generate_jobs  # Create new jobs based on results
```

### Debugging Failed Jobs

When jobs fail:

1. **Check job logs**: View execution logs for error details
2. **Review dependencies**: Ensure all prerequisites completed successfully
3. **Validate configuration**: Check job frontmatter for syntax errors
4. **Model limitations**: Consider if the task exceeds model capabilities
5. **Resource constraints**: Verify system resources are available

```bash
# View detailed job status
flow plan status --verbose

# Check specific job details
flow plan status my-plan --format list
```

## Advanced Topics

### Job Metadata and Frontmatter

Jobs support rich metadata in YAML frontmatter:

```yaml
---
id: implementation
title: "User Authentication Implementation"
status: pending
type: interactive_agent
model: claude-4-sonnet
depends_on:
  - 01-spec.md
output:
  type: file
  path: auth-implementation.md
worktree: auth-feature
tags:
  - authentication
  - security
  - api
estimated_duration: 2h
priority: high
assigned_to: team-backend
---
```

### Custom Job Configurations

Advanced configuration options:

- **Execution timeouts**: Set maximum execution time
- **Resource limits**: Control CPU and memory usage
- **Environment variables**: Pass configuration to jobs
- **Working directory**: Set specific execution context
- **Tool restrictions**: Limit available tools for security

### Integration with Worktrees

Jobs can be configured to run in specific worktrees:

```yaml
---
id: feature-work
title: "Feature Implementation"
type: interactive_agent
worktree: feature-branch
---
```

Benefits:
- **Isolated development**: Work on features without affecting main branch
- **Parallel workstreams**: Multiple jobs in different worktrees
- **Safe experimentation**: Easy rollback of changes
- **Clean history**: Organized commit history per feature

### Performance Considerations

**Job Granularity**:
- Break large tasks into smaller, focused jobs
- Balance overhead of job management with parallelism benefits
- Consider dependency bottlenecks in job structure

**Model Selection**:
- Use appropriate model power for task complexity
- Consider cost implications of model choices
- Leverage caching for repeated similar tasks

**Resource Management**:
- Monitor job execution times and resource usage
- Set appropriate timeouts for long-running jobs
- Clean up job outputs regularly to manage disk space

Jobs are the building blocks of Grove Flow workflows. Understanding how to create, configure, and manage them effectively will help you build robust, efficient automation for your development processes.