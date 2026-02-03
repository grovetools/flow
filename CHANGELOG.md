## v0.6.0 (2026-02-02)

This release brings structural improvements, including migration to the `grovetools` organization and adoption of XDG Base Directory standards for state and configuration storage (9a43f46, b0b5c50, c439d30). Configuration flexibility is enhanced with support for `grove.toml` alongside YAML (b6228ce, 51381db).

User experience in the terminal interface is improved with a new fullscreen toggle for the log pane (1c1b262) and detailed descriptions for CLI flags (4af39eb). The `flow run` command now automatically initializes plain markdown files as chat jobs (0c507fb), streamlining ad-hoc usage.

Reliability and correctness fixes include better session file discovery using content timestamps (55e4a1c), socket-aware tmux command execution (773a5e4), and support for the documented `workspace_init.yml` filename in recipes (258c580).

### Features
- Support `grove.toml` configuration (b6228ce)
- Add fullscreen toggle for status TUI log pane (1c1b262)
- Add detailed descriptions for --type and --inline flag options (4af39eb)
- Update configuration documentation (140b155)

### Bug Fixes
- Auto-initialize plain markdown files as chat jobs in flow run (0c507fb)
- Use content timestamps for session file discovery (55e4a1c)
- Support workspace_init.yml filename and add TUI option (258c580)
- Use tmux.Command() for socket-aware tmux calls (773a5e4)
- Fix version injection during build (11f9199)
- Use XDG paths for session directory lookup (b2a6974)
- Correct streaming indicator checks in agent-log-viewer test (21a2cda)
- Standardize flow logo SVG width (2635023)

### Refactoring
- Update go.mod for grovetools migration (c439d30)
- Use XDG-compliant paths for TUI state storage (9a43f46)
- Use XDG-compliant paths for session archiving (b0b5c50)
- Use config.FindConfigFile for TOML support (51381db)

### Documentation & Chores
- Add MIT License (5671091)
- Restore release workflow (b8379d6)
- Add logo with absolute URL for GitHub rendering (7cc80c1)
- Move README template to notebook (5a4cefe)
- Remove docgen files from repo (4c4b143)

### File Changes
```
 .cx/docs.rules                                     |   12 +
 .github/workflows/release.yml                      |   64 +-
 CLAUDE.md                                          |   15 +-
 LICENSE                                            |   21 +
 Makefile                                           |   10 +-
 README.md                                          |   71 +-
 cmd/config.go                                      |    1 +
 cmd/plan.go                                        |   16 +-
 cmd/plan_add_step.go                               |    8 +-
 cmd/plan_complete.go                               |   17 +-
 cmd/plan_init_tui.go                               |   55 +-
 cmd/plan_run.go                                    |   22 +-
 cmd/plan_session.go                                |    3 +-
 cmd/root_commands.go                               |   16 +-
 cmd/status_tui/keys.go                             |    6 +
 cmd/status_tui/model.go                            |   22 +-
 cmd/status_tui/state.go                            |    8 +-
 cmd/status_tui/update.go                           |   48 +-
 docs/01-overview.md                                |   93 +-
 docs/02-quick-start.md                             |   12 +-
 docs/14-configuration.md                           |   97 +
 docs/README.md.tpl                                 |    6 -
 docs/asciicasts/01-plan-init-status.cast           |  213 +++
 docs/asciicasts/01-plan-init.cast                  | 1942 ++++++++++++++++++++
 docs/docgen.config.yml                             |   78 -
 docs/videos/01-plan-init-dark.mp4                  |  Bin 0 -> 908371 bytes
 docs/videos/01-plan-init-light.mp4                 |  Bin 0 -> 942032 bytes
 docs/videos/01-plan-init-snippet.html              |   11 +
 flow-job.schema.json                               |    2 +-
 flow.schema.json                                   |    5 +-
 go.mod                                             |   36 +-
 go.sum                                             |   78 +-
 grove.toml                                         |   10 +
 grove.yml                                          |   21 -
 pkg/orchestration/archive.go                       |    9 +-
 pkg/orchestration/codex_agent_provider.go          |    4 +-
 pkg/orchestration/context_utils.go                 |   21 +-
 pkg/orchestration/interactive_agent_executor.go    |   61 +-
 pkg/orchestration/opencode_agent_provider.go       |    7 +-
 pkg/orchestration/recipes.go                       |   32 +-
 tests/e2e/tend/scenarios/agent_log_viewer.go       |   14 +-
 tests/e2e/tend/scenarios/plan_status_tui_layout.go |    4 +-
 .../scenarios/provider_session_registration.go     |    3 +-
 tests/e2e/tend/scenarios/session_archiving.go      |    6 +-
 44 files changed, 2716 insertions(+), 464 deletions(-)
```

