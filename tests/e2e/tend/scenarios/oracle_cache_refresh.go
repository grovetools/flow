package scenarios

// oracle-cache-lineage suite, part 2/4 — refresh verbs (spec 19 §5 scenarios
// 6–9, 18, 19): --append-delta, --rebase-context, rules-removal annotations,
// the rebase advisory, chat-reopen auto append-delta, and the
// context_snapshot: false opt-out.

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

// OracleCacheAppendDeltaScenario — spec 19 e2e scenario 6.
var OracleCacheAppendDeltaScenario = harness.NewScenario(
	"append-delta",
	"--append-delta appends a supersede-annotated delta layer with the changed file; prior layers untouched.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "delta")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run turn 1", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "delta-chat", "Describe alpha.", map[string]interface{}{
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
			baseHash, err := sha256File(filepath.Join(env.layersDir(job.ID), "00-base.xml"))
			if err != nil {
				return err
			}
			ctx.Set("base_hash_t1", baseHash)
			return nil
		}),
		harness.NewStep("Edit the captured file and run turn 2 with --append-delta", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			if err := fs.WriteString(filepath.Join(env.ProjectDir, "alpha.go"), oracleAlphaV2); err != nil {
				return err
			}
			if err := appendUserTurn(ctx.GetString("job_path"), "Pick up my edits."); err != nil {
				return err
			}
			return env.runTurn(ctx, ctx.GetString("job_filename"), "--append-delta").AssertSuccess()
		}),
		harness.NewStep("Delta layer supersedes the changed file; prior layers untouched", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")

			baseHash, err := sha256File(filepath.Join(env.layersDir(jobID), "00-base.xml"))
			if err != nil {
				return err
			}
			layers, err := env.loadLayers(jobID)
			if err != nil {
				return err
			}
			delta, err := findLayerBySource(layers, "git-delta")
			if err != nil {
				return err
			}
			deltaContent, err := fs.ReadString(filepath.Join(env.layersDir(jobID), delta.File))
			if err != nil {
				return err
			}
			jobLog := env.readJobLog(jobID)

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("prior layer untouched", ctx.GetString("base_hash_t1"), baseHash)
				v.Equal("two layers after the delta", 2, len(layers.Layers))
				v.True("delta layer filename", strings.HasPrefix(delta.File, "01-delta-"))
				v.Equal("delta supersedes the changed file", "alpha.go", strings.Join(delta.Supersedes, ","))
				v.Contains("delta artifact carries the supersedes annotation", deltaContent, `supersedes="alpha.go"`)
				v.Contains("delta artifact carries the NEW bytes", deltaContent, "AlphaEdited")
				v.Contains("job.log records the delta append", jobLog, "appended delta")
			})
		}),
	},
)

