# Orchestration Pipeline Demo

This demo showcases Grove's advanced orchestration capabilities, including:

- Dynamic job generation using `output.type: generate_jobs`
- Shell jobs for running commands like `grove cx update`
- Complex dependency chains with parallel and sequential execution
- Automated context management between job stages
- Baked-in prompt engineering for automatic plan generation

## Prerequisites

1. Ensure you have a Grove agent container running:
   ```bash
   grove agent start --name grove-agent
   ```

2. Initialize the demo:
   ```bash
   make setup
   ```

## Running the Demo

### Using Job Templates

This demo now uses Grove's built-in job template system. The first job, which generates the rest of the pipeline, is created from the `generate-plan` template.

You can view all available built-in templates by running:
```bash
make list-templates
```

### Execute the Pipeline

Execute the full pipeline:
```bash
make run-pipeline
```

This will:
1. Run the initial planning job that generates additional job files
2. Execute all generated jobs in the correct order based on dependencies
3. Update context automatically between stages
4. Create a complete TODO application implementation

## What Happens

The pipeline demonstrates:

1. **Dynamic Job Generation**: The first oneshot job reads the spec and generates multiple job files using the powerful `generate-plan` built-in template
2. **Context Updates**: Shell jobs run `grove cx update` to ensure downstream jobs see new files
3. **Parallel Execution**: Jobs with no dependencies or shared dependencies run in parallel
4. **Agent Jobs**: Code implementation happens in git worktrees within the shared container
5. **Automated Review**: The final step is a `oneshot` job that reviews the generated code against the original specification, showcasing how templates can build complex, end-to-end workflows

The initial planning job uses Grove's built-in template that includes detailed instructions for the LLM on how to:
- Format job definitions with proper YAML frontmatter
- Create dependency chains between jobs
- Insert context update shell jobs at strategic points
- Use the `===` separator between job definitions

## File Structure

- `spec.md` - The feature specification
- `01-high-level-plan.md` - Initial job that generates the rest of the pipeline
- Generated jobs will appear as `02-*.md`, `03-*.md`, etc.

## Cleanup

Remove all generated files:
```bash
make clean
```