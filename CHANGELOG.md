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