## v0.3.1-nightly.7c6bbee (2025-10-03)

## v0.3.0 (2025-10-01)

This release enhances user experience and workflow automation. The Terminal User Interfaces (TUIs) for adding, listing, and finishing plans have been completely overhauled with standardized help components, vim-style navigation, and a consistent visual theme aligned with the Grove ecosystem (adfb0ca, abf7eb7). The interactive TUI for `flow plan init` provides a guided way to create new plans with smart defaults (1bd36d1).

A new `flow plan rebase` command has been introduced to manage branches within worktrees, supporting both standard updates and integration testing workflows (a1a43cc). Worktree management is now more robust, with `plan init` directly supporting `--worktree` creation (8bbe5a5) and enhanced handling for complex ecosystem projects involving submodules or multiple repositories (af96315, 99cb1e4).

The recipe system has been improved with support for template variables via `--recipe-vars` and `grove.yml` configuration (f7eb185), as well as dynamic recipe loading from external commands (2bb0563), making workflows more reusable and configurable. As part of this evolution, the deprecated `flow chat launch` command has been removed in favor of a more integrated approach using `plan init` with extraction flags (b1d5993).

Under the hood, `grove-flow` has been refactored to use centralized workspace and logging services from `grove-core`, streamlining its architecture and improving maintainability (52be20b, c08e32a). Additionally, a full documentation suite has been generated, providing comprehensive guides for all features (0e21ccd).

### Features

- Implement `plan rebase` command with dual-mode functionality for standard and integration rebasing (a1a43cc)
- Implement comprehensive TUI styling improvements and navigation for `plan add` (abf7eb7)
- Implement standardized help component in plan list and plan add TUIs (b18b132, 5d015d3)
- Add interactive TUI for `flow plan init` for guided plan creation (1bd36d1)
- Implement worktree creation directly in `plan init` using `--worktree` flag (8bbe5a5)
- Add support for recipe variables via `--recipe-vars` and `grove.yml` (f7eb185)
- Add support for dynamic recipe loading via `get_recipe_cmd` (2bb0563)
- Make `--recipe` flag optional when `--recipe-cmd` is used (4aeb613)
- Add `--recipe-cmd` flag to `plan init` for one-off recipe providers (d06d895)
- Add `rules_file` support in job frontmatter for job-specific context (0dc5ea4)
- Add README template generation and `docgen` integration (ec0148b)
- Enhance `plan open` with convenience features to set active plan and launch status TUI (8bbe5a5)
- Move `.grove-workspace` marker to `.grove/workspace` for cleaner project structure (b5fb963)
- Add fallback paths for `rules_file` resolution (36338e7)
- Generate new documentation for all features (0e21ccd)
- Replace custom dependency tree with `list.Model` for improved UX in `plan add` TUI (e3fa6a9)
- Enhance worktree management with automatic `cx reset` (d2d9d10)
- Create `.grove-workspace` marker in worktrees for `grove-meta` integration (d4194d7, 74b546e)
- Enhance logging throughout the orchestration package (1f55b65)
- Enhance git-status recipe to show uncommitted and committed changes (1ec6d64)
- Auto-create `.grove/rules` with `cx reset` when missing (74c8808)
- Simplify ecosystem worktrees to create direct repo worktrees (99cb1e4)
- Implement linked worktrees for ecosystem submodules (af96315)
- Add initial draft documentation (a063b95)

### Bug Fixes

