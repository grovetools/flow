## v0.2.17 (2025-09-13)

### Chores

* **deps:** sync Grove dependencies to latest versions

### Features

* remove token limits from oneshot executor

### Bug Fixes

* resolve E2E test failures and remove Docker dependencies
* pass job context to grove-gemini for proper usage tracking

## v0.2.16 (2025-09-04)

### Tests

* migrate E2E tests from inline script mocks to compiled Go binaries

### Chores

* **deps:** sync Grove dependencies to latest versions
* **deps:** sync Grove dependencies to latest versions

### Bug Fixes

* ensure oneshot jobs are summarized on completion

### Features

* add TUI support for displaying job summaries
* add job summarization on completion

## v0.2.15 (2025-09-02)

### Features

* append clogs transcript when completing interactive agent jobs

## v0.2.14 (2025-08-31)

### Bug Fixes

* set agent_continue to false by default for interactive_agent jobs

## v0.2.13 (2025-08-29)

### Chores

* **deps:** sync Grove dependencies to latest versions
* **deps:** sync Grove dependencies to latest versions
* **deps:** sync Grove dependencies to latest versions

### Bug Fixes

* resolve API key in grove-flow context for gemini calls
* resolve keyboard input conflicts in flow plan add --interactive

## v0.2.12 (2025-08-28)

### Chores

* **deps:** sync Grove dependencies to latest versions
* **deps:** sync Grove dependencies to latest versions
* **deps:** sync Grove dependencies to latest versions
* remove test file

### Features

* implement flow plan recipes for common workflow templates
* improve plan status tree display for DAGs with multiple dependencies
* replace plan finish text prompt with interactive Bubble Tea TUI (#5)
* add interactive plan TUI for browsing and managing plans (#4)
* replace plan finish text prompt with interactive Bubble Tea TUI
* add agent-xml builtin template
* enhance flow plan finish command with advanced git warnings and remote branch support
* add flow plan open command and refactor session management
* add flow plan finish command with comprehensive cleanup workflow
* add debug logging for LLM prompts

### Bug Fixes

* filter finished plans from flow plan tui command
* remove duplicate prompt template content in oneshot jobs (#6)
* improve prompt structure and fix chat file parsing
* resolve duplicate context files and model precedence issues

### Tests

* add e2e tests for flow plan recipes feature

## v0.2.11 (2025-08-27)

### Features

* streamline flow plan init with extraction and session launching

## v0.2.10 (2025-08-26)

### Chores

* **deps:** sync Grove dependencies to latest versions
* update readme (#1)

### Bug Fixes

* disable agent polling test for now
* ensure job dependencies are properly passed as context files
* simplify template resolution with upward directory search
* template symlink sort of

### Features

* implement asynchronous interactive workflow with polling

## v0.2.9 (2025-08-25)

### Chores

* **deps:** sync Grove dependencies to latest versions
* **deps:** sync Grove dependencies to latest versions

### Bug Fixes

* disable lsf on release

## v0.2.8 (2025-08-25)

### Bug Fixes

* disable lint
* apply frontmatter template to first chat response

### Chores

* **deps:** sync Grove dependencies to latest versions
* **deps:** sync Grove dependencies to latest versions

### Features

* auto-inject template: chat for chat jobs without template
* integrate grove-gemini library for Gemini model handling

## v0.2.7 (2025-08-25)

### Chores

* bump dependencies
* remove gemini client
* merge branch 'fix-missing-rules'

### Features

* add cache control directives for Gemini API
* failed template symlinking wip
* automatically set new plan as active on init
* improve plan creation workflow
* add agent_continue support for interactive agent jobs
* add interactive status TUI for plan management
* add interactive prompt for missing .grove/rules files
* add job statistics to starship status
* add Starship prompt integration
* add interactive prompt for missing .grove/rules files
* add list command to plan extract for discovering block IDs
* enhance plan add TUI and extract command
* improve add job tui styling
* add borders and improve visual clarity of TUI fields
* implement TUI flow, view logic, and final integration
* implement interactive dependency tree view for TUI
* scaffold TUI model and initialization for interactive job creation
* allow empty prompts in flow plan add command
* auto-generate go.work files in worktrees with filtered dependencies
* improve chat extract command functionality
* improve worktree session management and add grove-hooks integration
* remove executor-specific prefixes from worktree branch names
* add interactive prompt for chat jobs in multi-job plans
* propagate plan config changes to job frontmatter
* add mock model support for e2e tests
* add .grove-plan.yml for plan-level configuration defaults
* better worktree defaults and interactive agent cli options
* add 'chat' as a valid job type in flow plan add command
* implement flow improvements from 20250819 doc
* add @freeze-cache directive support for Gemini cache management
* add comprehensive host mode support for Flow jobs
* when gemini cache breaks list files that changed
* add --host flag to chat launch for non-containerized sessions
* add first-pary gemini api support with caching

### Bug Fixes

* update chat template
* resolve more tests
* add missing oneshot_model config in flow-chat-pipeline test
* add TTY detection for .grove/rules context prompt
* add TTY detection for all interactive commands
* add TTY detection for plan status --tui flag
* update go workspace E2E test to use single git repository
* use stdin for claude --continue instead of args
* allow templates to be used without prompt_source files
* fix dependency selection in interactive job creation TUI
* simplify dependency list display and fix indentation issues
* correct active_job -> active_plan
* add JSON output support to flow models command
* auto-complete chat jobs in multi-job plans instead of erroring
* remove newlines from agent instruction string to prevent shell command splitting
* new get context sig
* oneshot jobs hsow completed
* add missing pending_user and pending_llm to valid job statuses

## v0.2.6 (2025-08-15)

### Features

* add grove-hooks integration and JSON output support
* make it easier to run chats
* auto-create plan directory when adding steps
* use worktree name as tmux session title for interactive agents
* add interactive_agent job type for interactive tmux sessions
* add ci.yml
* unify chat/plan execution, add smart worktree inheritance, and guided plan stepping

### Code Refactoring

* migrate from .grovectx to .grove/rules for context management
* standardize E2E binary naming and use grove.yml for binary discovery

### Continuous Integration

* switch to Linux runners to reduce costs
* consolidate to single test job on macOS
* reduce test matrix to macOS with Go 1.24.4 only

### Chores

* **deps:** bump dependencies
* bump deps
* remove legacy e2e tests
* bump deps

### Bug Fixes

* disable ci e2e for now
* cli command
* disable git lfs
* resolve oneshot job status race condition
* import os package for MkdirAll in shell executor
* flow plan run doesn't have to create docker client
* remove local replace directive for grove-tend

### Tests

* add basic e2e tests

## v0.2.5 (2025-08-08)

### Chores

* **deps:** bump dependencies
* update module name and imports from grovepm to mattsolo1

