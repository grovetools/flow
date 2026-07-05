package scenarios

// oracle-cache-lineage suite, part 4/4 — guards and edges (spec 19 §5
// scenarios 14–17, 20–23): pinned_context rejection, cache_ttl frontmatter,
// no_cache, transcript byte-stability, gemini passthrough, concurrent chats,
// unreadable rules files, and the untouched legacy oneshot path.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"

	"github.com/grovetools/flow/pkg/orchestration"
)

// OracleCachePinnedContextRejectedScenario — spec 19 e2e scenario 14.
var OracleCachePinnedContextRejectedScenario = harness.NewScenario(
	"pinned-context-rejected",
	"A job with the removed pinned_context: key fails with status failed, an actionable error in job.log, and frontmatter last_error.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "pinned")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Add a chat carrying the removed pinned_context key and run it", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "pinned-chat", "Describe alpha.", map[string]interface{}{
				"rules_file":     "base.rules",
				"pinned_context": []string{"alpha.go"},
			})
			if err != nil {
				return err
			}
			ctx.Set("job_id", job.ID)
			ctx.Set("job_filename", job.Filename)
			// The run is EXPECTED to fail; we assert on the persisted state, not
			// the exit code.
			result := env.runTurn(ctx, job.Filename)
			ctx.ShowCommandOutput("expected-failure run", result.Stdout, result.Stderr)
			return nil
		}),
		harness.NewStep("Status failed with actionable error in job.log AND frontmatter last_error", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")

			job, err := env.job("pinned-chat")
			if err != nil {
				return err
			}
			jobLog := env.readJobLog(jobID)
			jobContent, err := fs.ReadString(job.FilePath)
			if err != nil {
				return err
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("job status", string(orchestration.JobStatusFailed), string(job.Status))
				v.Contains("last_error names the removed key", job.Metadata.LastError, "pinned_context is no longer supported")
				v.Contains("last_error is actionable (points at the rules file)", job.Metadata.LastError, "rules")
				v.Contains("frontmatter carries last_error", jobContent, "last_error:")
				v.Contains("job.log carries the error", jobLog, "pinned_context is no longer supported")
			})
		}),
	},
)

// OracleCacheTTLFrontmatterScenario — spec 19 e2e scenario 15.
var OracleCacheTTLFrontmatterScenario = harness.NewScenario(
	"cache-ttl-frontmatter",
	"cache_ttl: 5m in frontmatter puts ttl: 5m on every manifest breakpoint.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "ttl")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run a chat with cache_ttl: 5m", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "ttl-chat", "Describe alpha.", map[string]interface{}{
				"rules_file": "base.rules",
				"cache_ttl":  "5m",
			})
			if err != nil {
				return err
			}
			ctx.Set("job_id", job.ID)
			return env.runTurn(ctx, job.Filename).AssertSuccess()
		}),
		harness.NewStep("Manifest breakpoints carry ttl: 5m", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			manifests, err := env.loadManifests(ctx.GetString("job_id"))
			if err != nil {
				return err
			}
			if len(manifests) != 1 {
				return fmt.Errorf("expected 1 manifest, got %d", len(manifests))
			}
			m := manifests[0]
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("manifest-level cache_ttl", "5m", m.CacheTTL)
				v.True("at least one breakpoint present", breakpointCount(m) > 0)
				for _, e := range m.Entries {
					if e.Breakpoint {
						v.Equal(fmt.Sprintf("breakpoint ttl on %s block", e.Kind), "5m", e.TTL)
					} else {
						v.Equal(fmt.Sprintf("no ttl on non-breakpoint %s block", e.Kind), "", e.TTL)
					}
				}
			})
		}),
	},
)

// OracleCacheNoCacheScenario — spec 19 e2e scenario 16.
var OracleCacheNoCacheScenario = harness.NewScenario(
	"no-cache",
	"no_cache: true yields a manifest with zero breakpoints while the layer store is still written (artifacts != caching).",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "nocache")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run a chat with no_cache: true", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "nocache-chat", "Describe alpha.", map[string]interface{}{
				"rules_file": "base.rules",
				"no_cache":   true,
			})
			if err != nil {
				return err
			}
			ctx.Set("job_id", job.ID)
			return env.runTurn(ctx, job.Filename).AssertSuccess()
		}),
		harness.NewStep("Zero breakpoints; layers still written", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")
			manifests, err := env.loadManifests(jobID)
			if err != nil {
				return err
			}
			if len(manifests) != 1 {
				return fmt.Errorf("expected 1 manifest, got %d", len(manifests))
			}
			m := manifests[0]
			layers, err := env.loadLayers(jobID)
			if err != nil {
				return err
			}
			return ctx.Verify(func(v *verify.Collector) {
				v.True("manifest records no_cache", m.NoCache)
				v.Equal("zero breakpoints anywhere", 0, breakpointCount(m))
				v.Equal("layer store still written", 1, len(layers.Layers))
				v.True("base artifact exists", fileExists(filepath.Join(env.layersDir(jobID), "00-base.xml")))
			})
		}),
	},
)