// OracleCacheRebaseScenario — spec 19 e2e scenario 7.
var OracleCacheRebaseScenario = harness.NewScenario(
	"rebase",
	"--rebase-context re-freezes a fresh 00-base.xml from the current worktree, archives (never deletes) old layers, resets layers.json, and the manifest shows a single layer.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "rebase")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run turn 1", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "rebase-chat", "Describe alpha.", map[string]interface{}{
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
			baseHash, err := sha256File(filepath.Join(env.layersDir(job.ID), "00-base.xml"))
			if err != nil {
				return err
			}
			ctx.Set("base_hash_t1", baseHash)
			return nil
		}),
		harness.NewStep("Edit the file and run turn 2 with --rebase-context", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			if err := fs.WriteString(filepath.Join(env.ProjectDir, "alpha.go"), oracleAlphaV2); err != nil {
				return err
			}
			if err := appendUserTurn(ctx.GetString("job_path"), "Re-freeze everything."); err != nil {
				return err
			}
			return env.runTurn(ctx, ctx.GetString("job_filename"), "--rebase-context").AssertSuccess()
		}),
		harness.NewStep("Fresh base reflects the worktree; old layers archived; layers.json reset; single-layer manifest", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")

			basePath := filepath.Join(env.layersDir(jobID), "00-base.xml")
			baseHash, err := sha256File(basePath)
			if err != nil {
				return err
			}
			baseContent, err := fs.ReadString(basePath)
			if err != nil {
				return err
			}
			layers, err := env.loadLayers(jobID)
			if err != nil {
				return err
			}
			archives, err := filepath.Glob(filepath.Join(env.layersDir(jobID), "archive-*"))
			if err != nil {
				return err
			}
			if len(archives) != 1 {
				return fmt.Errorf("expected exactly 1 archive dir after rebase, got %d (%v)", len(archives), archives)
			}
			archivedBaseHash, err := sha256File(filepath.Join(archives[0], "00-base.xml"))
			if err != nil {
				return fmt.Errorf("archived base missing: %w", err)
			}
			if err := fs.AssertExists(filepath.Join(archives[0], "layers.json")); err != nil {
				return fmt.Errorf("archived layers.json missing: %w", err)
			}
			if err := fs.AssertExists(filepath.Join(archives[0], "snapshot.json")); err != nil {
				return fmt.Errorf("archived snapshot.json missing: %w", err)
			}
			manifests, err := env.loadManifests(jobID)
			if err != nil {
				return err
			}
			t2Layers := entriesOfKind(manifests[len(manifests)-1], "layer")
			jobLog := env.readJobLog(jobID)

			return ctx.Verify(func(v *verify.Collector) {
				v.NotEqual("fresh base has a new hash", ctx.GetString("base_hash_t1"), baseHash)
				v.Contains("fresh base reflects the current worktree", baseContent, "AlphaEdited")
				v.Equal("archived base preserves the ORIGINAL bytes", ctx.GetString("base_hash_t1"), archivedBaseHash)
				v.Equal("layers.json reset to a single fresh base", 1, len(layers.Layers))
				v.Equal("fresh base source", "rules-base", layers.Layers[0].Source)
				v.Equal("manifest shows a single layer", 1, len(t2Layers))
				if len(t2Layers) == 1 {
					v.Equal("manifest layer is the fresh base", baseHash, t2Layers[0].ContentHash)
				}
				v.Contains("job.log records the rebase", jobLog, "Context rebase: archived existing layers")
				// A re-frozen snapshot.json exists at the job root again.
				v.True("snapshot.json re-frozen", fileExists(env.snapshotPath(jobID)))
			})
		}),
	},
)

// OracleCacheRulesRemovalScenario — spec 19 e2e scenario 8.
var OracleCacheRulesRemovalScenario = harness.NewScenario(
	"rules-removal",
	"Deleting a glob between turns mutates no layer; the removal is annotated in layers.json and the bytes stay in the uploaded set.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			env, err := setupOracleCacheEnv(ctx, "removal")
			if err != nil {
				return err
			}
			// Start wide: alpha + beta both captured at turn 1.
			return fs.WriteString(filepath.Join(env.ProjectDir, "base.rules"), "alpha.go\nbeta.go\n")
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run turn 1 with both files captured", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "removal-chat", "Describe both files.", map[string]interface{}{
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
			baseHash, err := sha256File(filepath.Join(env.layersDir(job.ID), "00-base.xml"))
			if err != nil {
				return err
			}
			ctx.Set("base_hash_t1", baseHash)
			return nil
		}),
		harness.NewStep("Remove beta.go from the rules and run turn 2", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			if err := fs.WriteString(filepath.Join(env.ProjectDir, "base.rules"), "alpha.go\n"); err != nil {
				return err
			}
			if err := appendUserTurn(ctx.GetString("job_path"), "Forget about beta."); err != nil {
				return err
			}
			return env.runTurn(ctx, ctx.GetString("job_filename")).AssertSuccess()
		}),
		harness.NewStep("No layer mutation; removal annotated; bytes stay uploaded", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")

			baseHash, err := sha256File(filepath.Join(env.layersDir(jobID), "00-base.xml"))
			if err != nil {
				return err
			}
			layers, err := env.loadLayers(jobID)
			if err != nil {
				return err
			}
			removedPaths := make([]string, 0, len(layers.Removals))
			for _, r := range layers.Removals {
				removedPaths = append(removedPaths, r.Path)
			}
			manifests, err := env.loadManifests(jobID)
			if err != nil {
				return err
			}
			t2Layers := entriesOfKind(manifests[len(manifests)-1], "layer")

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("layer bytes unmutated", ctx.GetString("base_hash_t1"), baseHash)
				v.Equal("no layer appended or removed", 1, len(layers.Layers))
				v.Equal("removal annotated in layers.json", "beta.go", strings.Join(removedPaths, ","))
				v.Equal("uploaded set still carries the layer with the removed file", 1, len(t2Layers))
				if len(t2Layers) == 1 {
					v.Equal("uploaded layer hash unchanged (bytes stay up)", ctx.GetString("base_hash_t1"), t2Layers[0].ContentHash)
				}
			})
		}),
	},
)

