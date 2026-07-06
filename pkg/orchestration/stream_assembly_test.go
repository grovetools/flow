package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/grovetools/grove-anthropic/pkg/anthropic"
)

// TestChatCacheLayout covers the frontmatter resolution: unset → ladder,
// "ladder"/"stream" pass through, anything else is an actionable error.
func TestChatCacheLayout(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "ladder", false},
		{"ladder", "ladder", false},
		{"stream", "stream", false},
		{"pinned", "", true},
	}
	for _, c := range cases {
		job := &Job{CacheLayout: c.in}
		got, err := job.ChatCacheLayout()
		if c.wantErr {
			if err == nil {
				t.Errorf("cache_layout %q: expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("cache_layout %q: unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("cache_layout %q = %q, want %q", c.in, got, c.want)
		}
	}
}

// streamParams builds layer-engine params with an explicit layout.
func (f *layerFixture) streamParams(turnID, layout, anchor string, refresh LayerRefreshMode) LayerEngineParams {
	p := f.params(turnID, refresh)
	p.Layout = layout
	p.AnchorExchange = anchor
	return p
}

// TestLayoutStampLifecycle asserts a fresh store stamps its layout, the free
// ladder→stream migration rewrites the stamp in place, and the refused
// stream→ladder direction fails pointing at --rebase-context (spec 27 §4).
func TestLayoutStampLifecycle(t *testing.T) {
	f := newLayerFixture(t)

	// Fresh store under ladder stamps "ladder".
	if _, err := PrepareContextLayers(f.ctx, f.streamParams("t1", "ladder", "", LayerRefreshNone)); err != nil {
		t.Fatalf("turn 1 (ladder): %v", err)
	}
	if got := f.manifest().Layout; got != "ladder" {
		t.Errorf("fresh manifest layout = %q, want ladder", got)
	}

	// Reopen as stream: free migration rewrites the stamp.
	if _, err := PrepareContextLayers(f.ctx, f.streamParams("t2", "stream", "", LayerRefreshNone)); err != nil {
		t.Fatalf("turn 2 (stream migration): %v", err)
	}
	if got := f.manifest().Layout; got != "stream" {
		t.Errorf("post-migration manifest layout = %q, want stream", got)
	}

	// Reopen back as ladder: refused.
	_, err := PrepareContextLayers(f.ctx, f.streamParams("t3", "ladder", "", LayerRefreshNone))
	if err == nil {
		t.Fatal("stream→ladder should be refused")
	}
	if !strings.Contains(err.Error(), "--rebase-context") {
		t.Errorf("refusal error should mention --rebase-context, got: %v", err)
	}
}

// TestBuildStreamItems asserts the interleave order: head layers (anchor ""),
// then context docs, then history blocks with any layer anchored to an
// exchange emitted right after that exchange's block; an orphaned anchor falls
// back to the head.
func TestBuildStreamItems(t *testing.T) {
	layerPaths := []string{"/L/00-base.xml", "/L/01-widen.xml", "/L/02-orphan.xml"}
	anchors := map[string]string{
		"/L/00-base.xml":   "",   // head
		"/L/01-widen.xml":  "e1", // interleave after exchange e1
		"/L/02-orphan.xml": "e9", // anchor absent from history → head fallback
	}
	includes := []string{"/inc/a.md"}
	history := HistoryBlocks{
		{Text: "<turn role=\"user\">q1</turn>", ExchangeID: ""},
		{Text: "<turn role=\"assistant\" id=\"e1\">a1</turn>", ExchangeID: "e1"},
	}

	items := buildStreamItems(context.Background(), "job-1", layerPaths, anchors, includes, history)

	type want struct {
		kind string
		ref  string // Path for docs, Text for history
	}
	got := make([]want, len(items))
	for i, it := range items {
		ref := it.Path
		if it.Kind == anthropic.RequestBlockHistory {
			ref = it.Text
		}
		got[i] = want{it.Kind, ref}
	}
	expect := []want{
		{anthropic.RequestBlockLayer, "/L/00-base.xml"},   // head
		{anthropic.RequestBlockLayer, "/L/02-orphan.xml"}, // orphan → head
		{anthropic.RequestBlockContext, "/inc/a.md"},      // context after layers
		{anthropic.RequestBlockHistory, "<turn role=\"user\">q1</turn>"},
		{anthropic.RequestBlockHistory, "<turn role=\"assistant\" id=\"e1\">a1</turn>"},
		{anthropic.RequestBlockLayer, "/L/01-widen.xml"}, // interleaved after e1
	}
	if len(got) != len(expect) {
		t.Fatalf("item count = %d, want %d: %+v", len(got), len(expect), got)
	}
	for i := range expect {
		if got[i] != expect[i] {
			t.Errorf("item %d = %+v, want %+v", i, got[i], expect[i])
		}
	}
}

// TestBuildSupersededIndex asserts the <current-files> block names the winning
// (highest-N) layer for every file captured by more than one layer, and is
// empty when nothing is superseded.
func TestBuildSupersededIndex(t *testing.T) {
	if idx := buildSupersededIndex(&LayerManifest{Layers: []LayerEntry{
		{N: 0, Files: []LayerFileRecord{{Path: "src/a.go"}}},
	}}, "/root"); idx != "" {
		t.Errorf("no supersession should yield empty index, got %q", idx)
	}

	m := &LayerManifest{Layers: []LayerEntry{
		{N: 0, Files: []LayerFileRecord{{Path: "src/a.go"}, {Path: "src/b.go"}}},
		{N: 2, Files: []LayerFileRecord{{Path: "src/a.go"}}, Supersedes: []string{"src/a.go"}},
	}}
	idx := buildSupersededIndex(m, "/root")
	if !strings.Contains(idx, "src/a.go → layer 2") {
		t.Errorf("index should point src/a.go at winning layer 2, got:\n%s", idx)
	}
	if strings.Contains(idx, "src/b.go") {
		t.Errorf("src/b.go is captured once (not superseded), should be absent:\n%s", idx)
	}
}

// TestFormatConversationRegionsExchangeID pins the stream anchor key: assistant
// history blocks carry ExchangeID = their directive id; user blocks carry "".
func TestFormatConversationRegionsExchangeID(t *testing.T) {
	turns := []*ChatTurn{
		mkUserTurn("q1", "chat"),
		mkAssistantTurn("id-a1", "2026-07-05 10:00:00", "a1"),
		mkUserTurn("q2", ""),
	}
	regions := FormatConversationRegions(turns)
	if len(regions.HistoryBlocks) != 2 {
		t.Fatalf("history blocks = %d, want 2", len(regions.HistoryBlocks))
	}
	if regions.HistoryBlocks[0].ExchangeID != "" {
		t.Errorf("user block ExchangeID = %q, want empty", regions.HistoryBlocks[0].ExchangeID)
	}
	if regions.HistoryBlocks[1].ExchangeID != "id-a1" {
		t.Errorf("assistant block ExchangeID = %q, want id-a1", regions.HistoryBlocks[1].ExchangeID)
	}
}
