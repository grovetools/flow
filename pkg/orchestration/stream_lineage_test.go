package orchestration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grovetools/grove-anthropic/pkg/anthropic"
)

// TestParentTranscriptBlocks pins the per-turn splice serialization: it tags
// the assistant block with its directive id, drops grove markers and the
// awaiting-response attribute, and joins byte-identically to the transcript
// document (the two forms share one serialization).
func TestParentTranscriptBlocks(t *testing.T) {
	f := newLayerFixture(t)
	mdPath := writeParentChatMD(f, "job-a")
	content, rerr := os.ReadFile(mdPath)
	if rerr != nil {
		t.Fatal(rerr)
	}

	blocks, err := parentTranscriptBlocks(content)
	if err != nil {
		t.Fatalf("parentTranscriptBlocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (user + assistant)", len(blocks))
	}
	if blocks[0].ExchangeID != "" {
		t.Errorf("user block ExchangeID = %q, want empty", blocks[0].ExchangeID)
	}
	if blocks[1].ExchangeID != "aaa111" {
		t.Errorf("assistant block ExchangeID = %q, want aaa111", blocks[1].ExchangeID)
	}
	joined := blocks[0].Text + blocks[1].Text
	doc, err := renderChatTranscriptXML(content)
	if err != nil {
		t.Fatalf("renderChatTranscriptXML: %v", err)
	}
	if joined != doc {
		t.Errorf("per-turn blocks not byte-identical to transcript document:\nblocks: %q\ndoc:    %q", joined, doc)
	}
	for _, forbidden := range []string{"<!-- grove:", "awaiting_response"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("spliced blocks contain %q: %q", forbidden, joined)
		}
	}
}

// TestVerifyInheritedPrefix covers the prefix hash-gate: it passes when the
// re-derived blocks match the parent manifest's recorded history hashes (the
// trailing final exchange, absent from the manifest, is not gated), fails with
// the sentinel on a byte mismatch, and errors on a missing manifest.
func TestVerifyInheritedPrefix(t *testing.T) {
	f := newLayerFixture(t)
	blocks := HistoryBlocks{
		{Text: "<turn role=\"user\">q1</turn>\n"},
		{Text: "<turn role=\"assistant\" id=\"e1\">a1</turn>\n", ExchangeID: "e1"},
		{Text: "<turn role=\"user\">q2 final</turn>\n"}, // final exchange: no manifest gate
	}
	// Parent manifest records only the first two blocks as history entries.
	h0, _ := hashAndEstimate([]byte(blocks[0].Text))
	h1, _ := hashAndEstimate([]byte(blocks[1].Text))
	manifest := RequestManifest{
		TurnID: "pt", JobID: "job-a", CreatedAt: time.Now().UTC(),
		Entries: []RequestManifestEntry{
			{Kind: anthropic.RequestBlockLayer, Path: "/x/00-base.xml", ContentHash: "deadbeef"},
			{Kind: anthropic.RequestBlockHistory, ContentHash: h0},
			{Kind: anthropic.RequestBlockHistory, ContentHash: h1},
			{Kind: anthropic.RequestBlockTurn, ContentHash: "volatile"},
		},
	}
	path, err := WriteRequestManifest(f.planDir, "job-a", "pt", manifest)
	if err != nil {
		t.Fatal(err)
	}

	if err := verifyInheritedPrefix(path, blocks); err != nil {
		t.Errorf("clean prefix should verify, got: %v", err)
	}

	// Corrupt block 0: the sentinel must fire.
	bad := append(HistoryBlocks{}, blocks...)
	bad[0] = HistoryBlock{Text: "<turn role=\"user\">EDITED</turn>\n"}
	if err := verifyInheritedPrefix(path, bad); !errors.Is(err, errInheritedPrefixMismatch) {
		t.Errorf("edited prefix should return errInheritedPrefixMismatch, got: %v", err)
	}

	// Missing manifest is a distinct (non-sentinel) error.
	if err := verifyInheritedPrefix(filepath.Join(f.planDir, "nope.json"), blocks); err == nil {
		t.Error("missing manifest should error")
	}
}

