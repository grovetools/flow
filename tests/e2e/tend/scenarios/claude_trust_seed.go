package scenarios

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/grovetools/core/util/pathutil"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// ClaudeTrustSeedScenario proves that provisioning a worktree via `flow plan
// init --worktree` pre-seeds Claude Code folder-trust for that worktree, so an
// agent launched inside it never stalls at the interactive folder-trust prompt
// ("Is this a project you created or one you trust?").
//
// Trust lives per-exact-path in ~/.claude.json under
// projects["<abs>"].hasTrustDialogAccepted = true (see core/pkg/claudetrust).
// workspace.Prepare canonicalizes the container + each <container>/<repo> subdir
// and calls claudetrust.SeedTrust. This scenario asserts the resulting
// ~/.claude.json (in the SANDBOX home — never the developer's real file) trusts
// the canonical worktree paths.
//
// Isolation: the whole run executes against the harness sandbox HOME/XDG, so
// SeedTrust's os.UserHomeDir() resolves to the sandbox home and the trust write
// lands there. The real ~/.claude.json is never read or written. The trust key
// is recomputed here with the SAME pathutil.CanonicalPath the production code
// uses, so the expectation never drifts from the real macOS case/symlink
// resolution.
//
// SCOPE NOTE: this exercises the in-process fast path (Prepare runs unsandboxed
// in tend, so the direct SeedTrust write succeeds). The daemon-delegated
// fallback — used when Prepare runs inside the OS sandbox and the ~/.claude.json
// write is denied — is covered by the daemon handler unit test
// (daemon/internal/daemon/server/trust_handler_test.go), which proves the
// daemon derives the trusted paths from the worktree registry and ignores
// caller-supplied paths. Standing up a real OS-sandboxed daemon inside tend is
// out of scope for this suite.
var ClaudeTrustSeedScenario = harness.NewScenario(
	"claude-trust-seed",
	"flow plan init --worktree pre-seeds Claude folder-trust (~/.claude.json hasTrustDialogAccepted) for the new worktree's canonical paths.",
	[]string{"claude", "trust", "worktree", "sandbox"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "trust-project")
			if err != nil {
				return err
			}
			// setupDefaultEnvironment already git-inits projectDir and writes
			// grove.yml; add an initial commit so `git worktree add` can branch.
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Trust Project\n"); err != nil {
				return err
			}
			return nil
		}),

		// grove is mocked so plan init's incidental ecosystem calls are safe
		// no-ops; no agent is launched, so claude/tmux mocks are unnecessary.
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),

		harness.NewStep("Initialize a plan with a worktree", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "init", "trust-plan", "--worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			// Single-repo (non-ecosystem) worktrees use the legacy in-repo
			// layout: <gitRoot>/.grove-worktrees/<name> (mirrors
			// AgentWorktreeLifecycleScenario).
			worktreePath := expectedWorktreePath(ctx, projectDir, "trust-plan", false /* legacy */)
			ctx.Set("worktree_path", worktreePath)
			return fs.AssertExists(worktreePath)
		}),

		harness.NewStep("Assert the worktree is folder-trusted in the sandbox ~/.claude.json", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")

			// The binary writes ~/.claude.json into the sandbox HOME (the env
			// ctx.Bin injects); read it from the same place.
			claudeJSON := filepath.Join(ctx.HomeDir(), ".claude.json")
			if err := fs.AssertExists(claudeJSON); err != nil {
				return fmt.Errorf("sandbox ~/.claude.json should have been created by trust seeding: %w", err)
			}

			data, err := os.ReadFile(claudeJSON) //nolint:gosec // sandbox path
			if err != nil {
				return fmt.Errorf("reading sandbox ~/.claude.json: %w", err)
			}
			var root struct {
				Projects map[string]struct {
					HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
				} `json:"projects"`
			}
			if err := json.Unmarshal(data, &root); err != nil {
				return fmt.Errorf("parsing sandbox ~/.claude.json: %w", err)
			}

			// Recompute the trust key exactly as production code does. The
			// container and the repo's own subdir are both scoped agent cwds, so
			// Prepare seeds both; assert at least the container is trusted.
			containerKey, err := pathutil.CanonicalPath(worktreePath)
			if err != nil {
				return fmt.Errorf("canonicalizing worktree path: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("worktree container is folder-trusted",
					true, root.Projects[containerKey].HasTrustDialogAccepted)
			})
		}),
	},
)
