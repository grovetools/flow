package orchestration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	grovelogging "github.com/grovetools/core/logging"
	grovecontext "github.com/grovetools/cx/pkg/context"
)

// The layer engine (spec 19 P3, D3/D4): turns the job's rules file — the only
// context surface the user ever edits — into an append-only sequence of
// immutable layer artifacts whose byte/position stability is what keeps the
// Anthropic cache prefix warm across turns.
//
//   - Turn 1 freezes the full rules sweep as 00-base.xml (+ layers.json +
//     snapshot.json).
//   - Later turns re-resolve the rules and diff the fileset against the UNION
//     of all existing layers: genuinely-new files append a rules-diff layer;
//     no new files, no new layer. Files whose worktree content changed are
//     NOT refreshed (layer-0 is a snapshot) — a staleness advisory goes to
//     job.log instead. Files removed from the rules keep their bytes and get
//     a removal annotation.
//   - --append-delta appends a supersede-annotated delta layer with the
//     changed files (content-hash compare against the union — correct for
//     committed and uncommitted changes alike, no git archaeology).
//   - --rebase-context archives the whole lineage (never deletes) and
//     re-freezes a fresh 00-base.xml: the one deliberate cache-busting verb.
//   - context_snapshot: false opts out of freezing entirely: every turn wipes
//     and regenerates the store (every turn is a rebase), for debug-style
//     chats tracking a moving worktree.

// LayerRefreshMode selects the refresh verb for one chat turn (spec 19 D4).
type LayerRefreshMode int

const (
	// LayerRefreshNone is the default: diff rules, append new files only.
	LayerRefreshNone LayerRefreshMode = iota
	// LayerRefreshAppendDelta appends a supersede-annotated delta layer with
	// the files whose content changed since capture (cache-preserving).
	LayerRefreshAppendDelta
	// LayerRefreshRebase archives all existing layers and re-freezes a fresh
	// base from the current worktree (one deliberate cold write).
	LayerRefreshRebase
)

// rebaseAdvisoryRatio is the superseded-bytes fraction past which the engine
// suggests --rebase-context in job.log (spec 19 D4 — advise, never auto-bust).
const rebaseAdvisoryRatio = 0.30

// layerManifestVersion is the version stamped on manifests written by the
// current engine. v2 adds the stream layout's LayerEntry.AnchorExchange and
// LayerManifest.Layout (spec 27); v1 stores read back with zero-value fields
// (no anchors = head region, empty Layout = ladder).
const layerManifestVersion = 2

// LayerEngineParams carries one turn's inputs to PrepareContextLayers.
type LayerEngineParams struct {
	PlanDir    string // plan directory (owns .artifacts/)
	JobID      string
	ContextDir string // worktree/sub-project dir the rules resolve against
	RulesPath  string // resolved rules file for this turn
	TurnID     string
	// StripComments mirrors the job's strip_comments setting so layer bytes
	// match what the legacy cx generation would have uploaded.
	StripComments bool
	// SnapshotEnabled is false under `context_snapshot: false` (opt-out):
	// every turn regenerates the store from scratch and the immutability
	// guard is deliberately bypassed (the store is wiped first).
	SnapshotEnabled bool
	Refresh         LayerRefreshMode
	// Layout is the resolved chat cache layout for this turn (spec 27):
	// "ladder" (or empty, treated as ladder) or "stream". It is stamped on a
	// fresh manifest and checked against an existing store's stamp — a
	// disagreement fails the turn, except the free ladder→stream migration.
	Layout string
	// AnchorExchange is the directive id of the last COMPLETED assistant
	// exchange at this turn's freeze time (spec 27). Layers appended this turn
	// (rules-diff, git-delta, and the child's own base under lineage) carry it
	// so the stream interleave places them after that exchange. Empty on a
	// plain turn 1 and under ladder → head region.
	AnchorExchange string
	// Lineage lists the job's completed chat dependencies whose layer
	// sequences this job extends (spec 19 P5 / D8), in depends_on order.
	// Integrated append-only and idempotently: parents already represented in
	// the manifest are skipped, so a reopen picks up only newly-completed
	// deps.
	Lineage []LineageParent
}

