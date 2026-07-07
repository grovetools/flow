package orchestration

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCandidateManifest fabricates a completed chat sibling's layers.json on
// tmpfs: one base layer capturing the given file records. Returns the *Job to
// place in plan.Jobs.
func writeCandidateManifest(t *testing.T, planDir, jobID, filename string, strip bool, records []LayerFileRecord, removals []LayerRemoval) *Job {
	t.Helper()
	m := &LayerManifest{
		Version: layerManifestVersion,
		Layers: []LayerEntry{{
			N:      0,
			File:   "00-base.xml",
			Source: LayerSourceRulesBase,
			Hash:   "layerhash-" + jobID,
			Bytes:  1,
			Files:  records,
		}},
		Removals: removals,
	}
	if err := SaveLayerManifest(ContextLayersDir(planDir, jobID), m); err != nil {
		t.Fatalf("SaveLayerManifest(%s): %v", jobID, err)
	}
	stripPtr := &strip
	return &Job{
		ID:            jobID,
		Title:         jobID,
		Filename:      filename,
		Type:          JobTypeChat,
		Status:        JobStatusCompleted,
		StripComments: stripPtr,
		EndTime:       time.Now(),
	}
}

// rec is a terse LayerFileRecord constructor.
func rec(path, hash string, bytes int64) LayerFileRecord {
	return LayerFileRecord{Path: path, Hash: hash, Bytes: bytes}
}

func TestBestLineageOverlap_PathProxyGatingAndBestCandidate(t *testing.T) {
	planDir := t.TempDir()
	// Child fileset: 5 files, ~50k bytes each in the parents' records.
	fileset := []string{"a.go", "b.go", "c.go", "d.go", "e.go"}

	// Weak candidate: only 2/5 files overlap (< 60%) — must NOT pass.
	weak := writeCandidateManifest(t, planDir, "weak", "01-weak.md", true, []LayerFileRecord{
		rec("a.go", "h-a", 60000),
		rec("b.go", "h-b", 60000),
	}, nil)
	// Strong candidate: 4/5 files, big bytes — passes both ratio and floor.
	strong := writeCandidateManifest(t, planDir, "strong", "02-strong.md", true, []LayerFileRecord{
		rec("a.go", "h-a", 50000),
		rec("b.go", "h-b", 50000),
		rec("c.go", "h-c", 50000),
		rec("d.go", "h-d", 50000),
	}, nil)

	candidates := []*Job{weak, strong}
	got := bestLineageOverlap(candidates, &Plan{Directory: planDir}, planDir, "model-x", true,
		fileset, nil, len(fileset), 250000,
		func(dep *Job) string { return "model-x" })
	if got == nil {
		t.Fatal("expected an advice, got nil")
	}
	if got.ParentJobID != "strong" {
		t.Fatalf("expected best candidate 'strong' (highest matched bytes), got %q", got.ParentJobID)
	}
	if got.MatchedFiles != 4 {
		t.Fatalf("expected 4 matched files, got %d", got.MatchedFiles)
	}
	if got.MatchedBytes != 200000 {
		t.Fatalf("expected 200000 matched bytes, got %d", got.MatchedBytes)
	}
	if got.Exact {
		t.Fatal("path proxy must report Exact=false")
	}
}

func TestBestLineageOverlap_WarmTokenFloorSuppresses(t *testing.T) {
	planDir := t.TempDir()
	fileset := []string{"a.go", "b.go", "c.go"}
	// 3/3 files (100% ratio) but tiny bytes → warm tokens = 900/4 = 225 < 10k.
	small := writeCandidateManifest(t, planDir, "small", "01-small.md", true, []LayerFileRecord{
		rec("a.go", "h-a", 300),
		rec("b.go", "h-b", 300),
		rec("c.go", "h-c", 300),
	}, nil)
	got := bestLineageOverlap([]*Job{small}, &Plan{Directory: planDir}, planDir, "m", true,
		fileset, nil, len(fileset), 900,
		func(dep *Job) string { return "m" })
	if got != nil {
		t.Fatalf("expected nil advice below the warm-token floor, got %+v", got)
	}
}

