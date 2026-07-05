package orchestration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/grovetools/grove-anthropic/pkg/anthropic"
)

func writeManifestFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestDescribeChatRequestManifest pins the manifest entries for the P2 chat
// shape (ladder, no system/history yet): ordered layer docs with the
// breakpoint+TTL on the last, then the volatile turn block; sha256 of the
// exact bytes; bytes/4 token estimates.
func TestDescribeChatRequestManifest(t *testing.T) {
	dir := t.TempDir()
	layer0 := writeManifestFixture(t, dir, "00-hot.xml", "hot bundle bytes!") // 17 bytes → 4 tokens
	layer1 := writeManifestFixture(t, dir, "dep.md", "dependency text")       // 15 bytes → 3 tokens

	opts := anthropic.RequestOptions{
		Model:       "claude-test",
		Prompt:      "the assembled turn prompt", // 25 bytes → 6 tokens
		WorkDir:     dir,
		CacheLayout: anthropic.CacheLayoutLadder,
		CacheTTL:    "1h",
		LayerFiles:  []string{layer0, layer1},
	}

	entries, err := DescribeChatRequestManifest(opts)
	if err != nil {
		t.Fatalf("DescribeChatRequestManifest: %v", err)
	}

	want := []RequestManifestEntry{
		{Kind: "layer", Path: layer0, ContentHash: sha256hex("hot bundle bytes!"), TokenEstimate: 4},
		{Kind: "layer", Path: layer1, ContentHash: sha256hex("dependency text"), Breakpoint: true, TTL: "1h", TokenEstimate: 3},
		{Kind: "turn", ContentHash: sha256hex("the assembled turn prompt"), TokenEstimate: 6},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries = %+v\nwant %+v", entries, want)
	}

	// Hash stability: identical inputs must produce identical entries.
	again, err := DescribeChatRequestManifest(opts)
	if err != nil {
		t.Fatalf("DescribeChatRequestManifest (second call): %v", err)
	}
	if !reflect.DeepEqual(entries, again) {
		t.Errorf("entries differ across identical calls:\n%+v\n%+v", entries, again)
	}

	// no_cache: same blocks, zero breakpoints, no TTLs.
	opts.NoCache = true
	entries, err = DescribeChatRequestManifest(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Breakpoint || e.TTL != "" {
			t.Errorf("no_cache entry carries breakpoint/TTL: %+v", e)
		}
	}

	// An unreadable layer file is an error, not a silently wrong manifest.
	opts.NoCache = false
	opts.LayerFiles = []string{filepath.Join(dir, "missing.xml")}
	if _, err := DescribeChatRequestManifest(opts); err == nil {
		t.Error("missing layer file: want error, got nil")
	}
}

// TestBuildFlattenedRequestManifestEntries covers the gemini passthrough
// shape: plain context docs, no breakpoints, then the turn block.
func TestBuildFlattenedRequestManifestEntries(t *testing.T) {
	dir := t.TempDir()
	f := writeManifestFixture(t, dir, "ctx.md", "abcd") // 4 bytes → 1 token

	entries, err := BuildFlattenedRequestManifestEntries([]string{f}, "turn")
	if err != nil {
		t.Fatal(err)
	}
	want := []RequestManifestEntry{
		{Kind: "context", Path: f, ContentHash: sha256hex("abcd"), TokenEstimate: 1},
		{Kind: "turn", ContentHash: sha256hex("turn"), TokenEstimate: 1},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries = %+v\nwant %+v", entries, want)
	}
	for _, e := range entries {
		if e.Breakpoint || e.TTL != "" {
			t.Errorf("flattened entry carries breakpoint/TTL: %+v", e)
		}
	}
}

// TestWriteRequestManifestRoundTrip exercises the atomic writer and the path
// contract (next to the briefing file, keyed by turn id).
func TestWriteRequestManifestRoundTrip(t *testing.T) {
	planDir := t.TempDir()
	manifest := RequestManifest{
		TurnID:      "abc123",
		JobID:       "job-1",
		Model:       "claude-test",
		Provider:    requestManifestProviderAnthropic,
		CacheLayout: anthropic.CacheLayoutLadder,
		CacheTTL:    "1h",
		Entries: []RequestManifestEntry{
			{Kind: "layer", Path: "/x/00.xml", ContentHash: "h", Breakpoint: true, TTL: "1h", TokenEstimate: 10},
			{Kind: "turn", ContentHash: "t", TokenEstimate: 2},
		},
	}

	path, err := WriteRequestManifest(planDir, "job-1", "abc123", manifest)
	if err != nil {
		t.Fatalf("WriteRequestManifest: %v", err)
	}
	if path != RequestManifestPath(planDir, "job-1", "abc123") {
		t.Errorf("path = %q, want %q", path, RequestManifestPath(planDir, "job-1", "abc123"))
	}
	if filepath.Dir(path) != filepath.Join(planDir, ".artifacts", "job-1") {
		t.Errorf("manifest not under the job artifact dir: %q", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got RequestManifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, manifest) {
		t.Errorf("round trip = %+v\nwant %+v", got, manifest)
	}
}
