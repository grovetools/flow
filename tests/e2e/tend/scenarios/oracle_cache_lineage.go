package scenarios

// oracle-cache-lineage suite, part 3/4 — cross-job cache lineage (spec 19 §5
// scenarios 10–13): inherited layer refs, dep-transcript layers replacing
// prompt inlining, the auto git-delta over inherited files, and the
// model-mismatch guard.

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"

	"github.com/grovetools/flow/pkg/orchestration"
)

// lineageModelA / lineageModelB are concrete model IDs (no alias resolution
// surprises). Both chats stay on the mock LLM either way.
const (
	lineageModelA = "claude-3-5-sonnet-20241022"
	lineageModelB = "claude-3-5-haiku-20241022"
)

// setupLineageParent adds chat A (rules: alpha.go, auto_complete so one mock
// turn completes it), runs it to completion, and stores its identifiers.
func setupLineageParent(ctx *harness.Context, model string) error {
	env := oracleEnv(ctx)
	jobA, err := env.addChat(ctx, "parent-chat", "Design the alpha subsystem.", map[string]interface{}{
		"rules_file":    "base.rules",
		"model":         model,
		"auto_complete": true,
	})
	if err != nil {
		return err
	}
	ctx.Set("job_a_id", jobA.ID)
	ctx.Set("job_a_filename", jobA.Filename)
	if err := env.runTurn(ctx, jobA.Filename).AssertSuccess(); err != nil {
		return err
	}
	if err := env.assertJobStatus("parent-chat", orchestration.JobStatusCompleted); err != nil {
		return err
	}
	baseHash, err := sha256File(filepath.Join(env.layersDir(jobA.ID), "00-base.xml"))
	if err != nil {
		return err
	}
	ctx.Set("parent_base_hash", baseHash)
	return nil
}

// addLineageChild adds chat B depending on A, with its own beta.go rules.
func addLineageChild(ctx *harness.Context, model string) error {
	env := oracleEnv(ctx)
	if err := fs.WriteString(filepath.Join(env.ProjectDir, "child.rules"), "beta.go\n"); err != nil {
		return err
	}
	jobB, err := env.addChat(ctx, "child-chat", "Plan phase 2 on top of the design.", map[string]interface{}{
		"rules_file": "child.rules",
		"model":      model,
	}, "--depends-on", ctx.GetString("job_a_filename"))
	if err != nil {
		return err
	}
	ctx.Set("job_b_id", jobB.ID)
	ctx.Set("job_b_filename", jobB.Filename)
	return nil
}

// addLineageSibling adds a sibling chat titled `title` depending on completed
// chat A, with the shared child.rules (beta.go). Because every sibling inherits
// the same parent lineage on the same model, its lineage prefix — the inherited
// base ref + the dep-transcript — is byte-identical across siblings, which is
// the precondition the K1 lineage-boundary breakpoint exploits.
func addLineageSibling(ctx *harness.Context, title, model string) (*orchestration.Job, error) {
	env := oracleEnv(ctx)
	if err := fs.WriteString(filepath.Join(env.ProjectDir, "child.rules"), "beta.go\n"); err != nil {
		return nil, err
	}
	return env.addChat(ctx, title, "Plan phase 2 on top of the design.", map[string]interface{}{
		"rules_file": "child.rules",
		"model":      model,
	}, "--depends-on", ctx.GetString("job_a_filename"))
}