// LayerEngineResult reports what the engine decided for this turn.
type LayerEngineResult struct {
	// LayerPaths are the ordered layer artifact paths to upload as the
	// request's LayerFiles (breakpoint on the last, per the ladder layout).
	LayerPaths []string
	// SourcesByPath maps each layer path to its provenance source, for
	// request-manifest annotation.
	SourcesByPath map[string]string
	// AnchorsByPath maps each layer path to its AnchorExchange (spec 27): ""
	// for head-region layers, else the directive id of the exchange the layer
	// interleaves after. The stream interleave in executeChatJob consults it.
	AnchorsByPath map[string]string
	// SupersededIndex is the compact `<current-files>` block (spec 27 §5b):
	// `path → layer N` for every superseded path in the lineage, or "" when
	// nothing is superseded. It rides in the volatile turn (uncached, both
	// layouts) so the oracle always has the winning-copy map even when a
	// supersession pair straddles interleaved dialogue.
	SupersededIndex string
	// AppendedLayer / DeltaLayer name the artifacts appended this turn
	// (empty when none). Rebased reports a --rebase-context re-freeze.
	AppendedLayer string
	DeltaLayer    string
	Rebased       bool
}

// PrepareContextLayers runs the layer engine for one chat turn and returns
// the ordered layer upload. Errors are hard failures for the turn — a layer
// store in an unknown state must never be papered over with a silent
// re-upload (that is exactly the silent cache bust this engine removes).
func PrepareContextLayers(ctx context.Context, p LayerEngineParams) (*LayerEngineResult, error) {
	writer := grovelogging.GetWriter(ctx)
	layersDir := ContextLayersDir(p.PlanDir, p.JobID)

	rulesBytes, err := os.ReadFile(p.RulesPath)
	if err != nil {
		return nil, fmt.Errorf("reading rules file %s: %w", p.RulesPath, err)
	}
	rulesHash := sha256Hex(rulesBytes)

	fileset, err := resolveRulesFileset(p.ContextDir, p.RulesPath)
	if err != nil {
		return nil, fmt.Errorf("resolving rules fileset: %w", err)
	}

	// context_snapshot: false — every turn is a rebase. Wipe the store first
	// so the write-once guard (an immutability promise the job explicitly
	// opted out of) never fires, then fall through to the fresh-freeze path.
	if !p.SnapshotEnabled {
		if err := os.RemoveAll(layersDir); err != nil {
			return nil, fmt.Errorf("clearing layer store (context_snapshot: false): %w", err)
		}
		ulog.Info("context_snapshot: false — regenerating context layers from scratch this turn").
			Field("job_id", p.JobID).
			Log(ctx)
	}

	manifest, err := LoadLayerManifest(layersDir)
	if err != nil {
		return nil, err
	}

	rebased := false
	if manifest != nil && p.Refresh == LayerRefreshRebase {
		if err := archiveLayerStore(p.PlanDir, p.JobID, layersDir); err != nil {
			return nil, fmt.Errorf("archiving layer store for rebase: %w", err)
		}
		rebased = true
		manifest = nil
		fmt.Fprintf(writer, "Context rebase: archived existing layers; re-freezing a fresh base from the current worktree\n")
		ulog.Info("Rebasing context layers (existing layers archived, fresh base)").
			Field("job_id", p.JobID).
			Log(ctx)
	}

	heads, dirty := collectGitState(p.ContextDir)

	// The canonical resolution root this turn resolved against — recorded on
	// fresh stores and enforced against existing ones (the D-2 root pin).
	rootCanon := canonicalLayerRoot(p.ContextDir)

	// Fresh store: assemble the cross-job lineage first (inherited refs +
	// dep-transcript layers + auto git-delta, spec 19 P5), then freeze this
	// job's own base from whatever the rules resolve BEYOND the inherited
	// union — files the lineage already carries are never duplicated.
	if manifest == nil {
		manifest = &LayerManifest{Version: layerManifestVersion, Root: rootCanon, Layout: p.Layout}
		if _, err := integrateLineage(ctx, writer, p, layersDir, manifest); err != nil {
			return nil, err
		}
		union := UnionFileRecords(manifest, p.ContextDir)
		baseFiles := make([]string, 0, len(fileset))
		for _, f := range fileset {
			if _, ok := union[f]; !ok {
				baseFiles = append(baseFiles, f)
			}
		}
		// The own base is appended when the rules resolve files the lineage
		// does not already carry; a lineage-less store always freezes a base
		// (even an empty one) so the store visibly exists.
		if len(baseFiles) > 0 || len(manifest.Layers) == 0 {
			n := len(manifest.Layers)
			data, records, err := renderLayerXML(p.ContextDir, p.StripComments, n, LayerSourceRulesBase, baseFiles, nil, p.AnchorExchange)
			if err != nil {
				return nil, err
			}
			baseName := fmt.Sprintf("%02d-base.xml", n)
			basePath := filepath.Join(layersDir, baseName)
			if err := WriteLayerArtifact(basePath, data); err != nil {
				return nil, err
			}
			manifest.Layers = append(manifest.Layers, LayerEntry{
				N:              n,
				File:           baseName,
				Source:         LayerSourceRulesBase,
				Hash:           sha256Hex(data),
				Bytes:          int64(len(data)),
				RulesHash:      rulesHash,
				GitHeads:       heads,
				Dirty:          dirty,
				Files:          records,
				TurnID:         p.TurnID,
				CreatedAt:      time.Now().UTC(),
				AnchorExchange: p.AnchorExchange,
			})
			fmt.Fprintf(writer, "Context layers: froze %s (%d files, %s)\n", baseName, len(records), formatByteCount(int64(len(data))))
		}
		if err := SaveLayerManifest(layersDir, manifest); err != nil {
			return nil, err
		}
		if err := WriteLayerSnapshot(p.PlanDir, p.JobID, LayerSnapshot{
			CreatedAt: time.Now().UTC(),
			TurnID:    p.TurnID,
			Root:      rootCanon,
			RulesFile: p.RulesPath,
			RulesHash: rulesHash,
			GitHeads:  heads,
			Dirty:     dirty,
		}); err != nil {
			return nil, err
		}
		res := layerResultFromManifest(layersDir, manifest)
		res.Rebased = rebased
		res.SupersededIndex = buildSupersededIndex(manifest, p.ContextDir)
		return res, nil
	}

	// Existing store: the root pin first. A store frozen against one
	// resolution root must never be extended by a turn that resolved a
	// DIFFERENT one — that is a turn reading another checkout (job 25: an
	// invoker-cwd-derived root read the main checkout), and a silently
	// wrong-checkout layer is worse than a failed turn. Spelling variants of
	// the same directory (symlinks, macOS case) canonicalize equal and pass.
	if manifest.Root != "" && manifest.Root != rootCanon {
		return nil, fmt.Errorf("layer store for job %s is pinned to resolution root %s, but this turn resolved context at %s — refusing to extend the lineage from a different checkout; run the turn against the job's worktree, or archive and re-freeze with --rebase-context", p.JobID, manifest.Root, rootCanon)
	}

	changed := false // manifest mutated this turn

	// Back-fill the pin on stores written before Root existed, so the next
	// turn after an upgrade locks the lineage to its (correct) root.
	if manifest.Root == "" {
		manifest.Root = rootCanon
		changed = true
	}

	// Layout stamp (spec 27): the request layout is lifetime-stable. A store
	// frozen under one layout must not be reassembled under another mid-lineage
	// — except the free ladder→stream migration (§0 equivalence: stream with
	// every layer head-anchored is byte-identical to ladder), which rewrites the
	// stamp in place. stream→ladder is refused (reordering interleaved layers
	// back above the dialogue would shift bytes); --rebase-context is the escape.
	storeLayout := manifest.Layout
	if storeLayout == "" {
		storeLayout = "ladder"
	}
	wantLayout := p.Layout
	if wantLayout == "" {
		wantLayout = "ladder"
	}
	if storeLayout != wantLayout {
		if storeLayout == "ladder" && wantLayout == "stream" {
			manifest.Layout = "stream"
			changed = true
			ulog.Info("Migrating chat cache layout ladder→stream (free; layers stay head-anchored)").
				Field("job_id", p.JobID).
				Log(ctx)
		} else {
			return nil, fmt.Errorf("layer store for job %s was frozen under cache layout %q but this turn requests %q — layout is lifetime-stable; archive and re-freeze with --rebase-context to switch", p.JobID, storeLayout, wantLayout)
		}
	}

	// Enforce immutability before trusting anything in the store — including
	// inherited refs into parent jobs' artifacts (a rebased or pruned parent
	// is a broken lineage and fails loudly, see AuditLayerArtifacts).
	if err := AuditLayerArtifacts(layersDir, manifest); err != nil {
		return nil, err
	}

	// Chat deps that completed after the store was frozen (a reopen following
	// new upstream work) extend the lineage append-only: inherited refs +
	// dep-transcript + git-delta ride BEFORE this turn's rules diffing so the
	// union below already accounts for the newly inherited files.
	lineageChanged, err := integrateLineage(ctx, writer, p, layersDir, manifest)
	if err != nil {
		return nil, err
	}
	changed = changed || lineageChanged

	union := UnionFileRecords(manifest, p.ContextDir)

	// Render current bytes for every file the rules resolve to right now.
	// One pass gives new-file detection, staleness compare, and delta
	// material; a read failure is a hard failure (an unreadable file the
	// rules demand must fail the turn, not silently thin the context).
	current := make(map[string]renderedFile, len(fileset))
	for _, f := range fileset {
		rf, err := readRenderedFile(p.ContextDir, f, p.StripComments)
		if err != nil {
			return nil, fmt.Errorf("reading context file %s: %w", f, err)
		}
		current[f] = rf
	}

	// Genuinely-new files (not in any layer) → rules-diff layer.
	var newFiles []string
	for _, f := range fileset {
		if _, ok := union[f]; !ok {
			newFiles = append(newFiles, f)
		}
	}
	result := &LayerEngineResult{Rebased: rebased}
	if len(newFiles) > 0 {
		n := len(manifest.Layers)
		name := fmt.Sprintf("%02d-add-%s.xml", n, layerSlug(p.TurnID))
		data, records, err := renderLayerXML(p.ContextDir, p.StripComments, n, LayerSourceRulesDiff, newFiles, nil, p.AnchorExchange)
		if err != nil {
			return nil, err
		}
		if err := WriteLayerArtifact(filepath.Join(layersDir, name), data); err != nil {
			return nil, err
		}
		manifest.Layers = append(manifest.Layers, LayerEntry{
			N:              n,
			File:           name,
			Source:         LayerSourceRulesDiff,
			Hash:           sha256Hex(data),
			Bytes:          int64(len(data)),
			RulesHash:      rulesHash,
			GitHeads:       heads,
			Dirty:          dirty,
			Files:          records,
			TurnID:         p.TurnID,
			CreatedAt:      time.Now().UTC(),
			AnchorExchange: p.AnchorExchange,
		})
		changed = true
		result.AppendedLayer = name
		fmt.Fprintf(writer, "Context layers: appended %s (%d new files from rules)\n", name, len(newFiles))
		ulog.Info("Appended rules-diff context layer").
			Field("job_id", p.JobID).
			Field("layer", name).
			Field("new_files", len(newFiles)).
			Log(ctx)
	}

	// Files removed from the rules: bytes stay in their layers (shrinking the
	// upload would bust the prefix); record the removal annotation once. Only
	// files this job's OWN rules layers captured count — inherited files were
	// never in this job's rules, so their absence from the fileset is their
	// normal state, not a removal.
	// All three sets key on canonical layer keys so removal detection stays
	// correct across stores holding mixed historical spellings.
	alreadyRemoved := make(map[string]bool, len(manifest.Removals))
	for _, r := range manifest.Removals {
		alreadyRemoved[canonicalLayerKey(p.ContextDir, r.Path)] = true
	}
	inFileset := make(map[string]bool, len(fileset))
	for _, f := range fileset {
		inFileset[f] = true
	}
	rulesCaptured := make(map[string]bool)
	for _, layer := range manifest.Layers {
		if layer.Source == LayerSourceRulesBase || layer.Source == LayerSourceRulesDiff {
			for _, f := range layer.Files {
				rulesCaptured[canonicalLayerKey(p.ContextDir, f.Path)] = true
			}
		}
	}
	var removedPaths []string
	for path := range rulesCaptured {
		if !inFileset[path] && !alreadyRemoved[path] {
			removedPaths = append(removedPaths, path)
		}
	}
	sort.Strings(removedPaths)
	for _, path := range removedPaths {
		manifest.Removals = append(manifest.Removals, LayerRemoval{Path: path, TurnID: p.TurnID, At: time.Now().UTC()})
		changed = true
	}
	if len(removedPaths) > 0 {
		fmt.Fprintf(writer, "Context layers: %d file(s) removed from rules — bytes stay uploaded, removal recorded in layers.json: %s\n",
			len(removedPaths), strings.Join(removedPaths, ", "))
	}

	// Content drift on captured files: never auto-refreshed. Either the user
	// asked for a delta layer, or they get a staleness advisory.
	var staleFiles []string
	for _, f := range fileset {
		rec, ok := union[f]
		if !ok {
			continue // new this turn — captured fresh above
		}
		if current[f].Hash != rec.Hash {
			staleFiles = append(staleFiles, f)
		}
	}
	sort.Strings(staleFiles)

	switch {
	case p.Refresh == LayerRefreshAppendDelta && len(staleFiles) > 0:
		n := len(manifest.Layers)
		name := fmt.Sprintf("%02d-delta-%s.xml", n, deltaSlug(heads))
		data, records, err := renderLayerXML(p.ContextDir, p.StripComments, n, LayerSourceGitDelta, staleFiles, staleFiles, p.AnchorExchange)
		if err != nil {
			return nil, err
		}
		if err := WriteLayerArtifact(filepath.Join(layersDir, name), data); err != nil {
			return nil, err
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
			Supersedes:     append([]string{}, staleFiles...),
			TurnID:         p.TurnID,
			CreatedAt:      time.Now().UTC(),
			AnchorExchange: p.AnchorExchange,
		})
		changed = true
		result.DeltaLayer = name
		fmt.Fprintf(writer, "Context layers: appended delta %s (%d changed files supersede earlier copies)\n", name, len(staleFiles))
		ulog.Info("Appended delta context layer").
			Field("job_id", p.JobID).
			Field("layer", name).
			Field("changed_files", len(staleFiles)).
			Log(ctx)
	case p.Refresh == LayerRefreshAppendDelta:
		fmt.Fprintf(writer, "Context layers: --append-delta found no changed files; nothing to append\n")
	case len(staleFiles) > 0:
		// Advisory only (spec 19 e2e 5): the frozen layers intentionally lag
		// the worktree until the user asks for a delta or a rebase.
		fmt.Fprintf(writer, "Context staleness advisory: %d file(s) changed on disk since their layer was frozen (%s) — the oracle still sees the frozen bytes; run with --append-delta to upload the changes or --rebase-context to re-freeze\n",
			len(staleFiles), strings.Join(staleFiles, ", "))
		ulog.Warn("Context layers are stale against the worktree (frozen bytes still uploaded)").
			Field("job_id", p.JobID).
			Field("stale_files", len(staleFiles)).
			Log(ctx)
	}

	if changed {
		if err := SaveLayerManifest(layersDir, manifest); err != nil {
			return nil, err
		}
	}

	// Rebase advisory (spec 19 e2e 9): suggest, never auto-bust.
	if ratio := SupersededBytesRatio(manifest, p.ContextDir); ratio > rebaseAdvisoryRatio {
		fmt.Fprintf(writer, "Context rebase advisory: %.0f%% of lineage bytes are superseded by later layers — consider --rebase-context to compact into a fresh base (one deliberate cold cache write)\n", ratio*100)
		ulog.Warn("Context layer lineage heavily superseded — rebase advised").
			Field("job_id", p.JobID).
			Field("superseded_ratio", fmt.Sprintf("%.2f", ratio)).
			Log(ctx)
	}

	res := layerResultFromManifest(layersDir, manifest)
	res.Rebased = result.Rebased
	res.AppendedLayer = result.AppendedLayer
	res.DeltaLayer = result.DeltaLayer
	res.SupersededIndex = buildSupersededIndex(manifest, p.ContextDir)
	return res, nil
}