func TestBestLineageOverlap_RemovalsNotSubtracted(t *testing.T) {
	planDir := t.TempDir()
	fileset := []string{"a.go", "b.go"}
	// Candidate froze a.go + b.go, but b.go was later REMOVED from its rules.
	// Inheritance still rides the whole union, so the overlap must count b.go.
	cand := writeCandidateManifest(t, planDir, "cand", "01-cand.md", true, []LayerFileRecord{
		rec("a.go", "h-a", 30000),
		rec("b.go", "h-b", 30000),
	}, []LayerRemoval{{Path: "b.go", At: time.Now()}})
	got := bestLineageOverlap([]*Job{cand}, &Plan{Directory: planDir}, planDir, "m", true,
		fileset, nil, len(fileset), 60000,
		func(dep *Job) string { return "m" })
	if got == nil {
		t.Fatal("expected advice; removals must not shrink the comparison set")
	}
	if got.MatchedFiles != 2 || got.MatchedBytes != 60000 {
		t.Fatalf("removed-but-frozen file must still count: got %d files / %d bytes", got.MatchedFiles, got.MatchedBytes)
	}
}

func TestBestLineageOverlap_HashExactVsPathProxy(t *testing.T) {
	planDir := t.TempDir()
	fileset := []string{"a.go", "b.go", "c.go"}
	// Parent froze a.go/b.go/c.go with specific content hashes.
	cand := writeCandidateManifest(t, planDir, "cand", "01-cand.md", true, []LayerFileRecord{
		rec("a.go", "hash-a", 40000),
		rec("b.go", "hash-b", 40000),
		rec("c.go", "hash-c-OLD", 40000),
	}, nil)
	// Child's rendered hashes: a.go & b.go match, c.go has drifted.
	hashes := map[string]string{"a.go": "hash-a", "b.go": "hash-b", "c.go": "hash-c-NEW"}

	exact := bestLineageOverlap([]*Job{cand}, &Plan{Directory: planDir}, planDir, "m", true,
		fileset, hashes, len(fileset), 120000,
		func(dep *Job) string { return "m" })
	if exact == nil {
		t.Fatal("expected hash-exact advice")
	}
	if !exact.Exact {
		t.Fatal("expected Exact=true when strip settings match and hashes provided")
	}
	// Only a.go & b.go hash-match → 2 files, 80000 bytes.
	if exact.MatchedFiles != 2 || exact.MatchedBytes != 80000 {
		t.Fatalf("hash overlap should match only unchanged files: got %d/%d", exact.MatchedFiles, exact.MatchedBytes)
	}

	// Same inputs but nil hashes (add-time) → path proxy matches all 3 by path.
	proxy := bestLineageOverlap([]*Job{cand}, &Plan{Directory: planDir}, planDir, "m", true,
		fileset, nil, len(fileset), 120000,
		func(dep *Job) string { return "m" })
	if proxy == nil || proxy.Exact {
		t.Fatalf("expected path-proxy advice with Exact=false, got %+v", proxy)
	}
	if proxy.MatchedFiles != 3 {
		t.Fatalf("path proxy should match all 3 paths, got %d", proxy.MatchedFiles)
	}
}

func TestBestLineageOverlap_StripMismatchDegradesToProxy(t *testing.T) {
	planDir := t.TempDir()
	fileset := []string{"a.go", "b.go", "c.go"}
	// Parent froze under strip=false; child renders under strip=true. Even
	// though we pass hashes, the strip-setting mismatch must degrade to the path
	// proxy (the parent's bytes were stripped in a different regime).
	cand := writeCandidateManifest(t, planDir, "cand", "01-cand.md", false, []LayerFileRecord{
		rec("a.go", "hash-a", 40000),
		rec("b.go", "hash-b", 40000),
		rec("c.go", "hash-c", 40000),
	}, nil)
	// Child hashes deliberately DON'T match the parent's (different strip),
	// so a hash intersection would find nothing — the proxy still finds 3.
	hashes := map[string]string{"a.go": "x", "b.go": "y", "c.go": "z"}
	got := bestLineageOverlap([]*Job{cand}, &Plan{Directory: planDir}, planDir, "m", true,
		fileset, hashes, len(fileset), 120000,
		func(dep *Job) string { return "m" })
	if got == nil {
		t.Fatal("expected proxy advice under strip mismatch")
	}
	if got.Exact {
		t.Fatal("strip-setting mismatch must degrade to Exact=false")
	}
	if got.MatchedFiles != 3 {
		t.Fatalf("degraded proxy should match all 3 paths, got %d", got.MatchedFiles)
	}
}

