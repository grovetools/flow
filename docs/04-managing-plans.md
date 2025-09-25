# Managing Plans

Plans are the core organizational unit in Grove Flow, representing structured workflows with multiple jobs. This document covers all aspects of creating, managing, and working with plans throughout their lifecycle.

## Plan Initialization

### Basic Plan Creation

The `flow plan init` command creates new plans. If you don't specify a directory name, Grove Flow launches an interactive TUI to help you set up your plan:

```bash
# Interactive plan creation
flow plan init

# Create a plan with a specific name
flow plan init my-feature-plan
```

### Using Worktrees

When working with Git repositories, you can create plans that automatically set up dedicated Git worktrees:

```bash
# Create a plan with an auto-named worktree
flow plan init my-feature --worktree

# Create a plan with a custom worktree name
flow plan init my-feature --worktree custom-branch-name
```

Worktrees provide isolated development environments where you can work on your plan without affecting your main branch. Grove Flow automatically manages the worktree lifecycle, creating it during plan initialization and cleaning it up when the plan is finished.

### Using Recipes

Recipes provide pre-configured plan templates for common workflows:

```bash
# Create a plan using a built-in recipe
flow plan init api-feature --recipe standard-feature

# List available recipes
flow plan recipes list

# Create a plan with recipe variables
flow plan init user-auth --recipe standard-feature \
  --recipe-vars "model=claude-3-5-sonnet-20241022,output_dir=docs"
```

Available built-in recipes include:
- **standard-feature**: A complete workflow with spec → implement → review phases
- **chat-workflow**: Chat-driven development with discuss → implement → review
- **chat**: Simple single chat job for brainstorming and planning
- **docgen-customize**: Documentation generation with planning and customization

### Converting Chats to Plans

You can bootstrap plans from existing chat sessions or markdown files:

```bash
# Extract all content from a chat into a new plan
flow plan init feature-impl --extract-all-from ./chats/feature-discussion.md
```

This analyzes the chat content and creates appropriate jobs based on the conversation flow.

### Advanced Initialization Options

```bash
# Create a plan with all options
flow plan init advanced-plan \
  --worktree \
  --recipe standard-feature \
  --model claude-3-5-sonnet-20241022 \
  --recipe-vars "output_dir=docs,rules_file=docs.rules" \
  --open-session \
  --force
```

Options include:
- `--model`: Set default LLM model for jobs in this plan
- `--target-agent-container`: Default container for agent jobs
- `--open-session`: Immediately open a tmux session after creation
- `--force`: Overwrite existing directories
- `--recipe-cmd`: Use a custom command to load recipe definitions

## Listing and Browsing Plans

### Command Line Listing

View all your plans with the list command:

```bash
# Basic plan listing
flow plan list

# Detailed view with job information
flow plan list --verbose

# Include completed/finished plans
flow plan list --include-finished
```

The output shows plan names, statuses, and job counts:

```
NAME                STATUS      JOBS    DESCRIPTION
user-profile-api    active      3/5     User profile management API
documentation-v2    pending     0/4     Documentation restructuring  
chat-interface      finished    8/8     Chat interface implementation
```

### Interactive Plan Browser

For visual browsing and management, use the TUI:

```bash
flow plan tui
```

The TUI provides:
- Visual plan overview with job statuses
- Navigate between plans with arrow keys
- Quick actions: view status, open sessions, run jobs
- Real-time updates of job progress
- Plan filtering and search capabilities

Navigation:
- `↑/↓`: Move between plans
- `Enter`: View detailed plan status
- `o`: Open plan in tmux session
- `r`: Run next pending jobs
- `q`: Quit TUI

## Active Plan Management

Grove Flow supports setting an "active plan" to streamline your workflow by avoiding the need to specify plan directories in every command.

### Setting the Active Plan

```bash
# Set active plan by name (from plans directory)
flow plan set user-profile-api

# Set active plan by path
flow plan set ./plans/feature-x

# Set active plan interactively
flow plan set
```

### Using the Active Plan

Once set, most commands will default to the active plan:

```bash
# These commands work on the active plan
flow plan status
flow plan add
flow plan run
flow plan graph
```

### Managing Active Plan State

```bash
# Show current active plan
flow plan current

# Clear the active plan
flow plan unset
```

The active plan is stored per-project and persists across terminal sessions.

## Status and Visualization

### Plan Status Overview

The status command provides comprehensive information about your plan's current state:

```bash
# Tree view (default)
flow plan status

# List format
flow plan status --format list

# Interactive TUI status
flow plan status --tui

# Verbose details
flow plan status --verbose
```

Example tree output:
```
Plan: user-profile-api (5 jobs)
├── ✓ spec (completed) - API specification
├── ⚠ implement (pending) - Implementation 
│   └── depends on: spec
├── ⚠ tests (pending) - Test suite
│   └── depends on: implement  
├── ⚠ docs (pending) - Documentation
│   └── depends on: implement
└── ⚠ review (pending) - Code review
    └── depends on: tests, docs
```

