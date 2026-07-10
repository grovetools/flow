package orchestration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// ---------------------------------------------------------------------------
// Per-turn cache-health line (oracle-plays J6).
//
// After each Anthropic chat turn, flow emits one grep-able advisory line —
// turn id, cache hit%, tokens written, and (only on a material hit% drop) the
// first manifest entry whose bytes changed vs the prior successful turn. The
// numbers come from the in-memory anthropic.UsageResult at the single accounting
// site in executeChatJob; the "what busted the cache" label comes from diffing
// the current request manifest against the baseline turn's manifest. The line
// is read-only over existing artifacts — no new persistent state — and is
// purely advisory: compute failures degrade to a ulog.Warn + no line, never a
// job error (mech-contract §2).
// ---------------------------------------------------------------------------

// cacheHitDropPoints is the percentage-point drop in hit% (vs the prior turn's
// recorded hit%) that flips cache-health from "healthy" to "look for the
// buster". 15pp separates routine breakpoint churn from a real cold rewrite.
const cacheHitDropPoints = 15

// CacheHealth is one chat turn's cache accounting: the turn id, the fraction of
// the prompt served from cache (HitPct in [0,1]), the tokens cold-written this
// turn, and — only when set — the first manifest entry that busted the cached
// suffix.
type CacheHealth struct {
	TurnID  string
	HitPct  float64
	Written int64
	Buster  string
}

// ComputeCacheHealth builds the per-turn CacheHealth from the turn's usage plus
// a manifest diff against the prior successful turn. HitPct = CacheReadTokens /
// (CacheReadTokens + CacheWrite5m + CacheWrite1h); Written = CacheWrite5m +
// CacheWrite1h. The buster diff runs only when hit% dropped materially vs
// priorHitPct (>= cacheHitDropPoints pp) OR — with no prior hit% — this turn's
// writes dominated its reads; a healthy turn returns stats with an empty Buster
// and never touches the manifests.
//
// The baseline manifest is keyed off the PRIOR cache-health line's turn id
// (recovered from job.log, addendum A3): loading
// RequestManifestPath(planDir, jobID, priorHealth.TurnID) pairs the hit%
// baseline and the manifest baseline to the same successful turn, so a failed
// pre-dispatch orphan manifest (written before an API error, with no health
// line) can never be selected. No prior health line → first-turn behavior
// (stats, no diff). Missing that specific baseline manifest → degrade to no diff
// (advisory-only; the caller's ulog carries the state).
func ComputeCacheHealth(planDir, jobID, turnID string, u *anthropic.UsageResult, priorHitPct float64, hasPrior bool) (*CacheHealth, error) {
	if u == nil {
		return nil, fmt.Errorf("cache health: nil usage result")
	}
	written := u.CacheWrite5m + u.CacheWrite1h
	read := u.CacheReadTokens
	var hit float64
	if denom := read + written; denom > 0 {
		hit = float64(read) / float64(denom)
	}
	h := &CacheHealth{TurnID: turnID, HitPct: hit, Written: written}

	// Only hunt for a buster when the cache visibly regressed: a material hit%
	// drop vs the prior turn, or (with no prior baseline) writes dominating reads.
	dropped := hasPrior && (priorHitPct-hit) >= float64(cacheHitDropPoints)/100
	writesDominate := !hasPrior && written > read
	if !dropped && !writesDominate {
		return h, nil
	}

	// Current turn's manifest (written pre-dispatch, so already on disk).
	cur, err := loadRequestManifestFile(RequestManifestPath(planDir, jobID, turnID))
	if err != nil || cur == nil {
		return h, nil // no manifest to diff against; stats only
	}

	// Baseline = the prior successful turn, keyed off its cache-health line.
	prior, ok := LastCacheHealthFromLog(filepath.Join(planDir, ".artifacts", jobID, "job.log"))
	if !ok {
		return h, nil // first turn (or only orphan turns so far): stats only
	}
	prev, err := loadRequestManifestFile(RequestManifestPath(planDir, jobID, prior.TurnID))
	if err != nil || prev == nil {
		return h, nil // baseline manifest gone (manual cleanup): degrade to no diff
	}

	h.Buster = firstChangedEntry(prev.Entries, cur.Entries)
	return h, nil
}

