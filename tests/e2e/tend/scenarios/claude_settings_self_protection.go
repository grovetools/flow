package scenarios

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/util/pathutil"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// protectionView is the slice of settings.local.json the self-protection
// assertions care about.
type protectionView struct {
	Permissions struct {
		Deny []string `json:"deny"`
	} `json:"permissions"`
	Sandbox struct {
		Filesystem struct {
			DenyWrite []string `json:"denyWrite"`
		} `json:"filesystem"`
	} `json:"sandbox"`
}

func parseProtection(content string) (protectionView, error) {
	var v protectionView
	err := json.Unmarshal([]byte(content), &v)
	return v, err
}

func sliceContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// anyHasSuffix reports whether any element ends with suffix.
func anyHasSuffix(items []string, suffix string) bool {
	for _, s := range items {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

// anyHasPrefix reports whether any element starts with prefix.
func anyHasPrefix(items []string, prefix string) bool {
	for _, s := range items {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// sandboxConfigGrove returns the sandboxed grove global-config dir
// (XDG_CONFIG_HOME/grove, canonicalized) the seeder protects, plus the real-home
// ~/.config/grove that it must NOT touch. The tend harness injects
// XDG_CONFIG_HOME=ctx.ConfigDir() into every command, so the subprocess seeder
// resolves paths.ConfigDir() inside the sandbox — never the developer's home.
func sandboxConfigGrove(ctx *harness.Context) (sandboxBase, realHomeGrove string) {
	base := ctx.ConfigDir()
	if canon, err := pathutil.CanonicalPath(base); err == nil {
		base = canon
	}
	realHomeGrove = ""
	if home, err := os.UserHomeDir(); err == nil {
		realHomeGrove = filepath.Join(home, ".config", "grove")
	}
	return base, realHomeGrove
}

// ClaudeSettingsSelfProtectionScenario makes the [claude] protectConfig toggle
// observable end-to-end through the real `grove-anthropic sync-settings`. It
// proves the security boundary AND its reversibility / escape-hatch:
//
//  1. APPLIED: with protectConfig=true the worktree's settings.local.json gains
//     sandbox.filesystem.denyWrite paths AND permissions.deny Edit/Write/MultiEdit
//     rules for the worktree + member-repo grove config files and the global
//     ~/.config/grove dir (the dir rule uses a /** subtree glob; the file rules
//     match the file exactly).
//  2. PATHS-NOT-TOOLS: not a single deny rule denies a Bash(<tool>:*) invocation —
//     protection targets config FILE paths only, so the agent can still run grove.
//  3. NO-CLOBBER: a pre-existing user permissions.deny rule (Read(/etc/passwd))
//     and user sandbox denyWrite path (/var/log) survive verbatim.
//  4. REVERSIBLE: flipping protectConfig=false strips ONLY the grove-owned entries
//     (via removeFromStringArray) while leaving the user entries intact.
//  5. ESCAPE HATCH: with protectConfig=true but GROVE_UNLOCK_CONFIG=1 in the launch
//     env, the seed behaves as off — the grove-owned entries are stripped.
//  6. REGRESSION (ShouldSeed): a SECOND ecosystem whose ONLY [claude] signal is
//     protectConfig=true (IsEmpty()==true) STILL gets protection written — proving
//     the upstream IsEmpty guards were widened to ShouldSeed and don't drop a
//     protect-only config before the seeder runs.
var ClaudeSettingsSelfProtectionScenario = harness.NewScenario(
	"claude-settings-self-protection",
	"protectConfig toggle: seeding denies writes to grove config files (sandbox denyWrite + permissions.deny Edit/Write) without denying any tool invocation, preserves user deny rules, strips only grove-owned entries on false, honors GROVE_UNLOCK_CONFIG, and still applies for a protectConfig-only config (ShouldSeed).",
	[]string{"claude", "settings-sync", "self-protection", "security", "sandbox"},
	[]harness.Step{
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "llm"},
		),

		harness.NewStep("Setup ecosystem with [claude] protectConfig=true + user deny/denyWrite", func(ctx *harness.Context) error {
			gitRoot, repoDirs, _, err := setupEcosystemEnvironment(ctx, "claude-protect-eco", []string{"svc-a", "svc-b"})
			if err != nil {
				return err
			}
			ctx.Set("git_root", gitRoot)

			ecoCfg := &config.Config{
				Name:       "claude-protect-eco",
				Version:    "1.0",
				Workspaces: []string{"svc-a", "svc-b"},
				Extensions: map[string]interface{}{
					"claude": map[string]interface{}{
						"protectConfig": true,
						"permissions": map[string]interface{}{
							"deny": []string{"Read(/etc/passwd)"},
						},
						"sandbox": map[string]interface{}{
							"enabled": true,
							"filesystem": map[string]interface{}{
								"denyWrite": []string{"/var/log"},
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
			if err := git.Commit(gitRoot, "Add [claude] protectConfig block"); err != nil {
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

			cmd := ctx.Bin("plan", "init", "claude-protect-plan",
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

			// Canonicalize like the seeder so expected paths/rules match on macOS
			// (the XDG worktree is a /var -> /private/var symlink).
			canonWt, cerr := pathutil.CanonicalPath(worktreePath)
			if cerr != nil {
				canonWt = worktreePath
			}
			ctx.Set("canon_worktree", canonWt)
			return nil
		}),

		harness.NewStep("Run sync-settings --all (apply) and assert protection applied + no-clobber + paths-not-tools", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			worktreePath := ctx.GetString("worktree_path")
			canonWt := ctx.GetString("canon_worktree")
			settingsPath := filepath.Join(worktreePath, ".claude", "settings.local.json")

			gaBin, err := harness.FindRealBinary("grove-anthropic")
			if err != nil {
				return fmt.Errorf("resolving grove-anthropic binary: %w", err)
			}
			cmd := ctx.Command(gaBin, "sync-settings", "--all")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("sync-settings --all failed: %w", err)
			}

			content, err := fs.ReadString(settingsPath)
			if err != nil {
				return fmt.Errorf("reading worktree settings after apply: %w", err)
			}
			pv, err := parseProtection(content)
			if err != nil {
				return fmt.Errorf("parsing worktree settings JSON: %w", err)
			}

			wtConfigPath := filepath.Join(canonWt, "grove.toml")
			memberConfigPath := filepath.Join(canonWt, "svc-a", "grove.toml")
			wtFileRule := "Edit(//" + strings.TrimPrefix(filepath.ToSlash(wtConfigPath), "/") + ")"
			wtWriteRule := "Write(//" + strings.TrimPrefix(filepath.ToSlash(wtConfigPath), "/") + ")"
			sandboxBase, realHomeGrove := sandboxConfigGrove(ctx)

			return ctx.Verify(func(v *verify.Collector) {
				// (1) APPLIED: sandbox denyWrite carries the grove config paths.
				v.True("denyWrite contains worktree grove.toml path", sliceContains(pv.Sandbox.Filesystem.DenyWrite, wtConfigPath))
				v.True("denyWrite contains member-repo grove.toml path", sliceContains(pv.Sandbox.Filesystem.DenyWrite, memberConfigPath))
				// The global config dir is the SANDBOXED XDG_CONFIG_HOME/grove, NOT
				// the developer's real ~/.config/grove — proving hermeticity.
				v.True("denyWrite contains the sandboxed global config dir", anyHasPrefix(pv.Sandbox.Filesystem.DenyWrite, sandboxBase))
				if realHomeGrove != "" {
					v.True("denyWrite must NOT reference the real ~/.config/grove", !sliceContains(pv.Sandbox.Filesystem.DenyWrite, realHomeGrove))
				}

				// (1) APPLIED: permissions.deny carries the Edit rules — file paths
				// match exactly (no glob); the global dir uses a /** subtree glob.
				v.True("deny contains exact-file Edit rule for worktree grove.toml", sliceContains(pv.Permissions.Deny, wtFileRule))
				v.True("deny contains exact-file Write rule for worktree grove.toml", sliceContains(pv.Permissions.Deny, wtWriteRule))
				v.True("deny contains a /** subtree glob rule for the global config dir", anyHasSuffix(pv.Permissions.Deny, "/grove/**)"))

				// (2) PATHS-NOT-TOOLS: never deny a Bash(<tool>:*) invocation.
				for _, r := range pv.Permissions.Deny {
					v.True("deny rule must not target a Bash invocation: "+r, !strings.Contains(r, "Bash("))
				}

				// (3) NO-CLOBBER: user entries survive verbatim.
				v.True("user deny rule Read(/etc/passwd) survives", sliceContains(pv.Permissions.Deny, "Read(/etc/passwd)"))
				v.True("user denyWrite /var/log survives", sliceContains(pv.Sandbox.Filesystem.DenyWrite, "/var/log"))
			})
		}),

		harness.NewStep("Flip protectConfig=false, re-sync, assert ONLY grove-owned entries stripped", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			worktreePath := ctx.GetString("worktree_path")
			canonWt := ctx.GetString("canon_worktree")
			settingsPath := filepath.Join(worktreePath, ".claude", "settings.local.json")

			// Rewrite the worktree's own grove.yml (the seeder loads config from the
			// worktree path) with protectConfig=false, keeping the user arrays.
			offCfg := &config.Config{
				Name:       "claude-protect-eco",
				Version:    "1.0",
				Workspaces: []string{"svc-a", "svc-b"},
				Extensions: map[string]interface{}{
					"claude": map[string]interface{}{
						"protectConfig": false,
						"permissions": map[string]interface{}{
							"deny": []string{"Read(/etc/passwd)"},
						},
						"sandbox": map[string]interface{}{
							"enabled": true,
							"filesystem": map[string]interface{}{
								"denyWrite": []string{"/var/log"},
							},
						},
					},
				},
			}
			if err := fs.WriteGroveConfig(worktreePath, offCfg); err != nil {
				return fmt.Errorf("rewriting worktree grove.yml to protectConfig=false: %w", err)
			}

			gaBin, err := harness.FindRealBinary("grove-anthropic")
			if err != nil {
				return fmt.Errorf("resolving grove-anthropic binary: %w", err)
			}
			cmd := ctx.Command(gaBin, "sync-settings", "--all")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("sync-settings --all (off) failed: %w", err)
			}

			content, err := fs.ReadString(settingsPath)
			if err != nil {
				return fmt.Errorf("reading worktree settings after off: %w", err)
			}
			pv, err := parseProtection(content)
			if err != nil {
				return fmt.Errorf("parsing worktree settings JSON after off: %w", err)
			}

			wtConfigPath := filepath.Join(canonWt, "grove.toml")
			wtFileRule := "Edit(//" + strings.TrimPrefix(filepath.ToSlash(wtConfigPath), "/") + ")"

			return ctx.Verify(func(v *verify.Collector) {
				// Grove-owned entries are gone.
				v.True("grove-owned denyWrite path stripped", !sliceContains(pv.Sandbox.Filesystem.DenyWrite, wtConfigPath))
				v.True("grove-owned Edit rule stripped", !sliceContains(pv.Permissions.Deny, wtFileRule))
				v.True("global config-dir glob rule stripped", !anyHasSuffix(pv.Permissions.Deny, "/.config/grove/**)"))
				// User entries survive the strip.
				v.True("user deny rule survives the strip", sliceContains(pv.Permissions.Deny, "Read(/etc/passwd)"))
				v.True("user denyWrite survives the strip", sliceContains(pv.Sandbox.Filesystem.DenyWrite, "/var/log"))
			})
		}),

		harness.NewStep("Re-lock then re-sync with GROVE_UNLOCK_CONFIG=1, assert escape hatch strips grove-owned entries", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			worktreePath := ctx.GetString("worktree_path")
			canonWt := ctx.GetString("canon_worktree")
			settingsPath := filepath.Join(worktreePath, ".claude", "settings.local.json")

			// Re-lock: protectConfig=true again.
			onCfg := &config.Config{
				Name:       "claude-protect-eco",
				Version:    "1.0",
				Workspaces: []string{"svc-a", "svc-b"},
				Extensions: map[string]interface{}{
					"claude": map[string]interface{}{
						"protectConfig": true,
						"sandbox":       map[string]interface{}{"enabled": true},
					},
				},
			}
			if err := fs.WriteGroveConfig(worktreePath, onCfg); err != nil {
				return fmt.Errorf("re-locking worktree grove.yml: %w", err)
			}

			gaBin, err := harness.FindRealBinary("grove-anthropic")
			if err != nil {
				return fmt.Errorf("resolving grove-anthropic binary: %w", err)
			}
			// The unlock env var makes the seed behave as off for this launch.
			cmd := ctx.Command(gaBin, "sync-settings", "--all")
			cmd.Dir(gitRoot)
			cmd.Env("GROVE_UNLOCK_CONFIG=1")
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("sync-settings --all (unlock) failed: %w", err)
			}

			content, err := fs.ReadString(settingsPath)
			if err != nil {
				return fmt.Errorf("reading worktree settings after unlock: %w", err)
			}
			pv, err := parseProtection(content)
			if err != nil {
				return fmt.Errorf("parsing worktree settings JSON after unlock: %w", err)
			}

			wtConfigPath := filepath.Join(canonWt, "grove.toml")
			wtFileRule := "Edit(//" + strings.TrimPrefix(filepath.ToSlash(wtConfigPath), "/") + ")"

			return ctx.Verify(func(v *verify.Collector) {
				v.True("unlock env strips grove-owned Edit rule", !sliceContains(pv.Permissions.Deny, wtFileRule))
				v.True("unlock env strips grove-owned denyWrite path", !sliceContains(pv.Sandbox.Filesystem.DenyWrite, wtConfigPath))
			})
		}),

		harness.NewStep("REGRESSION: a protectConfig-ONLY ecosystem still gets protection written (ShouldSeed)", func(ctx *harness.Context) error {
			// Second, independent ecosystem whose ONLY [claude] signal is
			// protectConfig=true — IsEmpty()==true. Without the ShouldSeed widening
			// the upstream guards would drop it and nothing would be written.
			gitRoot, repoDirs, _, err := setupEcosystemEnvironment(ctx, "claude-protect-only-eco", []string{"svc-c"})
			if err != nil {
				return err
			}

			ecoCfg := &config.Config{
				Name:       "claude-protect-only-eco",
				Version:    "1.0",
				Workspaces: []string{"svc-c"},
				Extensions: map[string]interface{}{
					"claude": map[string]interface{}{
						"protectConfig": true,
					},
				},
			}
			if err := fs.WriteGroveConfig(gitRoot, ecoCfg); err != nil {
				return err
			}
			if err := git.Add(gitRoot, "grove.yml"); err != nil {
				return err
			}
			if err := git.Commit(gitRoot, "protectConfig-only [claude] block"); err != nil {
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
			if err := fs.WriteString(filepath.Join(gitRoot, "go.work"), "go 1.23.5\n\nuse ./svc-c\n"); err != nil {
				return err
			}

			cmd := ctx.Bin("plan", "init", "claude-protect-only-plan",
				"--sibling-workspaces=svc-c",
				"--worktree=wt2")
			cmd.Dir(gitRoot)
			initResult := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), initResult.Stdout, initResult.Stderr)
			if err := initResult.AssertSuccess(); err != nil {
				return fmt.Errorf("protect-only plan init failed: %w", err)
			}
			worktreePath := expectedWorktreePath(ctx, gitRoot, "wt2", true)

			gaBin, err := harness.FindRealBinary("grove-anthropic")
			if err != nil {
				return fmt.Errorf("resolving grove-anthropic binary: %w", err)
			}
			syncCmd := ctx.Command(gaBin, "sync-settings", "--all")
			syncCmd.Dir(gitRoot)
			syncResult := syncCmd.Run()
			ctx.ShowCommandOutput(syncCmd.String(), syncResult.Stdout, syncResult.Stderr)
			if err := syncResult.AssertSuccess(); err != nil {
				return fmt.Errorf("protect-only sync-settings failed: %w", err)
			}

			content, err := fs.ReadString(filepath.Join(worktreePath, ".claude", "settings.local.json"))
			if err != nil {
				return fmt.Errorf("reading protect-only worktree settings: %w", err)
			}
			pv, err := parseProtection(content)
			if err != nil {
				return fmt.Errorf("parsing protect-only settings JSON: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.True("protectConfig-only config writes denyWrite entries", len(pv.Sandbox.Filesystem.DenyWrite) > 0)
				v.True("protectConfig-only config writes permissions.deny entries", len(pv.Permissions.Deny) > 0)
				v.True("protectConfig-only deny includes the global config-dir glob", anyHasSuffix(pv.Permissions.Deny, "/grove/**)"))
			})
		}),
	},
)