// OracleCacheTranscriptStabilityScenario — spec 19 e2e scenario 17.
var OracleCacheTranscriptStabilityScenario = harness.NewScenario(
	"transcript-stability",
	"Across a 3-turn chat the history region grows append-only: turn-3's manifest repeats turn-2's history hashes in order and appends; awaiting_response never enters history bytes.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "transtab")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run three chat turns", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "transtab-chat", "Turn one question.", map[string]interface{}{
				"rules_file": "base.rules",
			})
			if err != nil {
				return err
			}
			ctx.Set("job_id", job.ID)
			ctx.Set("job_path", job.FilePath)
			if err := env.runTurn(ctx, job.Filename).AssertSuccess(); err != nil {
				return err
			}
			if err := appendUserTurn(job.FilePath, "Turn two question."); err != nil {
				return err
			}
			if err := env.runTurn(ctx, job.Filename).AssertSuccess(); err != nil {
				return err
			}
			if err := appendUserTurn(job.FilePath, "Turn three question."); err != nil {
				return err
			}
			return env.runTurn(ctx, job.Filename).AssertSuccess()
		}),
		harness.NewStep("History region is append-only across manifests", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")
			manifests, err := env.loadManifests(jobID)
			if err != nil {
				return err
			}
			if len(manifests) != 3 {
				return fmt.Errorf("expected 3 manifests after 3 turns, got %d", len(manifests))
			}
			h1 := entriesOfKind(manifests[0], "history")
			h2 := entriesOfKind(manifests[1], "history")
			h3 := entriesOfKind(manifests[2], "history")

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("turn 1 has no history", 0, len(h1))
				v.Equal("turn 2 history: turn-1 user + assistant", 2, len(h2))
				v.Equal("turn 3 history: four prior turn blocks", 4, len(h3))
				for i := range h2 {
					if i < len(h3) {
						v.Equal(fmt.Sprintf("turn-3 history[%d] hash == turn-2 history[%d] hash (append-only)", i, i),
							h2[i].ContentHash, h3[i].ContentHash)
					}
				}
			})
		}),
		harness.NewStep("History bytes reconstruct exactly and never carry awaiting_response", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")

			content, err := os.ReadFile(ctx.GetString("job_path"))
			if err != nil {
				return err
			}
			turns, err := orchestration.ParseChatFile(content)
			if err != nil {
				return err
			}
			regions := orchestration.FormatConversationRegions(turns)

			manifests, err := env.loadManifests(jobID)
			if err != nil {
				return err
			}
			h3 := entriesOfKind(manifests[len(manifests)-1], "history")
			// The chat file has GROWN since turn 3 ran (its response + trailing
			// marker appended), so re-serializing yields MORE history blocks —
			// the append-only property says the leading blocks must reproduce
			// turn-3's uploaded bytes exactly.
			if len(regions.HistoryBlocks) < len(h3) {
				return fmt.Errorf("reconstructed history has %d blocks, turn-3 manifest recorded %d", len(regions.HistoryBlocks), len(h3))
			}

			return ctx.Verify(func(v *verify.Collector) {
				for i, e := range h3 {
					v.Equal(fmt.Sprintf("re-serialized history block %d reproduces turn-3's uploaded bytes", i), e.ContentHash, sha256String(regions.HistoryBlocks[i]))
				}
				for i, block := range regions.HistoryBlocks {
					v.NotContains(fmt.Sprintf("history block %d free of awaiting_response", i), block, "awaiting_response")
					v.NotContains(fmt.Sprintf("history block %d free of respond_as", i), block, "respond_as")
				}
				v.Contains("the volatile current turn is where awaiting_response lives", regions.CurrentTurn, "awaiting_response")
			})
		}),
	},
)