- Update README and workflow configurations (9252ecf, 962e6e6)
- Clean up README.md.tpl template format (b2e322a)
- Fix readme logo path (ca2bbf2)
- Prevent save shortcut from hijacking insert mode in `plan add` TUI (10687ef)
- Improve readability of `plan finish` congratulations message (6aa427c)
- Properly separate stdout/stderr when calling `grove ws list` (e2747b0)
- Improve ecosystem worktree handling and path resolution (a098796, 5c51b3c)
- Resolve `Plan.ID` field reference to `plan.Name` (cf76273)
- Remove deprecated `flow chat launch` command and references (b1d5993)
- Fix `main...HEAD` diff for proper change tracking in git recipes (41985e4)
- Ensure oneshot jobs update status to completed (c9a8f7c)
- Add lifecycle hooks for all job types to track in `grove-hooks` (cd3a9dd)
- Prevent `cx generate` from overwriting custom rules context in chat jobs (78f90df)
- Improve test messages and mock setups (6ae4806)
- Update `agent_continue` tests to match explicit opt-in behavior (5b601ca)
- Set empty recipe as default for `plan init` (940d4a1)
- Set `open-session` default to false in `plan init` (50a70fe)
- Hide finished plans from Starship prompt (f290cbc)
- Remove automatic worktree assignment from `plan init` (d1a4b2d)

### Code Refactoring

- Complete Phase II TUI unification for all plan TUIs to use `grove-core` theme system (adfb0ca)
- Refactor `grove-flow` to use centralized workspace services from `grove-core` (52be20b)
- Consolidate tmux session name sanitization to use `grove-core` (9ce25ac)
- Update to use new dual-logger pattern from `grove-core` (c08e32a)
- Migrate to centralized tmux client from `grove-core` (fd720ba)
- Restore pretty logging alongside structured logging (c85e536)

### Documentation

- Update docgen configuration and README templates (d5d4879)
- Make documentation more succinct and update `docs.rules` (3172d70, dfcce10)
- Simplify installation instructions to point to main Grove guide (1b6e24d)
- Rename Introduction sections to Overview (79c7faa)

### Chores

- Temporarily disable CI workflow (b5768c4)
- Update `.gitignore` rules for `go.work` files (e1109f4)

### File Changes