// OracleCacheSiblingFanoutScenario — K1 (oracle-plays/41): a sibling fan-out
// over one parent lineage. Two children depend on the same completed parent;
// each child's request manifest must carry the lineage-boundary breakpoint on
// its LAST lineage-sourced (dep-transcript) layer, and the two children must be
// byte-identical through that boundary by (Kind, ContentHash) — the prefix a
// second sibling cache-READs instead of re-writing.
var OracleCacheSiblingFanoutScenario = harness.NewScenario(
	"sibling-fanout",
	"Two sibling chats inheriting the same completed parent share a byte-identical lineage prefix; each manifest carries the K1 lineage-boundary breakpoint on its last dep-transcript layer.",
	[]string{"oracle-cache-lineage", "chat", "cache", "lineage"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "fanout")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Complete parent chat A", func(ctx *harness.Context) error {
			return setupLineageParent(ctx, lineageModelA)
		}),
		harness.NewStep("Fan out two sibling chats on A (same model), run each", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			b, err := addLineageSibling(ctx, "sibling-b", lineageModelA)
			if err != nil {
				return err
			}
			ctx.Set("job_b_id", b.ID)
			if err := env.runTurn(ctx, b.Filename).AssertSuccess(); err != nil {
				return err
			}
			c, err := addLineageSibling(ctx, "sibling-c", lineageModelA)
			if err != nil {
				return err
			}
			ctx.Set("job_c_id", c.ID)
			return env.runTurn(ctx, c.Filename).AssertSuccess()
		}),
		harness.NewStep("Both siblings breakpoint the lineage boundary and share it byte-for-byte", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			bManifests, err := env.loadManifests(ctx.GetString("job_b_id"))
			if err != nil {
				return err
			}
			cManifests, err := env.loadManifests(ctx.GetString("job_c_id"))
			if err != nil {
				return err
			}
			if len(bManifests) == 0 || len(cManifests) == 0 {
				return fmt.Errorf("missing sibling manifests (B=%d, C=%d)", len(bManifests), len(cManifests))
			}
			bLayers := entriesOfKind(bManifests[len(bManifests)-1], "layer")
			cLayers := entriesOfKind(cManifests[len(cManifests)-1], "layer")

			// Lineage prefix is [inherited, dep-transcript]; index 1 is the last
			// lineage-sourced layer (the K1 boundary), then the child's own base.
			const lineagePrefix = 2

			return ctx.Verify(func(v *verify.Collector) {
				v.True("sibling B has the full lineage prefix + own base", len(bLayers) >= 3)
				v.True("sibling C has the full lineage prefix + own base", len(cLayers) >= 3)
				if len(bLayers) < lineagePrefix || len(cLayers) < lineagePrefix {
					return
				}
				v.Equal("B boundary layer is the dep-transcript", "dep-transcript", bLayers[lineagePrefix-1].Source)
				v.Equal("C boundary layer is the dep-transcript", "dep-transcript", cLayers[lineagePrefix-1].Source)
				v.True("K1: B breakpoints its last lineage layer", bLayers[lineagePrefix-1].Breakpoint)
				v.True("K1: C breakpoints its last lineage layer", cLayers[lineagePrefix-1].Breakpoint)
				v.Equal("earlier inherited layer carries no boundary breakpoint (B)", false, bLayers[0].Breakpoint)
				v.Equal("earlier inherited layer carries no boundary breakpoint (C)", false, cLayers[0].Breakpoint)
				// The child's own last layer keeps its breakpoint too (BP on both
				// the boundary and the last layer — the ladder's full 4-BP budget).
				v.True("B still breakpoints its own last layer", bLayers[len(bLayers)-1].Breakpoint)
				v.True("C still breakpoints its own last layer", cLayers[len(cLayers)-1].Breakpoint)
				// Siblings share the lineage prefix byte-for-byte (Kind, ContentHash);
				// Path differs (each child owns its transcript copy) but bytes match,
				// so a second sibling cache-READs what the first wrote.
				for i := 0; i < lineagePrefix; i++ {
					v.Equal(fmt.Sprintf("lineage entry %d kind matches across siblings", i), bLayers[i].Kind, cLayers[i].Kind)
					v.Equal(fmt.Sprintf("lineage entry %d content hash matches across siblings", i), bLayers[i].ContentHash, cLayers[i].ContentHash)
				}
			})
		}),
	},
)

