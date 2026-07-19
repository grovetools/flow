package scenarios

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"

	"github.com/grovetools/agentlogs/pkg/usage"
	"github.com/grovetools/eval/pkg/record"
	"github.com/grovetools/flow/pkg/orchestration"
)

// readConfigVector loads a stamped config vector from a job's artifact dir.
func readConfigVector(planPath, jobID string) (record.ConfigVector, error) {
	var v record.ConfigVector
	b, err := os.ReadFile(filepath.Join(planPath, ".artifacts", jobID, "config-vector.json"))
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return v, fmt.Errorf("config-vector.json does not parse as a record.ConfigVector: %w", err)
	}
	return v, nil
}

// readMetricsRecord loads a job's partial run record.
func readMetricsRecord(planPath, jobID string) (record.RunRecord, error) {
	var r record.RunRecord
	b, err := os.ReadFile(filepath.Join(planPath, ".artifacts", jobID, "metrics.json"))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("metrics.json does not parse as a record.RunRecord: %w", err)
	}
	return r, nil
}

// seedJobRulesFiles writes an empty-but-present rules file for every job in the
// plan that names one.
//
// `plan add` stamps a canonical rules_file path into every job's frontmatter
// but deliberately leaves the file itself absent (directory.go: "Leave an
// unseeded rules file absent"). The oneshot/chat run path then treats a missing
// rules file as a hard error during context regeneration, so a job created by
// `plan add` and run immediately fails before it ever reaches the capture seam
// this scenario is about. Seeding the file is fixture setup, not a workaround
// for the code under test: it puts the plan in the state a real user's plan is
// in by the time a job runs.
func seedJobRulesFiles(planPath string) error {
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return err
	}
	for _, job := range plan.Jobs {
		if job.RulesFile == "" {
			continue
		}
		p := filepath.Join(planPath, job.RulesFile)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fmt.Errorf("creating rules dir: %w", err)
		}
		if err := os.WriteFile(p, []byte("*.md\n"), 0o600); err != nil {
			return fmt.Errorf("seeding rules file %s: %w", p, err)
		}
	}
	return nil
}

// plantTokenUsage writes a token-usage.json artifact for a job, so the
// completion seam's resolveCost finds usage and populates the Cost axis.
//
// This exists because ArchiveTokenUsage is a silent no-op under the harness
// mocks: it early-returns on an unverified session-registry binding, so
// nothing ever writes the artifact and Cost is always nil. Planting the
// artifact is what makes the Cost-axis assertions in the headless scenario
// EXECUTE rather than skip.
func plantTokenUsage(planPath, jobID string, s usage.Summary) error {
	path := orchestration.TokenUsageArtifactPath(planPath, jobID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating artifact dir for planted usage: %w", err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshaling planted usage summary: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("writing planted usage artifact: %w", err)
	}
	return nil
}

// jobFileByTitle finds a job's on-disk filename by its title. `flow complete`
// takes the job FILE as a positional argument, not just a plan.
func jobFileByTitle(planPath, title string) (string, error) {
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return "", err
	}
	for _, job := range plan.Jobs {
		if job.Title == title {
			return job.Filename, nil
		}
	}
	return "", fmt.Errorf("no job titled %q in %s", title, planPath)
}

// jobIDByTitle finds a job's id by its title.
func jobIDByTitle(planPath, title string) (string, error) {
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return "", err
	}
	for _, job := range plan.Jobs {
		if job.Title == title {
			return job.ID, nil
		}
	}
	return "", fmt.Errorf("no job titled %q in %s", title, planPath)
}

