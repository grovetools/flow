package orchestration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	anthropicmodels "github.com/grovetools/grove-anthropic/pkg/models"
)

// Cross-job cache lineage (spec 19 P5 / D8). Anthropic's prompt cache is
// org-scoped and content-keyed: a request whose byte prefix matches a stored
// prefix cache-reads it even across separate jobs and API clients (verified
// live by the P0 probe). A chat that depends on completed chat jobs therefore
// STARTS its layer sequence with the parents' exact layer artifacts — as
// read-only references, byte-identical and in identical order — so its first
// turn reads the parents' warm prefix instead of paying a cold write. On top
// of the inherited sequence it appends, in order:
//
//  1. one dep-transcript layer per parent — the parent chat's completed
//     conversation rendered as a layer document. This REPLACES prompt-text
//     inlining (`inline: dependencies`) for chat deps: the transcript rides in
//     the cached document region once, instead of being re-billed inside the
//     volatile prompt every turn;
//  2. one auto git-delta layer — files captured by the inherited layers whose
//     worktree content has changed since the parents froze them (the code
//     moved between the parent completing and this chat starting), superseding
//     the stale inherited copies;
//  3. its own rules layers — the job's rules sweep diffed against the
//     inherited union, so files the lineage already carries are never
//     duplicated.
//
// The guard: inherited prefixes only ever cache-hit under the SAME model, so
// a model mismatch between parent and child logs a warning and starts fresh
// layers (no inherited refs) — the turn still succeeds (spec 19 e2e 13).

// LineageParent identifies one completed chat dependency whose layer lineage
// this job extends. Built by executeChatJob from the job's depends_on entries
// (in depends_on order) and consumed by the layer engine.
type LineageParent struct {
	// JobID / Title / FilePath identify the parent chat job; FilePath is the
	// parent's .md file, the dep-transcript source.
	JobID    string
	Title    string
	FilePath string
	// PlanDir is the plan directory owning the parent's .artifacts store.
	PlanDir string
	// Model is the parent's resolved effective model; ModelMatch reports
	// whether it equals this job's effective model (the D8 lineage guard).
	Model      string
	ModelMatch bool
}

// lineageEffectiveModel resolves a dependency chat's effective model for the
// lineage guard, mirroring executeChatJob's model precedence minus the
// per-run inputs that don't survive a completed job (CLI --model, turn
// directives): job frontmatter → plan config → global oneshot model →
// provider default. Returns the alias-resolved model ID.
func lineageEffectiveModel(dep *Job, plan *Plan) string {
	switch {
	case dep.Model != "":
		return resolveModelAlias(dep.Model)
	case plan.Config != nil && plan.Config.Model != "":
		return resolveModelAlias(plan.Config.Model)
	case plan.Orchestration != nil && plan.Orchestration.OneshotModel != "":
		return resolveModelAlias(plan.Orchestration.OneshotModel)
	default:
		return resolveModelAlias(anthropicmodels.DefaultModel)
	}
}

// lineageParentKey extracts the parent job id from an InheritedFrom value —
// "<job-id>/<layer-file>" on inherited refs, bare "<job-id>" on dep-transcript
// layers.
func lineageParentKey(inheritedFrom string) string {
	if i := strings.IndexByte(inheritedFrom, '/'); i >= 0 {
		return inheritedFrom[:i]
	}
	return inheritedFrom
}

// integratedLineageParents reports which parent job ids the manifest already
// carries lineage for. Every integrated parent leaves at least a
// dep-transcript entry stamped with its job id, so membership here is the
// idempotence check that makes lineage assembly safe to re-run every turn
// (and is what lets a reopen pick up ONLY newly-completed deps).
func integratedLineageParents(m *LayerManifest) map[string]bool {
	set := make(map[string]bool)
	for _, e := range m.Layers {
		if e.Source == LayerSourceDepTranscript && e.InheritedFrom != "" {
			set[lineageParentKey(e.InheritedFrom)] = true
		}
	}
	return set
}