// OracleCacheGeminiPassthroughScenario — spec 19 e2e scenario 20.
//
// Gemini has no in-process mock, so the dispatch itself fails (the API key is
// deliberately neutralized — no live call can happen). Everything the spec row
// asserts is written BEFORE dispatch and is still observable: the layer store
// exists, the manifest records the flattened no-breakpoint upload, and the
// Anthropic-only warning lands in job.log.
var OracleCacheGeminiPassthroughScenario = harness.NewScenario(
	"gemini-passthrough",
	"A gemini-model chat flattens the upload: manifest has provider gemini with NO breakpoints and no cache layout; layers are still written; warning logged.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "gemini")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run a gemini-model chat (dispatch fails safely: no API key)", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "gemini-chat", "Describe alpha.", map[string]interface{}{
				"rules_file": "base.rules",
				"model":      "gemini-2.5-pro",
			})
			if err != nil {
				return err
			}
			ctx.Set("job_id", job.ID)
			// NO mock env: the mock branch would shadow the gemini assembly. The
			// API key is neutralized so the runner fails fast without a network
			// call; every assertion surface is written before dispatch.
			cmd := ctx.Bin("plan", "run", job.Filename, "--local", "--yes")
			cmd.Dir(env.ProjectDir)
			cmd.Env("GEMINI_API_KEY=")
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return nil
		}),
		harness.NewStep("Flattened manifest, layers on disk, warning in job.log", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")

			layers, err := env.loadLayers(jobID)
			if err != nil {
				return err
			}
			manifests, err := env.loadManifests(jobID)
			if err != nil {
				return err
			}
			if len(manifests) != 1 {
				return fmt.Errorf("expected 1 manifest, got %d", len(manifests))
			}
			m := manifests[0]
			jobLog := env.readJobLog(jobID)

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("manifest provider", "gemini", m.Provider)
				v.Equal("no cache layout recorded (Anthropic-only)", "", m.CacheLayout)
				v.Equal("no cache ttl recorded", "", m.CacheTTL)
				v.Equal("zero breakpoints in the flattened upload", 0, breakpointCount(m))
				v.Equal("no layer-kind entries (flat context documents only)", 0, len(entriesOfKind(m, "layer")))
				v.True("volatile turn block present", len(entriesOfKind(m, "turn")) == 1)
				v.Equal("layer store still written (artifacts != caching)", 1, len(layers.Layers))
				v.Contains("job.log carries the flattening warning", jobLog, "Context layers warning")
				v.Contains("warning says the ladder is Anthropic-only", jobLog, "Anthropic-only")
			})
		}),
	},
)

// OracleCacheConcurrentChatsScenario — spec 19 e2e scenario 21.
var OracleCacheConcurrentChatsScenario = harness.NewScenario(
	"concurrent-chats",
	"Two chats in one plan keep job-scoped layer stores that never cross-contaminate; each manifest references only its own artifacts.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			env, err := setupOracleCacheEnv(ctx, "parallel")
			if err != nil {
				return err
			}
			return fs.WriteString(filepath.Join(env.ProjectDir, "other.rules"), "beta.go\n")
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Add two chats and run them in one batch", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobOne, err := env.addChat(ctx, "chat-one", "Describe alpha.", map[string]interface{}{
				"rules_file": "base.rules",
			})
			if err != nil {
				return err
			}
			jobTwo, err := env.addChat(ctx, "chat-two", "Describe beta.", map[string]interface{}{
				"rules_file": "other.rules",
			})
			if err != nil {
				return err
			}
			ctx.Set("job_one_id", jobOne.ID)
			ctx.Set("job_two_id", jobTwo.ID)

			// Two SINGLE-target runs launched concurrently — genuinely parallel
			// turns in one plan. (Batch modes are unusable for chats here:
			// RunAll's orchestrator loop keeps re-selecting pending_user chats
			// until its step-limit safeguard trips — a pre-existing behavior,
			// see chat-strip-comments.)
			errCh := make(chan error, 2)
			for _, filename := range []string{jobOne.Filename, jobTwo.Filename} {
				go func(target string) {
					result := env.runTurn(ctx, target)
					errCh <- result.AssertSuccess()
				}(filename)
			}
			for i := 0; i < 2; i++ {
				if err := <-errCh; err != nil {
					return fmt.Errorf("parallel chat turn failed: %w", err)
				}
			}
			return nil
		}),
		harness.NewStep("Job-scoped stores don't cross-contaminate", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			oneID := ctx.GetString("job_one_id")
			twoID := ctx.GetString("job_two_id")

			baseOne, err := fs.ReadString(filepath.Join(env.layersDir(oneID), "00-base.xml"))
			if err != nil {
				return err
			}
			baseTwo, err := fs.ReadString(filepath.Join(env.layersDir(twoID), "00-base.xml"))
			if err != nil {
				return err
			}
			manifestsOne, err := env.loadManifests(oneID)
			if err != nil {
				return err
			}
			manifestsTwo, err := env.loadManifests(twoID)
			if err != nil {
				return err
			}
			if len(manifestsOne) != 1 || len(manifestsTwo) != 1 {
				return fmt.Errorf("expected 1 manifest per chat, got %d and %d", len(manifestsOne), len(manifestsTwo))
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("chat-one base has its own context", baseOne, "func Alpha")
				v.NotContains("chat-one base free of chat-two context", baseOne, "func Beta")
				v.Contains("chat-two base has its own context", baseTwo, "func Beta")
				v.NotContains("chat-two base free of chat-one context", baseTwo, "func Alpha")
				for _, e := range entriesOfKind(manifestsOne[0], "layer") {
					v.Contains("chat-one manifest references only its own artifacts", e.Path, filepath.Join(".artifacts", oneID)+string(filepath.Separator))
				}
				for _, e := range entriesOfKind(manifestsTwo[0], "layer") {
					v.Contains("chat-two manifest references only its own artifacts", e.Path, filepath.Join(".artifacts", twoID)+string(filepath.Separator))
				}
			})
		}),
	},
)