func TestBestLineageOverlap_ModelMismatchMessageVariant(t *testing.T) {
	planDir := t.TempDir()
	fileset := []string{"a.go", "b.go"}
	cand := writeCandidateManifest(t, planDir, "cand", "01-cand.md", true, []LayerFileRecord{
		rec("a.go", "h-a", 40000),
		rec("b.go", "h-b", 40000),
	}, nil)

	match := bestLineageOverlap([]*Job{cand}, &Plan{Directory: planDir}, planDir, "claude-opus", true,
		fileset, nil, len(fileset), 80000,
		func(dep *Job) string { return "claude-opus" })
	if match == nil || !match.ModelMatch {
		t.Fatalf("expected a model-match advice, got %+v", match)
	}
	if msg := match.FormatAdvice(); !strings.Contains(msg, "would inherit") || strings.Contains(msg, "align `model:`") {
		t.Fatalf("model-match message should offer inheritance without the align clause: %q", msg)
	}

	mismatch := bestLineageOverlap([]*Job{cand}, &Plan{Directory: planDir}, planDir, "claude-sonnet", true,
		fileset, nil, len(fileset), 80000,
		func(dep *Job) string { return "claude-opus" })
	if mismatch == nil || mismatch.ModelMatch {
		t.Fatalf("expected a model-mismatch advice, got %+v", mismatch)
	}
	msg := mismatch.FormatAdvice()
	if !strings.Contains(msg, "align `model:`") || !strings.Contains(msg, "claude-opus") || !strings.Contains(msg, "claude-sonnet") {
		t.Fatalf("mismatch message must name both models and the align clause: %q", msg)
	}
}

func TestLineageOverlapCandidates_ExcludesDepsAndSelf(t *testing.T) {
	planDir := t.TempDir()
	parentA := &Job{ID: "a", Filename: "01-a.md", Type: JobTypeChat, Status: JobStatusCompleted}
	parentB := &Job{ID: "b", Filename: "02-b.md", Type: JobTypeChat, Status: JobStatusCompleted}
	running := &Job{ID: "r", Filename: "03-r.md", Type: JobTypeChat, Status: JobStatusRunning}
	oneshot := &Job{ID: "o", Filename: "04-o.md", Type: JobTypeOneshot, Status: JobStatusCompleted}
	agentChat := &Job{ID: "g", Filename: "05-g.md", Type: JobTypeChat, Status: JobStatusCompleted, Responder: "agent"}
	self := &Job{ID: "self", Filename: "06-self.md", Type: JobTypeChat, Status: JobStatusPendingUser}

	plan := &Plan{Directory: planDir, Jobs: []*Job{parentA, parentB, running, oneshot, agentChat, self}}
	// self already depends on parentA (by filename, the add-time form).
	self.DependsOn = []string{"01-a.md"}

	got := lineageOverlapCandidates(plan, self)
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	// Only parentB survives: A is a dep, running not completed, oneshot wrong
	// type, agentChat is agent-responded, self is self.
	if len(ids) != 1 || ids[0] != "b" {
		t.Fatalf("expected only candidate 'b', got %v", ids)
	}
}

func TestDependencyJobIDs_ResolvedAndFilename(t *testing.T) {
	plan := &Plan{Jobs: []*Job{
		{ID: "x", Filename: "01-x.md"},
		{ID: "y", Filename: "02-y.md"},
	}}
	// Resolved dep (fire time) + filename dep (add time) both excluded.
	job := &Job{
		DependsOn:    []string{"02-y.md"},
		Dependencies: []*Job{{ID: "x"}},
	}
	ids := dependencyJobIDs(plan, job)
	if !ids["x"] || !ids["y"] {
		t.Fatalf("expected both x and y excluded, got %v", ids)
	}
}