// layerResultFromManifest builds the ordered upload list + provenance/anchor
// maps.
func layerResultFromManifest(layersDir string, m *LayerManifest) *LayerEngineResult {
	res := &LayerEngineResult{
		SourcesByPath: make(map[string]string, len(m.Layers)),
		AnchorsByPath: make(map[string]string, len(m.Layers)),
	}
	for _, e := range m.Layers {
		path := LayerArtifactPath(layersDir, e)
		res.LayerPaths = append(res.LayerPaths, path)
		res.SourcesByPath[path] = e.Source
		res.AnchorsByPath[path] = e.AnchorExchange
	}
	return res
}

// buildSupersededIndex renders the `<current-files>` block (spec 27 §5b): for
// every file captured by more than one layer (a later copy supersedes an
// earlier one), the path and the layer N that holds the winning (latest) copy.
// Empty when nothing is superseded. It rides in the volatile turn so the oracle
// always has the winning-copy map even when a supersession pair straddles
// interleaved dialogue. Paths are canonical layer keys relative to root,
// matching the union diff's identity.
func buildSupersededIndex(m *LayerManifest, root string) string {
	winning := make(map[string]int) // canonical path → highest layer N capturing it
	count := make(map[string]int)   // canonical path → number of layers capturing it
	for _, layer := range m.Layers {
		for _, f := range layer.Files {
			key := canonicalLayerKey(root, f.Path)
			count[key]++
			winning[key] = layer.N // layers iterate in ascending N, so last wins
		}
	}
	var paths []string
	for key, c := range count {
		if c > 1 {
			paths = append(paths, key)
		}
	}
	if len(paths) == 0 {
		return ""
	}
	sort.Strings(paths)
	var buf strings.Builder
	buf.WriteString("<current-files>\n")
	for _, p := range paths {
		fmt.Fprintf(&buf, "  %s → layer %d\n", p, winning[p])
	}
	buf.WriteString("</current-files>\n")
	return buf.String()
}