// OracleCacheRebaseAdvisoryScenario — spec 19 e2e scenario 9.
var OracleCacheRebaseAdvisoryScenario = harness.NewScenario(
	"rebase-advisory",
	"When superseded bytes exceed the threshold, job.log carries the rebase suggestion and nothing auto-busts.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "advisory")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run turn 1 (alpha.go is the whole lineage)", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "advisory-chat", "Describe alpha.", map[string]interface{}{
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
			baseHash, err := sha256File(filepath.Join(env.layersDir(job.ID), "00-base.xml"))
			if err != nil {
				return err
			}
			ctx.Set("base_hash_t1", baseHash)
			return nil
		}),
		harness.NewStep("Rewrite the only captured file and append a delta (supersedes ~50% of lineage bytes)", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			if err := fs.WriteString(filepath.Join(env.ProjectDir, "alpha.go"), oracleAlphaV2); err != nil {
				return err
			}
			if err := appendUserTurn(ctx.GetString("job_path"), "Take my rewrite."); err != nil {
				return err
			}
			return env.runTurn(ctx, ctx.GetString("job_filename"), "--append-delta").AssertSuccess()
		}),
		harness.NewStep("Advisory logged; nothing auto-busts", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")

			baseHash, err := sha256File(filepath.Join(env.layersDir(jobID), "00-base.xml"))
			if err != nil {
				return err
			}
			layers, err := env.loadLayers(jobID)
			if err != nil {
				return err
			}
			archives, err := filepath.Glob(filepath.Join(env.layersDir(jobID), "archive-*"))
			if err != nil {
				return err
			}
			jobLog := env.readJobLog(jobID)

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("job.log carries the rebase advisory", jobLog, "Context rebase advisory")
				v.Contains("advisory names --rebase-context", jobLog, "--rebase-context")
				v.Equal("base layer NOT rebased", ctx.GetString("base_hash_t1"), baseHash)
				v.Equal("lineage intact (base + delta)", 2, len(layers.Layers))
				v.Equal("nothing archived (no auto-bust)", 0, len(archives))
			})
		}),
	},
)

