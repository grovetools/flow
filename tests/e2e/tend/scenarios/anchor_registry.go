package scenarios

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/grovetools/core/pkg/worktreeregistry"
	"github.com/grovetools/core/util/pathutil"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

// AnchorRegistryScenario pins the `--anchor` worktree-placement contract and the
// per-worktree XDG registry lifecycle introduced by flow c3fadba / core ff8ad1d
// + 968adf7 / d76efa8.
//
//	init <name> --worktree --anchor svc-a
//	  -> the worktree CONTAINER lands under the ANCHOR repo's XDG base:
//	     paths.WorktreesDir()/DirIdentifier(svc-a)/<name>, NOT the ecosystem's.
//	  -> a registry entry is written at paths.StateDir()/worktrees/<id>.json
//	     where <id> = pathutil.WorktreeID(worktreeAbsPath); its owner == the
//	     anchor (svc-a) path and its repos list is populated.
//	finish <name> --prune-worktree --yes
//	  -> the registry entry is DELETED.
//
// The owner assertion is the crux: with --anchor svc-a the worktree is owned by
// the sub-repo, not by the ecosystem root that the command was invoked from.
//
// The orphan/reconcile case (rm -rf the worktree dir, then `grove env prune`
// removes the stale entry, grove f783e94) is intentionally NOT exercised here:
// it requires the REAL grove binary's reconcile path, but the binary-under-test
// in this repo is flow and grove is mocked, so an end-to-end prune cannot run
// hermetically. That path is covered by core's worktreeregistry reconcile unit
// tests.
var AnchorRegistryScenario = harness.NewScenario(
	"anchor-registry",
	"Anchored (--anchor) worktree placement under the sub-repo's XDG base + per-worktree registry create/prune lifecycle.",
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
			gitRoot, repoDirs, _, err := setupEcosystemEnvironment(ctx, "anchor-eco", []string{"svc-a", "svc-b"})
			if err != nil {
				return err
			}
			ctx.Set("git_root", gitRoot)
			// The anchor repo is svc-a; its realpath dir is the owner we expect.
			ctx.Set("anchor_dir", repoDirs["svc-a"])
			return nil
		}),

		harness.NewStep("Init plan with --worktree --anchor svc-a", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			notebooksRoot := ctx.GetString("notebooks_root")

			// --worktree REQUIRES the = form: its NoOptDefVal turns a bare
			// --worktree into the "__AUTO__" marker, so "--worktree", "anchored"
			// would be parsed as a flag with no value plus a stray positional.
			// --anchor is a plain string flag, so the space form is fine.
			cmd := ctx.Bin("plan", "init", "anchored",
				"--worktree=anchored",
				"--anchor", "svc-a")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init --anchor failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "anchor-eco", "plans", "anchored")
			ctx.Set("plan_path", planPath)
			return nil
		}),

		harness.NewStep("Worktree container lands under the ANCHOR repo's XDG base, not the ecosystem's", func(ctx *harness.Context) error {
			anchorDir := ctx.GetString("anchor_dir")
			gitRoot := ctx.GetString("git_root")

			// Expected location is computed from the ANCHOR's path via the SAME
			// DirIdentifier core uses, so the expectation never drifts.
			anchoredPath := expectedWorktreePath(ctx, anchorDir, "anchored", true /* XDG */)
			ctx.Set("worktree_path", anchoredPath)

			if err := fs.AssertExists(anchoredPath); err != nil {
				return fmt.Errorf("anchored worktree should exist under the anchor's XDG base at %s: %w", anchoredPath, err)
			}

			// Negative guard: it must NOT have been placed under the ecosystem
			// root's identifier (the pre-anchor default). If anchoring were a
			// no-op, the worktree would live here instead.
			ecoPath := expectedWorktreePath(ctx, gitRoot, "anchored", true)
			if ecoPath != anchoredPath {
				if err := fs.AssertNotExists(ecoPath); err != nil {
					return fmt.Errorf("worktree must NOT be placed under the ecosystem's XDG base (%s); --anchor should route it under svc-a: %w", ecoPath, err)
				}
			}
			return nil
		}),

		harness.NewStep("Registry entry exists with owner=anchor and repos populated", func(ctx *harness.Context) error {
			anchorDir := ctx.GetString("anchor_dir")
			worktreePath := ctx.GetString("worktree_path")

			// The registry ID is derived from the worktree's abs path exactly as
			// core's worktreeregistry.Save does. Resolve the symlink-free abs
			// form so WorktreeID's NormalizeForLookup matches the saved key.
			absWorktree, err := filepath.Abs(worktreePath)
			if err != nil {
				return fmt.Errorf("abs worktree path: %w", err)
			}
			id := pathutil.WorktreeID(absWorktree)
			registryPath := filepath.Join(ctx.StateDir(), "grove", "worktrees", id+".json")
			ctx.Set("registry_path", registryPath)

			if err := fs.AssertExists(registryPath); err != nil {
				return fmt.Errorf("per-worktree registry JSON should exist at %s: %w", registryPath, err)
			}

			// Parse it via the real Entry type and assert owner + repos. We read
			// the file directly (rather than worktreeregistry.Load, which would
			// resolve StateDir from the runner's own env, not the sandbox).
			data, err := fs.ReadString(registryPath)
			if err != nil {
				return fmt.Errorf("reading registry entry: %w", err)
			}
			entry := worktreeregistry.Entry{}
			if uerr := json.Unmarshal([]byte(data), &entry); uerr != nil {
				return fmt.Errorf("unmarshal registry entry: %w", uerr)
			}

			// owner must be the ANCHOR (svc-a) path, not the ecosystem root.
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
			if len(entry.Repos) == 0 {
				return fmt.Errorf("registry entry repos should be populated, got empty (entry=%+v)", entry)
			}
			return nil
		}),

		harness.NewStep("Plan operations from the anchored container stay in the owning ecosystem notebook", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")
			planPath := ctx.GetString("plan_path")
			notebooksRoot := ctx.GetString("notebooks_root")

			// Exercise the user-visible regression path: after init, enter the
			// anchored container and add a job without naming the plan. The active
			// plan + workspace resolver must canonicalize the container back to the
			// origin ecosystem (anchor-eco). The anchor repo controls XDG nesting
			// only; it must never turn the plan name into a notebook workspace.
			cmd := ctx.Bin("plan", "add",
				"--type", "shell",
				"--title", "anchored-context",
				"--prompt", "true")
			cmd.Dir(worktreePath)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add from anchored container failed: %w", err)
			}

			if err := fs.AssertExists(filepath.Join(planPath, "01-anchored-context.md")); err != nil {
				return fmt.Errorf("job should be written to the origin ecosystem plan at %s: %w", planPath, err)
			}

			wrongPlanPath := filepath.Join(notebooksRoot, "workspaces", "anchored", "plans", "anchored")
			if err := fs.AssertNotExists(wrongPlanPath); err != nil {
				return fmt.Errorf("anchor/plan name must not become the notebook workspace (%s): %w", wrongPlanPath, err)
			}
			return nil
		}),

		harness.NewStep("plan finish --prune-worktree deletes the registry entry", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			worktreePath := ctx.GetString("worktree_path")
			registryPath := ctx.GetString("registry_path")

			// finish requires the plan to be in review status first.
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

			// The worktree directory is gone...
			if err := fs.AssertNotExists(worktreePath); err != nil {
				return fmt.Errorf("anchored worktree dir should be pruned: %w", err)
			}
			// ...and so is its registry entry (flow d76efa8).
			if err := fs.AssertNotExists(registryPath); err != nil {
				return fmt.Errorf("registry entry should be deleted on --prune-worktree: %w", err)
			}
			return nil
		}),
	},
)
