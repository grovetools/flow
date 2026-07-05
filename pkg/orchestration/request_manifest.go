package orchestration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/grovetools/grove-anthropic/pkg/anthropic"
)

// Providers recorded in a RequestManifest. The mock provider describes the
// same Anthropic ladder assembly as a real claude dispatch would use — it is
// the tend e2e suite's assertion surface (spec 19 D9).
const (
	requestManifestProviderAnthropic = "anthropic"
	requestManifestProviderGemini    = "gemini"
	requestManifestProviderMock      = "mock"
)

// RequestManifestEntry is one block of an assembled LLM request, in emission
// order (spec 19 D9): its kind (system|layer|context|history|turn), the file
// backing it (document blocks only), the sha256 of the bytes uploaded, whether
// it carries a cache breakpoint (and at which TTL), and a rough token estimate
// (bytes/4 heuristic — hence the field name token_estimate, not tokens).
type RequestManifestEntry struct {
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"`
	// Source is the layer's provenance from layers.json (rules-base,
	// rules-diff, git-delta, …) — layer entries only (spec 19 P3).
	Source        string `json:"source,omitempty"`
	ContentHash   string `json:"content_hash"`
	Breakpoint    bool   `json:"breakpoint"`
	TTL           string `json:"ttl,omitempty"`
	TokenEstimate int64  `json:"token_estimate"`
}

// AnnotateLayerSources stamps each layer entry's provenance source (from the
// layer engine's layers.json view) onto the manifest entries, matching by
// upload path.
func AnnotateLayerSources(entries []RequestManifestEntry, sourcesByPath map[string]string) {
	if len(sourcesByPath) == 0 {
		return
	}
	for i := range entries {
		if entries[i].Kind == anthropic.RequestBlockLayer {
			if src, ok := sourcesByPath[entries[i].Path]; ok {
				entries[i].Source = src
			}
		}
	}
}

// RequestManifest is the per-turn record of what a chat turn actually
// uploaded: the ordered blocks with content hashes and cache save points. It
// is written next to the briefing file as
// .artifacts/<job-id>/request-manifest-<turnID>.json and is the on-disk
// assertion surface for the oracle-cache-lineage e2e suite (which runs
// against the mock LLM and cannot see the wire).
type RequestManifest struct {
	TurnID      string                 `json:"turn_id"`
	JobID       string                 `json:"job_id"`
	Model       string                 `json:"model"`
	Provider    string                 `json:"provider"`
	CacheLayout string                 `json:"cache_layout,omitempty"`
	CacheTTL    string                 `json:"cache_ttl,omitempty"`
	NoCache     bool                   `json:"no_cache,omitempty"`
	CreatedAt   time.Time              `json:"created_at"`
	Entries     []RequestManifestEntry `json:"entries"`
}

// DescribeChatRequestManifest converts the Anthropic block plan for opts (via
// anthropic.DescribeRequest, so breakpoint placement is never re-derived in
// flow) into manifest entries, hashing the exact bytes each block uploads:
// file bytes for document blocks, opts.SystemPrompt / opts.HistoryPrefix /
// opts.Prompt for the text blocks.
func DescribeChatRequestManifest(opts anthropic.RequestOptions) ([]RequestManifestEntry, error) {
	planEntries, err := anthropic.DescribeRequest(opts)
	if err != nil {
		return nil, err
	}

	entries := make([]RequestManifestEntry, 0, len(planEntries))
	for _, pe := range planEntries {
		entry := RequestManifestEntry{
			Kind:       pe.Kind,
			Path:       pe.Path,
			Breakpoint: pe.Breakpoint,
			TTL:        pe.TTL,
		}
		var content []byte
		switch pe.Kind {
		case anthropic.RequestBlockSystem:
			content = []byte(opts.SystemPrompt)
		case anthropic.RequestBlockHistory:
			content = []byte(opts.HistoryPrefix)
		case anthropic.RequestBlockTurn:
			content = []byte(opts.Prompt)
		default: // document blocks: layer / context
			content, err = os.ReadFile(pe.Path)
			if err != nil {
				return nil, fmt.Errorf("hashing manifest entry %s: %w", pe.Path, err)
			}
		}
		entry.ContentHash, entry.TokenEstimate = hashAndEstimate(content)
		entries = append(entries, entry)
	}
	return entries, nil
}

// BuildFlattenedRequestManifestEntries describes a non-Anthropic (gemini)
// upload: every file as a plain context document, then the volatile turn
// block. No breakpoints — the ladder cache layout is Anthropic-only.
func BuildFlattenedRequestManifestEntries(files []string, turnPrompt string) ([]RequestManifestEntry, error) {
	entries := make([]RequestManifestEntry, 0, len(files)+1)
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("hashing manifest entry %s: %w", f, err)
		}
		hash, est := hashAndEstimate(content)
		entries = append(entries, RequestManifestEntry{
			Kind:          anthropic.RequestBlockContext,
			Path:          f,
			ContentHash:   hash,
			TokenEstimate: est,
		})
	}
	hash, est := hashAndEstimate([]byte(turnPrompt))
	entries = append(entries, RequestManifestEntry{
		Kind:          anthropic.RequestBlockTurn,
		ContentHash:   hash,
		TokenEstimate: est,
	})
	return entries, nil
}

// hashAndEstimate returns the sha256 hex digest of content and its bytes/4
// token estimate.
func hashAndEstimate(content []byte) (hash string, tokenEstimate int64) {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), int64(len(content)) / 4
}

// RequestManifestPath returns the path of a turn's request manifest under the
// plan's .artifacts dir.
func RequestManifestPath(planDir, jobID, turnID string) string {
	return filepath.Join(planDir, ".artifacts", jobID, fmt.Sprintf("request-manifest-%s.json", turnID))
}

// WriteRequestManifest writes a turn's request manifest atomically (temp file
// + rename, like the token-usage artifact) so pollers never see a partial
// write. It returns the manifest path.
func WriteRequestManifest(planDir, jobID, turnID string, manifest RequestManifest) (string, error) {
	dest := RequestManifestPath(planDir, jobID, turnID)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil { //nolint:gosec // artifact dir
		return "", err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	return dest, nil
}
