# Import Updates Summary

## Changes Made

All Go files in the `/Users/solom4/Code/grove/.grove-worktrees/grovepm/grove-ecosystem/grove-jobs/cmd/` directory have been updated with the following changes:

### 1. Package Declaration
- Changed from `package cli` to `package cmd`

### 2. Import Updates
The following import paths have been updated:

| Old Import Path | New Import Path |
|----------------|-----------------|
| `github.com/mattsolo1/grove/internal/orchestration` | `github.com/mattsolo1/grove-jobs/pkg/orchestration` |
| `github.com/mattsolo1/grove/internal/state` | `github.com/mattsolo1/grove-jobs/pkg/state` |
| `github.com/mattsolo1/grove/internal/docker` | `github.com/mattsolo1/grove-core/docker` |
| `github.com/mattsolo1/grove/internal/git` | `github.com/mattsolo1/grove-core/git` |
| `github.com/mattsolo1/grove/internal/config` | `github.com/mattsolo1/grove-core/config` |
| `github.com/mattsolo1/grove/internal/errors` | `github.com/mattsolo1/grove-core/errors` |
| `github.com/mattsolo1/grove/internal/util` | `github.com/mattsolo1/grove-core/util` |

### 3. Files Updated
The following files were updated:
- jobs.go
- jobs_active.go
- jobs_add_step.go
- jobs_add_step_test.go
- jobs_cleanup_worktrees.go
- jobs_complete.go
- jobs_extract.go
- jobs_graph.go
- jobs_graph_test.go
- jobs_helpers.go
- jobs_init.go
- jobs_run.go
- jobs_status.go
- jobs_templates.go

### Notes
- No imports to `github.com/mattsolo1/grove/internal/cli` were found, as requested
- All updates were performed using a sed script to ensure consistency
- The package declaration change from `cli` to `cmd` was applied to all files