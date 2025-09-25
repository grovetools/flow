# Working with Jobs Documentation

Generate detailed documentation for creating and managing individual jobs within Grove Flow plans.

## Content to Cover:

### Creating Jobs
- Focus on `flow plan add` command:
  - Interactive TUI mode (default behavior)
  - Flag-based usage for scripting
  - How to specify job properties (title, type, prompt, dependencies)
- Best practices for writing effective job prompts

### Job Types Explained
Provide detailed explanations and use cases for each job type:
- **agent**: Autonomous AI agents for complex tasks
- **interactive_agent**: Agents requiring human interaction
- **headless_agent**: Background agents without UI
- **oneshot**: Simple, single-response LLM tasks
- **shell**: Shell command execution jobs
- **chat**: Multi-turn conversational jobs

Include when to use each type and typical examples.

### Dependencies Between Jobs
- How to define dependencies using `depends_on`
- Understanding dependency resolution
- Best practices for structuring dependent jobs
- Handling failed dependencies

### Models and LLM Configuration
- How to specify LLM models at the job level
- Available models and their trade-offs
- Model inheritance from plan and project configs
- Best practices for model selection

### Job Templates
- Using `--template` with `flow plan add`
- Listing available templates with `flow plan templates list`
- Understanding what templates provide
- Creating custom job templates

### Job Completion and Management
- Using `flow plan complete` for manual job completion
- Understanding automatic job summarization
- How job outputs are stored and accessed
- Debugging failed jobs

### Advanced Topics
- Job metadata and frontmatter
- Custom job configurations
- Integration with worktrees
- Performance considerations

Include code examples and practical scenarios throughout.