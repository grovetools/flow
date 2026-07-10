package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grovetools/grove-anthropic/pkg/anthropic"
)

// backdateManifests rewrites every request manifest in an artifact dir to the
// given CreatedAt, so the staleness scan reads deterministic timestamps.
func backdateManifests(t *testing.T, artifactDir string, at time.Time) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(artifactDir, "request-manifest-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatalf("no request manifests in %s to back-date", artifactDir)
	}
	for _, p := range matches {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var m RequestManifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		m.CreatedAt = at
		out, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, out, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func artifactDirOf(plan *Plan, jobID string) string {
	return filepath.Join(plan.Directory, ".artifacts", jobID)
}

// TestLastCacheActivity_MaxAcrossLineage: a child chat's last activity is the
// max RequestManifest.CreatedAt over its OWN dir AND its lineage parent's dir —
// so a parent request more recent than the child's own turn is what warms the
// shared prefix and drives the staleness clock.
func TestLastCacheActivity_MaxAcrossLineage(t *testing.T) {
	plan, parent, child := newLineageExecutorFixture(t, "", "")

	// Run the child's turn so it owns a manifest + inherited layer store.
	if err := NewOneShotExecutor(NewMockLLMClient(), nil).Execute(context.Background(), child, plan); err != nil {
		t.Fatalf("child turn: %v", err)
	}

	now := time.Now().UTC()
	childAt := now.Add(-70 * time.Minute)
	parentAt := now.Add(-30 * time.Minute) // more recent than the child's own turn
	backdateManifests(t, artifactDirOf(plan, child.ID), childAt)
	backdateManifests(t, artifactDirOf(plan, parent.ID), parentAt)

	last, sources, ok := LastCacheActivity(plan.Directory, child)
	if !ok {
		t.Fatal("LastCacheActivity ok = false, want activity")
	}
	if last.Sub(parentAt).Abs() > time.Second {
		t.Errorf("last activity = %v, want the parent's %v (the max across the chain)", last, parentAt)
	}
	// Sources span both the child's own dir and the parent's.
	var sawChild, sawParent bool
	for _, s := range sources {
		if s.JobID == child.ID {
			sawChild = true
		}
		if s.JobID == parent.ID {
			sawParent = true
		}
	}
	if !sawChild || !sawParent {
		t.Errorf("sources missing a dir: sawChild=%v sawParent=%v (%d sources)", sawChild, sawParent, len(sources))
	}
}

// TestLastCacheActivity_WarmReceiptCounts: a warm receipt refreshes the activity
// clock, and one placed in a parent's dir (more recent than any manifest) wins.
func TestLastCacheActivity_WarmReceiptCounts(t *testing.T) {
	plan, parent, child := newLineageExecutorFixture(t, "", "")
	if err := NewOneShotExecutor(NewMockLLMClient(), nil).Execute(context.Background(), child, plan); err != nil {
		t.Fatalf("child turn: %v", err)
	}

	now := time.Now().UTC()
	backdateManifests(t, artifactDirOf(plan, child.ID), now.Add(-70*time.Minute))
	backdateManifests(t, artifactDirOf(plan, parent.ID), now.Add(-60*time.Minute))

	// A warm receipt on the PARENT, more recent than every manifest.
	warmAt := now.Add(-5 * time.Minute)
	if _, err := writeWarmReceipt(plan.Directory, parent.ID, WarmReceipt{CreatedAt: warmAt, Model: "claude-x", ParityOK: true}); err != nil {
		t.Fatal(err)
	}

	last, sources, ok := LastCacheActivity(plan.Directory, child)
	if !ok {
		t.Fatal("LastCacheActivity ok = false, want activity")
	}
	if last.Sub(warmAt).Abs() > time.Second {
		t.Errorf("last activity = %v, want the warm receipt's %v", last, warmAt)
	}
	var sawWarm bool
	for _, s := range sources {
		if s.Kind == "warm" {
			sawWarm = true
		}
	}
	if !sawWarm {
		t.Error("no warm-kind source found; warm receipts must count as activity")
	}
}

// TestLastCacheActivity_OwnDirOnly: a standalone chat (no lineage) reports its
// own manifest's timestamp, and none when it never fired.
func TestLastCacheActivity_OwnDirOnly(t *testing.T) {
	plan, job := firedChatFixture(t)

	at := time.Now().UTC().Add(-40 * time.Minute)
	backdateManifests(t, artifactDirOf(plan, job.ID), at)

	last, _, ok := LastCacheActivity(plan.Directory, job)
	if !ok {
		t.Fatal("LastCacheActivity ok = false, want activity")
	}
	if last.Sub(at).Abs() > time.Second {
		t.Errorf("last activity = %v, want own manifest's %v", last, at)
	}

	// A fresh, never-fired chat (its own plan dir) reports no activity.
	freshPlan, freshJob := newChatJobFixture(t, "", "hi")
	if _, _, ok := LastCacheActivity(freshPlan.Directory, freshJob); ok {
		t.Error("LastCacheActivity ok = true for a never-fired chat, want false")
	}
}

// TestChatCacheStaleness_ThresholdAndTokens: past the 50-min (1h TTL) threshold
// the warning fires with a lineage-prefix token figure; fresh activity stays
// quiet.
func TestChatCacheStaleness_ThresholdAndTokens(t *testing.T) {
	plan, job := firedChatFixture(t)

	// Fresh: just fired, so no warning.
	if msg, stale := ChatCacheStaleness(plan.Directory, job); stale {
		t.Errorf("fresh chat flagged stale: %q", msg)
	}

	// Back-date past the 50-min threshold.
	backdateManifests(t, artifactDirOf(plan, job.ID), time.Now().UTC().Add(-74*time.Minute))
	msg, stale := ChatCacheStaleness(plan.Directory, job)
	if !stale {
		t.Fatal("stale chat not flagged")
	}
	for _, want := range []string{"cache-touching activity", "TTL 1h", "tokens"} {
		if !strings.Contains(msg, want) {
			t.Errorf("staleness message %q missing %q", msg, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Per-turn cache-health line (oracle-plays J6).
// ---------------------------------------------------------------------------

// me is a compact RequestManifestEntry constructor for the diff-walk table.
func me(kind, path, source, hash string) RequestManifestEntry {
	return RequestManifestEntry{Kind: kind, Path: path, Source: source, ContentHash: hash}
}

// TestFirstChangedEntry tables the §2/A1/A2 diff walk: the always-last turn
// entry is filtered from both sides, only ContentHash is compared, appended
// history/tail growth is silent, a genuine hash change reports its entry, a new
// mid-list layer reports as expected growth, and a reorder/kind swap is an
// ordering anomaly.
func TestFirstChangedEntry(t *testing.T) {
	sys, lyr, ctx, hist, turn := anthropic.RequestBlockSystem, anthropic.RequestBlockLayer,
		anthropic.RequestBlockContext, anthropic.RequestBlockHistory, anthropic.RequestBlockTurn

	cases := []struct {
		name       string
		prev, cur  []RequestManifestEntry
		wantSubstr string // "" means: expect no buster
	}{
		{
			name: "prev ends in turn, cur grew by one history block → no buster",
			prev: []RequestManifestEntry{me(sys, "", "", "s"), me(lyr, "base.xml", "rules-base", "b"), me(hist, "", "", "h0"), me(turn, "", "", "t1")},
			cur:  []RequestManifestEntry{me(sys, "", "", "s"), me(lyr, "base.xml", "rules-base", "b"), me(hist, "", "", "h0"), me(hist, "", "", "h1"), me(turn, "", "", "t2")},
		},
		{
			name:       "layer hash change reports the layer",
			prev:       []RequestManifestEntry{me(sys, "", "", "s"), me(lyr, "a/base.xml", "rules-base", "A"), me(turn, "", "", "t1")},
			cur:        []RequestManifestEntry{me(sys, "", "", "s"), me(lyr, "a/base.xml", "rules-base", "B"), me(turn, "", "", "t2")},
			wantSubstr: "layer base.xml (rules-base) changed",
		},
		{
			name:       "system hash change reports system",
			prev:       []RequestManifestEntry{me(sys, "", "", "s0"), me(lyr, "base.xml", "rules-base", "b"), me(turn, "", "", "t1")},
			cur:        []RequestManifestEntry{me(sys, "", "", "s1"), me(lyr, "base.xml", "rules-base", "b"), me(turn, "", "", "t2")},
			wantSubstr: "system system changed",
		},
		{
			name:       "history prefix hash change reports the position",
			prev:       []RequestManifestEntry{me(sys, "", "", "s"), me(lyr, "base.xml", "rules-base", "b"), me(hist, "", "", "hA"), me(turn, "", "", "t1")},
			cur:        []RequestManifestEntry{me(sys, "", "", "s"), me(lyr, "base.xml", "rules-base", "b"), me(hist, "", "", "hB"), me(turn, "", "", "t2")},
			wantSubstr: "history history[2] changed",
		},
		{
			name: "breakpoint-only movement does NOT report",
			prev: []RequestManifestEntry{me(sys, "", "", "s"), {Kind: lyr, Path: "base.xml", Source: "rules-base", ContentHash: "b", Breakpoint: true, TTL: "1h"}, me(hist, "", "", "h0"), me(turn, "", "", "t1")},
			cur:  []RequestManifestEntry{me(sys, "", "", "s"), {Kind: lyr, Path: "base.xml", Source: "rules-base", ContentHash: "b", Breakpoint: false}, {Kind: hist, ContentHash: "h0", Breakpoint: true, TTL: "1h"}, me(turn, "", "", "t2")},
		},
		{
			name:       "new mid-list layer reports as appended growth",
			prev:       []RequestManifestEntry{me(sys, "", "", "s"), me(lyr, "base.xml", "rules-base", "b"), me(ctx, "ref.md", "", "c"), me(turn, "", "", "t1")},
			cur:        []RequestManifestEntry{me(sys, "", "", "s"), me(lyr, "base.xml", "rules-base", "b"), me(lyr, "01-delta.xml", "rules-diff", "d"), me(ctx, "ref.md", "", "c"), me(hist, "", "", "h0"), me(turn, "", "", "t2")},
			wantSubstr: "new layer 01-delta.xml (rules-diff) — appended, suffix re-cached",
		},
		{
			name:       "reorder / kind swap is an ordering anomaly",
			prev:       []RequestManifestEntry{me(sys, "", "", "s"), me(ctx, "a.md", "", "ca"), me(ctx, "b.md", "", "cb"), me(turn, "", "", "t1")},
			cur:        []RequestManifestEntry{me(sys, "", "", "s"), me(ctx, "b.md", "", "cb"), me(turn, "", "", "t2")},
			wantSubstr: "ordering anomaly at context[1]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := firstChangedEntry(tc.prev, tc.cur)
			if tc.wantSubstr == "" {
				if got != "" {
					t.Errorf("firstChangedEntry = %q, want no buster", got)
				}
				return
			}
			if got != tc.wantSubstr {
				t.Errorf("firstChangedEntry = %q, want %q", got, tc.wantSubstr)
			}
		})
	}
}

// TestCacheHealthLine_RoundTrip: FormatCacheHealthLine ↔ ParseCacheHealthLine
// round-trips (turn id, hit%, written, and the optional buster), and parsing
// tolerates arbitrary leading log decoration.
func TestCacheHealthLine_RoundTrip(t *testing.T) {
	cases := []CacheHealth{
		{TurnID: "794c99abcd1234", HitPct: 0.89, Written: 10300},
		{TurnID: "abc123", HitPct: 0.42, Written: 1_200_000, Buster: "layer 02-delta.xml (git-delta) changed"},
		{TurnID: "def456", HitPct: 0, Written: 0},
		{TurnID: "ff00aa", HitPct: 1, Written: 500, Buster: "new layer 03-x.xml (rules-diff) — appended, suffix re-cached"},
	}
	for _, want := range cases {
		line := FormatCacheHealthLine(want)
		got, ok := ParseCacheHealthLine(line)
		if !ok {
			t.Fatalf("ParseCacheHealthLine(%q) not ok", line)
		}
		if got.TurnID != want.TurnID || got.Buster != want.Buster || got.HitPct != want.HitPct || got.Written != want.Written {
			t.Errorf("round-trip mismatch for %q:\n got=%+v\nwant=%+v", line, got, want)
		}
	}

	// Leading log decoration is tolerated (the marker is scanned for anywhere).
	decorated := "2026-07-10 12:00:00 INFO " + FormatCacheHealthLine(cases[0])
	if got, ok := ParseCacheHealthLine(decorated); !ok || got.TurnID != cases[0].TurnID {
		t.Errorf("decorated parse = (%+v, %v), want turn %q", got, ok, cases[0].TurnID)
	}

	// A non-cache-health line is rejected.
	if _, ok := ParseCacheHealthLine("just a regular log line"); ok {
		t.Error("ParseCacheHealthLine accepted a non-matching line")
	}
}

// TestLastCacheHealthFromLog: the scanner returns the LAST cache-health line in
// a job.log (and false for a log with none / a missing file).
func TestLastCacheHealthFromLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "job.log")

	if _, ok := LastCacheHealthFromLog(logPath); ok {
		t.Error("missing log reported a health line")
	}

	body := strings.Join([]string{
		"some preamble",
		FormatCacheHealthLine(CacheHealth{TurnID: "turn-one", HitPct: 0.95, Written: 1000}),
		"an unrelated line",
		FormatCacheHealthLine(CacheHealth{TurnID: "turn-two", HitPct: 0.5, Written: 20000, Buster: "system system changed"}),
		"trailing noise",
	}, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got, ok := LastCacheHealthFromLog(logPath)
	if !ok {
		t.Fatal("LastCacheHealthFromLog ok = false, want the last line")
	}
	if got.TurnID != "turn-two" || got.Buster != "system system changed" {
		t.Errorf("last health = %+v, want turn-two with its buster", got)
	}
}

// TestComputeCacheHealth_HealthyTurnSkipsDiff: a turn whose hit% did not drop
// (and whose writes did not dominate) reports stats with no buster and never
// touches the manifests.
func TestComputeCacheHealth_HealthyTurnSkipsDiff(t *testing.T) {
	dir := t.TempDir()
	u := &anthropic.UsageResult{CacheReadTokens: 9000, CacheWrite5m: 1000}
	// prior hit 0.90, this turn 0.90 → no material drop.
	h, err := ComputeCacheHealth(dir, "job-x", "turn-x", u, 0.90, true)
	if err != nil {
		t.Fatalf("ComputeCacheHealth: %v", err)
	}
	if h.Buster != "" {
		t.Errorf("healthy turn produced a buster: %q", h.Buster)
	}
	wantHit := 9000.0 / 10000.0
	if h.HitPct != wantHit || h.Written != 1000 || h.TurnID != "turn-x" {
		t.Errorf("health = %+v, want hit %.3f / written 1000 / turn-x", h, wantHit)
	}
}

// TestComputeCacheHealth_NilUsage: a nil usage result is an error (the caller
// only reaches ComputeCacheHealth on the Anthropic path, where usage is set).
func TestComputeCacheHealth_NilUsage(t *testing.T) {
	if _, err := ComputeCacheHealth(t.TempDir(), "j", "t", nil, 0, false); err == nil {
		t.Error("ComputeCacheHealth(nil usage) = nil error, want an error")
	}
}

// TestChatTurn_MockEmitsNoHealthLine_AndComputeDiffsRealManifests is the
// manifest-sequence-realism test (§5): two mock turns produce REAL manifests
// (turn 2 appends a rules-diff layer), the mock path emits NO cache-health line
// (apiUsage == nil), and ComputeCacheHealth over a synthetic dropped-hit usage
// names the appended layer as the buster by diffing the two real manifests.
func TestChatTurn_MockEmitsNoHealthLine_AndComputeDiffsRealManifests(t *testing.T) {
	mockResponse := filepath.Join(t.TempDir(), "mock-response.md")
	if err := os.WriteFile(mockResponse, []byte("mock oracle response"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", mockResponse)

	plan, job := newChatJobFixture(t, "rules_file: ctx.rules\n", "First question.")
	gitInitDir(t, plan.Directory)
	writeSrc := func(rel, content string) {
		t.Helper()
		path := filepath.Join(plan.Directory, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSrc("src/a.go", "package src\n\nvar A = 1\n")
	writeSrc("src/b.go", "package src\n\nvar B = 2\n")
	writeSrc("ctx.rules", "src/a.go\n")
	// An include so turn 1's prefix has a post-base entry — the appended
	// rules-diff layer then lands mid-list (not past len(prev)) at turn 2.
	writeSrc("ref.md", "reference material")
	job.Include = []string{"ref.md"}

	executor := NewOneShotExecutor(NewMockLLMClient(), nil)
	if err := executor.Execute(context.Background(), job, plan); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	turn1ID := soleManifestTurnID(t, plan, job.ID)

	// Widen the rules and append the next user turn (as TestExecuteChatJob_*
	// does) so turn 2 appends exactly one rules-diff layer.
	writeSrc("ctx.rules", "src/a.go\nsrc/b.go\n")
	chatContent, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(job.FilePath, append(chatContent, []byte("\nNow widen to b.\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	job2, err := LoadJob(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	job2.Filename, job2.FilePath, job2.Include = job.Filename, job.FilePath, job.Include
	if err := executor.Execute(context.Background(), job2, plan); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	turn2ID := newestManifestTurnIDExcluding(t, plan, job.ID, turn1ID)

	// The mock path yields apiUsage == nil, so the emit never fires: job.log
	// carries no cache-health line.
	logPath := filepath.Join(plan.Directory, ".artifacts", job.ID, "job.log")
	if data, err := os.ReadFile(logPath); err == nil {
		if strings.Contains(string(data), "cache-health t=") {
			t.Errorf("mock turn emitted a cache-health line:\n%s", data)
		}
	}

	// Seed the prior-turn baseline the way a real Anthropic turn 1 would have:
	// a cache-health line in job.log keyed to turn 1's id.
	if err := os.WriteFile(logPath, []byte(FormatCacheHealthLine(CacheHealth{TurnID: turn1ID, HitPct: 0.95, Written: 1000})+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Synthetic dropped-hit usage for turn 2: reads collapse, writes dominate.
	u := &anthropic.UsageResult{CacheReadTokens: 100, CacheWrite5m: 10000}
	h, err := ComputeCacheHealth(plan.Directory, job.ID, turn2ID, u, 0.95, true)
	if err != nil {
		t.Fatalf("ComputeCacheHealth: %v", err)
	}
	if h.Written != 10000 {
		t.Errorf("Written = %d, want 10000", h.Written)
	}
	if !strings.Contains(h.Buster, "new layer") || !strings.Contains(h.Buster, "rules-diff") {
		t.Errorf("Buster = %q, want the appended rules-diff layer", h.Buster)
	}
}

// soleManifestTurnID returns the turn id of a job's single request manifest.
func soleManifestTurnID(t *testing.T, plan *Plan, jobID string) string {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(plan.Directory, ".artifacts", jobID, "request-manifest-*.json"))
	if len(matches) != 1 {
		t.Fatalf("want exactly 1 manifest, got %v", matches)
	}
	return manifestTurnID(t, matches[0])
}

// newestManifestTurnIDExcluding returns the newest manifest turn id whose id is
// not exclude (i.e. the turn-2 manifest).
func newestManifestTurnIDExcluding(t *testing.T, plan *Plan, jobID, exclude string) string {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(plan.Directory, ".artifacts", jobID, "request-manifest-*.json"))
	for _, p := range matches {
		if id := manifestTurnID(t, p); id != exclude {
			return id
		}
	}
	t.Fatalf("no manifest other than %s among %v", exclude, matches)
	return ""
}

// manifestTurnID reads a manifest file and returns its turn id.
func manifestTurnID(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m RequestManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m.TurnID
}
