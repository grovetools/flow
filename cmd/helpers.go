package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/tui/theme"
	grovecontext "github.com/grovetools/cx/pkg/context"
)

var helpersUlog = grovelogging.NewUnifiedLogger("grove-flow.helpers")

// configureDefaultContextRules applies default context rules to a given repository path.
func configureDefaultContextRules(repoPath string) error {
	// Check for zombie worktree - refuse to create rules in deleted worktrees
	if grovecontext.IsZombieWorktree(repoPath) {
		return fmt.Errorf("cannot create rules file: worktree has been deleted")
	}

	// Create a context manager scoped to the repository path. This is crucial
	// for it to find the correct grove.yml for that specific repository.
	mgr := grovecontext.NewManager(repoPath)

	// Load only the default rules content as defined by the repo's grove.yml.
	// This function doesn't read any existing .grove/rules file.
	defaultContent, rulesDestPath := mgr.LoadDefaultRulesContent()

	// If no default is configured in grove.yml, seed a commented, non-including
	// placeholder. We intentionally do NOT default to a whole-repo "*" — an
	// uncurated repo must not silently pull its entire tree into context.
	if defaultContent == nil {
		defaultContent = []byte(grovecontext.DefaultRulesTemplate)
	}

	// Ensure the .grove directory exists within the target repo path.
	groveDir := filepath.Dir(rulesDestPath)
	if err := os.MkdirAll(groveDir, 0o755); err != nil {
		return fmt.Errorf("failed to create .grove directory in %s: %w", repoPath, err)
	}

	// Write the rules to .grove/rules within the target repo.
	if err := os.WriteFile(rulesDestPath, defaultContent, 0o600); err != nil {
		return fmt.Errorf("failed to write default rules to %s: %w", rulesDestPath, err)
	}

	ctx := context.Background()
	helpersUlog.Success("Applied default context rules").
		Field("repo_path", repoPath).
		Field("rules_path", rulesDestPath).
		Pretty(theme.IconSuccess + " Applied default context rules to: " + repoPath).
		PrettyOnly().
		Log(ctx)
	return nil
}

// configureGoWorkspace creates a go.work file for a worktree container.
// sourceGitRoot is the git root of the ORIGINAL checkout the worktree was made
// from; it is used to locate the root go.work (and thus the go version).
// Resolving from the source rather than from the worktree itself keeps this
// correct regardless of where the worktree lives on disk (legacy in-repo or
// XDG).
//
// Every worktree is now a container holding repos as <repo>/ subdirs (a
// single-repo worktree is just a 1-repo container), so this always emits the
// container-style `use ( ./<repo> ... )` block. The legacy single-repo path
// (worktree dir == repo checkout, via SetupGoWorkspaceForWorktree) is gone:
// new worktrees always pass repos >= 1. The empty-repos guard below covers the
// residual edge where the caller could not resolve any repo (e.g. plan init
// from a path with no workspace node, or a failed --sibling-workspaces
// expansion) — there is nothing to wire, so we skip go.work generation rather
// than synthesize an invalid file.
func configureGoWorkspace(worktreePath string, repos []string, sourceGitRoot string, provider *workspace.Provider) error {
	if len(repos) == 0 {
		return nil
	}

	// Find the root go.work (from the source repo) to get the go version.
	goWorkConfig, err := workspace.FindRootGoWorkspace(sourceGitRoot)
	goVersion := "go 1.24.4" // Fallback.
	if err == nil && goWorkConfig != nil && goWorkConfig.GoVersion != "" {
		goVersion = goWorkConfig.GoVersion
	}

	// Check which of the repos are Go modules.
	var goRepos []string
	for _, repo := range repos {
		repoGoModPath := filepath.Join(worktreePath, repo, "go.mod")
		if _, err := os.Stat(repoGoModPath); err == nil {
			goRepos = append(goRepos, repo)
		}
	}

	if len(goRepos) == 0 {
		return nil
	}

	var content strings.Builder
	content.WriteString(goVersion + "\n\n")
	content.WriteString("use (\n")
	// The container root is only a module if it has its own go.mod (some
	// ecosystems are a Go module at the root; most, like grovetools, are
	// not). Emitting a bare `use .` when no root go.mod exists produces an
	// invalid go.work that breaks `go build` in every sub-project (the
	// missing root module is reported as `../go.mod` from a sub-dir). Guard
	// it with the same stat check the repos get below.
	if _, err := os.Stat(filepath.Join(worktreePath, "go.mod")); err == nil {
		content.WriteString("\t.\n")
	}
	for _, repo := range goRepos {
		content.WriteString(fmt.Sprintf("\t./%s\n", repo))
	}
	content.WriteString(")\n")

	if err := os.WriteFile(filepath.Join(worktreePath, "go.work"), []byte(content.String()), 0o600); err != nil {
		return fmt.Errorf("failed to write go.work for worktree container: %w", err)
	}
	ctx := context.Background()
	helpersUlog.Success("Configured go.work in worktree container").
		Field("worktree_path", worktreePath).
		Field("go_modules_count", len(goRepos)).
		Field("go_modules", goRepos).
		Pretty(fmt.Sprintf(theme.IconSuccess+" Configured go.work in worktree container with %d Go modules.", len(goRepos))).
		PrettyOnly().
		Log(ctx)
	return nil
}