// integrateLineage appends the not-yet-integrated lineage parents to the
// manifest: inherited refs for every model-matched parent (in depends_on
// order, deduplicated by artifact hash — diamond dependencies contribute each
// artifact once, first occurrence wins), then one dep-transcript layer per
// parent, then one auto git-delta layer covering inherited files whose
// worktree content drifted since the parents froze them. Returns whether the
// manifest changed. Errors are hard failures for the turn: a parent whose
// artifacts are missing or rewritten is a broken lineage that must never be
// papered over silently.
func integrateLineage(ctx context.Context, writer io.Writer, p LayerEngineParams, layersDir string, manifest *LayerManifest) (bool, error) {
	if len(p.Lineage) == 0 {
		return false, nil
	}
	integrated := integratedLineageParents(manifest)
	seenHashes := make(map[string]bool, len(manifest.Layers))
	for _, e := range manifest.Layers {
		seenHashes[e.Hash] = true
	}

	var pending []LineageParent
	for _, parent := range p.Lineage {
		if parent.JobID == "" || integrated[parent.JobID] {
			continue
		}
		pending = append(pending, parent)
	}
	if len(pending) == 0 {
		return false, nil
	}

	// 1. Inherited refs, in depends_on order: each parent's exact layer
	// sequence, referenced (never copied) as absolute paths into the parent's
	// immutable context-layers dir — one copy of each artifact on disk per
	// lineage (spec 19 §3). Every referenced artifact is verified against the
	// parent's recorded hash at inheritance time.
	var newInherited []LayerEntry
	for _, parent := range pending {
		parentLayersDir := ContextLayersDir(parent.PlanDir, parent.JobID)
		parentManifest, err := LoadLayerManifest(parentLayersDir)
		if err != nil {
			return false, fmt.Errorf("loading layer manifest of dependency chat %s: %w", parent.JobID, err)
		}
		switch {
		case parentManifest == nil:
			// No layer store on the parent (pre-layer-engine chat, agent-
			// responded chat, or a rules-less chat): nothing to inherit; the
			// dep-transcript layer below still carries the conversation.
			fmt.Fprintf(writer, "Context lineage: dependency chat %s has no layer store — nothing to inherit (its transcript still rides as a layer)\n", parent.JobID)
		case !parent.ModelMatch:
			// D8 guard: a cached prefix only hits under the same model, so
			// inherited refs from a different-model parent buy nothing and
			// would misprice the lineage. Warn and start fresh — never fail
			// the turn (spec 19 e2e 13).
			fmt.Fprintf(writer, "Context lineage warning: dependency chat %s ran on model %s but this chat resolves to a different model — starting fresh layers (no inherited refs); align the models to share the parent's cache lineage\n", parent.JobID, parent.Model)
			ulog.Warn("Cross-job lineage model mismatch — not inheriting parent layers").
				Field("job_id", p.JobID).
				Field("parent_job_id", parent.JobID).
				Field("parent_model", parent.Model).
				Log(ctx)
		default:
			inheritedCount := 0
			for _, pe := range parentManifest.Layers {
				absPath := LayerArtifactPath(parentLayersDir, pe)
				data, err := os.ReadFile(absPath)
				if err != nil {
					return false, fmt.Errorf("inheriting layers from dependency chat %s: artifact %s is missing or unreadable — the parent's context layers were removed or rebased, breaking the cache lineage; run this chat with --rebase-context to build fresh context without the inherited refs: %w", parent.JobID, absPath, err)
				}
				if got := sha256Hex(data); got != pe.Hash {
					return false, fmt.Errorf("inheriting layers from dependency chat %s: artifact %s changed after the parent froze it (hash %s, recorded %s) — the parent's context was rebased, breaking the cache lineage; run this chat with --rebase-context to build fresh context without the inherited refs", parent.JobID, absPath, got, pe.Hash)
				}
				if seenHashes[pe.Hash] {
					// Diamond deps: the same artifact already rides earlier in
					// this job's sequence (e.g. two parents that share a
					// grandparent) — include it once, first occurrence wins.
					continue
				}
				seenHashes[pe.Hash] = true
				inheritedFrom := pe.InheritedFrom
				if inheritedFrom == "" {
					inheritedFrom = parent.JobID + "/" + pe.File
				}
				entry := LayerEntry{
					N:             len(manifest.Layers),
					File:          absPath,
					Source:        LayerSourceInherited,
					Hash:          pe.Hash,
					Bytes:         pe.Bytes,
					RulesHash:     pe.RulesHash,
					GitHeads:      pe.GitHeads,
					Dirty:         pe.Dirty,
					Files:         pe.Files,
					Supersedes:    pe.Supersedes,
					InheritedFrom: inheritedFrom,
					TurnID:        p.TurnID,
					CreatedAt:     time.Now().UTC(),
					// Copy the parent's AnchorExchange (spec 27): the child
					// inherits the parent's exchanges verbatim, so a parent's
					// interleaved-layer anchor id stays a valid key in the
					// child's stream and reproduces the layer in position rather
					// than collapsing it to the head. "" for head-region /
					// ladder-parent layers.
					AnchorExchange: pe.AnchorExchange,
				}
				manifest.Layers = append(manifest.Layers, entry)
				newInherited = append(newInherited, entry)
				inheritedCount++
			}
			fmt.Fprintf(writer, "Context lineage: inherited %d layer(s) from dependency chat %s (read-only refs into its context-layers)\n", inheritedCount, parent.JobID)
			ulog.Info("Inherited context layers from dependency chat").
				Field("job_id", p.JobID).
				Field("parent_job_id", parent.JobID).
				Field("layers", inheritedCount).
				Log(ctx)
		}
	}

	// 2. One dep-transcript layer per parent, in depends_on order: the
	// parent's completed conversation as a layer document. Appended regardless
	// of the model guard — the transcript is this job's OWN artifact, cached
	// under this job's model, and it is the vehicle that replaces inline:
	// dependencies for chat deps (spec 19 e2e 11).
	for _, parent := range pending {
		content, err := os.ReadFile(parent.FilePath)
		if err != nil {
			return false, fmt.Errorf("reading dependency chat %s for its transcript layer: %w", parent.FilePath, err)
		}
		n := len(manifest.Layers)
		data, err := renderDepTranscriptLayer(n, parent, content)
		if err != nil {
			return false, err
		}
		name := fmt.Sprintf("%02d-transcript-%s.xml", n, parent.JobID)
		if err := WriteLayerArtifact(filepath.Join(layersDir, name), data); err != nil {
			return false, err
		}
		manifest.Layers = append(manifest.Layers, LayerEntry{
			N:             n,
			File:          name,
			Source:        LayerSourceDepTranscript,
			Hash:          sha256Hex(data),
			Bytes:         int64(len(data)),
			InheritedFrom: parent.JobID,
			TurnID:        p.TurnID,
			CreatedAt:     time.Now().UTC(),
		})
		fmt.Fprintf(writer, "Context lineage: appended %s (completed transcript of dependency chat %s)\n", name, parent.JobID)
	}

	// 3. Auto git-delta against the inherited lineage's recorded state:
	// re-render every file the newly-inherited layers captured and supersede
	// the ones whose content drifted (a commit landing between the parent
	// completing and this turn — spec 19 e2e 12). Content-hash based, so it is
	// correct for committed and uncommitted changes alike. Files that vanished
	// from the worktree can't be re-uploaded; they get a removal annotation.
	if len(newInherited) > 0 {
		// Keyed canonically against THIS job's resolution root: parent stores
		// record worktree-relative keys, but a parent poisoned with foreign
		// absolute spellings must still map onto this worktree's files.
		union := make(map[string]LayerFileRecord)
		for _, e := range newInherited {
			for _, f := range e.Files {
				union[canonicalLayerKey(p.ContextDir, f.Path)] = f
			}
		}
		paths := make([]string, 0, len(union))
		for path := range union {
			paths = append(paths, path)
		}
		sort.Strings(paths)

		var changedFiles []string
		for _, path := range paths {
			rf, err := readRenderedFile(p.ContextDir, path, p.StripComments)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					manifest.Removals = append(manifest.Removals, LayerRemoval{Path: path, TurnID: p.TurnID, At: time.Now().UTC()})
					fmt.Fprintf(writer, "Context lineage: inherited file %s no longer exists in the worktree — removal recorded (its frozen bytes stay uploaded)\n", path)
					continue
				}
				return false, fmt.Errorf("computing lineage git-delta for %s: %w", path, err)
			}
			if rf.Hash != union[path].Hash {
				changedFiles = append(changedFiles, path)
			}
		}
		if len(changedFiles) > 0 {
			heads, dirty := collectGitState(p.ContextDir)
			n := len(manifest.Layers)
			name := fmt.Sprintf("%02d-delta-%s.xml", n, deltaSlug(heads))
			// The lineage git-delta completes the child's head material: under
			// stream lineage p.AnchorExchange is the parent's final exchange id,
			// so this delta anchors after the inherited stream, before the
			// child's q1; "" (head) otherwise (spec 27 §3).
			data, records, err := renderLayerXML(p.ContextDir, p.StripComments, n, LayerSourceGitDelta, changedFiles, changedFiles, p.AnchorExchange)
			if err != nil {
				return false, err
			}
			if err := WriteLayerArtifact(filepath.Join(layersDir, name), data); err != nil {
				return false, err
			}
			manifest.Layers = append(manifest.Layers, LayerEntry{
				N:              n,
				File:           name,
				Source:         LayerSourceGitDelta,
				Hash:           sha256Hex(data),
				Bytes:          int64(len(data)),
				GitHeads:       heads,
				Dirty:          dirty,
				Files:          records,
				Supersedes:     append([]string{}, changedFiles...),
				TurnID:         p.TurnID,
				CreatedAt:      time.Now().UTC(),
				AnchorExchange: p.AnchorExchange,
			})
			fmt.Fprintf(writer, "Context lineage: appended delta %s (%d file(s) changed since the inherited lineage was frozen)\n", name, len(changedFiles))
			ulog.Info("Appended lineage git-delta layer").
				Field("job_id", p.JobID).
				Field("layer", name).
				Field("changed_files", len(changedFiles)).
				Log(ctx)
		}
	}

	return true, nil
}

