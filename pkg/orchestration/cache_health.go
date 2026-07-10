package orchestration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/grovetools/grove-anthropic/pkg/anthropic"
)

// Cache-health surfaces (oracle-plays J5). Anthropic's prompt cache TTL is
// refreshed by every read of a cached prefix; when a chat's inherited lineage
// sees no activity for longer than the TTL window, the next turn cold-writes
// the whole prefix again (the two 69.5/81.2-min phase-chat gaps in the trial
// each paid a ~196K-token cold rewrite). Nothing in flow surfaced "your
// lineage last saw activity N minutes ago" — these helpers derive that from the
// per-turn request manifests (request_manifest.go) plus the warm receipts that
// `flow plan warm` writes, scanning the job's own artifact dir AND its lineage
// parents' (the shared prefix is warmed by the parents' requests too).

// ActivitySource is one cache-touching event the staleness scan found: a
// per-turn request manifest or a warm receipt, with the artifact dir it came
// from and its timestamp.
type ActivitySource struct {
	// Kind is "manifest" (a real turn) or "warm" (a keepalive receipt).
	Kind string
	// Path is the artifact file the event was read from.
	Path string
	// JobID owns the artifact dir (own job or a lineage parent/ancestor).
	JobID string
	// CreatedAt is the event's recorded timestamp.
	CreatedAt time.Time
}

// cacheActivityDirs returns the artifact dirs whose request manifests / warm
// receipts count as activity for this chat's cached lineage: the job's own dir
// plus every lineage parent/ancestor discoverable from its layer manifest
// (oracle-plays J5 addendum A6):
//
//   - LayerSourceInherited entries carry an ABSOLUTE File pointing into the
//     originating ANCESTOR's store (<planDir>/.artifacts/<ancestorID>/
//     context-layers/<file>). Two filepath.Dir hops yield that ancestor's
//     artifact dir — a valid scan target (any chain member's activity warms the
//     shared bytes), just not necessarily the immediate parent.
//   - LayerSourceDepTranscript (and any relative-File inherited) entries live in
//     the child's own layersDir, so Dir-hops resolve to the child (wrong). For
//     these use lineageParentKey(InheritedFrom) + planDir — the parent is a
//     dependency in the same plan.
//
// Union of both sets, deduped by dir.
func cacheActivityDirs(planDir string, job *Job) []string {
	seen := make(map[string]bool)
	var dirs []string
	add := func(d string) {
		if d == "" {
			return
		}
		d = filepath.Clean(d)
		if seen[d] {
			return
		}
		seen[d] = true
		dirs = append(dirs, d)
	}

	// The job's own artifact dir.
	add(filepath.Join(planDir, ".artifacts", job.ID))

	// Lineage parents/ancestors, from the job's layer manifest (read-only).
	layersDir := ContextLayersDir(planDir, job.ID)
	if m, err := LoadLayerManifest(layersDir); err == nil && m != nil {
		for _, e := range m.Layers {
			switch {
			case e.Source == LayerSourceInherited && filepath.IsAbs(e.File):
				// <ancestorPlanDir>/.artifacts/<ancestorID>/context-layers/<file>
				//   Dir -> .../context-layers ; Dir -> .../<ancestorID>
				add(filepath.Dir(filepath.Dir(e.File)))
			case e.InheritedFrom != "":
				// Dep-transcript (or relative-File inherited): the parent is a
				// dependency in this same plan.
				if key := lineageParentKey(e.InheritedFrom); key != "" {
					add(filepath.Join(planDir, ".artifacts", key))
				}
			}
		}
	}
	return dirs
}

// LastCacheActivity reports the most recent cache-touching event for this
// chat's lineage — max RequestManifest.CreatedAt across the job's own artifact
// dir and its lineage parents/ancestors, unioned with warm receipts
// (warm-*.json). The bool is false when no activity is found at all (a chat
// that never fired and has no warmed lineage — nothing to go stale).
func LastCacheActivity(planDir string, job *Job) (time.Time, []ActivitySource, bool) {
	var latest time.Time
	var sources []ActivitySource

	for _, dir := range cacheActivityDirs(planDir, job) {
		manifests, _ := filepath.Glob(filepath.Join(dir, "request-manifest-*.json"))
		for _, p := range manifests {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var m RequestManifest
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			sources = append(sources, ActivitySource{Kind: "manifest", Path: p, JobID: m.JobID, CreatedAt: m.CreatedAt})
			if m.CreatedAt.After(latest) {
				latest = m.CreatedAt
			}
		}

		receipts, _ := filepath.Glob(filepath.Join(dir, "warm-*.json"))
		for _, p := range receipts {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var r WarmReceipt
			if json.Unmarshal(data, &r) != nil {
				continue
			}
			sources = append(sources, ActivitySource{Kind: "warm", Path: p, CreatedAt: r.CreatedAt})
			if r.CreatedAt.After(latest) {
				latest = r.CreatedAt
			}
		}
	}

	if latest.IsZero() {
		return time.Time{}, sources, false
	}
	return latest, sources, true
}

