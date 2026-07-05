package scenarios

// oracle-cache-lineage suite, part 1/4 — layer store mechanics (spec 19 §5
// scenarios 1–5): turn-1 freeze, byte immutability across turns, rules-file
// widening (append-only diff layers), overlap dedup, and the frozen-snapshot
// semantics for worktree edits (staleness advisory, never a silent refresh).

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"

	"github.com/grovetools/flow/pkg/orchestration"
)

// OracleCacheLayer0CreationScenario — spec 19 e2e scenario 1.
var OracleCacheLayer0CreationScenario = harness.NewScenario(
	"layer0-creation",
	"Fresh chat turn 1 freezes 00-base.xml + layers.json + snapshot.json; manifest order [system, layer-0, turn] with breakpoints and ttl: 1h.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "layer0")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Add chat job with a rules file and run turn 1", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "layer0-chat", "Describe alpha.", map[string]interface{}{
				"rules_file": "base.rules",
			})
			if err != nil {
				return err
			}
			ctx.Set("job_id", job.ID)
			ctx.Set("job_filename", job.Filename)
			return env.runTurn(ctx, job.Filename).AssertSuccess()
		}),
		harness.NewStep("Layer store artifacts exist and manifest shows the ladder", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")

			basePath := filepath.Join(env.layersDir(jobID), "00-base.xml")
			if err := fs.AssertExists(basePath); err != nil {
				return err
			}
			if err := fs.AssertExists(filepath.Join(env.layersDir(jobID), "layers.json")); err != nil {
				return err
			}
			if err := fs.AssertExists(env.snapshotPath(jobID)); err != nil {
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
			if len(manifests) != 1 {
				return fmt.Errorf("expected 1 request manifest after turn 1, got %d", len(manifests))
			}
			m := manifests[0]
			baseHash, err := sha256File(basePath)
			if err != nil {
				return err
			}
			baseContent, err := fs.ReadString(basePath)
			if err != nil {
				return err
			}

			return ctx.Verify(func(v *verify.Collector) {
				// Layer store shape.
				v.Equal("layers.json has exactly the frozen base", 1, len(layers.Layers))
				v.Equal("base layer source", "rules-base", layers.Layers[0].Source)
				v.Equal("base layer hash matches artifact bytes", baseHash, layers.Layers[0].Hash)
				v.Contains("base layer captured alpha.go", baseContent, "func Alpha")
				v.NotContains("base layer excludes files outside the rules", baseContent, "func Beta")

				// Manifest: order [system, layer, turn]; ttl 1h; ladder; mock provider.
				v.Equal("manifest block order", "system,layer,turn", strings.Join(entryKinds(m), ","))
				v.Equal("manifest provider", "mock", m.Provider)
				v.Equal("manifest cache layout", "ladder", m.CacheLayout)
				v.Equal("manifest cache ttl defaults to 1h", "1h", m.CacheTTL)
				v.Equal("no history region on turn 1", 0, len(entriesOfKind(m, "history")))

				sys := entriesOfKind(m, "system")
				v.Equal("one system entry", 1, len(sys))
				if len(sys) == 1 {
					v.True("system carries a breakpoint", sys[0].Breakpoint)
					v.Equal("system breakpoint ttl", "1h", sys[0].TTL)
				}
				layerEntries := entriesOfKind(m, "layer")
				v.Equal("one layer entry (the frozen base)", 1, len(layerEntries))
				if len(layerEntries) == 1 {
					v.Equal("layer entry path", basePath, layerEntries[0].Path)
					v.Equal("layer entry hash matches the artifact", baseHash, layerEntries[0].ContentHash)
					v.True("layer-0 carries a breakpoint", layerEntries[0].Breakpoint)
					v.Equal("layer breakpoint ttl", "1h", layerEntries[0].TTL)
					v.Equal("layer entry provenance source", "rules-base", layerEntries[0].Source)
				}
				turn := entriesOfKind(m, "turn")
				v.Equal("one volatile turn entry", 1, len(turn))
				if len(turn) == 1 {
					v.True("turn block never carries a breakpoint", !turn[0].Breakpoint)
				}
			})
		}),
	},
)

