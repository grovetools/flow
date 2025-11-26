# Working with Jobs Documentation

Generate documentation for job creation and management, emphasizing TUI workflows.

## Outline

### Creating Jobs

#### From the Plan Status TUI (Recommended)
- Keyboard shortcuts for job creation
  - `A` - Add new job
  - `x` - Extract XML plan from chat job
  - `i` - Create interactive agent implementation job
- Automatic dependency setup based on selection
- Example workflow: chat → xml-plan → implementation

#### Using the CLI
- Flag-based job creation for scripting
- Key flags: title, type, depends-on, prompt, prompt-file, source-files, template
- Interactive TUI mode with `-i` flag

#### Writing Effective Prompts
- State goals and constraints clearly
- Provide context (source files, dependencies)
- Structure appropriately for job type

### Job Types Explained

Table format comparing:
- `chat` - Exploration and planning
- `oneshot` - Single-shot generation
- `interactive_agent` - Multi-step coding sessions
- `headless_agent` - Background automation
- `shell` - Shell commands

Include purpose, interaction model, output, and use cases for each.

### Dependencies Between Jobs
- Defining with `depends_on` frontmatter
- CLI specification with `-d` flag
- Automatic dependency creation in TUI
- How orchestrator resolves dependencies

### Models and LLM Configuration
- Three-level hierarchy: job, plan, project
- Using `flow models` to see options
- Model selection best practices

### Job Templates
- Listing with `flow plan templates list`
- Using with `--template` flag
- Template locations (built-in, user, project)
- Creating custom templates

### Job Management

#### Managing from TUI
- Keyboard shortcuts: r, c, e, d, space
- Batch operations with selection

#### Managing from CLI
- Manual completion with `flow plan complete`
- Renaming with `flow plan jobs rename`
- Updating dependencies with `flow plan jobs update-deps`

#### Automatic Features
- Automatic summarization (if configured)
- Job output appending
- Note reference (`note_ref`) for traceability

### Advanced Topics
- Job frontmatter structure
- Worktree integration
- Context management
