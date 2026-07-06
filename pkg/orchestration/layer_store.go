package orchestration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The on-disk layer store (spec 19 §3). Each chat job owns an append-only
// sequence of immutable context-layer artifacts under
//
//	plans/<plan>/.artifacts/<job-id>/context-layers/
//	  00-base.xml            frozen rules sweep (turn 1)
//	  01-add-<slug>.xml      rules-diff layer (new files only)
//	  02-delta-<slug>.xml    delta layer (changed files, supersede-annotated)
//	  layers.json            ordered sequence with provenance (LayerManifest)
//	  archive-<ts>/          layers retired by --rebase-context (never deleted)
//	plans/<plan>/.artifacts/<job-id>/snapshot.json   turn-1 freeze record
//
// Existing layer artifacts are NEVER rewritten (immutability is what keeps
// the Anthropic cache prefix byte-stable); the only sanctioned ways bytes
// change are appending a new layer or archiving the whole lineage via
// --rebase-context. WriteLayerArtifact and AuditLayerArtifacts enforce this.

// Layer provenance sources recorded in layers.json (spec 19 §3). The rules
// engine (P3) emits rules-base / rules-diff / git-delta; cross-job lineage
// (P5, layer_lineage.go) emits inherited (read-only refs to a parent chat's
// artifacts), dep-transcript (a completed parent chat's conversation as a
// layer document), and lineage git-delta layers.
const (
	LayerSourceRulesBase     = "rules-base"
	LayerSourceRulesDiff     = "rules-diff"
	LayerSourceGitDelta      = "git-delta"
	LayerSourceDepTranscript = "dep-transcript"
	LayerSourceInherited     = "inherited"
)

// layerManifestName is the manifest filename within the context-layers dir.
const layerManifestName = "layers.json"

// LayerFileRecord is one file captured inside a layer artifact: its path as
// resolved by the rules engine (relative to the context dir when inside it),
// the sha256 of the rendered content that went into the layer (post
// strip_comments), and its rendered size. The per-file hashes are what later
// turns diff against: new-file detection, staleness advisories, and
// --append-delta all compare current rendered bytes to these records.
type LayerFileRecord struct {
	Path  string `json:"path"`
	Hash  string `json:"hash"`
	Bytes int64  `json:"bytes"`
}