Status indicators:
- `✓` Completed
- `▶` Running
- `⚠` Pending
- `✗` Failed
- `⏸` Paused

### Dependency Visualization

Visualize job dependencies with the graph command:

```bash
# Generate dependency graph
flow plan graph

# Show dependency graph in status
flow plan status --graph
```

The graph shows:
- Job nodes with current status
- Dependency arrows showing execution order
- Critical path highlighting
- Bottleneck identification

Example graph output:
```
     spec
       │
       ▼
   implement
    ┌──┴──┐
    ▼     ▼
  tests  docs
    └──┬──┘
       ▼
     review
```

## Development Environment Integration

### Opening Plan Directories

Quickly access plan files and directories:

```bash
# Open plan directory in your default editor
flow plan open

# Open specific plan
flow plan open user-profile-api
```

### Tmux Integration

Grove Flow integrates seamlessly with tmux for development sessions:

```bash
# Launch tmux session for the plan
flow plan launch

# Launch with specific plan
flow plan launch user-profile-api
```

Tmux integration features:
- **Automatic session naming**: Sessions named after the plan
- **Worktree integration**: If the plan has a worktree, the session opens there
- **Context preservation**: Working directory, environment variables
- **Window management**: Separate windows for different aspects (editing, running, logs)
- **Session persistence**: Sessions survive terminal closures

The tmux session includes:
1. **Main window**: Plan directory with your editor
2. **Jobs window**: For running individual jobs
3. **Logs window**: Real-time job execution logs
4. **Shell window**: General purpose shell

### Session Management

```bash
# List active plan sessions
tmux list-sessions | grep grove-plan

# Attach to existing session
tmux attach -t grove-plan-user-profile-api

# Kill plan session
tmux kill-session -t grove-plan-user-profile-api
```

## Plan Cleanup and Completion

### Finishing Plans

When your plan is complete, use the finish command to clean up:

```bash
# Interactive finish workflow
flow plan finish

# Finish specific plan
flow plan finish user-profile-api

# Force finish (skip confirmations)
flow plan finish --force
```

The finish workflow:

1. **Status check**: Verifies all jobs are completed
2. **Final summary**: Shows plan results and outputs
3. **Worktree cleanup**: Removes associated Git worktrees
4. **Archive creation**: Optionally archives plan directory
5. **Session cleanup**: Closes related tmux sessions

### What Happens During Cleanup

**Worktree Management**:
- Prompts to merge changes back to main branch
- Safely removes the worktree directory
- Updates Git references
- Cleans up tracking branches

**Plan Archiving**:
- Moves completed plans to archive directory
- Preserves all job outputs and metadata  
- Maintains searchable history
- Compresses large output files

**Resource Cleanup**:
- Terminates any running jobs
- Closes file handles and connections
- Cleans up temporary files
- Updates active plan state

### Manual Cleanup

For emergency cleanup or bulk operations:

```bash
# Clean up orphaned worktrees
flow plan cleanup-worktrees

# List worktrees that can be cleaned
flow plan cleanup-worktrees --dry-run

# Force cleanup of specific worktree
flow plan cleanup-worktrees --force worktree-name
```

## Best Practices for Plan Lifecycle

### Plan Organization

- Use descriptive plan names that indicate the feature or goal
- Group related plans in subdirectories by project or team
- Use consistent naming conventions across your organization
- Archive old plans regularly to keep workspace clean

### Workflow Integration

- Set active plan at start of work session
- Use tmux sessions for persistent development environment
- Leverage worktrees for experimental or feature branch work
- Review plan status regularly to track progress

### Collaboration

- Share plan directories via version control
- Use consistent recipe templates across team
- Document custom workflows in team recipes
- Establish conventions for plan naming and structure

### Performance Optimization

- Finish completed plans promptly to free resources
- Use `--include-finished false` when listing plans
- Archive old plans to separate directories
- Clean up worktrees regularly to save disk space

## Troubleshooting

### Common Issues

**Plan not found**: Check that you're in the right directory and that `grove.yml` specifies the correct `plans_directory`.

**Worktree conflicts**: Use `flow plan cleanup-worktrees` to remove orphaned worktrees before creating new ones.

**Permission errors**: Ensure you have write access to the plans directory and worktree base directory.

**Tmux session issues**: Kill existing sessions with `tmux kill-session` before launching new ones.

### Recovery Procedures

**Corrupted plan state**: Edit `.grove-plan.yml` manually or reinitialize the plan.

**Missing dependencies**: Check job frontmatter for correct `depends_on` specifications.

**Stale worktrees**: Use `git worktree prune` followed by `flow plan cleanup-worktrees`.

Plans form the foundation of Grove Flow's orchestration capabilities. Master these management techniques to build efficient, reproducible workflows for your development projects.