```
 .github/workflows/ci.yml                           |    4 +-
 .github/workflows/release.yml                      |   10 +-
 .gitignore                                         |    7 +
 CLAUDE.md                                          |   31 +
 Makefile                                           |    9 +-
 README.md                                          |  162 +-
 cmd/agent_continue_auto_test.go                    |   18 +-
 cmd/chat.go                                        |  288 ----
 cmd/config.go                                      |   24 +-
 cmd/plan.go                                        |   56 +-
 cmd/plan_add_tui.go                                |  766 ++++++---
 cmd/plan_finish.go                                 |  340 +++-
 cmd/plan_finish_tui.go                             |  205 +--
 cmd/plan_helpers.go                                |   32 +
 cmd/plan_init.go                                   |  286 +++-
 cmd/plan_init_tui.go                               |  289 +++-
 cmd/plan_launch.go                                 |   92 +-
 cmd/plan_open.go                                   |   78 +-
 cmd/plan_rebase.go                                 |  523 ++++++
 cmd/plan_recipes_cmd.go                            |   15 +-
 cmd/plan_run.go                                    |    5 +-
 cmd/plan_session.go                                |   59 +-
 cmd/plan_status_tui.go                             |  431 ++---
 cmd/plan_tui.go                                    |  176 +-
 cmd/starship.go                                    |    6 +
 docs/01-overview.md                                |   45 +
 docs/02-examples.md                                |  175 ++
 docs/03-managing-plans.md                          |  137 ++
 docs/04-working-with-jobs.md                       |  146 ++
 docs/05-chats.md                                   |  193 +++
 docs/06-recipes-and-templates.md                   |  211 +++
 docs/07-configuration.md                           |  135 ++
 docs/08-command-reference.md                       |  458 +++++
 docs/README.md.tpl                                 |    6 +
 docs/docgen.config.yml                             |   60 +
 docs/docs.rules                                    |    1 +
 docs/images/grove-flow-readme.svg                  | 1753 ++++++++++++++++++++
 docs/prompts/01-overview.md                        |   33 +
 docs/prompts/02-examples.md                        |   33 +
 docs/prompts/03-managing-plans.md                  |   51 +
 docs/prompts/04-working-with-jobs.md               |   55 +
 docs/prompts/05-chats.md                           |   53 +
 docs/prompts/06-recipes-and-templates.md           |   68 +
 docs/prompts/07-configuration.md                   |   65 +
 docs/prompts/08-command-reference.md               |   72 +
 go.mod                                             |    2 +-
 pkg/docs/docs.json                                 |  335 ++++
 .../builtin_recipes/chat-workflow/01-chat.md       |    1 -
 .../builtin_recipes/chat-workflow/02-implement.md  |    1 -
 .../chat-workflow/03-git-changes.md                |    3 +-
 .../builtin_recipes/chat-workflow/04-git-status.md |    7 +-
 .../builtin_recipes/chat-workflow/05-review.md     |    1 -
 pkg/orchestration/builtin_recipes/chat/01-chat.md  |    1 -
 .../docgen-customize/01-customize-docs.md          |   38 +
 .../docgen-customize/02-generate-docs.md           |   40 +
 .../standard-feature/02-implement.md               |    1 -
 .../standard-feature/03-git-changes.md             |    3 +-
 .../standard-feature/04-git-status.md              |    5 +-
 .../builtin_recipes/standard-feature/05-review.md  |    1 -
 pkg/orchestration/go_workspace.go                  |  287 ----
 pkg/orchestration/go_workspace_test.go             |  214 ---
 pkg/orchestration/headless_agent_executor.go       |  154 +-
 pkg/orchestration/hooks.go                         |   49 +-
 pkg/orchestration/interactive_agent_executor.go    |  254 ++-
 pkg/orchestration/job.go                           |    1 +
 pkg/orchestration/llm_client.go                    |  106 +-
 pkg/orchestration/oneshot_executor.go              |  520 ++++--
 pkg/orchestration/orchestrator.go                  |   48 +-
 pkg/orchestration/plan.go                          |    9 +-
 pkg/orchestration/recipes.go                       |  161 +-
 pkg/orchestration/shell_executor.go                |  139 +-
 pkg/orchestration/worktree_manager.go              |   16 -
 tests/e2e/tend/main.go                             |   11 +-
 tests/e2e/tend/mocks/src/grove/main.go             |  116 +-
 tests/e2e/tend/scenarios_chat.go                   |   49 -
 tests/e2e/tend/scenarios_ecosystem_worktrees.go    |  699 ++++++++
 tests/e2e/tend/scenarios_plan.go                   |  698 +++++++-
 tests/e2e/tend/scenarios_plan_dynamic_recipes.go   |  301 ++++
 tests/e2e/tend/scenarios_plan_recipe_vars.go       |  464 ++++++
 tests/e2e/tend/scenarios_plan_recipes.go           |  120 +-
 tests/e2e/tend/scenarios_rules_in_frontmatter.go   |  402 +++++
 81 files changed, 10426 insertions(+), 2493 deletions(-)
```

## v0.2.18 (2025-09-17)

### Chores

* update Grove dependencies to latest versions

### Bug Fixes

* restore default behavior to create empty plans
* resolve E2E test failures from worktree flag consolidation
* handle type assertions correctly in plan init TUI
* skip duplicate spec file when using --recipe with --extract-all-from
* include worktree in extracted job frontmatter
* derive plan name from directory base name in plan init
* complete generate-recipe implementation with job type validation
* update generate-recipe E2E test to use command.New pattern

### Code Refactoring

* rename chat recipe to chat-workflow, create minimal chat recipe
* consolidate session launching logic
* merge extracted content into recipe's first job

### Tests

* add comprehensive E2E tests for recipe with extraction
* add E2E tests for plan init with --extract-all-from and --with-worktree

### Features

* add CLI command for listing job types
* add CLI command for querying job types
* add chat recipe and make it default for plan init
* keep old agent behavior as headless_agent, make agent an alias for interactive_agent
* consolidate --with-worktree and --worktree flags
* add interactive TUI form for creating new plans
* enhance --open-session to work with and without worktrees
* allow --recipe with --extract-all-from for combined initialization
* implement generate-recipe job type and fix E2E tests

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