// LayerEntry is one layer of the sequence, with full provenance (spec 19 §3).
type LayerEntry struct {
	// N is the layer's position in the upload sequence (0 = base).
	N int `json:"n"`
	// File is the artifact filename within the job's context-layers dir for
	// own layers, or an absolute path for inherited references to a parent
	// job's immutable artifacts (P5). See LayerArtifactPath.
	File string `json:"file"`
	// Source is the layer's provenance kind (LayerSource* constants).
	Source string `json:"source"`
	// Hash is the sha256 of the layer artifact's bytes — the immutability
	// fingerprint checked by AuditLayerArtifacts every turn.
	Hash string `json:"hash"`
	// Bytes is the artifact size.
	Bytes int64 `json:"bytes"`
	// RulesHash is the sha256 of the rules file that produced this layer
	// (rules-base / rules-diff layers).
	RulesHash string `json:"rules_hash,omitempty"`
	// GitHeads records the HEAD of the context dir's repo when the layer was
	// written (provenance only — layer diffing is content-hash based, never
	// git based). Keyed by repo root path.
	GitHeads map[string]string `json:"git_heads,omitempty"`
	// Dirty reports whether that repo had uncommitted changes at write time.
	Dirty bool `json:"dirty,omitempty"`
	// Files are the per-file records captured in this layer.
	Files []LayerFileRecord `json:"files"`
	// Supersedes lists paths whose earlier-layer copies this layer replaces
	// (delta layers). The chat template instructs the oracle that later
	// layers win.
	Supersedes []string `json:"supersedes,omitempty"`
	// InheritedFrom records the parent-job provenance of lineage entries (P5):
	// "<job-id>/<layer-file>" on inherited refs (the job that OWNS the
	// artifact — a ref inherited through a chain keeps its original owner),
	// bare "<job-id>" on dep-transcript layers (the direct parent whose
	// transcript this is; also the marker integrateLineage uses to know a
	// parent is already integrated). Empty for this job's own rules layers.
	InheritedFrom string `json:"inherited_from,omitempty"`
	// TurnID / CreatedAt record when the layer was appended.
	TurnID    string    `json:"turn_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// AnchorExchange is the stream layout's position marker (spec 27): the
	// directive id of the last COMPLETED assistant exchange at freeze time.
	// Empty = the head region (turn-1 layers, inherited refs, transcripts,
	// and every ladder-born layer). A non-empty value places this layer in the
	// stream immediately after the exchange with that id — this is how a
	// mid-chat widening interleaves adjacent to the turn that pulled it in.
	// NOT joined on TurnID, which is the in-flight turn and would dangle if a
	// turn fails before its assistant response exists. Ignored under ladder.
	AnchorExchange string `json:"anchor_exchange,omitempty"`
}

// LayerRemoval is the annotation recorded when a file disappears from the
// rules between turns. The bytes stay in their layer (removing uploaded bytes
// would bust the cache prefix); the annotation is the honest record.
type LayerRemoval struct {
	Path   string    `json:"path"`
	TurnID string    `json:"turn_id,omitempty"`
	At     time.Time `json:"at"`
}

// LayerManifest is the layers.json document: the ordered layer sequence plus
// removal annotations.
type LayerManifest struct {
	Version int `json:"version"`
	// Root pins the canonical resolution root (the job's worktree /
	// sub-project dir, see canonicalLayerRoot) the store was frozen against.
	// Every later turn must resolve the SAME root: a mismatch is a hard
	// failure — a turn resolved from another checkout must never silently
	// extend this lineage (oracle-plays job 25: an invoker-cwd-derived root
	// duplicated the whole fileset from the main checkout). Stores written
	// before this field exists ("") are back-filled on their next turn.
	Root     string         `json:"root,omitempty"`
	Layers   []LayerEntry   `json:"layers"`
	Removals []LayerRemoval `json:"removals,omitempty"`
	// Layout stamps the request cache layout this lineage was frozen under
	// (spec 27): "ladder" (or empty on v1 stores, read as ladder) or "stream".
	// Layout is lifetime-stable; PrepareContextLayers fails a turn whose job
	// frontmatter disagrees, except the free ladder→stream migration (§0
	// equivalence) which rewrites the stamp in place.
	Layout string `json:"layout,omitempty"`
}

// LayerSnapshot is the snapshot.json document written when a lineage's base
// layer is frozen (turn 1, or a --rebase-context re-freeze): the rules file
// and its hash, and the git state of the context dir's repo at freeze time.
// Git heads here are provenance/labels — delta computation compares rendered
// content hashes against layers.json records, so it is correct for both
// committed and uncommitted changes without consulting git.
type LayerSnapshot struct {
	CreatedAt time.Time `json:"created_at"`
	TurnID    string    `json:"turn_id,omitempty"`
	// Root is the canonical resolution root the base was frozen against
	// (provenance mirror of LayerManifest.Root).
	Root      string            `json:"root,omitempty"`
	RulesFile string            `json:"rules_file"`
	RulesHash string            `json:"rules_hash"`
	GitHeads  map[string]string `json:"git_heads,omitempty"`
	Dirty     bool              `json:"dirty,omitempty"`
	// LastManifest is the filename (within the job's .artifacts/<jobID>/ dir)
	// of the most recently written per-turn request manifest (spec 27 P3).
	// Stream lineage uses it to locate the parent's last manifest for the
	// prefix hash-verify without globbing; empty falls back to newest-by-
	// CreatedAt globbing.
	LastManifest string `json:"last_manifest,omitempty"`
}

// ContextLayersDir returns the job's layer-store directory.
func ContextLayersDir(planDir, jobID string) string {
	return filepath.Join(planDir, ".artifacts", jobID, "context-layers")
}

// LayerSnapshotPath returns the job's snapshot.json path (sibling of the
// context-layers dir, per spec 19 §3).
func LayerSnapshotPath(planDir, jobID string) string {
	return filepath.Join(planDir, ".artifacts", jobID, "snapshot.json")
}

// LayerArtifactPath resolves a manifest entry to the artifact file it names:
// own layers live inside layersDir; inherited entries (P5) carry an absolute
// path to the parent job's immutable artifact and are used as-is.
func LayerArtifactPath(layersDir string, e LayerEntry) string {
	if filepath.IsAbs(e.File) {
		return e.File
	}
	return filepath.Join(layersDir, e.File)
}

// LoadLayerManifest reads layers.json from layersDir. A missing manifest
// returns (nil, nil) — the "no store yet" signal for turn 1.
func LoadLayerManifest(layersDir string) (*LayerManifest, error) {
	data, err := os.ReadFile(filepath.Join(layersDir, layerManifestName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", layerManifestName, err)
	}
	var m LayerManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", layerManifestName, err)
	}
	return &m, nil
}

// SaveLayerManifest writes layers.json atomically (temp + rename).
func SaveLayerManifest(layersDir string, m *LayerManifest) error {
	if err := os.MkdirAll(layersDir, 0o755); err != nil { //nolint:gosec // artifact dir
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	dest := filepath.Join(layersDir, layerManifestName)
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// WriteLayerSnapshot writes snapshot.json atomically.
func WriteLayerSnapshot(planDir, jobID string, s LayerSnapshot) error {
	dest := LayerSnapshotPath(planDir, jobID)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil { //nolint:gosec // artifact dir
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// LoadLayerSnapshot reads snapshot.json; missing returns (nil, nil).
func LoadLayerSnapshot(planDir, jobID string) (*LayerSnapshot, error) {
	data, err := os.ReadFile(LayerSnapshotPath(planDir, jobID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s LayerSnapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing snapshot.json: %w", err)
	}
	return &s, nil
}

// UpdateLayerSnapshotLastManifest records the filename of the job's most recent
// per-turn request manifest in snapshot.json (spec 27 P3). Load-modify-save; a
// missing snapshot is created with just the pointer set (a chat with no frozen
// base — e.g. context_snapshot: false — still records its last manifest so a
// stream child can locate it).
func UpdateLayerSnapshotLastManifest(planDir, jobID, filename string) error {
	s, err := LoadLayerSnapshot(planDir, jobID)
	if err != nil {
		return err
	}
	if s == nil {
		s = &LayerSnapshot{}
	}
	s.LastManifest = filename
	return WriteLayerSnapshot(planDir, jobID, *s)
}

// WriteLayerArtifact writes a layer artifact under the write-once contract:
// a missing file is created; an existing file with byte-identical content is
// a no-op; an existing file with DIFFERENT content is a loud error — layer
// artifacts are immutable and rewriting one silently would bust the cache
// lineage the whole store exists to protect.
func WriteLayerArtifact(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if sha256Hex(existing) == sha256Hex(data) {
			return nil
		}
		return fmt.Errorf("layer artifact %s already exists with different content — layer artifacts are immutable (append a new layer, or archive the lineage with --rebase-context)", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // artifact dir
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// AuditLayerArtifacts verifies every layer artifact — own AND inherited —
// still exists with exactly the bytes recorded at append time. Any drift is a
// hard error: a mutated layer means the uploaded prefix silently changed.
// Inherited refs point into a PARENT job's context-layers dir, so their
// failure mode is different (the parent was rebased or its artifacts pruned —
// a broken lineage) and gets its own actionable message: --rebase-context on
// THIS job rebuilds the context from the parent's current state.
func AuditLayerArtifacts(layersDir string, m *LayerManifest) error {
	for _, e := range m.Layers {
		path := LayerArtifactPath(layersDir, e)
		data, err := os.ReadFile(path)
		if err != nil {
			if e.Source == LayerSourceInherited {
				return fmt.Errorf("inherited layer artifact %s (layer %d, inherited from %s) is missing or unreadable — the parent job's context layers were removed or rebased, breaking this chat's cache lineage; run this chat with --rebase-context to rebuild its context without the broken refs: %w", path, e.N, e.InheritedFrom, err)
			}
			return fmt.Errorf("layer artifact %s (layer %d, %s) is missing or unreadable: %w", path, e.N, e.Source, err)
		}
		if got := sha256Hex(data); got != e.Hash {
			if e.Source == LayerSourceInherited {
				return fmt.Errorf("inherited layer artifact %s (layer %d, inherited from %s) changed after this job inherited it (hash %s, recorded %s) — the parent job's context was rebased, breaking this chat's cache lineage; run this chat with --rebase-context to rebuild its context from the parent's current state", path, e.N, e.InheritedFrom, got, e.Hash)
			}
			return fmt.Errorf("layer artifact %s (layer %d, %s) was modified after freeze (hash %s, recorded %s) — layer artifacts are immutable; restore it or rebase with --rebase-context", path, e.N, e.Source, got, e.Hash)
		}
	}
	return nil
}

// UnionFileRecords folds the manifest's layers, in order, into the latest
// record per path: later layers (rules-diff appends, delta supersessions)
// win. This union is the diff baseline for every turn-N decision.
//
// Keys are canonical layer keys relative to root (canonicalLayerKey), so a
// store that already contains mixed path spellings — worktree-relative in
// one layer, another checkout's absolute paths in the next (oracle-plays
// job 25) — still yields ONE record per file and stops producing duplicate
// layers, while the already-written layer artifacts stay immutable.
func UnionFileRecords(m *LayerManifest, root string) map[string]LayerFileRecord {
	union := make(map[string]LayerFileRecord)
	for _, layer := range m.Layers {
		for _, f := range layer.Files {
			union[canonicalLayerKey(root, f.Path)] = f
		}
	}
	return union
}

// SupersededBytesRatio reports the fraction of lineage bytes that later
// layers have superseded: for each file record, every earlier capture of the
// same path counts as superseded. Above rebaseAdvisoryRatio the engine logs
// the rebase advisory (never auto-busts). Paths compare by canonical layer
// key relative to root, matching the union diff's identity.
func SupersededBytesRatio(m *LayerManifest, root string) float64 {
	var total, superseded int64
	seenLater := make(map[string]bool) // canonical paths captured by any later layer
	for i := len(m.Layers) - 1; i >= 0; i-- {
		for _, f := range m.Layers[i].Files {
			total += f.Bytes
			if seenLater[canonicalLayerKey(root, f.Path)] {
				superseded += f.Bytes
			}
		}
		for _, f := range m.Layers[i].Files {
			seenLater[canonicalLayerKey(root, f.Path)] = true
		}
	}
	if total == 0 {
		return 0
	}
	return float64(superseded) / float64(total)
}

// sha256Hex returns the hex sha256 digest of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
