package scenarios

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/util/pathutil"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// editRuleForAbsDir mirrors claudenotebook.editRuleForAbsDir (the helper is
// unexported in the leaf package): the "//" prefix is the absolute anchor.
func editRuleForAbsDir(absDir string) string {
	return "Edit(//" + strings.TrimPrefix(filepath.ToSlash(absDir), "/") + "/**)"
}

// ClaudeSettingsMemberUnionScenario makes AXIS B (the member-repo merge)
// observable, and pins the CODE semantics against the misleading doc-comment.
//
// For an ecosystem-level (multi-repo) worktree, workspace.SeedClaudeSettingsForWorktree
// (core/pkg/workspace/claude_notebook.go:139) loads the worktree-root [claude]
// block, then folds in every member repo's [claude] block via
// ClaudeConfig.Merge (core/pkg/claudenotebook/config.go:86). That Merge has TWO
// different rules for two kinds of field:
//
//   - ARRAYS (permissions.allow, sandbox.network.allowedDomains, ...) UNION and
//     dedupe across root + all members (unionStrings, config.go:109).
//   - SCALARS / BOOLS (sandbox.enabled, ...) are ROOT-WINS: a member fills the
//     slot only when the root left it nil (config.go:96-104). NOTE the
//     doc-comment at config.go:84 says "other-wins" — that is WRONG; this test
//     asserts the CODE.
//
// Fixture:
//
//	root grove.yml   allow=[Root(r:*)]  sandbox.enabled=FALSE
//	svc-a grove.yml  allow=[Aaa(a:*)]   sandbox.enabled=TRUE  domains=[a.example.com]  allowGroveTools=TRUE
//	svc-b grove.yml  allow=[Bbb(b:*)]                          domains=[b.example.com]
//
// Assertions on the ecosystem worktree's settings.local.json:
//   - arrays UNION: Root + Aaa + Bbb all present; a.example.com + b.example.com
//     both present.
//   - bool ROOT-WINS: "enabled": false present, "enabled": true ABSENT — even
//     though member svc-a explicitly set enabled=true, the root's false wins.
//   - Edit() AUTO-DERIVATION (Task 1): a narrow Edit(//<worktree>/**) rule plus
//     an Edit(//<dir>/**) rule for each resolved notebook dir are emitted into
//     permissions.allow (they ride the notebook-dir gate).
//   - allowGroveTools EXPANSION (Task 2): svc-a's allowGroveTools=true (a
//     root-wins-gap scalar) expands into Bash(<tool>:*) rules — Bash(grove:*),
//     Bash(nb:*), ... — in the unioned settings.
var ClaudeSettingsMemberUnionScenario = harness.NewScenario(
	"claude-settings-member-union",
	"Axis B: an ecosystem worktree unions every member repo's [claude] arrays (allow/allowedDomains) while resolving the conflicting sandbox.enabled bool to the ecosystem-ROOT value (root-wins, not the doc-comment's other-wins).",
	[]string{"claude", "settings-sync", "ecosystem", "worktree", "union"},
	[]harness.Step{
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "llm"},
		),

		harness.NewStep("Setup ecosystem with conflicting bool at root and distinct arrays per member", func(ctx *harness.Context) error {
			gitRoot, repoDirs, _, err := setupEcosystemEnvironment(ctx, "claude-eco", []string{"svc-a", "svc-b"})
			if err != nil {
				return err
			}
			ctx.Set("git_root", gitRoot)

			// Ecosystem root: distinct allow sentinel + sandbox.enabled=false (the
			// root side of the conflict).
			enabledFalse := false
			ecoCfg := &config.Config{
				Name:       "claude-eco",
				Version:    "1.0",
				Workspaces: []string{"svc-a", "svc-b"},
				Extensions: map[string]interface{}{
					"claude": map[string]interface{}{
						"permissions": map[string]interface{}{
							"allow": []string{"Root(r:*)"},
						},
						"sandbox": map[string]interface{}{
							"enabled": enabledFalse,
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

			// Each member becomes a Go module so plan init's sibling-workspace
			// go.work wiring is satisfied (mirrors the J5 template).
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

			// svc-a: distinct allow + distinct domain + sandbox.enabled=TRUE (the
			// member side of the conflict — must LOSE to the root's false).
			enabledTrue := true
			svcACfg := &config.Config{
				Name:    "svc-a",
				Version: "1.0",
				Extensions: map[string]interface{}{
					"claude": map[string]interface{}{
						"permissions": map[string]interface{}{
							"allow": []string{"Aaa(a:*)"},
						},
						// allowGroveTools is a root-wins-gap scalar; the root
						// leaves it nil, so this member's true flows up and the
						// seeder expands it into Bash(<tool>:*) rules.
						"allowGroveTools": true,
						"sandbox": map[string]interface{}{
							"enabled": enabledTrue,
							"network": map[string]interface{}{
								"allowedDomains": []string{"a.example.com"},
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

			// svc-b: distinct allow + distinct domain (no bool — inherits).
			svcBCfg := &config.Config{
				Name:    "svc-b",
				Version: "1.0",
				Extensions: map[string]interface{}{
					"claude": map[string]interface{}{
						"permissions": map[string]interface{}{
							"allow": []string{"Bbb(b:*)"},
						},
						"sandbox": map[string]interface{}{
							"network": map[string]interface{}{
								"allowedDomains": []string{"b.example.com"},
							},
						},
					},
				},
			}
			if err := fs.WriteGroveConfig(repoDirs["svc-b"], svcBCfg); err != nil {
				return err
			}
			if err := git.Add(repoDirs["svc-b"], "grove.yml"); err != nil {
				return err
			}
			if err := git.Commit(repoDirs["svc-b"], "Add distinct [claude] block to svc-b"); err != nil {
				return err
			}

			// Source go.work so plan init's configureGoWorkspace has a base.
			goWork := "go 1.23.5\n\nuse (\n\t./svc-a\n\t./svc-b\n)\n"
			return fs.WriteString(filepath.Join(gitRoot, "go.work"), goWork)
		}),

		harness.NewStep("Create the ecosystem-level (multi-repo) worktree via plan init", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")

			cmd := ctx.Bin("plan", "init", "claude-union-plan",
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
			for _, repo := range []string{"svc-a", "svc-b"} {
				if err := fs.AssertExists(filepath.Join(worktreePath, repo, ".git")); err != nil {
					return fmt.Errorf("member repo %q should be linked into the worktree: %w", repo, err)
				}
			}
			return nil
		}),

		harness.NewStep("Run grove-anthropic sync-settings --all to seed the worktree union", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")

			gaBin, err := harness.FindRealBinary("grove-anthropic")
			if err != nil {
				return fmt.Errorf("resolving grove-anthropic binary: %w", err)
			}
			cmd := ctx.Command(gaBin, "sync-settings", "--all")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("grove-anthropic sync-settings --all failed: %w", err)
			}
			return nil
		}),

		harness.NewStep("Assert arrays union across members and the bool resolves to the root value", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")
			settingsPath := filepath.Join(worktreePath, ".claude", "settings.local.json")

			if err := fs.AssertExists(settingsPath); err != nil {
				return fmt.Errorf("ecosystem worktree settings.local.json should exist: %w", err)
			}
			content, err := fs.ReadString(settingsPath)
			if err != nil {
				return fmt.Errorf("reading worktree settings: %w", err)
			}

			// Parse to derive the auto-Edit rule for a resolved notebook dir
			// (whatever path the locator produced for the members).
			var parsed struct {
				Permissions struct {
					AdditionalDirectories []string `json:"additionalDirectories"`
				} `json:"permissions"`
			}
			if err := json.Unmarshal([]byte(content), &parsed); err != nil {
				return fmt.Errorf("parsing worktree settings JSON: %w", err)
			}

			// Canonicalize to match the seeder, which resolves symlinks so the Edit
			// rule matches Claude's resolved cwd (on macOS the XDG worktree is a
			// /var -> /private/var symlink). An un-canonicalized expectation would
			// hide the very bug the seeder fix addresses.
			canonWt, canonErr := pathutil.CanonicalPath(worktreePath)
			if canonErr != nil {
				canonWt = worktreePath
			}
			worktreeEditRule := editRuleForAbsDir(canonWt)

			return ctx.Verify(func(v *verify.Collector) {
				// Arrays UNION across root + every member.
				v.Contains("root allow unioned", content, "Root(r:*)")
				v.Contains("svc-a allow unioned", content, "Aaa(a:*)")
				v.Contains("svc-b allow unioned", content, "Bbb(b:*)")
				v.Contains("svc-a allowedDomains unioned", content, "a.example.com")
				v.Contains("svc-b allowedDomains unioned", content, "b.example.com")

				// Bool is ROOT-WINS: the ecosystem root's false survives, the
				// member's explicit true is dropped.
				v.Contains("sandbox.enabled resolves to root=false", content, "\"enabled\": false")
				v.NotContains("member sandbox.enabled=true does NOT win", content, "\"enabled\": true")

				// Task 1: the narrow worktree Edit rule is auto-derived.
				v.Contains("worktree Edit rule auto-derived", content, worktreeEditRule)
				// And every resolved notebook dir gets a matching Edit rule.
				v.True("at least one notebook dir resolved", len(parsed.Permissions.AdditionalDirectories) > 0)
				for _, d := range parsed.Permissions.AdditionalDirectories {
					v.Contains("notebook-dir Edit rule auto-derived", content, editRuleForAbsDir(d))
				}

				// Task 2: svc-a's allowGroveTools=true expands into Bash(<tool>:*)
				// rules in the unioned settings.
				v.Contains("allowGroveTools expands Bash(grove:*)", content, "Bash(grove:*)")
				v.Contains("allowGroveTools expands Bash(nb:*)", content, "Bash(nb:*)")
			})
		}),
	},
)