// OracleCacheLineageInheritScenario — spec 19 e2e scenario 10.
var OracleCacheLineageInheritScenario = harness.NewScenario(
	"lineage-inherit",
	"Chat B depending on completed chat A starts its layers.json with refs to A's artifacts (same hashes); B's manifest = A's layer sequence + dep-transcript + B's own layers.",
	[]string{"oracle-cache-lineage", "chat", "cache", "lineage"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "inherit")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Complete parent chat A", func(ctx *harness.Context) error {
			return setupLineageParent(ctx, lineageModelA)
		}),
		harness.NewStep("Add and run dependent chat B (same model)", func(ctx *harness.Context) error {
			if err := addLineageChild(ctx, lineageModelA); err != nil {
				return err
			}
			env := oracleEnv(ctx)
			return env.runTurn(ctx, ctx.GetString("job_b_filename")).AssertSuccess()
		}),
		harness.NewStep("B inherits A's layer sequence by reference and extends it", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobAID := ctx.GetString("job_a_id")
			jobBID := ctx.GetString("job_b_id")

			layersB, err := env.loadLayers(jobBID)
			if err != nil {
				return err
			}
			if len(layersB.Layers) < 3 {
				return fmt.Errorf("expected >=3 layers on B (inherited + transcript + own base), got: %s", describeLayers(layersB))
			}
			inherited := layersB.Layers[0]
			manifests, err := env.loadManifests(jobBID)
			if err != nil {
				return err
			}
			bLayers := entriesOfKind(manifests[len(manifests)-1], "layer")
			parentBasePath := filepath.Join(env.layersDir(jobAID), "00-base.xml")

			ownBase, err := findLayerBySource(layersB, "rules-base")
			if err != nil {
				return err
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("B's first layer is an inherited ref", "inherited", inherited.Source)
				v.Equal("inherited ref points at A's artifact (absolute path, one copy on disk)", parentBasePath, inherited.File)
				v.Equal("inherited ref carries A's exact hash", ctx.GetString("parent_base_hash"), inherited.Hash)
				v.Equal("inherited provenance names the owning job/layer", jobAID+"/00-base.xml", inherited.InheritedFrom)
				v.Equal("B's second layer is A's transcript", "dep-transcript", layersB.Layers[1].Source)
				v.Equal("B's own rules base captures only its new file", "beta.go", strings.Join(layerFilePaths(ownBase), ","))

				v.Equal("B uploads the full lineage (A base + transcript + own base)", 3, len(bLayers))
				if len(bLayers) == 3 {
					v.Equal("lineage rides first, byte-identical to A's upload", ctx.GetString("parent_base_hash"), bLayers[0].ContentHash)
					v.Equal("A's artifact path is referenced, not copied", parentBasePath, bLayers[0].Path)
					v.Equal("inherited manifest entry source", "inherited", bLayers[0].Source)
					v.Equal("transcript manifest entry source", "dep-transcript", bLayers[1].Source)
					v.Equal("own base manifest entry source", "rules-base", bLayers[2].Source)
					v.True("breakpoint on the LAST layer of the lineage", bLayers[2].Breakpoint)
				}
			})
		}),
	},
)

// OracleCacheDepTranscriptScenario — spec 19 e2e scenario 11.
var OracleCacheDepTranscriptScenario = harness.NewScenario(
	"dep-transcript-layer",
	"A completed chat dependency's body rides as a dep-transcript layer document, NOT inlined into B's prompt text; B's briefing has no <prepended_dependency> for chat deps.",
	[]string{"oracle-cache-lineage", "chat", "cache", "lineage"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "transcript")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Complete parent chat A", func(ctx *harness.Context) error {
			return setupLineageParent(ctx, lineageModelA)
		}),
		harness.NewStep("Add and run dependent chat B", func(ctx *harness.Context) error {
			if err := addLineageChild(ctx, lineageModelA); err != nil {
				return err
			}
			env := oracleEnv(ctx)
			return env.runTurn(ctx, ctx.GetString("job_b_filename")).AssertSuccess()
		}),
		harness.NewStep("Transcript is a layer document, not prompt text", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobAID := ctx.GetString("job_a_id")
			jobBID := ctx.GetString("job_b_id")

			layersB, err := env.loadLayers(jobBID)
			if err != nil {
				return err
			}
			transcript, err := findLayerBySource(layersB, "dep-transcript")
			if err != nil {
				return err
			}
			transcriptContent, err := fs.ReadString(filepath.Join(env.layersDir(jobBID), transcript.File))
			if err != nil {
				return err
			}

			briefings, err := filepath.Glob(filepath.Join(env.PlanPath, ".artifacts", jobBID, "briefing-*.xml"))
			if err != nil {
				return err
			}
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file for chat B")
			}
			briefingContent, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}
			jobLog := env.readJobLog(jobBID)

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("transcript provenance names parent job", jobAID, transcript.InheritedFrom)
				v.Contains("transcript layer envelope", transcriptContent, `source="dep-transcript"`)
				v.Contains("transcript carries A's user turn", transcriptContent, "Design the alpha subsystem.")
				v.Contains("transcript carries A's assistant turn", transcriptContent, "Mock oracle response.")
				v.NotContains("transcript is byte-stable (no volatile marker)", transcriptContent, "awaiting_response")
				v.NotContains("briefing has no <prepended_dependency> for the chat dep", briefingContent, "<prepended_dependency")
				v.NotContains("A's body is NOT inlined into B's prompt text", briefingContent, "Design the alpha subsystem.")
				v.Contains("job.log records the transcript layer append", jobLog, "completed transcript of dependency chat")
			})
		}),
	},
)

