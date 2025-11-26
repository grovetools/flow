# Managing Plans Documentation

Generate documentation for plan management, emphasizing grove-notebook integration and TUI workflows.

## Outline

### Plan Initialization

#### From grove-notebook (Recommended)
- Primary workflow starting in `nb tui`
- Pressing `P` to promote notes
- Automatic plan creation with initial chat job
- Bidirectional linking via `note_ref`
- Worktree creation during promotion

#### Direct Initialization via CLI
- Basic `flow plan init` usage
- Worktree flags (auto-named and custom)
- Recipe usage
- File extraction
- Combined options
- Interactive TUI mode

### Listing and Browsing Plans

#### Plan TUI (Primary Interface)
- High-level overview with `flow plan tui`
- Columns: status, git state, merge state, review status, notes
- Available actions and navigation
- Accessing detailed views

#### List Command
- Simple table output for scripting
- Filtering options

### Active Plan Management
- Concept of active plan
- `flow plan set`, `current`, `unset` commands
- How active plan affects other commands

### Status and Visualization

#### Plan Status TUI (Primary Interface)
- Interactive mode with `flow plan status -t`
- Keyboard shortcuts (r, A, x, i, e, d, c, space, arrow keys)
- Dependency tree visualization
- Recommended as primary workflow tool

#### Command-Line Status
- Tree view, verbose, JSON formats
- Status indicators
- Use cases for scripting

#### Dependency Graphs
- `flow plan graph` command
- Output formats (mermaid, dot, ascii)

### Interaction with Development Environment

#### Opening Plans
- `flow plan open` as primary entrypoint
- Tmux session creation
- Worktree navigation
- Status TUI launch

#### Running Interactive Agents
- Agents in dedicated tmux windows
- Monitoring with `hooks b`
- Cross-plan visibility

### Plan Lifecycle

#### Plan States
- Active, Review, Finished, Hold

#### Reviewing Plans
- `flow plan review` command
- Trigger hooks (PR creation, etc.)
- Update notebook links
- TUI review features

#### Finishing and Cleanup
- `flow plan finish` guided workflow
- Cleanup checklist
- Worktree and branch deletion
- Archiving
- Notebook updates

#### Holding Plans
- `flow plan hold` and `unhold`
- Visibility in list views