func TestAddTimeEffectiveModel_DropsOrchestrationRung(t *testing.T) {
	// plan.Orchestration is nil at add time; a plan config model wins; a job
	// model wins over that. No Orchestration rung is consulted.
	plan := &Plan{Config: &PlanConfig{Model: "claude-config"}}
	if got := addTimeEffectiveModel(&Job{}, plan); got != resolveModelAlias("claude-config") {
		t.Fatalf("expected plan config model, got %q", got)
	}
	if got := addTimeEffectiveModel(&Job{Model: "claude-job"}, plan); got != resolveModelAlias("claude-job") {
		t.Fatalf("expected job model to win, got %q", got)
	}
	// No config, no job model → provider default (never nil/empty).
	if got := addTimeEffectiveModel(&Job{}, &Plan{}); got == "" {
		t.Fatal("expected a default model, got empty")
	}
}

func TestPassesThreshold_RatioUnits(t *testing.T) {
	// Exact: byte ratio. 30k/50k = 0.60 exactly → passes; warm 30k/4=7500 <10k
	// would fail the floor, so bump bytes.
	exact := &LineageOverlapAdvice{Exact: true, MatchedBytes: 60000, FilesetBytes: 100000, MatchedFiles: 3, FilesetFiles: 5}
	if !exact.passesThreshold() {
		t.Fatal("exact 60% byte ratio + warm floor should pass")
	}
	// Exact just under ratio.
	under := &LineageOverlapAdvice{Exact: true, MatchedBytes: 59000, FilesetBytes: 100000}
	if under.passesThreshold() {
		t.Fatal("exact 59% byte ratio should fail")
	}
	// Proxy: file-count ratio, byte ratio irrelevant. 3/5=0.6 passes with warm.
	proxy := &LineageOverlapAdvice{Exact: false, MatchedFiles: 3, FilesetFiles: 5, MatchedBytes: 60000, FilesetBytes: 1}
	if !proxy.passesThreshold() {
		t.Fatal("proxy 3/5 file ratio + warm floor should pass regardless of byte ratio")
	}
	proxyUnder := &LineageOverlapAdvice{Exact: false, MatchedFiles: 2, FilesetFiles: 5, MatchedBytes: 60000}
	if proxyUnder.passesThreshold() {
		t.Fatal("proxy 2/5 file ratio should fail")
	}
}

func TestFormatAdvice_PathVsContent(t *testing.T) {
	base := LineageOverlapAdvice{
		ParentFilename: "27-spec.md", ParentJobID: "spec27",
		MatchedFiles: 8, FilesetFiles: 10, MatchedBytes: 164000,
		ModelMatch: true, ChildModel: "m", ParentModel: "m",
	}
	pathMsg := base.FormatAdvice()
	if !strings.Contains(pathMsg, "path overlap") || !strings.Contains(pathMsg, "~41k tokens") {
		t.Fatalf("path advice should say 'path overlap' and ~41k tokens: %q", pathMsg)
	}
	if !strings.Contains(pathMsg, "context layers alone") {
		t.Fatalf("estimate must be qualified 'context layers alone': %q", pathMsg)
	}
	exact := base
	exact.Exact = true
	if !strings.Contains(exact.FormatAdvice(), "content-hash match") {
		t.Fatalf("exact advice should say 'content-hash match': %q", exact.FormatAdvice())
	}
}

// TestResolveFilesetPath covers the key→absolute join both ways.
func TestResolveFilesetPath(t *testing.T) {
	if got := resolveFilesetPath("/root", "pkg/a.go"); got != filepath.Join("/root", "pkg/a.go") {
		t.Fatalf("relative key: got %q", got)
	}
	if got := resolveFilesetPath("/root", "/abs/a.go"); got != "/abs/a.go" {
		t.Fatalf("absolute key must pass through: got %q", got)
	}
}
