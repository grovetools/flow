package scenarios

// oracle-cache-lineage suite, K2 addendum — lineage-overlap advisor (oracle-plays
// job 42). flow warns at `plan add --type chat` and at first fire when a
// completed sibling chat's frozen layers already cover the new job's rules
// resolution, so `-d <chat>` would inherit them warm instead of paying a fresh
// cold base write (the measured $10.80 incident). Advisory only — never blocks.

import (
	"fmt"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"

	"github.com/grovetools/flow/pkg/orchestration"
)

// bigOverlapSource is a comment-free (>40 KB so strip_comments=true leaves it
// byte-identical) Go source large enough to clear the advisor's 10k warm-token
// floor: ~2000 trivial functions.
func bigOverlapSource() string {
	var sb strings.Builder
	sb.WriteString("package big\n\n")
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&sb, "func Big%04d() int {\n\treturn %d\n}\n\n", i, i)
	}
	return sb.String()
}

// OracleCacheLineageAdvisorScenario — K2 (oracle-plays 42). Complete chat A over
// a large fileset; add chat B with the SAME rules but no `-d` and assert both
// the add-time stderr Note and the fire-time job.log advisory; a control chat C
// created WITH `-d A` gets no note.
var OracleCacheLineageAdvisorScenario = harness.NewScenario(
	"lineage-overlap-advisor",
	"Adding a chat whose rules re-resolve a completed sibling's frozen fileset warns to `-d` it (add-time Note + fire-time advisory); a chat created with `-d` is silent.",
	[]string{"oracle-cache-lineage", "chat", "cache", "lineage"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			env, err := setupOracleCacheEnv(ctx, "advisor")
			if err != nil {
				return err
			}
			// A large fileset + a rules file both chats resolve to.
			if err := fs.WriteString(env.ProjectDir+"/big.go", bigOverlapSource()); err != nil {
				return err
			}
			if err := fs.WriteString(env.ProjectDir+"/k2.rules", "big.go\n"); err != nil {
				return err
			}
			return env.Repo.AddCommit("seed K2 big overlap fixture")
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Complete parent chat A over the big fileset", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobA, err := env.addChat(ctx, "k2-parent", "Design over the big file.", map[string]interface{}{
				"rules_file":    "k2.rules",
				"model":         lineageModelA,
				"auto_complete": true,
			})
			if err != nil {
				return err
			}
			ctx.Set("k2_parent_filename", jobA.Filename)
			if err := env.runTurn(ctx, jobA.Filename).AssertSuccess(); err != nil {
				return err
			}
			return env.assertJobStatus("k2-parent", orchestration.JobStatusCompleted)
		}),
		harness.NewStep("Add dependent-less chat B: add-time Note fires", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			// Pass rules_file + model as CLI FLAGS so they are present at add
			// time (addChat patches frontmatter only after the add).
			addCmd := ctx.Bin("plan", "add", env.PlanName,
				"--type", "chat", "--title", "k2-child", "-p", "Plan phase 2 over the big file.",
				"--rules-file", "k2.rules", "--model", lineageModelA)
			addCmd.Dir(env.ProjectDir)
			result := addCmd.Run()
			ctx.ShowCommandOutput(addCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}
			ctx.Set("k2_child_add_stderr", result.Stderr)
			return nil
		}),
		harness.NewStep("Run chat B turn 1: fire-time advisory fires", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobB, err := env.job("k2-child")
			if err != nil {
				return err
			}
			ctx.Set("k2_child_id", jobB.ID)
			return env.runTurn(ctx, jobB.Filename).AssertSuccess()
		}),
		harness.NewStep("Control chat C created WITH -d is silent", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			addCmd := ctx.Bin("plan", "add", env.PlanName,
				"--type", "chat", "--title", "k2-control", "-p", "Plan phase 2, wired.",
				"--rules-file", "k2.rules", "--model", lineageModelA,
				"--depends-on", ctx.GetString("k2_parent_filename"))
			addCmd.Dir(env.ProjectDir)
			result := addCmd.Run()
			ctx.ShowCommandOutput(addCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}
			ctx.Set("k2_control_add_stderr", result.Stderr)
			return nil
		}),
		harness.NewStep("Assert advisory at add + fire, silence with -d", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			parentFile := ctx.GetString("k2_parent_filename")
			addStderr := ctx.GetString("k2_child_add_stderr")
			controlStderr := ctx.GetString("k2_control_add_stderr")
			jobLog := env.readJobLog(ctx.GetString("k2_child_id"))

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("add-time Note advises inheriting warm", addStderr, "would inherit")
				v.Contains("add-time Note names the sibling to -d", addStderr, parentFile)
				v.Contains("add-time Note carries the -d hint", addStderr, "-d")
				v.Contains("fire-time advisory lands in job.log", jobLog, "Lineage advisory:")
				v.NotContains("a chat created WITH -d gets no add-time advisory", controlStderr, "would inherit")
			})
		}),
	},
)
