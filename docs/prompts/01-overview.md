# Overview Documentation for Grove Flow

Generate overview documentation for Grove Flow, emphasizing its integration with grove-notebook and TUI-first workflow.

## Outline

### High-level Description
- Define Grove Flow as a multi-step development workflow tool
- Emphasize deep integration with grove-notebook for idea management
- Highlight interactive terminal interfaces (TUIs) as primary interaction mode

### Key Features
Cover these core capabilities:
- **Notebook-Integrated Workflow**: Plans created from grove-notebook notes with bidirectional linking
- **TUI-First Interface**: Interactive interfaces at every stage (nb tui, flow plan tui, flow plan status -t, hooks b)
- **Job Orchestration**: Dependency graphs of jobs (chat, oneshot, interactive agents, shell commands)
- **Git Worktree Integration**: Isolated development environments in .grove-worktrees/
- **Plan Lifecycle Management**: Track from creation through review to completion
- **Chat-Driven Development**: Conversational exploration followed by structured implementation

### How It Works
Explain the complete workflow:
1. Start in grove-notebook (nb tui)
2. Promote notes to plans (Press P)
3. Explore with chat jobs
4. Structure work using TUI keyboard shortcuts
5. Execute in dependency order
6. Monitor across all plans
7. Review and finish with lifecycle commands

Also cover technical architecture:
- Plans as directories of Markdown job files
- YAML frontmatter for configuration
- Dependency graph execution
- Worktree and tmux integration

### Ecosystem Integration
Describe integration with:
- Grove Notebook (nb): Primary entry point, note promotion
- Grove Context (cx): File context generation for LLMs
- Grove Hooks (hooks): Session management and lifecycle hooks
- Agent Tools (claude, gemini): Interactive coding sessions
- Grove Meta (grove): Binary management

### Installation
Standard installation via grove meta-CLI with verification steps.
