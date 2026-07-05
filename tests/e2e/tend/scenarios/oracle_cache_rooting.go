package scenarios

// oracle-cache-lineage suite — resolution rooting (oracle-plays job-25
// regression): a chat turn submitted from a DIFFERENT cwd, addressing the
// plan through a differently-spelled (symlinked) path, must still resolve
// context at the job's project root and extend the SAME layer lineage —
// zero duplicate layers, zero removal annotations. Before the fix this
// spelling defeated the notebook prefix match, resolution re-rooted at a
// fabricated fallback, and the union diff (keyed on raw path spellings)
// re-uploaded the entire fileset as "new".

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// OracleCacheInvokerCwdRootingScenario — invoker cwd/spelling independence.
var OracleCacheInvokerCwdRootingScenario = harness.NewScenario(
	"invoker-cwd-rooting",
	"Turn 2 run from a foreign cwd via a symlink-aliased plan path resolves the job's project root and appends NO duplicate layer (job-25 regression).",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "rooting")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run turn 1 from the project dir", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "rooting-chat", "Describe alpha.", map[string]interface{}{
				"rules_file": "base.rules",
			})
			if err != nil {
				return err
			}
			ctx.Set("job_id", job.ID)
			ctx.Set("job_filename", job.Filename)
			if err := env.runTurn(ctx, job.Filename).AssertSuccess(); err != nil {
				return err
			}
			layers, err := env.loadLayers(job.ID)
			if err != nil {
				return err
			}
			if len(layers.Layers) != 1 {
				return fmt.Errorf("expected 1 layer after turn 1, got %d", len(layers.Layers))
			}
			if layers.Root == "" {
				return fmt.Errorf("turn 1 did not record the root pin in layers.json")
			}
			ctx.Set("base_hash", layers.Layers[0].Hash)
			ctx.Set("root_pin", layers.Root)
			return nil
		}),
		harness.NewStep("Run turn 2 from a foreign cwd via a symlink-aliased plan path", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobFilename := ctx.GetString("job_filename")

			// A spelling variant of the notebooks root: the alias resolves to
			// the same directory but string-prefix matching would miss it.
			notebooksRoot := ctx.GetString("notebooks_root")
			alias := filepath.Join(filepath.Dir(notebooksRoot), "notebooks-alias")
			if err := os.Symlink(notebooksRoot, alias); err != nil {
				return err
			}
			rel, err := filepath.Rel(notebooksRoot, env.PlanPath)
			if err != nil {
				return err
			}
			aliasJobPath := filepath.Join(alias, rel, jobFilename)

			// A cwd unrelated to the project (the invoker-cwd-differs case).
			elsewhere := filepath.Join(ctx.RootDir, "elsewhere")
			if err := os.MkdirAll(elsewhere, 0o755); err != nil {
				return err
			}

			if err := appendUserTurn(filepath.Join(env.PlanPath, jobFilename), "Thanks, one more question."); err != nil {
				return err
			}
			cmd := ctx.Bin("plan", "run", aliasJobPath, "--local", "--yes")
			cmd.Dir(elsewhere)
			cmd.Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + env.MockFile)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),
		harness.NewStep("Layer lineage unchanged: no duplicate layer, no removals, same root pin", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")
			layers, err := env.loadLayers(jobID)
			if err != nil {
				return err
			}
			jobLog, err := fs.ReadString(env.jobLogPath(jobID))
			if err != nil {
				return err
			}
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("still exactly one layer (no fileset duplication)", 1, len(layers.Layers))
				if len(layers.Layers) == 1 {
					v.Equal("base layer hash unchanged", ctx.GetString("base_hash"), layers.Layers[0].Hash)
				}
				v.Equal("no removal annotations for respelled paths", 0, len(layers.Removals))
				v.Equal("root pin unchanged across the foreign-cwd turn", ctx.GetString("root_pin"), layers.Root)
				v.NotContains("job.log shows no appended rules-diff layer", jobLog, "appended 01-add-")
			})
		}),
	},
)
