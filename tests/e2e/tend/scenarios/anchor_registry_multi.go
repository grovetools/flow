package scenarios

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/pkg/worktreeregistry"
	"github.com/grovetools/core/util/pathutil"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

// AnchorRegistryMultiScenario reproduces the REAL anchored-worktree-lookup gap
// the clean AnchorRegistryScenario fixture missed: an ecosystem that ALREADY
// has another worktree present before the anchored worktree is created.
//
// The bug (inbox 20260616-anchored-worktree-lookup-gap):
//   - `flow plan init <name> --worktree --anchor <sub-repo>` places the worktree
//     under the ANCHOR repo's XDG base, NOT the ecosystem's. Flow's worktree
//     LOOKUP used the ecosystem-scoped workspace.FindWorktreePath, so:
//   - at create, provisionEnvironment printed
//     `Warning: worktree "<name>" not found under <eco>; skipping
//     environment provisioning` (false miss — the worktree DID exist);
//   - at finish --prune-worktree, the prune reported success but LEFT the
//     worktree dir AND its registry JSON behind.
//
// The fix routes all consumers through the registry-first
// workspace.ResolveWorktreePathByName so anchored worktrees resolve.
//
// This scenario asserts BOTH halves on the multi-worktree path:
//  1. create --anchor svc-a → NO "not found / skipping environment
//     provisioning" warning;
//  2. finish --prune-worktree → the worktree dir AND its registry JSON are
//     BOTH gone.
//
// The pre-existing first worktree (created via a separate plan WITHOUT --anchor)
// is what exercises the multi-worktree path: it sits under the ecosystem's own
// XDG base, so a naive ecosystem-scoped scan has a sibling to step over while
// the anchored worktree hides under the sub-repo's base.
var AnchorRegistryMultiScenario = harness.NewScenario(
	"anchor-registry-multi",
	"Anchored (--anchor) worktree lookup in an ecosystem that already has another worktree: create emits no false provisioning warning, and finish --prune-worktree removes the dir AND the registry JSON.",
	[]string{"xdg", "worktree", "anchor", "registry", "plan", "lifecycle"},
	[]harness.Step{
		// grove is mocked so plan finish's incidental env-teardown calls are safe
		// no-ops; cx/llm cover any context regeneration triggered by plan ops.
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "llm"},
		),

		harness.NewStep("Setup realpath-rooted ecosystem with two sub-repos", func(ctx *harness.Context) error {
			gitRoot, repoDirs, _, err := setupEcosystemEnvironment(ctx, "anchor-eco-multi", []string{"svc-a", "svc-b"})
			if err != nil {
				return err
			}
			ctx.Set("git_root", gitRoot)
			ctx.Set("anchor_dir", repoDirs["svc-a"])
			return nil
		}),

		harness.NewStep("Create a FIRST, non-anchored worktree so the ecosystem is not empty", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")

			// A plain --worktree (no --anchor) lands under the ECOSYSTEM's own XDG
			// base. Its presence is the whole point: the anchored worktree created
			// next must still resolve even though a sibling already occupies the
			// ecosystem base. This is the multi-worktree case the clean fixture
			// (one worktree, created anchored from empty) never exercised.
			cmd := ctx.Bin("plan", "init", "first",
				"--worktree=first")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("first (non-anchored) plan init failed: %w", err)
			}

			firstPath := expectedWorktreePath(ctx, gitRoot, "first", true /* XDG */)
			if err := fs.AssertExists(firstPath); err != nil {
				return fmt.Errorf("first worktree should exist under the ecosystem XDG base at %s: %w", firstPath, err)
			}
			return nil
		}),

		harness.NewStep("Init anchored plan with --anchor svc-a → NO false 'not found / skipping provisioning' warning", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "anchored",
				"--worktree=anchored",
				"--anchor", "svc-a")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init --anchor failed: %w", err)
			}

			// CRUX of the bug: with the ecosystem-scoped lookup, the just-created
			// anchored worktree (which lives under svc-a's XDG base) is invisible
			// to provisionEnvironment, which then emits this warning. With the
			// registry-aware fix the worktree resolves and the warning never fires.
			combined := result.Stdout + result.Stderr
			if strings.Contains(combined, "skipping environment provisioning") ||
				strings.Contains(combined, "not found under") {
				return fmt.Errorf("anchored worktree should resolve for env provisioning; got a false 'not found' warning:\n%s", combined)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "anchor-eco-multi", "plans", "anchored")
			ctx.Set("plan_path", planPath)
			return nil
		}),

		harness.NewStep("Anchored worktree + its registry entry exist (owner=anchor)", func(ctx *harness.Context) error {
			anchorDir := ctx.GetString("anchor_dir")

			anchoredPath := expectedWorktreePath(ctx, anchorDir, "anchored", true /* XDG */)
			ctx.Set("worktree_path", anchoredPath)
			if err := fs.AssertExists(anchoredPath); err != nil {
				return fmt.Errorf("anchored worktree should exist under the anchor's XDG base at %s: %w", anchoredPath, err)
			}

			absWorktree, err := filepath.Abs(anchoredPath)
			if err != nil {
				return fmt.Errorf("abs worktree path: %w", err)
			}
			id := pathutil.WorktreeID(absWorktree)
			registryPath := filepath.Join(ctx.StateDir(), "grove", "worktrees", id+".json")
			ctx.Set("registry_path", registryPath)
			if err := fs.AssertExists(registryPath); err != nil {
				return fmt.Errorf("per-worktree registry JSON should exist at %s: %w", registryPath, err)
			}

			data, err := fs.ReadString(registryPath)
			if err != nil {
				return fmt.Errorf("reading registry entry: %w", err)
			}
			entry := worktreeregistry.Entry{}
			if uerr := json.Unmarshal([]byte(data), &entry); uerr != nil {
				return fmt.Errorf("unmarshal registry entry: %w", uerr)
			}
			wantOwner, _ := filepath.Abs(anchorDir)
			gotOwner := entry.Owner
			if r, rerr := filepath.EvalSymlinks(gotOwner); rerr == nil {
				gotOwner = r
			}
			if w, werr := filepath.EvalSymlinks(wantOwner); werr == nil {
				wantOwner = w
			}
			if gotOwner != wantOwner {
				return fmt.Errorf("registry owner = %q, want anchor path %q", entry.Owner, wantOwner)
			}
			return nil
		}),

		harness.NewStep("plan finish --prune-worktree removes the anchored worktree dir AND its registry entry", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			worktreePath := ctx.GetString("worktree_path")
			registryPath := ctx.GetString("registry_path")

			reviewCmd := ctx.Bin("plan", "review", "anchored", "-d", gitRoot)
			reviewCmd.Dir(gitRoot)
			if err := reviewCmd.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("plan review failed: %w", err)
			}

			finishCmd := ctx.Bin("plan", "finish", "anchored",
				"--yes", "--prune-worktree", "--delete-branch", "-d", gitRoot)
			finishCmd.Dir(gitRoot)
			result := finishCmd.Run()
			ctx.ShowCommandOutput(finishCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan finish --prune-worktree failed: %w", err)
			}

			// Both the anchored worktree dir AND its registry entry must be gone.
			// With the ecosystem-scoped lookup the prune found nothing to remove
			// and BOTH survived.
			if err := fs.AssertNotExists(worktreePath); err != nil {
				return fmt.Errorf("anchored worktree dir should be pruned: %w", err)
			}
			if err := fs.AssertNotExists(registryPath); err != nil {
				return fmt.Errorf("registry entry should be deleted on --prune-worktree: %w", err)
			}
			return nil
		}),
	},
)