// EvalCaptureOneshotScenario covers P1-23(a): a oneshot job under the mock LLM
// stamps a config vector and writes a metrics record, both parsing back into
// the record schema.
//
// The mock branch short-circuits before the anthropic call, so apiUsage stays
// nil, AccumulateAPITokenUsage never runs and token-usage.json is never
// written. Cost is therefore necessarily nil here — asserting that is a
// genuine D4 regression test ("nil means not measured"), and the present-Cost
// mapping is covered by the metrics_record unit tests instead.
var EvalCaptureOneshotScenario = harness.NewScenario(
	"eval-capture-oneshot",
	"Verifies a oneshot job stamps config-vector.json and writes metrics.json, with a nil Cost under the mock LLM path",
	[]string{"eval", "capture", "oneshot", "config-vector", "metrics"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			if _, _, err := setupDefaultEnvironment(ctx, "eval-capture-project"); err != nil {
				return err
			}
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			return fs.WriteString(responseFile, "Mock response for eval capture.")
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "grove"},
		),

		harness.NewStep("Create and run a oneshot job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			if err := ctx.Bin("plan", "init", "capture-plan").
				Dir(projectDir).Run().AssertSuccess(); err != nil {
				return err
			}
			planPath := filepath.Join(ctx.GetString("notebooks_root"),
				"workspaces", "eval-capture-project", "plans", "capture-plan")
			ctx.Set("plan_path", planPath)

			if err := ctx.Bin("plan", "add", "capture-plan",
				"--type", "oneshot", "--title", "capture-oneshot",
				"-p", "Do the capture thing").
				Dir(projectDir).Run().AssertSuccess(); err != nil {
				return err
			}
			if err := seedJobRulesFiles(planPath); err != nil {
				return err
			}

			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			runCmd := ctx.Bin("plan", "run", "--local", "--all", "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			return runCmd.Run().AssertSuccess()
		}),

		harness.NewStep("Verify the capture artifacts", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobID, err := jobIDByTitle(planPath, "capture-oneshot")
			if err != nil {
				return err
			}

			v, err := readConfigVector(planPath, jobID)
			if err != nil {
				return fmt.Errorf("config vector: %w", err)
			}
			r, err := readMetricsRecord(planPath, jobID)
			if err != nil {
				return fmt.Errorf("metrics record: %w", err)
			}

			return ctx.Verify(func(vc *verify.Collector) {
				vc.NotEqual("config vector carries a model", "", v.Model)
				vc.Equal("oneshot vectors carry no agent provider", "", v.Provider)
				vc.True("briefing component was hashed",
					v.Components["briefing"] != "")
				vc.Equal("record schema is stamped", record.SchemaVersion, r.Schema)
				vc.Equal("record task id defaults to the job id", jobID, r.Key.TaskID)
				vc.Equal("record joins to the stamped vector", v.Hash(), r.Key.ConfigHash)
				// D4: the mock path archives no usage, so Cost must be absent
				// rather than a zeroed struct claiming a free run.
				vc.True("Cost is nil under the mock path (nothing was measured)",
					r.Cost == nil)
				// D6: axes owned by other writers must not appear.
				vc.True("no Process axis (owned by aglogs)", r.Process == nil)
				vc.True("no Outcome axis (owned by the grader)", r.Outcome == nil)
			})
		}),
	},
)