// renderDepTranscriptLayer renders a completed parent chat's conversation as
// a dep-transcript layer document:
//
//	<layer n="2" source="dep-transcript" job="design-chat-abc" file="03-design.md">
//	<turn role="user">…</turn>
//	<turn role="assistant" template="chat" id="a1b2c3" timestamp="…">…</turn>
//	</layer>
//
// The turn serialization mirrors the history-region formatter (roles,
// template/id/timestamp attributes, grove markers stripped) but NEVER emits
// the volatile status="awaiting_response"/respond_as attributes — the parent
// is completed, so the rendering is deterministic and the artifact freezes
// byte-stably.
func renderDepTranscriptLayer(n int, parent LineageParent, content []byte) ([]byte, error) {
	transcript, err := renderChatTranscriptXML(content)
	if err != nil {
		return nil, fmt.Errorf("rendering transcript of dependency chat %s: %w", parent.JobID, err)
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "<layer n=\"%d\" source=\"dep-transcript\" job=%q file=%q>\n", n, parent.JobID, filepath.Base(parent.FilePath))
	buf.WriteString(transcript)
	buf.WriteString("</layer>\n")
	return buf.Bytes(), nil
}

// renderChatTranscriptXML serializes a chat file's completed turns as <turn>
// elements (see renderDepTranscriptLayer).
func renderChatTranscriptXML(content []byte) (string, error) {
	turns, err := ParseChatFile(content)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	var pendingTemplate string
	for _, turn := range turns {
		// Skip incomplete turns, mirroring the history formatter.
		if turn.Directive != nil {
			if state, ok := turn.Directive.Vars["state"].(string); ok {
				if state == "running" || state == "pending" {
					continue
				}
			}
		}
		role := "assistant"
		if turn.Speaker == "user" {
			role = "user"
		}
		attrs := []string{fmt.Sprintf(`role="%s"`, role)}
		body := turn.Content
		if role == "user" {
			if turn.Directive != nil && turn.Directive.Template != "" {
				pendingTemplate = turn.Directive.Template
			}
		} else {
			if pendingTemplate != "" {
				attrs = append(attrs, fmt.Sprintf(`template="%s"`, pendingTemplate))
				pendingTemplate = ""
			}
			if turn.Directive != nil && turn.Directive.ID != "" {
				attrs = append(attrs, fmt.Sprintf(`id="%s"`, turn.Directive.ID))
			}
			if matches := timestampRegex.FindStringSubmatch(body); len(matches) > 1 {
				attrs = append(attrs, fmt.Sprintf(`timestamp="%s"`, matches[1]))
				body = timestampRegex.ReplaceAllString(body, "")
			}
		}
		cleaned := cleanTurnContent(body)
		if cleaned == "" {
			continue
		}
		sb.WriteString(formatTurnXML(attrs, cleaned))
	}
	return sb.String(), nil
}