// latestRequestManifest returns the job's newest request manifest by
// RequestManifest.CreatedAt (oracle-plays J5 addendum A4 — turnID is random hex,
// so a filename glob-sort would pick a random turn). Returns (nil, "") when the
// job has no manifests.
func latestRequestManifest(planDir, jobID string) (*RequestManifest, string) {
	matches, err := filepath.Glob(RequestManifestPath(planDir, jobID, "*"))
	if err != nil || len(matches) == 0 {
		return nil, ""
	}
	var best *RequestManifest
	var bestPath string
	for _, p := range matches {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var m RequestManifest
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if best == nil || m.CreatedAt.After(best.CreatedAt) {
			mm := m
			best = &mm
			bestPath = p
		}
	}
	return best, bestPath
}

// sumPrefixTokens sums TokenEstimate over a manifest's non-turn entries — the
// cached prefix (system + layers + context + history); the volatile turn block
// is never cached.
func sumPrefixTokens(m *RequestManifest) int64 {
	if m == nil {
		return 0
	}
	var total int64
	for _, e := range m.Entries {
		if e.Kind == anthropic.RequestBlockTurn {
			continue
		}
		total += e.TokenEstimate
	}
	return total
}

// EstimateCachedPrefixTokens sums TokenEstimate over the latest manifest's
// non-turn entries — the ~196K-token cold-rewrite figure the staleness warning
// quotes. Zero when the job has no manifest yet.
func EstimateCachedPrefixTokens(planDir, jobID string) int64 {
	m, _ := latestRequestManifest(planDir, jobID)
	return sumPrefixTokens(m)
}

// estimateStalePrefixTokens is the warning's best-effort prefix-size figure: the
// job's own latest manifest when it has one, else the newest manifest anywhere
// in its lineage (a chat about to run turn 1 has no own manifest, but a parent's
// records the shared prefix size).
func estimateStalePrefixTokens(planDir string, job *Job) int64 {
	if n := EstimateCachedPrefixTokens(planDir, job.ID); n > 0 {
		return n
	}
	var best *RequestManifest
	for _, dir := range cacheActivityDirs(planDir, job) {
		matches, _ := filepath.Glob(filepath.Join(dir, "request-manifest-*.json"))
		for _, p := range matches {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var m RequestManifest
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			if best == nil || m.CreatedAt.After(best.CreatedAt) {
				mm := m
				best = &mm
			}
		}
	}
	return sumPrefixTokens(best)
}

// cacheTTLInfo maps a chat's cache_ttl to the durations the staleness warning
// needs. WarnThreshold is TTL − 10min for 1h (→ 50min); for 5m the window is so
// short that warm-keepalive is impractical (KeepaliveImpractical), and the
// threshold is the full 5m.
type cacheTTLInfo struct {
	Label                string // "5m" | "1h"
	Duration             time.Duration
	WarnThreshold        time.Duration
	KeepaliveImpractical bool
}

// chatCacheTTLInfo resolves cacheTTLInfo from job.ChatCacheTTL() ("5m"/"1h",
// oracle-plays J5 addendum A3 — the accessor returns (string, error)).
func chatCacheTTLInfo(job *Job) (cacheTTLInfo, error) {
	ttl, err := job.ChatCacheTTL()
	if err != nil {
		return cacheTTLInfo{}, err
	}
	switch ttl {
	case "1h":
		return cacheTTLInfo{Label: "1h", Duration: time.Hour, WarnThreshold: time.Hour - 10*time.Minute}, nil
	default: // "5m"
		return cacheTTLInfo{Label: "5m", Duration: 5 * time.Minute, WarnThreshold: 5 * time.Minute, KeepaliveImpractical: true}, nil
	}
}

// ChatCacheStaleness reports whether this chat's cached lineage prefix has gone
// (or is about to go) cold, plus a human message describing it. It returns
// ("", false) when the cache is still fresh OR when there is no prior activity
// to keep warm (a first-ever turn with no lineage — nothing cached yet). The
// returned message is the shared core sentence; callers frame it (the CLI adds
// the `flow plan warm` hint before its confirmation, the executor logs it as an
// advisory).
func ChatCacheStaleness(planDir string, job *Job) (string, bool) {
	info, err := chatCacheTTLInfo(job)
	if err != nil {
		return "", false
	}
	last, _, ok := LastCacheActivity(planDir, job)
	if !ok {
		return "", false
	}
	elapsed := time.Since(last)
	if elapsed < info.WarnThreshold {
		return "", false
	}
	mins := int(elapsed.Minutes())
	tokens := FormatTokenCount(estimateStalePrefixTokens(planDir, job))
	if info.KeepaliveImpractical {
		return sprintfStaleness(mins, info.Label, tokens, true), true
	}
	return sprintfStaleness(mins, info.Label, tokens, false), true
}

// sprintfStaleness formats the shared staleness sentence.
func sprintfStaleness(mins int, ttlLabel, tokens string, keepaliveImpractical bool) string {
	base := fmt.Sprintf(
		"last cache-touching activity for this chat's lineage was %d min ago (TTL %s); this turn will re-write the cached prefix cold (~%s tokens)",
		mins, ttlLabel, tokens)
	if keepaliveImpractical {
		base += " — the 5m TTL makes `flow plan warm` keepalive impractical"
	}
	return base
}