// archiveLayerStore moves every entry of the context-layers dir (artifacts +
// layers.json) plus the job's snapshot.json into
// context-layers/archive-<ts>/ — rebases retire layers, they never delete
// them (spec 19 e2e 7).
func archiveLayerStore(planDir, jobID, layersDir string) error {
	entries, err := os.ReadDir(layersDir)
	if err != nil {
		return err
	}
	archiveDir := filepath.Join(layersDir, fmt.Sprintf("archive-%s", time.Now().UTC().Format("20060102-150405")))
	if err := os.MkdirAll(archiveDir, 0o755); err != nil { //nolint:gosec // artifact dir
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "archive-") {
			continue // prior archives stay where they are
		}
		if err := os.Rename(filepath.Join(layersDir, entry.Name()), filepath.Join(archiveDir, entry.Name())); err != nil {
			return err
		}
	}
	snapPath := LayerSnapshotPath(planDir, jobID)
	if _, err := os.Stat(snapPath); err == nil {
		if err := os.Rename(snapPath, filepath.Join(archiveDir, "snapshot.json")); err != nil {
			return err
		}
	}
	return nil
}

// resolveRulesFileset resolves the rules file to its ordered fileset: cold
// files first, then hot (mirroring the legacy cold→hot upload order),
// deduplicated. Every path is normalized to its canonical layer key
// (canonicalLayerKey — worktree-relative for files under contextDir), so
// layer records, the union diff, removal annotations, and supersede lists
// all share one worktree-spelling-independent identity per file.
// readRenderedFile resolves the keys back against contextDir the same way
// cx's writeFileToXML does.
func resolveRulesFileset(contextDir, rulesPath string) ([]string, error) {
	mgr := grovecontext.NewManager(contextDir)
	hot, cold, err := mgr.ResolveFilesFromCustomRulesFile(rulesPath)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(hot)+len(cold))
	fileset := make([]string, 0, len(hot)+len(cold))
	for _, f := range append(append([]string{}, cold...), hot...) {
		key := canonicalLayerKey(contextDir, f)
		if !seen[key] {
			seen[key] = true
			fileset = append(fileset, key)
		}
	}
	return fileset, nil
}

