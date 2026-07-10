package scenarios

// oracle-cache-lineage suite — `flow plan warm` (oracle-plays J5, scenario 27).
// Warm rides a chat's cached prefix as a near-zero-output request to refresh the
// Anthropic prefix-cache TTL WITHOUT appending a turn, a history block, or a
// request manifest that would bust the prefix. Under the mock LLM the verb runs
// assembly + the parity gate and writes only its receipt (no API dispatch), so
// the whole no-mutation contract is assertable on-disk here:
//   - a warm-<ts>.json receipt appears with parity_ok=true
//   - NO new request-manifest is written (the manifest count stays at 1)
//   - the chat body is byte-identical (no turn/history appended)
//   - the job frontmatter/status is untouched (still pending_user)
//   - nothing in the layer store is archived or rewritten
// A second scenario covers the refuse-before-dispatch guard: warming a chat that
// never fired errors (there is no cached prefix), writing nothing.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/command"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"

	"github.com/grovetools/flow/pkg/orchestration"
)

// runWarm executes `flow plan warm <plan> <job-file>` through the real binary
// under the mock LLM (which short-circuits warm before the API, still running
// assembly + parity + receipt).
func (e *oracleCacheEnv) runWarm(ctx *harness.Context, target string, extraArgs ...string) *command.Result {
	args := append([]string{"plan", "warm", e.PlanName, target}, extraArgs...)
	cmd := ctx.Bin(args...)
	cmd.Dir(e.ProjectDir)
	cmd.Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + e.MockFile)
	result := cmd.Run()
	ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
	return result
}

// OracleCacheWarmScenario — J5 happy path: warm writes a receipt and mutates
// nothing else.
var OracleCacheWarmScenario = harness.NewScenario(
	"warm",
	"flow plan warm rides the cached prefix: writes a warm receipt (parity OK) and adds NO request manifest, no turn, no frontmatter change, no layer rewrite.",
	[]string{"oracle-cache-lineage", "chat", "cache", "warm"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "warm")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run turn 1 (establishes the cached prefix + one manifest)", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "warm-chat", "Describe alpha.", map[string]interface{}{
				"rules_file": "base.rules",
			})
			if err != nil {
				return err
			}
			ctx.Set("job_id", job.ID)
			ctx.Set("job_filename", job.Filename)
			ctx.Set("job_path", job.FilePath)
			if err := env.runTurn(ctx, job.Filename).AssertSuccess(); err != nil {
				return err
			}
			// Snapshot the pre-warm state the warm must not disturb.
			body, err := fs.ReadString(job.FilePath)
			if err != nil {
				return err
			}
			ctx.Set("body_before_warm", body)
			baseHash, err := sha256File(filepath.Join(env.layersDir(job.ID), "00-base.xml"))
			if err != nil {
				return err
			}
			ctx.Set("base_hash_t1", baseHash)
			return env.assertJobStatus("warm-chat", orchestration.JobStatusPendingUser)
		}),
		harness.NewStep("Warm the chat", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			result := env.runWarm(ctx, ctx.GetString("job_filename"))
			if err := result.AssertSuccess(); err != nil {
				return err
			}
			return result.AssertStdoutContains("Warm (mock): parity OK")
		}),
		harness.NewStep("Receipt written; no new manifest, no turn, no frontmatter/layer change", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")

			receipts, err := filepath.Glob(filepath.Join(env.PlanPath, ".artifacts", jobID, "warm-*.json"))
			if err != nil {
				return err
			}
			manifests, err := env.loadManifests(jobID)
			if err != nil {
				return err
			}
			bodyAfter, err := fs.ReadString(ctx.GetString("job_path"))
			if err != nil {
				return err
			}
			baseHash, err := sha256File(filepath.Join(env.layersDir(jobID), "00-base.xml"))
			if err != nil {
				return err
			}
			archives, err := filepath.Glob(filepath.Join(env.layersDir(jobID), "archive-*"))
			if err != nil {
				return err
			}

			var receiptParityOK bool
			if len(receipts) == 1 {
				data, rerr := os.ReadFile(receipts[0])
				if rerr != nil {
					return rerr
				}
				var r orchestration.WarmReceipt
				if rerr := json.Unmarshal(data, &r); rerr != nil {
					return rerr
				}
				receiptParityOK = r.ParityOK
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("exactly one warm receipt written", 1, len(receipts))
				v.True("receipt records parity OK", receiptParityOK)
				v.Equal("no new request manifest (warm writes none)", 1, len(manifests))
				v.Equal("chat body byte-identical (no turn/history appended)", ctx.GetString("body_before_warm"), bodyAfter)
				v.Equal("frozen base layer untouched", ctx.GetString("base_hash_t1"), baseHash)
				v.Equal("nothing archived (no rebase)", 0, len(archives))
			})
		}),
		harness.NewStep("Frontmatter/status unchanged by warm", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			return env.assertJobStatus("warm-chat", orchestration.JobStatusPendingUser)
		}),
	},
)

// OracleCacheWarmNeverFiredScenario — J5 guard: warming a chat that never fired
// a turn refuses (no cached prefix), writing no receipt.
var OracleCacheWarmNeverFiredScenario = harness.NewScenario(
	"warm-never-fired",
	"flow plan warm refuses a chat that never fired a turn (no request manifest → no cached prefix) and writes no receipt.",
	[]string{"oracle-cache-lineage", "chat", "cache", "warm"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "warm-never")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Add a chat but do NOT run any turn", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "never-chat", "Describe alpha.", map[string]interface{}{
				"rules_file": "base.rules",
			})
			if err != nil {
				return err
			}
			ctx.Set("job_id", job.ID)
			ctx.Set("job_filename", job.Filename)
			return nil
		}),
		harness.NewStep("Warm refuses; nothing written", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")

			result := env.runWarm(ctx, ctx.GetString("job_filename"))
			if err := result.AssertFailure(); err != nil {
				return err
			}
			receipts, err := filepath.Glob(filepath.Join(env.PlanPath, ".artifacts", jobID, "warm-*.json"))
			if err != nil {
				return err
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("error explains there is no cached prefix", strings.ToLower(result.Stderr+result.Stdout), "never fired")
				v.Equal("no warm receipt written on refusal", 0, len(receipts))
			})
		}),
	},
)