// EvalCaptureChatScenario covers P1-23(b): a chat turn stamps a vector, and a
// following `action: complete` turn writes the metrics record WITHOUT
// re-stamping the vector.
//
// The completion branch returns before ever reaching WriteBriefingFile, so it
// renders no new bytes to hash — the vector on disk stays the last
// response-producing turn's, which is exactly D12's "a record's vector is the
// stamping turn's".
var EvalCaptureChatScenario = harness.NewScenario(
	"eval-capture-chat",
	"Verifies a chat turn stamps config-vector.json and that a subsequent completion writes metrics.json without re-stamping the vector",
	[]string{"eval", "capture", "chat", "config-vector", "metrics"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "eval-chat-project")
			if err != nil {
				return err
			}
			// The chat path has an empty-freeze gate: a job whose rules file
			// resolves zero files fails before the turn fires. Give the seeded
			// rules ("*.md") something real to select.
			if err := fs.WriteString(filepath.Join(projectDir, "notes.md"),
				"# Notes\n\nContext for the chat turn.\n"); err != nil {
				return err
			}
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			return fs.WriteString(responseFile, "Mock chat response.")
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "grove"},
		),

		harness.NewStep("Run a chat turn", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			if err := ctx.Bin("plan", "init", "chat-plan").
				Dir(projectDir).Run().AssertSuccess(); err != nil {
				return err
			}
			planPath := filepath.Join(ctx.GetString("notebooks_root"),
				"workspaces", "eval-chat-project", "plans", "chat-plan")
			ctx.Set("plan_path", planPath)

			if err := ctx.Bin("plan", "add", "chat-plan",
				"--type", "chat", "--title", "capture-chat",
				"-p", "Discuss the capture thing").
				Dir(projectDir).Run().AssertSuccess(); err != nil {
				return err
			}
			if err := seedJobRulesFiles(planPath); err != nil {
				return err
			}

			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			runCmd := ctx.Bin("plan", "run", "--local", "--all", "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			result := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), result.Stdout, result.Stderr)
			return nil
		}),

		harness.NewStep("Capture the vector stamped by the response turn", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobID, err := jobIDByTitle(planPath, "capture-chat")
			if err != nil {
				return err
			}
			ctx.Set("job_id", jobID)

			vectorPath := filepath.Join(planPath, ".artifacts", jobID, "config-vector.json")
			raw, err := os.ReadFile(vectorPath)
			if err != nil {
				return fmt.Errorf("no vector stamped by the chat turn: %w", err)
			}
			ctx.Set("vector_bytes", string(raw))
			return nil
		}),

		harness.NewStep("Complete the chat and verify the vector is unchanged", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			jobID := ctx.GetString("job_id")

			// The completion must actually SUCCEED. Without this check, a
			// completion that errored out and did nothing at all still leaves
			// the vector byte-identical, so the byte-equality assertion below
			// would report "does not re-stamp" about a run that never ran.
			// `flow complete` is `complete [slug] <job-file>`: the job file is
			// REQUIRED. The previous invocation passed no job argument at all
			// and exited non-zero every run — invisible, because the result
			// was never checked.
			jobFile, err := jobFileByTitle(planPath, "capture-chat")
			if err != nil {
				return err
			}
			completeCmd := ctx.Bin("complete", "--plan", "chat-plan", jobFile)
			completeCmd.Dir(projectDir)
			result := completeCmd.Run()
			ctx.ShowCommandOutput(completeCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("chat completion failed, so nothing was "+
					"exercised: %w", err)
			}

			after, err := os.ReadFile(
				filepath.Join(planPath, ".artifacts", jobID, "config-vector.json"))
			if err != nil {
				return fmt.Errorf("vector disappeared after completion: %w", err)
			}

			// The other half of the claim in this scenario's doc comment: the
			// completion WRITES THE METRICS RECORD. Nothing asserted that, so
			// a completion that produced no record at all satisfied the
			// byte-equality check above by doing nothing.
			r, err := readMetricsRecord(planPath, jobID)
			if err != nil {
				return fmt.Errorf("completion did not write metrics.json: %w", err)
			}

			return ctx.Verify(func(vc *verify.Collector) {
				vc.Equal("completion does not re-stamp the vector",
					ctx.GetString("vector_bytes"), string(after))

				// Shape of the record the completion wrote.
				vc.Equal("record schema is stamped", record.SchemaVersion, r.Schema)
				vc.Equal("record task id defaults to the job id", jobID, r.Key.TaskID)
				vc.NotEqual("record carries a config hash envelope", "", r.Key.ConfigHash)
				// D12: the record joins to the vector the response turn
				// stamped — the same bytes the completion left untouched.
				var stamped record.ConfigVector
				if err := json.Unmarshal([]byte(ctx.GetString("vector_bytes")), &stamped); err != nil {
					vc.True("stamped vector parses back into a ConfigVector", false)
				} else {
					vc.Equal("record joins to the stamping turn's vector",
						stamped.Hash(), r.Key.ConfigHash)
				}
				// D6: axes owned by other writers must be absent, not zeroed.
				vc.True("no Process axis (owned by aglogs)", r.Process == nil)
				vc.True("no Outcome axis (owned by the grader)", r.Outcome == nil)
			})
		}),
	},
)