// OracleCacheChatReopenScenario — spec 19 e2e scenario 18.
var OracleCacheChatReopenScenario = harness.NewScenario(
	"chat-reopen",
	"Reopening a completed chat with a new user turn triggers append-delta (not rebase); inherited/own layers preserved.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "reopen")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run turn 1 and complete the chat", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "reopen-chat", "Describe alpha.", map[string]interface{}{
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
			baseHash, err := sha256File(filepath.Join(env.layersDir(job.ID), "00-base.xml"))
			if err != nil {
				return err
			}
			ctx.Set("base_hash_t1", baseHash)

			completeCmd := ctx.Bin("plan", "complete", env.PlanName, job.Filename)
			completeCmd.Dir(env.ProjectDir)
			if err := completeCmd.Run().AssertSuccess(); err != nil {
				return err
			}
			return env.assertJobStatus("reopen-chat", orchestration.JobStatusCompleted)
		}),
		harness.NewStep("Edit the worktree, append a new user turn, and re-run", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			if err := fs.WriteString(filepath.Join(env.ProjectDir, "alpha.go"), oracleAlphaV2); err != nil {
				return err
			}
			if err := appendUserTurn(ctx.GetString("job_path"), "One more thing about alpha."); err != nil {
				return err
			}
			result := env.runTurn(ctx, ctx.GetString("job_filename"))
			if err := result.AssertSuccess(); err != nil {
				return err
			}
			return result.AssertStdoutContains("Auto-reopening")
		}),
		harness.NewStep("Reopen appended a delta (not a rebase); layers preserved", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")

			baseHash, err := sha256File(filepath.Join(env.layersDir(jobID), "00-base.xml"))
			if err != nil {
				return err
			}
			layers, err := env.loadLayers(jobID)
			if err != nil {
				return err
			}
			delta, err := findLayerBySource(layers, "git-delta")
			if err != nil {
				return err
			}
			archives, err := filepath.Glob(filepath.Join(env.layersDir(jobID), "archive-*"))
			if err != nil {
				return err
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("frozen base preserved across the reopen", ctx.GetString("base_hash_t1"), baseHash)
				v.Equal("reopen appended exactly one delta layer", 2, len(layers.Layers))
				v.Equal("delta supersedes the edited file", "alpha.go", strings.Join(delta.Supersedes, ","))
				v.Equal("no rebase happened (nothing archived)", 0, len(archives))
			})
		}),
	},
)

// OracleCacheSnapshotOptOutScenario — spec 19 e2e scenario 19.
var OracleCacheSnapshotOptOutScenario = harness.NewScenario(
	"snapshot-optout",
	"context_snapshot: false regenerates the store every turn (layer-0 hash may change); the manifest reflects it and no immutability violation is reported.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "optout")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run turn 1 with context_snapshot: false", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "optout-chat", "Describe alpha.", map[string]interface{}{
				"rules_file":       "base.rules",
				"context_snapshot": false,
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
			baseHash, err := sha256File(filepath.Join(env.layersDir(job.ID), "00-base.xml"))
			if err != nil {
				return err
			}
			ctx.Set("base_hash_t1", baseHash)
			return nil
		}),
		harness.NewStep("Edit the file and run turn 2 (no refresh verb)", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			if err := fs.WriteString(filepath.Join(env.ProjectDir, "alpha.go"), oracleAlphaV2); err != nil {
				return err
			}
			if err := appendUserTurn(ctx.GetString("job_path"), "Anything new?"); err != nil {
				return err
			}
			return env.runTurn(ctx, ctx.GetString("job_filename")).AssertSuccess()
		}),
		harness.NewStep("Every turn regenerates: fresh base, no violation reported", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")

			basePath := filepath.Join(env.layersDir(jobID), "00-base.xml")
			baseHash, err := sha256File(basePath)
			if err != nil {
				return err
			}
			baseContent, err := fs.ReadString(basePath)
			if err != nil {
				return err
			}
			layers, err := env.loadLayers(jobID)
			if err != nil {
				return err
			}
			manifests, err := env.loadManifests(jobID)
			if err != nil {
				return err
			}
			t2Layers := entriesOfKind(manifests[len(manifests)-1], "layer")
			jobLog := env.readJobLog(jobID)

			if err := env.assertJobStatus("optout-chat", orchestration.JobStatusPendingUser); err != nil {
				return err
			}
			return ctx.Verify(func(v *verify.Collector) {
				v.NotEqual("layer-0 regenerated (hash changed)", ctx.GetString("base_hash_t1"), baseHash)
				v.Contains("regenerated base tracks the moving worktree", baseContent, "AlphaEdited")
				v.Equal("store regenerated as a single fresh base", 1, len(layers.Layers))
				v.Equal("manifest reflects the regenerated layer", 1, len(t2Layers))
				if len(t2Layers) == 1 {
					v.Equal("manifest layer hash is the fresh base", baseHash, t2Layers[0].ContentHash)
				}
				v.NotContains("no immutability violation reported (opted out)", jobLog, "immutable")
			})
		}),
	},
)

// fileExists reports whether path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
