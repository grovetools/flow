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

// ClaudeSettingsGroveCascadeScenario makes AXIS A (the grove-config cascade)
// observable: the [claude] profile is declared at THREE cascade layers —
//
//	global fragment  ~/.config/grove/claude-settings.toml   (lowest)
//	ecosystem        ~/code/claude-eco/grove.yml
//	project          ~/code/claude-eco/svc-a/grove.yml      (highest)
//
// and merged by core/config mergeExtensions (core/config/merge.go), where the
// [claude] extension ACCUMULATES (unions) its list-typed leaves down the cascade
// by default, while nested MAPS recurse and scalars keep highest-wins. A
// block-level `inherit = false` opts out, resetting accumulation beneath it. The
// test asserts every half of that rule:
//
//   - ACCUMULATE (same key at all three layers): permissions.allow carries a
//     distinct sentinel at each layer (GlobalOnly / EcoWins / ProjWins). Because
//     [claude] list leaves UNION down the cascade by default, svc-a's resolved
//     settings show ALL THREE sentinels — the accumulate-by-default property that
//     replaces the old override-not-union behavior.
//   - DISTINCT keys (a different key per layer, under the shared `sandbox` map):
//     global -> sandbox.network.allowedDomains, ecosystem ->
//     sandbox.filesystem.allowWrite, project -> sandbox.failIfUnavailable. All
//     three survive together because the union merger recurses into the `sandbox`
//     map and merges sibling sub-keys (map recursion is unchanged).
//   - OPT-OUT (`inherit = false`): svc-b's project grove.yml carries a [claude]
//     block with `inherit = false` + its own allow (SvcBOnly). Because that layer
//     resets accumulation, svc-b's resolved settings show SvcBOnly ONLY — the
//     accumulated EcoWins/GlobalOnly from lower layers are GONE.
//
// The assertion targets are the svc-a and svc-b PRIMARY CHECKOUTs (single-repo
// workspaces, member set empty), so each seeded settings.local.json is the
// product of a SINGLE config.LoadFrom cascade with NO member-repo union (Axis B)
// muddying the result. Trigger: `grove-anthropic sync-settings --all`, which
// seeds every discovered workspace via workspace.SeedClaudeSettingsForWorktree
// (core/pkg/workspace/claude_notebook.go) — for svc-a/svc-b that is rootCfg only.
var ClaudeSettingsGroveCascadeScenario = harness.NewScenario(
	"claude-settings-grove-cascade",
	"Axis A: the [claude] grove-config cascade (global fragment -> ecosystem -> project) ACCUMULATES conflicting array leaves by default (all layers' permissions.allow union) while merging distinct sibling keys under sandbox; a block-level inherit=false opts out and resets accumulation.",
	[]string{"claude", "settings-sync", "cascade", "config", "accumulate", "inherit"},
	[]harness.Step{
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "llm"},
		),

		harness.NewStep("Setup ecosystem with a [claude] block at all three cascade layers", func(ctx *harness.Context) error {
			gitRoot, repoDirs, _, err := setupEcosystemEnvironment(ctx, "claude-eco", []string{"svc-a", "svc-b"})
			if err != nil {
				return err
			}
			ctx.Set("git_root", gitRoot)
			ctx.Set("svc_a_dir", repoDirs["svc-a"])
			ctx.Set("svc_b_dir", repoDirs["svc-b"])

			// Layer 1 (lowest): the GLOBAL fragment. Written as raw TOML directly
			// into the sandbox XDG config dir so it is globbed as a *.toml
			// fragment by config.LoadFrom (core/config/config.go:317) — NOT via
			// WriteGroveConfig (which would write grove.yml, the base layer).
			// Carries the conflicting allow (GlobalOnly) + a DISTINCT key
			// (sandbox.network.allowedDomains).
			globalFragment := `# Global [claude] fragment (lowest cascade layer)
[claude.permissions]
allow = ["GlobalOnly(g:*)"]

[claude.sandbox.network]
allowedDomains = ["global-distinct.example.com"]
`
			fragmentPath := filepath.Join(ctx.ConfigDir(), "grove", "claude-settings.toml")
			if err := fs.WriteString(fragmentPath, globalFragment); err != nil {
				return err
			}

			// Layer 2 (middle): the ECOSYSTEM grove.yml. Conflicting allow
			// (EcoWins) + a DISTINCT key (sandbox.filesystem.allowWrite).
			ecoCfg := &config.Config{
				Name:       "claude-eco",
				Version:    "1.0",
				Workspaces: []string{"svc-a", "svc-b"},
				Extensions: map[string]interface{}{
					"claude": map[string]interface{}{
						"permissions": map[string]interface{}{
							"allow": []string{"EcoWins(e:*)"},
						},
						"sandbox": map[string]interface{}{
							"filesystem": map[string]interface{}{
								"allowWrite": []string{"/eco-distinct-dir"},
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

			// Layer 3 (highest): the PROJECT (svc-a) grove.yml. Conflicting allow
			// (ProjWins) + a DISTINCT key (sandbox.failIfUnavailable bool).
			svcACfg := &config.Config{
				Name:    "svc-a",
				Version: "1.0",
				Extensions: map[string]interface{}{
					"claude": map[string]interface{}{
						"permissions": map[string]interface{}{
							"allow": []string{"ProjWins(p:*)"},
						},
						"sandbox": map[string]interface{}{
							"failIfUnavailable": true,
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
			if err := git.Commit(repoDirs["svc-a"], "Add [claude] block to svc-a grove.yml"); err != nil {
				return err
			}

			// OPT-OUT layer: svc-b's PROJECT grove.yml carries a [claude] block
			// with `inherit = false`, which resets accumulation beneath it. Its
			// own allow (SvcBOnly) is the only one that should survive — the
			// accumulated EcoWins/GlobalOnly from the lower layers are dropped.
			svcBCfg := &config.Config{
				Name:    "svc-b",
				Version: "1.0",
				Extensions: map[string]interface{}{
					"claude": map[string]interface{}{
						"inherit": false,
						"permissions": map[string]interface{}{
							"allow": []string{"SvcBOnly(b:*)"},
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
			return git.Commit(repoDirs["svc-b"], "Add [claude] inherit=false block to svc-b grove.yml")
		}),

		harness.NewStep("Run grove-anthropic sync-settings --all", func(ctx *harness.Context) error {
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

		harness.NewStep("Assert the svc-a cascade: highest layer wins for the conflict, all distinct keys survive", func(ctx *harness.Context) error {
			svcADir := ctx.GetString("svc_a_dir")
			settingsPath := filepath.Join(svcADir, ".claude", "settings.local.json")

			if err := fs.AssertExists(settingsPath); err != nil {
				return fmt.Errorf("svc-a settings.local.json should exist after sync: %w", err)
			}
			content, err := fs.ReadString(settingsPath)
			if err != nil {
				return fmt.Errorf("reading svc-a settings: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				// ACCUMULATE (Axis A default): all three layers' allow values
				// survive because [claude] list leaves UNION down the cascade.
				v.Contains("project-layer allow accumulated", content, "ProjWins(p:*)")
				v.Contains("ecosystem-layer allow accumulated (unioned)", content, "EcoWins(e:*)")
				v.Contains("global-layer allow accumulated (unioned)", content, "GlobalOnly(g:*)")

				// DISTINCT keys (map recursion): a different sandbox sub-key from
				// each layer all coexist after the cascade merge.
				v.Contains("global distinct key (network.allowedDomains) survives", content, "global-distinct.example.com")
				v.Contains("ecosystem distinct key (filesystem.allowWrite) survives", content, "/eco-distinct-dir")
				v.Contains("project distinct key (sandbox.failIfUnavailable) survives", content, "\"failIfUnavailable\": true")
			})
		}),

		harness.NewStep("Assert the svc-b opt-out: inherit=false resets accumulation to svc-b's own rule", func(ctx *harness.Context) error {
			svcBDir := ctx.GetString("svc_b_dir")
			settingsPath := filepath.Join(svcBDir, ".claude", "settings.local.json")

			if err := fs.AssertExists(settingsPath); err != nil {
				return fmt.Errorf("svc-b settings.local.json should exist after sync: %w", err)
			}
			content, err := fs.ReadString(settingsPath)
			if err != nil {
				return fmt.Errorf("reading svc-b settings: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				// OPT-OUT (inherit=false): only svc-b's own rule survives; the
				// accumulated lower-layer sentinels are reset away.
				v.Contains("svc-b own allow survives", content, "SvcBOnly(b:*)")
				v.NotContains("ecosystem-layer allow reset by inherit=false", content, "EcoWins(e:*)")
				v.NotContains("global-layer allow reset by inherit=false", content, "GlobalOnly(g:*)")
			})
		}),
	},
)
