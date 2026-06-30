package orchestration

import (
	"context"
	"os"
	"path/filepath"

	coreconfig "github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/sirupsen/logrus"
)

// newWorkspaceProvider builds a discovery-backed workspace provider, the same
// way the cmd-layer reference sites (plan_add_worktrees.go) do. It is used to
// scope the registry-first worktree resolver to this ecosystem's sub-repos.
func newWorkspaceProvider() (*workspace.Provider, error) {
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)
	result, err := workspace.NewDiscoveryService(logger).DiscoverAll()
	if err != nil {
		return nil, err
	}
	return workspace.NewProvider(result), nil
}

// ecosystemWorktreeOwners returns the set of repo roots that may legitimately
// OWN a worktree of the ecosystem rooted at gitRoot: the ecosystem root itself
// plus every local workspace (sub-repo), any of which an `--anchor <repo>` could
// name. It is the owner-scope passed to workspace.ResolveWorktreePathByName so
// the registry lookup stays scoped to this ecosystem and never matches a
// same-named worktree owned by an unrelated ecosystem. Mirrors the cmd-layer
// helper of the same name (cmd/plan_init.go) so every consumer agrees on scope.
func ecosystemWorktreeOwners(gitRoot string, provider *workspace.Provider) []string {
	owners := []string{gitRoot}
	if provider != nil {
		for _, p := range provider.LocalWorkspacesInEcosystem(gitRoot) {
			owners = append(owners, p)
		}
	}
	return owners
}

// resolveWorktreeLayout decides the on-disk layout ("xdg"/"legacy") for a NEW
// worktree, mirroring the cmd-layer resolver of the same name: explicit
// GROVE_WORKTREE_LAYOUT env > grove.toml [worktree] layout under gitRoot >
// default (xdg for ecosystems, legacy otherwise). Existing worktrees are reused
// wherever they live regardless of this value.
func resolveWorktreeLayout(gitRoot string, isEcosystem bool) string {
	valid := func(v string) bool { return v == "xdg" || v == "legacy" }
	if env := os.Getenv("GROVE_WORKTREE_LAYOUT"); valid(env) {
		return env
	}
	if gitRoot != "" {
		if cfg, err := coreconfig.LoadFrom(gitRoot); err == nil && cfg != nil && cfg.Worktree != nil && valid(cfg.Worktree.Layout) {
			return cfg.Worktree.Layout
		}
	}
	if isEcosystem {
		return "xdg"
	}
	return "legacy"
}

// resolveOrPrepareWorktree resolves the EXISTING worktree named worktreeName for
// the ecosystem rooted at gitRoot using the registry-first, anchor-aware
// resolver (workspace.ResolveWorktreePathByName). Anchored worktrees created
// with `flow plan init --anchor <sub-repo>` live under the ANCHOR repo's XDG
// base rather than gitRoot's, so a legacy-only probe (FindWorktreePath /
// GetOrPrepareWorktree) misses them and creates a duplicate empty-submodule
// legacy stub. This routes through the shared resolver instead.
//
// Only when NO existing worktree resolves does it CREATE one via
// workspace.Prepare — mirroring createWorktreeIfRequested (cmd/plan_init.go):
// it builds PrepareOptions from the plan's persisted repos: list (the
// SiblingWorkspaces that make this an ecosystem worktree) and the resolved
// layout. It NEVER falls back to git.WorktreeManager.GetOrPrepareWorktree,
// which hardcodes <gitRoot>/.grove-worktrees/<name>.
func resolveOrPrepareWorktree(ctx context.Context, gitRoot, worktreeName string, plan *Plan) (string, error) {
	// A discovery failure must not block resolution: registry-first lookup still
	// works with a nil provider (owners collapse to just gitRoot), and the
	// resolver also accepts any owner under gitRoot on disk.
	provider, _ := newWorkspaceProvider()
	owners := ecosystemWorktreeOwners(gitRoot, provider)

	if path, ok := workspace.ResolveWorktreePathByName(gitRoot, worktreeName, owners); ok {
		return path, nil
	}

	// Not found under any layout base or in the registry — create it.
	var repos []string
	if plan != nil && plan.Config != nil {
		repos = plan.Config.Repos
	}
	opts := workspace.PrepareOptions{
		GitRoot:           gitRoot,
		WorktreeName:      worktreeName,
		BranchName:        worktreeName,
		SiblingWorkspaces: repos,
		UseXDGWorktrees:   resolveWorktreeLayout(gitRoot, len(repos) > 0) == "xdg",
		// Delegate the privileged ~/.claude.json trust write to the gitRoot-scope
		// daemon when Prepare runs sandbox-side (see NewTrustSeedFallback).
		TrustSeedFallback: NewTrustSeedFallback(gitRoot),
	}
	if plan != nil {
		opts.PlanName = filepath.Base(plan.Directory)
	}
	return workspace.Prepare(ctx, opts, CopyProjectFilesToWorktree)
}