// TestLocateParentLastManifest prefers the snapshot pointer, then falls back to
// the newest request-manifest by CreatedAt.
func TestLocateParentLastManifest(t *testing.T) {
	f := newLayerFixture(t)
	older := RequestManifest{TurnID: "t1", JobID: "job-a", CreatedAt: time.Unix(1000, 0).UTC()}
	newer := RequestManifest{TurnID: "t2", JobID: "job-a", CreatedAt: time.Unix(2000, 0).UTC()}
	if _, err := WriteRequestManifest(f.planDir, "job-a", "t1", older); err != nil {
		t.Fatal(err)
	}
	newerPath, err := WriteRequestManifest(f.planDir, "job-a", "t2", newer)
	if err != nil {
		t.Fatal(err)
	}

	// No snapshot pointer → newest by CreatedAt.
	if got := locateParentLastManifest(f.planDir, "job-a"); got != newerPath {
		t.Errorf("glob fallback = %q, want newest %q", got, newerPath)
	}

	// Snapshot pointer wins.
	if err := UpdateLayerSnapshotLastManifest(f.planDir, "job-a", "request-manifest-t1.json"); err != nil {
		t.Fatal(err)
	}
	want := RequestManifestPath(f.planDir, "job-a", "t1")
	if got := locateParentLastManifest(f.planDir, "job-a"); got != want {
		t.Errorf("snapshot pointer = %q, want %q", got, want)
	}
}

// TestLineageTemplateMismatchDegrades asserts the spec-27 §3 template guard:
// a parent whose template differs is not inherited (no inherited refs), while
// its dep-transcript layer still rides so the conversation is not lost.
func TestLineageTemplateMismatchDegrades(t *testing.T) {
	f := newLayerFixture(t)
	f.writeRules("src/a.go\n")
	mustRunEngineAs(f, "job-a", "pa", nil, LayerRefreshNone)
	mdPath := writeParentChatMD(f, "job-a")

	parent := parentOf(f, "job-a", mdPath, true)
	parent.Template = "chef"
	parent.TemplateMatch = false // template differs from the child's

	f.writeRules("src/a.go\nsrc/b.go\n")
	mustRunEngineAs(f, "job-b", "t1", []LineageParent{parent}, LayerRefreshNone)
	m := loadManifestFor(f, "job-b")

	for _, l := range m.Layers {
		if l.Source == LayerSourceInherited {
			t.Errorf("template mismatch must not inherit refs, got: %+v", l)
		}
	}
	sawTranscript := false
	for _, l := range m.Layers {
		if l.Source == LayerSourceDepTranscript {
			sawTranscript = true
		}
	}
	if !sawTranscript {
		t.Error("dep-transcript layer must still ride under a template mismatch")
	}
}

// TestInheritedEntryCopiesAnchorExchange: a parent's interleaved layer (an
// anchored diff) is inherited with its AnchorExchange preserved, so the child
// can reproduce it in position.
func TestInheritedEntryCopiesAnchorExchange(t *testing.T) {
	f := newLayerFixture(t)

	// Parent turn 1: base (head).
	f.writeRules("src/a.go\n")
	pp := f.params("pa", LayerRefreshNone)
	pp.JobID = "job-a"
	if _, err := PrepareContextLayers(f.ctx, pp); err != nil {
		t.Fatal(err)
	}
	// Parent turn 2: a widened file frozen with an anchor id.
	f.writeRules("src/a.go\nsrc/b.go\n")
	pp2 := f.params("pb", LayerRefreshNone)
	pp2.JobID = "job-a"
	pp2.AnchorExchange = "e1"
	if _, err := PrepareContextLayers(f.ctx, pp2); err != nil {
		t.Fatal(err)
	}
	parentManifest := loadManifestFor(f, "job-a")
	var anchoredFound bool
	for _, l := range parentManifest.Layers {
		if l.Source == LayerSourceRulesDiff && l.AnchorExchange == "e1" {
			anchoredFound = true
		}
	}
	if !anchoredFound {
		t.Fatalf("parent should have an anchored diff layer: %+v", parentManifest.Layers)
	}
	mdPath := writeParentChatMD(f, "job-a")

	// Child inherits: the anchored diff's AnchorExchange must be copied.
	f.writeRules("src/a.go\nsrc/b.go\nsrc/c.go\n")
	f.writeSource("src/c.go", "package src\n\nvar C = 3\n")
	res := mustRunEngineAs(f, "job-b", "t1", []LineageParent{parentOf(f, "job-a", mdPath, true)}, LayerRefreshNone)
	m := loadManifestFor(f, "job-b")

	var copied bool
	for _, l := range m.Layers {
		if l.Source == LayerSourceInherited && l.AnchorExchange == "e1" {
			copied = true
		}
	}
	if !copied {
		t.Errorf("inherited entry did not preserve AnchorExchange=e1:\n%+v", m.Layers)
	}
	// The result's anchor map exposes it too.
	anchored := false
	for _, p := range res.LayerPaths {
		if res.AnchorsByPath[p] == "e1" {
			anchored = true
		}
	}
	if !anchored {
		t.Errorf("AnchorsByPath missing the inherited anchor: %+v", res.AnchorsByPath)
	}
}
