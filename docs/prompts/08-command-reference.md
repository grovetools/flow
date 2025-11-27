# Command Reference Documentation

Generate comprehensive CLI reference for Grove Flow, with note about TUI being recommended.

## Introduction Section

Add note at beginning:
- All operations can be done via CLI
- **Recommended workflow uses TUIs**: `nb tui`, `flow plan tui`, `flow plan status -t`, `hooks b`
- TUIs provide better visual feedback and keyboard-driven workflows
- CLI documented below is useful for scripting, automation, and quick operations

## Structure

### `flow plan` Commands

Document each with syntax, description, flags, and examples:

#### Core Plan Management
- `init` - Initialize new plan (note: usually done via `nb tui` promotion)
- `list` - List all plans
- `tui` - Interactive plan browser (RECOMMENDED - include description of features)
- `set`, `current`, `unset` - Active plan management

#### Plan Status and Visualization
- `status` - Show plan status
  - Emphasize `-t` flag for TUI mode (RECOMMENDED)
  - Document all TUI keyboard shortcuts (r, A, x, i, e, d, c, R (resume), space, arrows)
  - CLI modes: tree, list, JSON
- `graph` - Visualize dependency graph

#### Job Management
- `add` - Add jobs (note TUI is easier with automatic dependencies)
- `run` - Execute jobs
- `complete` - Mark job complete
- `resume` - Resume completed interactive agent session
- `jobs rename` - Rename job
- `jobs update-deps` - Update dependencies

#### Development Environment
- `open` - Open plan environment (RECOMMENDED - describe full workflow)
  - What it does: tmux session, worktree navigation, TUI launch
- `tmux status` - Launch in tmux window

#### Lifecycle Management
- `review` - Mark ready for review (with hook triggers)
- `finish` - Complete and cleanup (guided workflow)
- `hold` / `unhold` - Pause/resume plans

#### Configuration and Extraction
- `config` - Manage plan config
- `context` - Manage job context
- `extract` - Extract from chat
- `templates list` - List templates
- `recipes list` - List recipes

### `flow chat` Commands

- Initialize chats (`-s` flag)
- `list` - List chats with filtering
- `run` - Run chat jobs

**Important**: Document that there is NO `flow chat launch` - extraction to plan required for execution

### `flow models` Command

List available LLM models

### `flow tmux` Commands

Tmux integration commands

### `flow version` Command

Version information with `--json` option

### Global Options

Standard flags available across commands

### Environment Variables

- `GROVE_ECOSYSTEM_ROOT`
- `GROVE_FLOW_SKIP_DOCKER_CHECK`
- `GROVE_CONFIG`

### Configuration Files

- `grove.yml` - Project-level
- `.grove-plan.yml` - Plan-level
- `.grove/state.yml` - Local state (not committed)