// OracleCacheUnreadableGlobScenario — spec 19 e2e scenario 22.
var OracleCacheUnreadableGlobScenario = harness.NewScenario(
	"unreadable-glob-file",
	"Rules matching an unreadable file hard-fail the turn: status failed + last_error, never a silent running state.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with an unreadable file", func(ctx *harness.Context) error {
			env, err := setupOracleCacheEnv(ctx, "unreadable")
			if err != nil {
				return err
			}
			gammaPath := filepath.Join(env.ProjectDir, "gamma.go")
			if err := fs.WriteString(gammaPath, "package main\n\nfunc Gamma() int {\n\treturn 3\n}\n"); err != nil {
				return err
			}
			if err := os.Chmod(gammaPath, 0o000); err != nil {
				return err
			}
			return fs.WriteString(filepath.Join(env.ProjectDir, "gamma.rules"), "gamma.go\n")
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run the chat (expected to fail)", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "unreadable-chat", "Describe gamma.", map[string]interface{}{
				"rules_file": "gamma.rules",
			})
			if err != nil {
				return err
			}
			ctx.Set("job_id", job.ID)
			result := env.runTurn(ctx, job.Filename)
			ctx.ShowCommandOutput("expected-failure run", result.Stdout, result.Stderr)
			return nil
		}),
		harness.NewStep("Hard failure: failed status + last_error, no silent running", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.job("unreadable-chat")
			if err != nil {
				return err
			}
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("job status", string(orchestration.JobStatusFailed), string(job.Status))
				v.NotEqual("never stuck at running", string(orchestration.JobStatusRunning), string(job.Status))
				v.Contains("last_error names the unreadable file", job.Metadata.LastError, "gamma.go")
			})
		}),
	},
)

// OracleCacheLegacyUntouchedScenario — spec 19 e2e scenario 23.
var OracleCacheLegacyUntouchedScenario = harness.NewScenario(
	"legacy-untouched",
	"A oneshot job stays on the legacy layout: no layer store, no snapshot, no request manifests; context generation still runs.",
	[]string{"oracle-cache-lineage", "oneshot", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "legacy")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Add and run a oneshot job with a rules file", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			addCmd := ctx.Bin("plan", "add", env.PlanName, "--type", "oneshot", "--title", "legacy-oneshot", "-p", "Review alpha.")
			addCmd.Dir(env.ProjectDir)
			if err := addCmd.Run().AssertSuccess(); err != nil {
				return err
			}
			if err := updateJobFrontmatter(env.PlanPath, "legacy-oneshot", map[string]interface{}{
				"rules_file": "base.rules",
			}); err != nil {
				return err
			}
			job, err := env.job("legacy-oneshot")
			if err != nil {
				return err
			}
			ctx.Set("job_id", job.ID)
			return env.runTurn(ctx, job.Filename).AssertSuccess()
		}),
		harness.NewStep("No layer store, no manifests; legacy context artifacts intact", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")

			if err := env.assertJobStatus("legacy-oneshot", orchestration.JobStatusCompleted); err != nil {
				return err
			}
			manifestGlobs, err := filepath.Glob(filepath.Join(env.PlanPath, ".artifacts", jobID, "request-manifest-*.json"))
			if err != nil {
				return err
			}
			jobLog := env.readJobLog(jobID)

			return ctx.Verify(func(v *verify.Collector) {
				v.True("no context-layers/ dir", !fileExists(env.layersDir(jobID)))
				v.True("no snapshot.json", !fileExists(env.snapshotPath(jobID)))
				v.Equal("no request manifests under legacy", 0, len(manifestGlobs))
				// Spot-check the legacy path still ran its context pipeline.
				v.Contains("legacy context generation ran (job.log spot-check)", strings.ToLower(jobLog), "context")
			})
		}),
	},
)
