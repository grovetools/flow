package scenarios

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

// AnchorRegistryReapStubScenario pins site B of the worktree-resolution fix:
// `flow plan finish --prune-worktree` must ALSO reap a stray legacy
// `<eco-root>/.grove-worktrees/<name>` superproject git worktree that has no
// registry entry.
//
// Background: the registry-first prune removes the real (anchored, XDG-located)
// container because it carries a registry entry. But an older legacy-only
// worktree-prep path could `git worktree add` the superproject at the in-repo
// `.grove-worktrees/<name>` location, producing a duplicate stub with NO
// registry entry. The registry-driven prune is blind to it, so it survives.
// The defensive reaper (reapLegacyStubWorktree) removes it during finish.
//
// This scenario:
//  1. creates an anchored worktree (registry-tracked, under svc-a's XDG base);
//  2. plants a stray legacy stub via a raw `git worktree add` of the eco repo
//     at <eco-root>/.grove-worktrees/anchored — exactly the orphan shape;
//  3. runs finish --prune-worktree and asserts BOTH the anchored container AND
//     the stray legacy stub are gone, and the stub is deregistered from git.
var AnchorRegistryReapStubScenario = harness.NewScenario(
	"anchor-registry-reap-stub",
	"plan finish --prune-worktree reaps a stray legacy .grove-worktrees/<name> superproject stub (no registry entry) alongside the anchored container.",
	[]string{"xdg", "worktree", "anchor", "registry", "plan", "finish", "lifecycle"},
	[]harness.Step{
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "llm"},
		),

		harness.NewStep("Setup realpath-rooted ecosystem with two sub-repos", func(ctx *harness.Context) error {
			gitRoot, repoDirs, _, err := setupEcosystemEnvironment(ctx, "anchor-eco-reap", []string{"svc-a", "svc-b"})
			if err != nil {
				return err
			}
			ctx.Set("git_root", gitRoot)
			ctx.Set("anchor_dir", repoDirs["svc-a"])
			return nil
		}),

		harness.NewStep("Init anchored plan with --anchor svc-a", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")

			cmd := ctx.Bin("plan", "init", "anchored",
				"--worktree=anchored",
				"--anchor", "svc-a")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init --anchor failed: %w", err)
			}

			anchorDir := ctx.GetString("anchor_dir")
			anchoredPath := expectedWorktreePath(ctx, anchorDir, "anchored", true /* XDG */)
			ctx.Set("worktree_path", anchoredPath)
			if err := fs.AssertExists(anchoredPath); err != nil {
				return fmt.Errorf("anchored worktree should exist under anchor's XDG base at %s: %w", anchoredPath, err)
			}
			return nil
		}),

		harness.NewStep("Plant a stray legacy .grove-worktrees/anchored superproject stub", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			// The orphan shape: a raw `git worktree add` of the ecosystem
			// superproject at the in-repo legacy location, with NO registry
			// entry. --detach avoids colliding with the `anchored` branch that
			// plan init already created for the real worktree.
			legacyStub := expectedWorktreePath(ctx, gitRoot, "anchored", false /* legacy */)
			ctx.Set("legacy_stub", legacyStub)

			add := exec.Command("git", "-C", gitRoot, "worktree", "add", "--detach", legacyStub)
			if out, err := add.CombinedOutput(); err != nil {
				return fmt.Errorf("planting legacy stub via git worktree add: %v\n%s", err, out)
			}
			if err := fs.AssertExists(legacyStub); err != nil {
				return fmt.Errorf("legacy stub should exist after planting at %s: %w", legacyStub, err)
			}
			return nil
		}),

		harness.NewStep("finish --prune-worktree reaps BOTH the anchored container and the stray legacy stub", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			worktreePath := ctx.GetString("worktree_path")
			legacyStub := ctx.GetString("legacy_stub")

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

			// The anchored (registry-tracked) container is gone via the normal
			// prune path...
			if err := fs.AssertNotExists(worktreePath); err != nil {
				return fmt.Errorf("anchored worktree dir should be pruned: %w", err)
			}
			// ...and the stray legacy stub is reaped by the defensive cleanup.
			if err := fs.AssertNotExists(legacyStub); err != nil {
				return fmt.Errorf("stray legacy stub should be reaped by finish: %w", err)
			}
			// And git no longer registers the stub as a linked worktree.
			list := exec.Command("git", "-C", gitRoot, "worktree", "list", "--porcelain")
			out, lerr := list.Output()
			if lerr != nil {
				return fmt.Errorf("git worktree list: %w", lerr)
			}
			if worktreeListContainsLegacy(string(out), legacyStub) {
				return fmt.Errorf("legacy stub should be deregistered from git, still listed:\n%s", out)
			}
			return nil
		}),
	},
)

// worktreeListContainsLegacy reports whether the porcelain worktree list still
// registers a worktree at target (symlink-resolved comparison).
func worktreeListContainsLegacy(porcelain, target string) bool {
	canon := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Clean(r)
		}
		return filepath.Clean(p)
	}
	want := canon(target)
	for _, line := range splitPorcelainLines(porcelain) {
		if len(line) > len("worktree ") && line[:len("worktree ")] == "worktree " {
			if canon(line[len("worktree "):]) == want {
				return true
			}
		}
	}
	return false
}

func splitPorcelainLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