// OracleCacheByteImmutabilityScenario — spec 19 e2e scenario 2.
var OracleCacheByteImmutabilityScenario = harness.NewScenario(
	"byte-immutability",
	"Turn 2 with unchanged rules: 00-base.xml byte-identical, no new layer, manifest layer hashes unchanged, history block present with breakpoint.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "immutable")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run turn 1 and record the frozen base", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "immutable-chat", "Describe alpha.", map[string]interface{}{
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
		harness.NewStep("Run turn 2 with rules unchanged", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			if err := appendUserTurn(ctx.GetString("job_path"), "Follow-up question about alpha."); err != nil {
				return err
			}
			return env.runTurn(ctx, ctx.GetString("job_filename")).AssertSuccess()
		}),
		harness.NewStep("Base bytes and manifest layer hashes are unchanged; history region appears", func(ctx *harness.Context) error {
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
			manifests, err := env.loadManifests(jobID)
			if err != nil {
				return err
			}
			if len(manifests) != 2 {
				return fmt.Errorf("expected 2 request manifests after 2 turns, got %d", len(manifests))
			}
			t1Layers := entriesOfKind(manifests[0], "layer")
			t2Layers := entriesOfKind(manifests[1], "layer")
			t2History := entriesOfKind(manifests[1], "history")

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("00-base.xml is byte-identical across turns", ctx.GetString("base_hash_t1"), baseHash)
				v.Equal("no new layer appended", 1, len(layers.Layers))
				v.Equal("turn-2 uploads the same single layer", len(t1Layers), len(t2Layers))
				if len(t1Layers) == 1 && len(t2Layers) == 1 {
					v.Equal("layer hash unchanged across turns", t1Layers[0].ContentHash, t2Layers[0].ContentHash)
				}
				v.Equal("turn 2 carries the prior turns as history blocks", 2, len(t2History))
				if len(t2History) == 2 {
					v.True("history region ends with a breakpoint", t2History[len(t2History)-1].Breakpoint)
					v.Equal("history breakpoint ttl", "1h", t2History[len(t2History)-1].TTL)
					v.True("non-final history blocks carry no breakpoint", !t2History[0].Breakpoint)
				}
			})
		}),
	},
)

// OracleCacheRulesWideningScenario — spec 19 e2e scenario 3.
var OracleCacheRulesWideningScenario = harness.NewScenario(
	"rules-widening",
	"Adding a glob to the rules file between turns appends 01-add-*.xml with ONLY the new files; layer-0 untouched; manifest appends the layer after existing ones.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "widening")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run turn 1 against alpha-only rules", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "widening-chat", "Describe alpha.", map[string]interface{}{
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
		harness.NewStep("Widen the rules file and run turn 2", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			if err := fs.WriteString(filepath.Join(env.ProjectDir, "base.rules"), "alpha.go\nbeta.go\n"); err != nil {
				return err
			}
			if err := appendUserTurn(ctx.GetString("job_path"), "Now also consider beta."); err != nil {
				return err
			}
			return env.runTurn(ctx, ctx.GetString("job_filename")).AssertSuccess()
		}),
		harness.NewStep("New layer holds only the new file; base untouched; manifest appends after existing layers", func(ctx *harness.Context) error {
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
			diffLayer, err := findLayerBySource(layers, "rules-diff")
			if err != nil {
				return err
			}
			diffContent, err := fs.ReadString(filepath.Join(env.layersDir(jobID), diffLayer.File))
			if err != nil {
				return err
			}
			manifests, err := env.loadManifests(jobID)
			if err != nil {
				return err
			}
			t2Layers := entriesOfKind(manifests[len(manifests)-1], "layer")

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("layer-0 bytes untouched", ctx.GetString("base_hash_t1"), baseHash)
				v.Equal("two layers after widening", 2, len(layers.Layers))
				v.True("diff layer filename is an add layer", strings.HasPrefix(diffLayer.File, "01-add-"))
				v.Contains("diff layer contains the NEW file", diffContent, "func Beta")
				v.NotContains("diff layer does not re-upload layer-0 files", diffContent, "func Alpha")
				v.Equal("diff layer records only beta.go", "beta.go", strings.Join(layerFilePaths(diffLayer), ","))
				v.Equal("turn-2 manifest uploads both layers", 2, len(t2Layers))
				if len(t2Layers) == 2 {
					v.Equal("existing layer rides first", ctx.GetString("base_hash_t1"), t2Layers[0].ContentHash)
					v.Equal("appended layer rides after it", diffLayer.Hash, t2Layers[1].ContentHash)
					v.True("breakpoint moved to the LAST layer", t2Layers[1].Breakpoint)
					v.True("earlier layer carries no breakpoint", !t2Layers[0].Breakpoint)
				}
			})
		}),
	},
)

