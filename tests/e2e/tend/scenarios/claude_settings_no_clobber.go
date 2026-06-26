package scenarios

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/grovetools/core/config"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// ClaudeSettingsNoClobberScenario makes AXIS C (the seed-into-the-file merge)
// observable — the safety contract of the leaf seeder
// (core/pkg/claudenotebook/seeder.go:92 SeedSettings). Four distinct guarantees:
//
//  1. ADDITIVE arrays / preserved keys: a pre-existing user permission rule and
//     an unrelated top-level key survive verbatim; grove's rule is APPENDED
//     (mergeStringArray, seeder.go:217 — union, never clobber).
//  2. BOOL OVERWRITE vs nil untouched: grove's sandbox.enabled=true OVERWRITES a
//     user's existing false (mergeBool, seeder.go:194); but a bool grove leaves
//     unset (sandbox.failIfUnavailable) is left exactly as the user wrote it.
//  3. MALFORMED file is never clobbered: a target whose settings.local.json is
//     unparseable JSON is left BYTE-IDENTICAL (seeder.go:125-127 returns an error
//     without writing). The eco-root workspace is used as that target.
//  4. DRY-RUN writes nothing: `sync-settings --dry-run` leaves both the valid and
//     the malformed file unchanged (content + mtime), proving reportDryRun
//     (grove-anthropic/cmd/sync_settings.go:232) is read-only.
//
// Because the malformed target makes `sync-settings` exit non-zero (one of N
// targets failed, sync_settings.go:223), the command runs are NOT asserted for
// success — the contract under test is the on-disk effect, not the exit code.
var ClaudeSettingsNoClobberScenario = harness.NewScenario(
	"claude-settings-no-clobber",
	"Axis C: seeding into settings.local.json is additive (user rules + unrelated keys survive, grove rules appended), bools overwrite while unset bools are left untouched, a malformed file is left byte-identical, and --dry-run writes nothing.",
	[]string{"claude", "settings-sync", "no-clobber", "dry-run", "safety"},
	[]harness.Step{
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "llm"},
		),

		harness.NewStep("Setup ecosystem with a [claude] block (allow + sandbox.enabled=true)", func(ctx *harness.Context) error {
			gitRoot, repoDirs, _, err := setupEcosystemEnvironment(ctx, "claude-eco", []string{"svc-a", "svc-b"})
			if err != nil {
				return err
			}
			ctx.Set("git_root", gitRoot)

			enabledTrue := true
			ecoCfg := &config.Config{
				Name:       "claude-eco",
				Version:    "1.0",
				Workspaces: []string{"svc-a", "svc-b"},
				Extensions: map[string]interface{}{
					"claude": map[string]interface{}{
						"permissions": map[string]interface{}{
							"allow": []string{"GroveAppended(g:*)"},
						},
						"sandbox": map[string]interface{}{
							"enabled": enabledTrue,
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

			goWork := "go 1.23.5\n\nuse (\n\t./svc-a\n\t./svc-b\n)\n"
			return fs.WriteString(filepath.Join(gitRoot, "go.work"), goWork)
		}),

		harness.NewStep("Create the ecosystem worktree via plan init", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")

			cmd := ctx.Bin("plan", "init", "claude-noclobber-plan",
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
			return nil
		}),

		harness.NewStep("Pre-author a valid user file in the worktree and a malformed file in the eco-root", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			worktreePath := ctx.GetString("worktree_path")

			// Valid user content with: a user rule (must survive + grove appended),
			// an unrelated top-level key (must survive), sandbox.enabled=false (grove
			// must OVERWRITE to true), and sandbox.failIfUnavailable=false (grove
			// leaves unset -> must be left untouched).
			userSettings := `{
  "permissions": {
    "allow": [
      "UserKept(u:*)"
    ]
  },
  "sandbox": {
    "enabled": false,
    "failIfUnavailable": false
  },
  "unrelatedTopKey": "preserve-me"
}
`
			worktreeSettings := filepath.Join(worktreePath, ".claude", "settings.local.json")
			ctx.Set("worktree_settings_path", worktreeSettings)
			if err := fs.WriteString(worktreeSettings, userSettings); err != nil {
				return err
			}

			// Malformed (unparseable) JSON in the eco-root workspace target. The
			// seeder must refuse to clobber it and leave it byte-identical.
			malformed := "{ this is : not valid json,,, ]"
			ecoRootSettings := filepath.Join(gitRoot, ".claude", "settings.local.json")
			ctx.Set("eco_root_settings_path", ecoRootSettings)
			ctx.Set("eco_root_malformed", malformed)
			return fs.WriteString(ecoRootSettings, malformed)
		}),

		harness.NewStep("Run sync-settings --dry-run and assert it writes nothing (content + mtime unchanged)", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			worktreeSettings := ctx.GetString("worktree_settings_path")
			ecoRootSettings := ctx.GetString("eco_root_settings_path")

			beforeWorktree, err := fs.ReadString(worktreeSettings)
			if err != nil {
				return fmt.Errorf("reading worktree settings before dry-run: %w", err)
			}
			wtInfoBefore, err := os.Stat(worktreeSettings)
			if err != nil {
				return fmt.Errorf("stat worktree settings before dry-run: %w", err)
			}
			beforeEcoRoot, err := fs.ReadString(ecoRootSettings)
			if err != nil {
				return fmt.Errorf("reading eco-root settings before dry-run: %w", err)
			}

			gaBin, err := harness.FindRealBinary("grove-anthropic")
			if err != nil {
				return fmt.Errorf("resolving grove-anthropic binary: %w", err)
			}
			// Not asserting success: the malformed eco-root makes dry-run report a
			// failure for that one target. We only care that nothing was written.
			cmd := ctx.Command(gaBin, "sync-settings", "--dry-run")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			afterWorktree, err := fs.ReadString(worktreeSettings)
			if err != nil {
				return fmt.Errorf("reading worktree settings after dry-run: %w", err)
			}
			wtInfoAfter, err := os.Stat(worktreeSettings)
			if err != nil {
				return fmt.Errorf("stat worktree settings after dry-run: %w", err)
			}
			afterEcoRoot, err := fs.ReadString(ecoRootSettings)
			if err != nil {
				return fmt.Errorf("reading eco-root settings after dry-run: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("dry-run leaves worktree content unchanged", beforeWorktree, afterWorktree)
				v.True("dry-run leaves worktree mtime unchanged", wtInfoBefore.ModTime().Equal(wtInfoAfter.ModTime()))
				v.Equal("dry-run leaves malformed eco-root content unchanged", beforeEcoRoot, afterEcoRoot)
			})
		}),

		harness.NewStep("Run sync-settings --all (apply) then assert no-clobber, append, bool overwrite, and malformed safety", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			worktreeSettings := ctx.GetString("worktree_settings_path")
			ecoRootSettings := ctx.GetString("eco_root_settings_path")
			malformed := ctx.GetString("eco_root_malformed")

			gaBin, err := harness.FindRealBinary("grove-anthropic")
			if err != nil {
				return fmt.Errorf("resolving grove-anthropic binary: %w", err)
			}
			// Not asserting success: the malformed eco-root target intentionally
			// fails the run; the contract under test is the on-disk result.
			cmd := ctx.Command(gaBin, "sync-settings", "--all")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			worktreeContent, err := fs.ReadString(worktreeSettings)
			if err != nil {
				return fmt.Errorf("reading worktree settings after apply: %w", err)
			}
			ecoRootContent, err := fs.ReadString(ecoRootSettings)
			if err != nil {
				return fmt.Errorf("reading eco-root settings after apply: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				// Additive: user entries preserved, grove rule appended.
				v.Contains("user permission rule survives", worktreeContent, "UserKept(u:*)")
				v.Contains("unrelated top-level key survives", worktreeContent, "unrelatedTopKey")
				v.Contains("unrelated key value survives", worktreeContent, "preserve-me")
				v.Contains("grove rule appended", worktreeContent, "GroveAppended(g:*)")

				// Bool OVERWRITE: grove's enabled=true replaces the user's false.
				v.Contains("grove bool overwrites user false", worktreeContent, "\"enabled\": true")
				v.NotContains("user sandbox.enabled=false overwritten", worktreeContent, "\"enabled\": false")
				// Unset grove bool leaves the user's value untouched.
				v.Contains("unset grove bool leaves user failIfUnavailable untouched", worktreeContent, "\"failIfUnavailable\": false")

				// Malformed safety: byte-identical to the original.
				v.Equal("malformed eco-root file left byte-identical", malformed, ecoRootContent)
			})
		}),
	},
)