// OracleCacheGitDeltaOnLineageScenario — spec 19 e2e scenario 12.
var OracleCacheGitDeltaOnLineageScenario = harness.NewScenario(
	"git-delta-on-lineage",
	"A commit landing between A's completion and B's turn 1 gives B an auto delta layer listing exactly the changed files, supersede-annotated against inherited layers.",
	[]string{"oracle-cache-lineage", "chat", "cache", "lineage"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "lindelta")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Complete parent chat A", func(ctx *harness.Context) error {
			return setupLineageParent(ctx, lineageModelA)
		}),
		harness.NewStep("Land a commit changing an inherited file, then run B", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			if err := fs.WriteString(filepath.Join(env.ProjectDir, "alpha.go"), oracleAlphaV2); err != nil {
				return err
			}
			if err := env.Repo.AddCommit("impl lands between A and B"); err != nil {
				return err
			}
			if err := addLineageChild(ctx, lineageModelA); err != nil {
				return err
			}
			return env.runTurn(ctx, ctx.GetString("job_b_filename")).AssertSuccess()
		}),
		harness.NewStep("B carries an auto git-delta over the inherited lineage", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobBID := ctx.GetString("job_b_id")

			layersB, err := env.loadLayers(jobBID)
			if err != nil {
				return err
			}
			delta, err := findLayerBySource(layersB, "git-delta")
			if err != nil {
				return err
			}
			deltaContent, err := fs.ReadString(filepath.Join(env.layersDir(jobBID), delta.File))
			if err != nil {
				return err
			}
			jobLog := env.readJobLog(jobBID)

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("delta lists exactly the changed inherited file", "alpha.go", strings.Join(layerFilePaths(delta), ","))
				v.Equal("delta supersedes the inherited copy", "alpha.go", strings.Join(delta.Supersedes, ","))
				v.Contains("delta artifact carries the annotation", deltaContent, `supersedes="alpha.go"`)
				v.Contains("delta carries the post-commit bytes", deltaContent, "AlphaEdited")
				v.Contains("job.log records the lineage delta", jobLog, "changed since the inherited lineage was frozen")
				// Order: inherited ref, dep-transcript, git-delta, own base.
				v.Equal("lineage order", "inherited,dep-transcript,git-delta,rules-base", lineageSourceOrder(layersB))
			})
		}),
	},
)

// OracleCacheLineageModelMismatchScenario — spec 19 e2e scenario 13.
var OracleCacheLineageModelMismatchScenario = harness.NewScenario(
	"lineage-model-mismatch",
	"A dependent chat on a different model gets a warning and fresh layers (no inherited refs); the turn still succeeds.",
	[]string{"oracle-cache-lineage", "chat", "cache", "lineage"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, err := setupOracleCacheEnv(ctx, "mismatch")
			return err
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),
		harness.NewStep("Complete parent chat A", func(ctx *harness.Context) error {
			return setupLineageParent(ctx, lineageModelA)
		}),
		harness.NewStep("Add and run dependent chat B on a DIFFERENT model", func(ctx *harness.Context) error {
			if err := addLineageChild(ctx, lineageModelB); err != nil {
				return err
			}
			env := oracleEnv(ctx)
			return env.runTurn(ctx, ctx.GetString("job_b_filename")).AssertSuccess()
		}),
		harness.NewStep("Warning logged; fresh layers with no inherited refs; turn succeeded", func(ctx *harness.Context) error {
			env := oracleEnv(ctx)
			jobBID := ctx.GetString("job_b_id")

			layersB, err := env.loadLayers(jobBID)
			if err != nil {
				return err
			}
			inheritedCount := 0
			for _, l := range layersB.Layers {
				if l.Source == "inherited" {
					inheritedCount++
				}
			}
			jobLog := env.readJobLog(jobBID)

			if err := env.assertJobStatus("child-chat", orchestration.JobStatusPendingUser); err != nil {
				return err
			}
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("no inherited refs under a model mismatch", 0, inheritedCount)
				v.Contains("job.log carries the lineage warning", jobLog, "Context lineage warning")
				v.Contains("warning names the parent's model", jobLog, lineageModelA)
				// The transcript is B's OWN artifact (cached under B's model) and
				// still rides; B's own base freezes fresh.
				v.Equal("fresh lineage shape: transcript + own base", "dep-transcript,rules-base", lineageSourceOrder(layersB))
			})
		}),
	},
)

// lineageSourceOrder renders the layer provenance sequence, e.g.
// "inherited,dep-transcript,rules-base".
func lineageSourceOrder(m *orchestration.LayerManifest) string {
	sources := make([]string, len(m.Layers))
	for i, l := range m.Layers {
		sources[i] = l.Source
	}
	return strings.Join(sources, ",")
}