// loadRequestManifestFile reads and unmarshals a single request manifest by
// path. Returns (nil, nil) when the file is absent so callers can degrade to a
// stats-only line.
func loadRequestManifestFile(path string) (*RequestManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m RequestManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// firstChangedEntry names the first manifest entry that busted the cached suffix
// between two turns, or "" when the shared prefix is byte-identical. Turn
// entries are filtered from BOTH slices first (each manifest has exactly one,
// always last — addendum A1); left in, prev's trailing turn entry would pair
// against cur's first new history block and report a false history buster every
// turn >= 2. Only ContentHash is compared — never Breakpoint/TTL, which move
// every turn by design as the last-history breakpoint slides.
//
// At the first mismatch index i (addendum A2):
//   - cur[i] is a layer whose Path is absent from prev → routine mid-list layer
//     growth (spec-19 lineage): "new layer <base> (<source>) — appended, suffix
//     re-cached", NOT an anomaly.
//   - same Kind+Path, different ContentHash → a genuine byte-level buster:
//     "<kind> <label> changed".
//   - anything else (disappeared / reordered / kind swap) → "ordering anomaly at
//     <kind>[i]".
//
// A clean common prefix returns "" — remaining cur entries (appended history
// before the always-last turn, or a tail-appended layer) are expected growth.
func firstChangedEntry(prev, cur []RequestManifestEntry) string {
	fp := filterOutTurnEntries(prev)
	fc := filterOutTurnEntries(cur)
	n := min(len(fp), len(fc))
	for i := 0; i < n; i++ {
		p, c := fp[i], fc[i]
		if p.Kind == c.Kind && p.Path == c.Path && p.ContentHash == c.ContentHash {
			continue
		}
		switch {
		case c.Kind == anthropic.RequestBlockLayer && !entriesContainPath(fp, c.Path):
			return fmt.Sprintf("new layer %s (%s) — appended, suffix re-cached", filepath.Base(c.Path), c.Source)
		case p.Kind == c.Kind && p.Path == c.Path:
			return fmt.Sprintf("%s %s changed", c.Kind, entryLabel(c, i))
		default:
			return fmt.Sprintf("ordering anomaly at %s[%d]", c.Kind, i)
		}
	}
	return ""
}

// filterOutTurnEntries returns the entries with the volatile turn block removed
// (it is always last and never cached, so it must never participate in the walk).
func filterOutTurnEntries(entries []RequestManifestEntry) []RequestManifestEntry {
	out := make([]RequestManifestEntry, 0, len(entries))
	for _, e := range entries {
		if e.Kind == anthropic.RequestBlockTurn {
			continue
		}
		out = append(out, e)
	}
	return out
}

// entriesContainPath reports whether any entry uploads the given file path
// (used to tell a brand-new layer from a byte-changed existing one).
func entriesContainPath(entries []RequestManifestEntry, path string) bool {
	if path == "" {
		return false
	}
	for _, e := range entries {
		if e.Path == path {
			return true
		}
	}
	return false
}

// entryLabel renders the human identifier for a manifest entry at filtered index
// i: layers by base name + source, history positionally, system/context by name.
func entryLabel(e RequestManifestEntry, i int) string {
	switch e.Kind {
	case anthropic.RequestBlockLayer:
		return fmt.Sprintf("%s (%s)", filepath.Base(e.Path), e.Source)
	case anthropic.RequestBlockHistory:
		return fmt.Sprintf("history[%d]", i)
	case anthropic.RequestBlockSystem:
		return "system"
	default: // context (and any document block)
		return filepath.Base(e.Path)
	}
}

// FormatCacheHealthLine renders the stable, grep-able cache-health line, e.g.
//
//	cache-health t=794c99…: 89% hit, 10.3k written — buster: layer 02-delta (git-delta) changed
//
// The FULL turn id is embedded (not truncated) so ParseCacheHealthLine can
// recover it and load the matching manifest. The buster suffix appears only when
// non-empty.
func FormatCacheHealthLine(h CacheHealth) string {
	line := fmt.Sprintf("cache-health t=%s: %.0f%% hit, %s written", h.TurnID, h.HitPct*100, FormatTokenCount(h.Written))
	if h.Buster != "" {
		line += " — buster: " + h.Buster
	}
	return line
}

// cacheHealthBusterSep is the delimiter between the stats and the buster label.
const cacheHealthBusterSep = " — buster: "

// ParseCacheHealthLine parses a cache-health line (tolerating any leading log
// decoration by scanning for the "cache-health t=" marker) back into a
// CacheHealth. It round-trips with FormatCacheHealthLine. The bool is false when
// the line is not a cache-health line.
func ParseCacheHealthLine(line string) (CacheHealth, bool) {
	const marker = "cache-health t="
	idx := strings.Index(line, marker)
	if idx < 0 {
		return CacheHealth{}, false
	}
	rest := line[idx+len(marker):]

	colon := strings.Index(rest, ": ")
	if colon < 0 {
		return CacheHealth{}, false
	}
	h := CacheHealth{TurnID: strings.TrimSpace(rest[:colon])}
	stats := rest[colon+2:]

	if b := strings.Index(stats, cacheHealthBusterSep); b >= 0 {
		h.Buster = strings.TrimSpace(stats[b+len(cacheHealthBusterSep):])
		stats = stats[:b]
	}

	// stats == "<pct>% hit, <written> written"
	parts := strings.SplitN(stats, " hit, ", 2)
	if len(parts) != 2 {
		return CacheHealth{}, false
	}
	pct, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(parts[0]), "%"), 64)
	if err != nil {
		return CacheHealth{}, false
	}
	h.HitPct = pct / 100
	h.Written = parseHumanTokenCount(strings.TrimSuffix(strings.TrimSpace(parts[1]), " written"))
	return h, true
}

// parseHumanTokenCount inverts FormatTokenCount ("10.3k" → 10300, "1.2M" →
// 1200000, "500" → 500). Best-effort: unparseable input yields 0.
func parseHumanTokenCount(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "M"), strings.HasSuffix(s, "m"):
		mult = 1_000_000
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "k"), strings.HasSuffix(s, "K"):
		mult = 1000
		s = s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(v * mult)
}

// LastCacheHealthFromLog scans a job.log for the last cache-health line and
// returns it parsed. The bool is false when the log has no such line (a fresh
// job, or a job whose only turns were pre-dispatch failures). Used for both the
// prior-hit baseline in ComputeCacheHealth and the TUI token pane's latest-line.
func LastCacheHealthFromLog(logPath string) (CacheHealth, bool) {
	f, err := os.Open(logPath)
	if err != nil {
		return CacheHealth{}, false
	}
	defer f.Close()

	var last CacheHealth
	var found bool
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if h, ok := ParseCacheHealthLine(sc.Text()); ok {
			last, found = h, true
		}
	}
	return last, found
}