// renderedFile is one file's rendered (post strip_comments) content.
type renderedFile struct {
	Content []byte
	Hash    string
}

// readRenderedFile reads a resolved rules path (relative paths join
// contextDir, mirroring cx) and applies comment stripping when enabled, so
// hashes and layer bytes match what legacy cx generation would upload.
func readRenderedFile(contextDir, file string, strip bool) (renderedFile, error) {
	path := file
	if !filepath.IsAbs(path) {
		path = filepath.Join(contextDir, path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return renderedFile{}, err
	}
	if strip {
		content = grovecontext.StripComments(file, content)
	}
	return renderedFile{Content: content, Hash: sha256Hex(content)}, nil
}

// renderLayerXML renders one layer artifact: cx's `<file path=…>` blocks
// wrapped in a `<layer>` envelope carrying provenance attributes (spec 19
// §3). Unreadable files are hard errors (spec 19 e2e 22) — cx's inline
// `<error>` placeholder would silently thin the oracle's context.
func renderLayerXML(contextDir string, strip bool, n int, source string, files, supersedes []string, afterTurn string) ([]byte, []LayerFileRecord, error) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "<layer n=\"%d\" source=%q files=\"%d\"", n, source, len(files))
	if len(supersedes) > 0 {
		fmt.Fprintf(&buf, " supersedes=%q", strings.Join(supersedes, ","))
	}
	// after_turn marks a stream layer interleaved mid-dialogue (spec 27 §5a):
	// the exchange id it sits after. Head-region layers pass "" → no attribute,
	// so existing ladder-born artifact bytes are unchanged (immutability holds).
	if afterTurn != "" {
		fmt.Fprintf(&buf, " after_turn=%q", afterTurn)
	}
	buf.WriteString(">\n")

	records := make([]LayerFileRecord, 0, len(files))
	for _, file := range files {
		rf, err := readRenderedFile(contextDir, file, strip)
		if err != nil {
			return nil, nil, fmt.Errorf("reading context file %s: %w", file, err)
		}
		fmt.Fprintf(&buf, "  <file path=%q>\n", file)
		buf.Write(rf.Content)
		if len(rf.Content) > 0 && rf.Content[len(rf.Content)-1] != '\n' {
			buf.WriteByte('\n')
		}
		buf.WriteString("  </file>\n")
		records = append(records, LayerFileRecord{Path: file, Hash: rf.Hash, Bytes: int64(len(rf.Content))})
	}
	buf.WriteString("</layer>\n")
	return buf.Bytes(), records, nil
}

