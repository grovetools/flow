package scenarios

import (
	"fmt"
	"path/filepath"

	"github.com/grovetools/core/config"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// ClaudeSettingsSyncScenario is the end-to-end coverage for the
// claude-settings-sync feature: the `[claude]` grove.toml profile
// (permissions.allow + sandbox{enabled, filesystem.allowWrite,
// network.allowedDomains}) must propagate into every workspace and worktree's
// .claude/settings.local.json when `grove-anthropic sync-settings --all` runs,
// additively (never clobbering user edits) and — for ecosystem-level (multi-repo)
// worktrees — as the UNION of every member repo's [claude] block.
//
// Fixture shape (built on setupEcosystemEnvironment):
//
//	~/code/claude-eco/grove.yml      [claude] permissions.allow=["Bash(git:*)"]
//	                                          sandbox.enabled=true
//	                                          sandbox.filesystem.allowWrite=["/tmp/eco-shared"]
//	~/code/claude-eco/svc-a/grove.yml [claude] permissions.allow=["Bash(make:*)"]
//	                                           sandbox.network.allowedDomains=["api.example.com"]
//	~/code/claude-eco/svc-b/grove.yml (no [claude] — inherits the ecosystem block)
//
// A `plan init --sibling-workspaces=svc-a,svc-b --worktree=wt1` builds the
// ecosystem-level XDG worktree (wt1) whose member repos are linked as per-repo
// sub-worktrees; the primary checkouts (~/code/claude-eco{,/svc-a,/svc-b}) are
// the per-repo workspaces. `sync-settings --all` must seed BOTH kinds.
//
// The ecosystem worktree's settings must show the union: the ecosystem's
// "Bash(git:*)"/enabled=true AND svc-a's distinct "Bash(make:*)"/"api.example.com".
// A user-added key pre-seeded into wt1's settings.local.json must survive the
// sync (the no-clobber, additive-only contract).
//
// REGRESSION GUARD for the ecosystem-ROOT seeding fix (grove-anthropic 46bdf5b,
// daemon b45fb58): the primary checkout (~/code/claude-eco) is a
// workspace.KindEcosystemRoot node that DiscoverAll surfaces under
// result.Ecosystems (never result.Projects), so the pre-fix projects-only loop
// skipped it and its .claude/settings.local.json went unwritten. The final step
// asserts the ROOT's settings (a) exist, (b) carry the ecosystem's own block,
// and (c) carry svc-a's member union — proving sync-settings now both
// enumerates ecosystems AND resolves each root's member repos.
var ClaudeSettingsSyncScenario = harness.NewScenario(
	"claude-settings-sync",
	"sync-settings --all propagates the [claude] grove.toml profile (member-repo union, ecosystem worktree, no-clobber) into every workspace/worktree's settings.local.json.",
	[]string{"claude", "settings-sync", "ecosystem", "worktree", "sandbox"},
	[]harness.Step{
		// grove/cx/llm are mocked so plan init's incidental ecosystem calls are
		// safe no-ops; grove-anthropic is invoked as the REAL ecosystem binary.
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "llm"},
		),

		harness.NewStep("Setup ecosystem with [claude] blocks in the root and one member repo", func(ctx *harness.Context) error {
			gitRoot, repoDirs, _, err := setupEcosystemEnvironment(ctx, "claude-eco", []string{"svc-a", "svc-b"})
			if err != nil {
				return err
			}
			ctx.Set("git_root", gitRoot)
			ctx.Set("svc_a_dir", repoDirs["svc-a"])
			ctx.Set("svc_b_dir", repoDirs["svc-b"])

			// Re-write the ecosystem-root grove.yml with a [claude] block (and
			// re-commit) so the committed config lands in the wt1 worktree.
			enabled := true
			ecoCfg := &config.Config{
				Name:       "claude-eco",
				Version:    "1.0",
				Workspaces: []string{"svc-a", "svc-b"},
				Extensions: map[string]interface{}{
					"claude": map[string]interface{}{
						"permissions": map[string]interface{}{
							"allow": []string{"Bash(git:*)"},
						},
						"sandbox": map[string]interface{}{
							"enabled": enabled,
							"filesystem": map[string]interface{}{
								"allowWrite": []string{"/tmp/eco-shared"},
							},
						},
					},
				},
			}
			if err := fs.WriteGroveConfig(gitRoot, ecoCfg); err != nil {
				return err
			}
			if err := git.Add(gitRoot, "grove.yml"); err != nil {
				return err
			}
			if err := git.Commit(gitRoot, "Add [claude] block to ecosystem grove.yml"); err != nil {
				return err
			}

			// Each member becomes a Go module (committed) so plan init's
			// sibling-workspace go.work wiring is satisfied.
			for repo, dir := range repoDirs {
				goMod := fmt.Sprintf("module example.com/%s\n\ngo 1.23.5\n", repo)
				if err := fs.WriteString(filepath.Join(dir, "go.mod"), goMod); err != nil {
					return err
				}
				if err := git.Add(dir, "go.mod"); err != nil {
					return err
				}
				if err := git.Commit(dir, "Add go.mod"); err != nil {
					return err
				}
			}

			// svc-a carries a DISTINCT [claude] block so the ecosystem-worktree
			// union is observable; svc-b carries none (it inherits the ecosystem
			// block via the config cascade).
			svcACfg := &config.Config{
				Name:    "svc-a",
				Version: "1.0",
				Extensions: map[string]interface{}{
					"claude": map[string]interface{}{
						"permissions": map[string]interface{}{
							"allow": []string{"Bash(make:*)"},
						},
						"sandbox": map[string]interface{}{
							"network": map[string]interface{}{
								"allowedDomains": []string{"api.example.com"},
							},
						},
					},
				},
			}
			if err := fs.WriteGroveConfig(repoDirs["svc-a"], svcACfg); err != nil {
				return err
			}
			if err := git.Add(repoDirs["svc-a"], "grove.yml"); err != nil {
				return err
			}
			if err := git.Commit(repoDirs["svc-a"], "Add distinct [claude] block to svc-a"); err != nil {
				return err
			}

			// Source go.work so plan init's configureGoWorkspace has a base.
			goWork := "go 1.23.5\n\nuse (\n\t./svc-a\n\t./svc-b\n)\n"
			return fs.WriteString(filepath.Join(gitRoot, "go.work"), goWork)
		}),

		harness.NewStep("Create the ecosystem-level (multi-repo) worktree via plan init", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")

			// --worktree REQUIRES the = form (NoOptDefVal); mirror the sibling
			// lifecycle scenario.
			cmd := ctx.Bin("plan", "init", "claude-sync-plan",
				"--sibling-workspaces=svc-a,svc-b",
				"--worktree=wt1")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			worktreePath := expectedWorktreePath(ctx, gitRoot, "wt1", true /* sibling/XDG */)
			ctx.Set("worktree_path", worktreePath)

			if err := fs.AssertExists(worktreePath); err != nil {
				return fmt.Errorf("ecosystem worktree should exist at %s: %w", worktreePath, err)
			}
			// The member repos are linked as per-repo sub-worktrees.
			for _, repo := range []string{"svc-a", "svc-b"} {
				if err := fs.AssertExists(filepath.Join(worktreePath, repo, ".git")); err != nil {
					return fmt.Errorf("member repo %q should be linked into the worktree: %w", repo, err)
				}
			}
			return nil
		}),

		harness.NewStep("Pre-seed a user-added key into the worktree settings (no-clobber fixture)", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")
			// Overwrite any creation-time-seeded file with user content so the
			// post-sync assertions prove BOTH that sync re-adds the managed
			// entries AND that the user's entries survive untouched.
			userSettings := `{
  "permissions": {
    "allow": [
      "UserAdded(custom:*)"
    ]
  },
  "customUserKey": "keepme"
}
`
			settingsPath := filepath.Join(worktreePath, ".claude", "settings.local.json")
			ctx.Set("worktree_settings_path", settingsPath)
			return fs.WriteString(settingsPath, userSettings)
		}),

		harness.NewStep("Run grove-anthropic sync-settings --all", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")

			gaBin, err := harness.FindRealBinary("grove-anthropic")
			if err != nil {
				return fmt.Errorf("resolving grove-anthropic binary: %w", err)
			}

			// ctx.Command injects the sandbox HOME/XDG env so sync-settings'
			// DiscoverAll + worktreeregistry.Reconcile enumerate the sandbox's
			// workspaces and worktrees (not the host's).
			cmd := ctx.Command(gaBin, "sync-settings", "--all")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("grove-anthropic sync-settings --all failed: %w", err)
			}
			return nil
		}),

		harness.NewStep("Assert the ecosystem worktree carries the unioned profile and the user key survives", func(ctx *harness.Context) error {
			settingsPath := ctx.GetString("worktree_settings_path")

			if err := fs.AssertExists(settingsPath); err != nil {
				return fmt.Errorf("ecosystem worktree settings.local.json should exist: %w", err)
			}
			return ctx.Verify(func(v *verify.Collector) {
				// Ecosystem-root contributions.
				v.Equal("ecosystem permission allow seeded", nil, fs.AssertContains(settingsPath, "Bash(git:*)"))
				v.Equal("ecosystem sandbox.enabled seeded", nil, fs.AssertContains(settingsPath, "\"enabled\": true"))
				// Member-repo (svc-a) UNION contributions — the heart of the
				// ecosystem-worktree union behavior.
				v.Equal("member permission allow unioned", nil, fs.AssertContains(settingsPath, "Bash(make:*)"))
				v.Equal("member allowedDomains unioned", nil, fs.AssertContains(settingsPath, "api.example.com"))
				// No-clobber: the pre-existing user entries survive the sync.
				v.Equal("user-added permission survives", nil, fs.AssertContains(settingsPath, "UserAdded(custom:*)"))
				v.Equal("unrelated user key survives", nil, fs.AssertContains(settingsPath, "customUserKey"))
			})
		}),

		harness.NewStep("Assert the ecosystem ROOT and the per-repo workspaces were also seeded", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			svcADir := ctx.GetString("svc_a_dir")

			ecoSettings := filepath.Join(gitRoot, ".claude", "settings.local.json")
			svcASettings := filepath.Join(svcADir, ".claude", "settings.local.json")

			return ctx.Verify(func(v *verify.Collector) {
				// REGRESSION GUARD (grove-anthropic 46bdf5b / daemon b45fb58): the
				// ecosystem-root primary checkout (`~/code/claude-eco`) is a
				// workspace.KindEcosystemRoot node — DiscoverAll surfaces it under
				// result.Ecosystems, NOT result.Projects. The pre-fix sync-settings
				// only walked result.Projects (plus the worktree registry), so the
				// primary checkout was NEVER added as a seed target and this file
				// did not exist. An agent launched at the ecosystem root reads its
				// own .claude/settings.local.json, so it MUST be seeded. Its mere
				// existence here proves sync-settings now enumerates ecosystems.
				v.Equal("eco-root (KindEcosystemRoot) settings created", nil, fs.AssertExists(ecoSettings))
				v.Equal("eco-root carries ecosystem allow", nil, fs.AssertContains(ecoSettings, "Bash(git:*)"))
				v.Equal("eco-root carries sandbox.enabled", nil, fs.AssertContains(ecoSettings, "\"enabled\": true"))

				// The fix resolves each ecosystem root's member repos from the
				// sub-projects grouped beneath it (ParentEcosystemPath), so the root
				// target receives the SAME member union an XDG ecosystem worktree
				// gets. Assert svc-a's DISTINCT [claude] contributions reached the
				// ROOT's settings — not just the ecosystem's own block. This is what
				// pins the member-resolution half of the eco-root fix; without it
				// the root could be seeded with an empty member set and still pass.
				v.Equal("eco-root unions member allow", nil, fs.AssertContains(ecoSettings, "Bash(make:*)"))
				v.Equal("eco-root unions member allowedDomains", nil, fs.AssertContains(ecoSettings, "api.example.com"))

				// The svc-a primary checkout resolves its own [claude] block plus
				// the cascaded ecosystem block.
				v.Equal("svc-a workspace settings created", nil, fs.AssertExists(svcASettings))
				v.Equal("svc-a carries member allowedDomains", nil, fs.AssertContains(svcASettings, "api.example.com"))
				v.Equal("svc-a carries member allow", nil, fs.AssertContains(svcASettings, "Bash(make:*)"))
			})
		}),
	},
)
