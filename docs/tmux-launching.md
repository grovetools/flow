# Tmux Session Launching

Grove Flow provides tmux session launching capabilities that bridge the gap between planning and interactive development. This feature allows you to quickly jump from markdown-based workflows into live coding sessions with pre-configured prompts.

## Overview

The tmux launching feature supports two workflows:

1. **Structured Workflow** (`flow plan launch`) - Launch from formal plan jobs
2. **Ad-hoc Workflow** (`flow chat launch`) - Launch directly from chat/issue files

Both approaches create detached tmux sessions with:
- Pre-filled agent prompts ready to execute
- Proper working directories (git worktrees)
- Two-window layout (shell + agent)
- Automatic command execution

## Prerequisites

- `tmux` installed on your system
- Docker container running (via `grove-proxy up`)
- Configured `grove.yml` with:
  ```yaml
  flow:
    target_agent_container: grove_flow-main-grove-agent
  agent:
    args: ["--dangerously-skip-permissions"]
  ```

## Chat Launch (Ad-hoc Workflow)

The chat launch feature is perfect for quickly jumping from an idea or issue into an interactive session.

### Basic Usage

```bash
# Launch by title (searches in chat directory)
flow chat launch issue123

# Launch by file path
flow chat launch /path/to/issue123.md
```

### Workflow Example

1. Create an issue or idea file:
   ```bash
   cat > issue123.md << 'EOF'
   # Fix Authentication Bug
   
   Users are reporting login failures after password reset.
   
   Investigation needed:
   - Check password reset token expiration
   - Verify email sending logic
   - Test with different user accounts
   EOF
   ```

2. Initialize it as a chat:
   ```bash
   flow chat -s issue123.md --title "issue123"
   ```

3. Refine the job using one shot chats that can see the context of the codebase

  `flow chat run issue123`

4. Launch the interactive session:
   ```bash
   flow chat launch issue123
   ```

5. Attach to the session:
   ```bash
   tmux attach -t grove-flow__issue123
   ```

### Session Naming

Chat sessions use the pattern: `<repo>__<title>`

Examples:
- `grove-flow__issue123`
- `grove-flow__fix-auth-bug`
- `grove-flow__new-feature`

### Worktree Management

Each chat automatically gets its own git worktree:
- Derived from the chat title/filename
- Created at `.grove-worktrees/<sanitized-name>`
- Allows isolated development without affecting main branch

## Plan Launch (Structured Workflow)

For formal, multi-step plans with dependencies.

### Basic Usage

```bash
# Launch a specific job from a plan
flow plan launch my-plan/02-implement-feature.md
```

### Requirements

The job must:
- Be of type `agent`
- Have a `worktree` specified

Example job file:
```yaml
---
id: implement-auth
title: Implement Authentication
type: agent
worktree: auth-feature
prompt_source:
  - design/auth-spec.md
  - src/auth/
---

Implement the authentication system according to the design spec.
Focus on security best practices and comprehensive error handling.
```

## How It Works

1. **Session Creation**: Creates a detached tmux session
2. **Host Shell**: First window opens in the host worktree directory
3. **Agent Window**: Second window runs `docker exec` into the container
4. **Agent Preparation**: Agent command is pre-filled in the container
5. **Auto-execution**: Agent command runs automatically (you can review/modify if needed)

## Window Layout

Each session has two windows:

1. **Window 1 (Shell)**: Host shell in the worktree directory (for git, file operations, etc.)
2. **Window 2 (Agent)**: Docker container with pre-filled agent command

You start in Window 1. Switch between windows with:
- `Ctrl-b 1` - Shell window
- `Ctrl-b 2` - Agent window

## Configuration

### Agent Arguments

Configure agent arguments in `grove.yml`:

```yaml
agent:
  args: ["--dangerously-skip-permissions", "--verbose"]
```

These arguments are automatically included in the pre-filled command.

### Container Configuration

Specify the target container:

```yaml
flow:
  target_agent_container: grove_flow-main-grove-agent
```

This container must be running (via `grove-proxy up`).

## Troubleshooting

### "Container is not running"

```
Error: container 'grove_flow-main-grove-agent' is not running. Did you run 'grove-proxy up'?
```

**Solution**: Start your development environment:
```bash
grove-proxy up
```

### "Session already exists"

```
⚠️  Session 'grove-flow__issue123' already exists. Attach with `tmux attach -t grove-flow__issue123`
```

**Solution**: Either attach to the existing session or kill it first:
```bash
# Option 1: Attach to existing
tmux attach -t grove-flow__issue123

# Option 2: Kill and recreate
tmux kill-session -t grove-flow__issue123
flow chat launch issue123
```

### "tmux command not found"

**Solution**: Install tmux:
```bash
# macOS
brew install tmux

# Ubuntu/Debian
sudo apt-get install tmux

# Fedora
sudo dnf install tmux
```

## Best Practices

1. **Meaningful Names**: Use descriptive titles for your chats/issues
   - Good: `fix-auth-bug`, `add-user-dashboard`
   - Bad: `test`, `bug1`

2. **Session Management**: 
   - List sessions: `tmux ls`
   - Kill old sessions: `tmux kill-session -t <name>`
   - Kill all Grove sessions: `tmux ls | grep grove- | cut -d: -f1 | xargs -I {} tmux kill-session -t {}`

3. **Prompt Organization**: Structure your prompts clearly
   ```markdown
   # Context
   [Describe the current situation]
   
   # Task
   [What needs to be done]
   
   # Constraints
   [Any limitations or requirements]
   ```

4. **Worktree Cleanup**: Periodically clean old worktrees
   ```bash
   flow plan cleanup-worktrees --older-than 7d
   ```

## Advanced Usage

### Custom Session Names

While you can't directly specify session names, they're derived from your file/title:
- File: `auth-redesign.md` → Session: `grove-flow__auth-redesign`
- Title: `API v2 Migration` → Session: `grove-flow__api-v2-migration`

### Integration with Git Workflows

1. Create feature branch in main repo
2. Launch chat for the feature
3. Work in isolated worktree
4. When done, changes are in the worktree
5. Create PR from the worktree

### Scripting

You can script session creation:

```bash
#!/bin/bash
# create-work-session.sh

TITLE="$1"
PROMPT="$2"

# Create chat file
cat > "/tmp/${TITLE}.md" << EOF
# ${TITLE}

${PROMPT}
EOF

# Initialize and launch
flow chat -s "/tmp/${TITLE}.md" --title "${TITLE}"
flow chat launch "${TITLE}"
echo "Session created. Attach with: tmux attach -t grove-flow__${TITLE}"
```

Usage:
```bash
./create-work-session.sh "fix-memory-leak" "Investigate and fix the memory leak in the user service"
```

## Architecture Notes

The feature is implemented with:

1. **Command Abstraction**: `pkg/exec` provides testable command execution
2. **Shared Core Logic**: Both `plan launch` and `chat launch` use the same `launchTmuxSession` function
3. **Docker Integration**: Validates container status before launching
4. **Git Worktree Management**: Leverages `grove-core` for worktree operations

This design ensures consistency between workflows while maintaining flexibility for different use cases.