// collectGitState records the context dir's repo HEAD + dirty flag for layer
// provenance. Deliberately simple (spec 19 P3): one entry for the repo
// containing contextDir — diffing never depends on git (content hashes do
// that work), so heads are labels for humans and the delta layer's slug.
// Non-repo dirs return (nil, false).
func collectGitState(dir string) (map[string]string, bool) {
	root, err := gitOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return nil, false
	}
	head, err := gitOutput(dir, "rev-parse", "HEAD")
	if err != nil || head == "" {
		// A repo with no commits yet: record the root with an empty head.
		head = ""
	}
	status, err := gitOutput(dir, "status", "--porcelain")
	dirty := err == nil && status != ""
	return map[string]string{root: head}, dirty
}

// gitOutput runs git -C dir args… and returns trimmed stdout.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// layerSlug shortens a turn id for use in a layer filename.
func layerSlug(turnID string) string {
	if turnID == "" {
		return "turn"
	}
	return turnID
}

// deltaSlug names a delta layer after the current HEAD's short sha, falling
// back to "worktree" for dirty-only or non-repo states.
func deltaSlug(heads map[string]string) string {
	for _, head := range heads {
		if len(head) >= 7 {
			return head[:7]
		}
	}
	return "worktree"
}

// formatByteCount renders a human-readable byte size for job.log lines.
func formatByteCount(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