// EvalCaptureHeadlessWallClockScenario covers P1-23(c): a headless_agent job
// run against a stubbed `claude` binary on the sandbox PATH produces a metrics
// record whose wall clock is nil-or-positive, never zero or negative.
//
// The addendum flagged this as unverified (F9), on the theory that no scenario
// stubs an agent binary onto PATH. That is not so: harness.SetupMocks does
// exactly this, and provider_headless.go / agent_provider_generic.go rely on
// it for every agent family. A claude mock ships at tests/e2e/tend/mocks/src/claude.
//
// The assertion is deliberately "nil or positive", never "positive": nil is a
// legitimate, D4-honest outcome for a family whose duration was not persisted,
// and asserting positive would encode an expectation the schema does not make.
//
// BRANCH COVERED — read this before changing the fixture. The P1-14 assertions
// below live on the COST-PRESENT branch: they only mean anything when the
// record carries a Cost axis. Under harness.Mock that branch is not reachable
// on its own — ArchiveTokenUsage (token_usage.go) early-returns on an
// unverified session-registry binding, so token-usage.json is never written
// and resolveCost returns nil. Guarding the assertions behind `if r.Cost !=
// nil` therefore skipped all three, every run, while the scenario reported
// green — the phase's headline invariant had no end-to-end coverage at all.
//
// So the scenario PLANTS a token-usage.json artifact before the run, which is
// exactly what resolveCost reads, and then demands `r.Cost != nil` outright.
// The branch is now taken deliberately and its absence is a failure, not a
// silent skip. The nil-Cost branch is covered separately and explicitly by
// EvalCaptureOneshotScenario, which asserts `r.Cost == nil` under the same
// mocks with no artifact planted.
var EvalCaptureHeadlessWallClockScenario = harness.NewScenario(
	"eval-capture-headless-wallclock",
	"Runs a headless_agent job against a stubbed claude binary and asserts metrics.json carries a nil-or-positive wall clock",
	[]string{"eval", "capture", "headless", "agent", "metrics", "wallclock"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, _, err := setupDefaultEnvironment(ctx, "eval-headless-project")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "claude"},
			harness.Mock{CommandName: "tmux"},
			harness.Mock{CommandName: "cx"},
		),

		harness.NewStep("Create and run a headless agent job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			if err := ctx.Bin("plan", "init", "headless-plan").
				Dir(projectDir).Run().AssertSuccess(); err != nil {
				return err
			}
			planPath := filepath.Join(ctx.GetString("notebooks_root"),
				"workspaces", "eval-headless-project", "plans", "headless-plan")
			ctx.Set("plan_path", planPath)

			if err := ctx.Bin("plan", "add", "headless-plan",
				"--type", "headless_agent", "--title", "capture-headless",
				"-p", "Do the headless capture thing").
				Dir(projectDir).Run().AssertSuccess(); err != nil {
				return err
			}

			// Plant the usage artifact BEFORE the run, so the completion
			// seam's resolveCost finds it and the Cost axis is genuinely
			// populated. See the scenario doc comment for why this is
			// required rather than incidental.
			jobID, err := jobIDByTitle(planPath, "capture-headless")
			if err != nil {
				return err
			}
			if err := plantTokenUsage(planPath, jobID, usage.Summary{
				Usage:   usage.Usage{Input: 100, Output: 200, CacheRead: 300},
				CostUSD: 0.5,
			}); err != nil {
				return err
			}

			runCmd := ctx.Bin("plan", "run", "--local", "--all", "--yes")
			runCmd.Dir(projectDir)
			result := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), result.Stdout, result.Stderr)
			// The launch detaches; the artifact assertions below are the real
			// contract, not the CLI exit code.
			return nil
		}),

		harness.NewStep("Wait for the metrics record and verify the wall clock", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobID, err := jobIDByTitle(planPath, "capture-headless")
			if err != nil {
				return err
			}

			metricsPath := filepath.Join(planPath, ".artifacts", jobID, "metrics.json")
			deadline := time.Now().Add(20 * time.Second)
			for {
				if _, statErr := os.Stat(metricsPath); statErr == nil {
					break
				}
				if time.Now().After(deadline) {
					// Completion may not have been reached in the sandbox; drive
					// it explicitly so the capture seam runs.
					jobFile, fileErr := jobFileByTitle(planPath, "capture-headless")
					if fileErr != nil {
						return fileErr
					}
					completeCmd := ctx.Bin("complete", "--plan", "headless-plan", jobFile)
					completeCmd.Dir(ctx.GetString("project_dir"))
					completeCmd.Run()
					break
				}
				time.Sleep(250 * time.Millisecond)
			}

			r, err := readMetricsRecord(planPath, jobID)
			if err != nil {
				return fmt.Errorf("metrics record: %w", err)
			}

			return ctx.Verify(func(vc *verify.Collector) {
				vc.Equal("record schema is stamped", record.SchemaVersion, r.Schema)

				// The cost-present branch is REQUIRED, not optional: usage was
				// planted before the run precisely so it is taken. A nil Cost
				// here means the three P1-14 assertions below did not run, and
				// that must fail rather than read as a pass.
				if r.Cost == nil {
					vc.True("Cost axis is populated (usage was planted, so the "+
						"P1-14 assertions must actually execute)", false)
					return
				}

				// The core P1-14 assertion, now genuinely reached. A wall clock
				// is either absent (not measured) or strictly positive — never
				// zero, never negative. A non-positive value would mean some
				// path performed timestamp subtraction.
				vc.True("wall clock is nil-or-positive, never <= 0",
					r.Cost.WallClockSeconds == nil || *r.Cost.WallClockSeconds > 0)
				vc.Equal("agent families record a transcript-usage cost source",
					"transcript_usage", r.Cost.CostSource)
				vc.True("agent families never carry an API response time",
					r.Cost.APIResponseSeconds == nil)
				// The planted usage must be the usage that was mapped — a Cost
				// axis full of zeros would satisfy every assertion above while
				// proving the mapping never ran.
				vc.Equal("planted input tokens reached the record", int64(100), r.Cost.InputTokens)
				vc.Equal("planted output tokens reached the record", int64(200), r.Cost.OutputTokens)
				vc.Equal("planted cache-read tokens reached the record", int64(300), r.Cost.CacheReadTokens)
				// D7 amendment: the record must self-describe how far
				// estimated_usd can be trusted. The planted summary leaves
				// MissingPricing false, so this run is fully priced — and
				// crucially the flag must be STAMPED, not left at the unknown
				// zero value, which is what a writer that skipped the
				// amendment would emit.
				vc.Equal("a fully-priced run is stamped complete",
					record.PricingCompletenessComplete, r.Cost.PricingCompleteness)
			})
		}),
	},
)