// OracleCacheWideningDedupScenario — spec 19 e2e scenario 4.
var OracleCacheWideningDedupScenario = harness.NewScenario(
	"widening-dedup",
	"A new glob overlapping layer-0 files appends only the genuinely-new files; overlapping files are never duplicated.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "dedup")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run turn 1 against alpha-only rules", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "dedup-chat", "Describe alpha.", map[string]interface{}{
				"rules_file": "base.rules",
			})
			if err != nil {
				return err
			}
			ctx.Set("job_id", job.ID)
			ctx.Set("job_filename", job.Filename)
			ctx.Set("job_path", job.FilePath)
			return env.runTurn(ctx, job.Filename).AssertSuccess()
		}),
		harness.NewStep("Replace the glob with an overlapping wildcard and run turn 2", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			// *.go overlaps alpha.go (already in layer-0) and adds beta.go.
			if err := fs.WriteString(filepath.Join(env.ProjectDir, "base.rules"), "*.go\n"); err != nil {
				return err
			}
			if err := appendUserTurn(ctx.GetString("job_path"), "Widen to the whole package."); err != nil {
				return err
			}
			return env.runTurn(ctx, ctx.GetString("job_filename")).AssertSuccess()
		}),
		harness.NewStep("Only genuinely-new files land in the appended layer", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobID := ctx.GetString("job_id")

			layers, err := env.loadLayers(jobID)
			if err != nil {
				return err
			}
			diffLayer, err := findLayerBySource(layers, "rules-diff")
			if err != nil {
				return err
			}
			diffContent, err := fs.ReadString(filepath.Join(env.layersDir(jobID), diffLayer.File))
			if err != nil {
				return err
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("exactly two layers (base + one diff)", 2, len(layers.Layers))
				v.Equal("diff layer records only the genuinely-new file", "beta.go", strings.Join(layerFilePaths(diffLayer), ","))
				v.NotContains("overlapping alpha.go NOT duplicated into the new layer", diffContent, "func Alpha")
				v.Contains("new beta.go present", diffContent, "func Beta")
			})
		}),
	},
)

// OracleCacheWorktreeEditFrozenScenario — spec 19 e2e scenario 5.
var OracleCacheWorktreeEditFrozenScenario = harness.NewScenario(
	"worktree-edit-frozen",
	"Editing a layer-0 file between turns leaves the frozen bytes untouched, appends nothing uninvited, and logs a staleness advisory to job.log.",
	[]string{"oracle-cache-lineage", "chat", "cache"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "frozen")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Run turn 1", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			job, err := env.addChat(ctx, "frozen-chat", "Describe alpha.", map[string]interface{}{
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
		harness.NewStep("Edit the captured file and run a NORMAL turn 2 (no refresh verb)", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			if err := fs.WriteString(filepath.Join(env.ProjectDir, "alpha.go"), oracleAlphaV2); err != nil {
				return err
			}
			if err := appendUserTurn(ctx.GetString("job_path"), "Anything new?"); err != nil {
				return err
			}
			return env.runTurn(ctx, ctx.GetString("job_filename")).AssertSuccess()
		}),
		harness.NewStep("Frozen bytes stay, no uninvited delta layer, staleness advisory logged", func(ctx *harness.Context) error {
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
			jobLog := env.readJobLog(jobID)

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("layer-0 bytes unchanged (snapshot semantics)", ctx.GetString("base_hash_t1"), baseHash)
				v.NotContains("frozen layer still holds the ORIGINAL bytes", baseContent, "AlphaEdited")
				v.Equal("no delta layer appeared uninvited", 1, len(layers.Layers))
				v.Contains("job.log carries the staleness advisory", jobLog, "Context staleness advisory")
				v.Contains("advisory names the refresh verbs", jobLog, "--append-delta")
			})
		}),
		harness.NewStep("Chat is healthy after both turns", func(ctx *harness.Context) error {
			return oracleEnv(ctx).assertJobStatus("frozen-chat", orchestration.JobStatusPendingUser)
		}),
	},
)
