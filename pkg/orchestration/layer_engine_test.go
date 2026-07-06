package orchestration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	grovelogging "github.com/grovetools/core/logging"
)

// layerFixture is a minimal on-disk world for driving the layer engine
// directly: a context dir with source files, a rules file, and a plan dir
// that receives the .artifacts layer store.
type layerFixture struct {
	t          *testing.T
	contextDir string
	planDir    string
	rulesPath  string
	logBuf     *bytes.Buffer
	ctx        context.Context
}

func newLayerFixture(t *testing.T) *layerFixture {
	t.Helper()
	root := t.TempDir()
	f := &layerFixture{
		t:          t,
		contextDir: filepath.Join(root, "wt"),
		planDir:    filepath.Join(root, "plan"),
		rulesPath:  filepath.Join(root, "plan", "ctx.rules"),
		logBuf:     &bytes.Buffer{},
	}
	f.ctx = grovelogging.WithWriter(context.Background(), f.logBuf)
	for _, dir := range []string{filepath.Join(f.contextDir, "src"), f.planDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.writeSource("src/a.go", "package src\n\nvar A = 1\n")
	f.writeSource("src/b.go", "package src\n\nvar B = 2\n")
	f.writeRules("src/a.go\n")
	return f
}

func (f *layerFixture) writeSource(rel, content string) {
	f.t.Helper()
	path := filepath.Join(f.contextDir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *layerFixture) writeRules(content string) {
	f.t.Helper()
	if err := os.WriteFile(f.rulesPath, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *layerFixture) params(turnID string, refresh LayerRefreshMode) LayerEngineParams {
	return LayerEngineParams{
		PlanDir:         f.planDir,
		JobID:           "job-1",
		ContextDir:      f.contextDir,
		RulesPath:       f.rulesPath,
		TurnID:          turnID,
		StripComments:   false,
		SnapshotEnabled: true,
		Refresh:         refresh,
	}
}

func (f *layerFixture) run(turnID string, refresh LayerRefreshMode) *LayerEngineResult {
	f.t.Helper()
	res, err := PrepareContextLayers(f.ctx, f.params(turnID, refresh))
	if err != nil {
		f.t.Fatalf("PrepareContextLayers(%s): %v", turnID, err)
	}
	return res
}

func (f *layerFixture) layersDir() string {
	return ContextLayersDir(f.planDir, "job-1")
}

func (f *layerFixture) manifest() *LayerManifest {
	f.t.Helper()
	m, err := LoadLayerManifest(f.layersDir())
	if err != nil {
		f.t.Fatal(err)
	}
	if m == nil {
		f.t.Fatal("layers.json missing")
	}
	return m
}

func (f *layerFixture) readArtifact(name string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.layersDir(), name))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

// TestLayerManifestRoundTrip covers layers.json persistence for every field,
// including the P5-reserved inherited shape and removal annotations.
func TestLayerManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	in := &LayerManifest{
		Version: 1,
		Layers: []LayerEntry{
			{
				N: 0, File: "00-base.xml", Source: LayerSourceRulesBase,
				Hash: "aa", Bytes: 10, RulesHash: "rr",
				GitHeads: map[string]string{"/repo": "deadbeef"}, Dirty: true,
				Files:  []LayerFileRecord{{Path: "src/a.go", Hash: "h1", Bytes: 5}},
				TurnID: "t1", CreatedAt: now,
			},
			{
				N: 1, File: "/other/job/.artifacts/x/context-layers/00-base.xml",
				Source: LayerSourceInherited, Hash: "bb", Bytes: 20,
				InheritedFrom: "job-x/00-base.xml", CreatedAt: now,
			},
			{
				N: 2, File: "02-delta-abc1234.xml", Source: LayerSourceGitDelta,
				Hash: "cc", Bytes: 30,
				Files:      []LayerFileRecord{{Path: "src/a.go", Hash: "h2", Bytes: 6}},
				Supersedes: []string{"src/a.go"}, TurnID: "t3", CreatedAt: now,
			},
		},
		Removals: []LayerRemoval{{Path: "src/gone.go", TurnID: "t2", At: now}},
	}
	if err := SaveLayerManifest(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadLayerManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip mismatch:\n in: %+v\nout: %+v", in, out)
	}

	// Inherited entries resolve to their absolute artifact path; own entries
	// resolve inside the layers dir.
	if got := LayerArtifactPath(dir, in.Layers[1]); got != in.Layers[1].File {
		t.Errorf("inherited LayerArtifactPath = %q, want the absolute ref", got)
	}
	if got := LayerArtifactPath(dir, in.Layers[0]); got != filepath.Join(dir, "00-base.xml") {
		t.Errorf("own LayerArtifactPath = %q", got)
	}

	// Missing manifest is (nil, nil), not an error.
	m, err := LoadLayerManifest(filepath.Join(dir, "nope"))
	if err != nil || m != nil {
		t.Errorf("missing manifest = (%v, %v), want (nil, nil)", m, err)
	}
}

// TestWriteLayerArtifact_WriteOnce is the immutability guard at the file
// level: identical rewrite is a no-op, divergent rewrite is a loud error.
func TestWriteLayerArtifact_WriteOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "00-base.xml")
	if err := WriteLayerArtifact(path, []byte("frozen")); err != nil {
		t.Fatal(err)
	}
	if err := WriteLayerArtifact(path, []byte("frozen")); err != nil {
		t.Errorf("identical rewrite must be a no-op, got %v", err)
	}
	err := WriteLayerArtifact(path, []byte("mutated"))
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Errorf("divergent rewrite = %v, want immutability error", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "frozen" {
		t.Errorf("artifact content = %q, want the original frozen bytes", data)
	}
}

// TestPrepareContextLayers_FreezeAndDiff covers turn-1 freeze, the no-change
// turn (no new layer, byte-identical base), and rules widening (a rules-diff
// layer with ONLY the new files) — spec 19 e2e scenarios 1–3.
func TestPrepareContextLayers_FreezeAndDiff(t *testing.T) {
	f := newLayerFixture(t)

	res := f.run("t1", LayerRefreshNone)
	if len(res.LayerPaths) != 1 || filepath.Base(res.LayerPaths[0]) != "00-base.xml" {
		t.Fatalf("turn 1 layer paths = %v, want [00-base.xml]", res.LayerPaths)
	}
	base := f.readArtifact("00-base.xml")
	if !strings.Contains(base, `<layer n="0" source="rules-base"`) {
		t.Errorf("base layer envelope missing: %s", base)
	}
	if !strings.Contains(base, `<file path="src/a.go">`) || !strings.Contains(base, "var A = 1") {
		t.Errorf("base layer missing rules sweep content: %s", base)
	}
	m := f.manifest()
	if len(m.Layers) != 1 || m.Layers[0].Source != LayerSourceRulesBase || m.Layers[0].TurnID != "t1" {
		t.Fatalf("manifest after turn 1 = %+v", m.Layers)
	}
	snap, err := LoadLayerSnapshot(f.planDir, "job-1")
	if err != nil || snap == nil {
		t.Fatalf("snapshot.json = (%v, %v), want written", snap, err)
	}
	if snap.RulesFile != f.rulesPath || snap.RulesHash == "" {
		t.Errorf("snapshot = %+v, want rules file + hash recorded", snap)
	}

	// Turn 2, rules unchanged: no new layer, base byte-identical.
	baseHash := m.Layers[0].Hash
	res = f.run("t2", LayerRefreshNone)
	if len(res.LayerPaths) != 1 {
		t.Fatalf("turn 2 layer paths = %v, want just the base", res.LayerPaths)
	}
	if got := f.manifest().Layers[0].Hash; got != baseHash {
		t.Errorf("base hash changed across an unchanged turn: %s → %s", baseHash, got)
	}

	// Turn 3, widened rules: exactly one appended layer with ONLY src/b.go.
	f.writeRules("src/a.go\nsrc/b.go\n")
	res = f.run("t3", LayerRefreshNone)
	if len(res.LayerPaths) != 2 {
		t.Fatalf("turn 3 layer paths = %v, want base + rules-diff", res.LayerPaths)
	}
	if res.AppendedLayer == "" || !strings.HasPrefix(res.AppendedLayer, "01-add-") {
		t.Errorf("AppendedLayer = %q, want 01-add-*", res.AppendedLayer)
	}
	added := f.readArtifact(res.AppendedLayer)
	if !strings.Contains(added, `<file path="src/b.go">`) {
		t.Errorf("rules-diff layer missing the new file: %s", added)
	}
	if strings.Contains(added, `path="src/a.go"`) {
		t.Errorf("rules-diff layer re-uploaded an already-captured file: %s", added)
	}
	m = f.manifest()
	if m.Layers[1].Source != LayerSourceRulesDiff || m.Layers[1].N != 1 {
		t.Errorf("appended entry = %+v", m.Layers[1])
	}
	if got := m.Layers[0].Hash; got != baseHash {
		t.Errorf("widening mutated the base layer: %s → %s", baseHash, got)
	}
	if res.SourcesByPath[res.LayerPaths[1]] != LayerSourceRulesDiff {
		t.Errorf("SourcesByPath = %v", res.SourcesByPath)
	}
}

// TestPrepareContextLayers_WideningDedup: a widened glob that overlaps
// already-captured files appends only the genuinely-new ones (e2e 4).
func TestPrepareContextLayers_WideningDedup(t *testing.T) {
	f := newLayerFixture(t)
	f.run("t1", LayerRefreshNone)

	f.writeSource("src/c.go", "package src\n\nvar C = 3\n")
	f.writeRules("src/*.go\n") // overlaps src/a.go, adds src/b.go + src/c.go
	res := f.run("t2", LayerRefreshNone)
	if res.AppendedLayer == "" {
		t.Fatal("expected a rules-diff layer for the widened glob")
	}
	added := f.readArtifact(res.AppendedLayer)
	if strings.Contains(added, `path="src/a.go"`) {
		t.Errorf("overlapping file duplicated into the new layer: %s", added)
	}
	for _, want := range []string{`path="src/b.go"`, `path="src/c.go"`} {
		if !strings.Contains(added, want) {
			t.Errorf("rules-diff layer missing %s: %s", want, added)
		}
	}
}

// TestPrepareContextLayers_ImmutabilityGuard: a tampered layer artifact fails
// the next turn loudly instead of silently uploading mutated bytes.
func TestPrepareContextLayers_ImmutabilityGuard(t *testing.T) {
	f := newLayerFixture(t)
	f.run("t1", LayerRefreshNone)

	basePath := filepath.Join(f.layersDir(), "00-base.xml")
	if err := os.WriteFile(basePath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := PrepareContextLayers(f.ctx, f.params("t2", LayerRefreshNone))
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Errorf("tampered base → %v, want immutability error", err)
	}
}

// TestPrepareContextLayers_RemovalAnnotation: narrowing the rules never
// mutates layers — the bytes stay uploaded and layers.json records the
// removal (e2e 8).
func TestPrepareContextLayers_RemovalAnnotation(t *testing.T) {
	f := newLayerFixture(t)
	f.writeRules("src/a.go\nsrc/b.go\n")
	f.run("t1", LayerRefreshNone)
	baseHash := f.manifest().Layers[0].Hash

	f.writeRules("src/a.go\n") // drop src/b.go
	res := f.run("t2", LayerRefreshNone)
	if len(res.LayerPaths) != 1 {
		t.Fatalf("layer paths = %v, want the untouched base only", res.LayerPaths)
	}
	m := f.manifest()
	if m.Layers[0].Hash != baseHash {
		t.Errorf("removal mutated the base layer")
	}
	if len(m.Removals) != 1 || m.Removals[0].Path != "src/b.go" || m.Removals[0].TurnID != "t2" {
		t.Fatalf("removals = %+v, want src/b.go recorded at t2", m.Removals)
	}

	// Turn 3: the removal is not re-recorded.
	f.run("t3", LayerRefreshNone)
	if got := len(f.manifest().Removals); got != 1 {
		t.Errorf("removals re-recorded: %d entries", got)
	}
}

// TestPrepareContextLayers_StalenessAdvisory: a worktree edit does NOT
// refresh frozen layers; the turn logs an advisory instead (e2e 5).
func TestPrepareContextLayers_StalenessAdvisory(t *testing.T) {
	f := newLayerFixture(t)
	f.run("t1", LayerRefreshNone)
	baseHash := f.manifest().Layers[0].Hash

	f.writeSource("src/a.go", "package src\n\nvar A = 999 // changed\n")
	res := f.run("t2", LayerRefreshNone)
	if len(res.LayerPaths) != 1 || res.DeltaLayer != "" {
		t.Fatalf("uninvited delta layer appeared: %+v", res)
	}
	if f.manifest().Layers[0].Hash != baseHash {
		t.Errorf("worktree edit mutated the frozen base")
	}
	if !strings.Contains(f.logBuf.String(), "staleness advisory") {
		t.Errorf("job.log missing staleness advisory:\n%s", f.logBuf.String())
	}
}

// TestPrepareContextLayers_AppendDelta: --append-delta uploads the changed
// files as a supersede-annotated delta layer; prior layers untouched (e2e 6).
func TestPrepareContextLayers_AppendDelta(t *testing.T) {
	f := newLayerFixture(t)
	f.run("t1", LayerRefreshNone)
	baseHash := f.manifest().Layers[0].Hash

	f.writeSource("src/a.go", "package src\n\nvar A = 999 // changed\n")
	res := f.run("t2", LayerRefreshAppendDelta)
	if res.DeltaLayer == "" || !strings.HasPrefix(res.DeltaLayer, "01-delta-") {
		t.Fatalf("DeltaLayer = %q, want 01-delta-*", res.DeltaLayer)
	}
	delta := f.readArtifact(res.DeltaLayer)
	if !strings.Contains(delta, `supersedes="src/a.go"`) || !strings.Contains(delta, "var A = 999") {
		t.Errorf("delta layer missing supersede annotation or new content: %s", delta)
	}
	m := f.manifest()
	if m.Layers[0].Hash != baseHash {
		t.Errorf("delta mutated the base layer")
	}
	if got := m.Layers[1]; got.Source != LayerSourceGitDelta || !reflect.DeepEqual(got.Supersedes, []string{"src/a.go"}) {
		t.Errorf("delta entry = %+v", got)
	}

	// A second --append-delta with no further changes appends nothing.
	res = f.run("t3", LayerRefreshAppendDelta)
	if res.DeltaLayer != "" || len(f.manifest().Layers) != 2 {
		t.Errorf("no-change --append-delta appended a layer: %+v", res)
	}
}

// TestPrepareContextLayers_Rebase: --rebase-context archives (never deletes)
// the lineage and re-freezes a fresh base from the current worktree (e2e 7).
func TestPrepareContextLayers_Rebase(t *testing.T) {
	f := newLayerFixture(t)
	f.run("t1", LayerRefreshNone)
	oldBaseHash := f.manifest().Layers[0].Hash
	f.writeRules("src/a.go\nsrc/b.go\n")
	f.run("t2", LayerRefreshNone) // lineage now has 2 layers

	f.writeSource("src/a.go", "package src\n\nvar A = 42 // rebased view\n")
	res := f.run("t3", LayerRefreshRebase)
	if !res.Rebased {
		t.Error("Rebased = false")
	}
	if len(res.LayerPaths) != 1 || filepath.Base(res.LayerPaths[0]) != "00-base.xml" {
		t.Fatalf("post-rebase layer paths = %v, want a single fresh base", res.LayerPaths)
	}
	m := f.manifest()
	if len(m.Layers) != 1 || m.Layers[0].Hash == oldBaseHash {
		t.Errorf("post-rebase manifest = %+v, want one fresh base with a new hash", m.Layers)
	}
	fresh := f.readArtifact("00-base.xml")
	if !strings.Contains(fresh, "var A = 42") || !strings.Contains(fresh, `path="src/b.go"`) {
		t.Errorf("fresh base does not reflect the current worktree + rules: %s", fresh)
	}

	// Old layers archived, not deleted.
	entries, err := os.ReadDir(f.layersDir())
	if err != nil {
		t.Fatal(err)
	}
	var archiveDir string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "archive-") {
			archiveDir = filepath.Join(f.layersDir(), e.Name())
		}
	}
	if archiveDir == "" {
		t.Fatalf("no archive-<ts> dir after rebase; entries: %v", entries)
	}
	archived, err := os.ReadFile(filepath.Join(archiveDir, "00-base.xml"))
	if err != nil {
		t.Fatalf("archived base missing: %v", err)
	}
	if sha256Hex(archived) != oldBaseHash {
		t.Errorf("archived base bytes differ from the original freeze")
	}
	for _, want := range []string{"layers.json", "snapshot.json"} {
		if _, err := os.Stat(filepath.Join(archiveDir, want)); err != nil {
			t.Errorf("archive missing %s: %v", want, err)
		}
	}
}

// TestPrepareContextLayers_RebaseAdvisory: when superseded bytes exceed the
// threshold the engine logs the rebase suggestion and busts nothing (e2e 9).
func TestPrepareContextLayers_RebaseAdvisory(t *testing.T) {
	f := newLayerFixture(t)
	f.run("t1", LayerRefreshNone)
	f.writeSource("src/a.go", "package src\n\nvar A = 999 // changed\n")
	f.run("t2", LayerRefreshAppendDelta) // supersedes 100% of the base's bytes

	if !strings.Contains(f.logBuf.String(), "rebase advisory") {
		t.Errorf("job.log missing rebase advisory:\n%s", f.logBuf.String())
	}
	if len(f.manifest().Layers) != 2 {
		t.Errorf("advisory must not auto-bust: %+v", f.manifest().Layers)
	}
}

// TestSupersededBytesRatio exercises the pure ratio computation.
func TestSupersededBytesRatio(t *testing.T) {
	m := &LayerManifest{Layers: []LayerEntry{
		{Files: []LayerFileRecord{{Path: "a", Bytes: 60}, {Path: "b", Bytes: 20}}},
		{Files: []LayerFileRecord{{Path: "a", Bytes: 20}}, Supersedes: []string{"a"}},
	}}
	// superseded: the layer-0 copy of "a" (60) out of 100 total.
	if got := SupersededBytesRatio(m, t.TempDir()); got != 0.6 {
		t.Errorf("ratio = %v, want 0.6", got)
	}
	if got := SupersededBytesRatio(&LayerManifest{}, t.TempDir()); got != 0 {
		t.Errorf("empty ratio = %v, want 0", got)
	}
}

// TestUnionFileRecords: later layers win per path.
func TestUnionFileRecords(t *testing.T) {
	m := &LayerManifest{Layers: []LayerEntry{
		{Files: []LayerFileRecord{{Path: "a", Hash: "old"}, {Path: "b", Hash: "b1"}}},
		{Files: []LayerFileRecord{{Path: "a", Hash: "new"}}},
	}}
	union := UnionFileRecords(m, t.TempDir())
	if union["a"].Hash != "new" || union["b"].Hash != "b1" || len(union) != 2 {
		t.Errorf("union = %+v", union)
	}
}

// TestUnionFileRecords_MixedSpellings: a store whose layers recorded the SAME
// files under different spellings — worktree-relative in one layer, another
// checkout's absolute paths in the next (the oracle-plays job-25 poisoning) —
// still folds to one canonical record per file, with the later layer winning.
func TestUnionFileRecords_MixedSpellings(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "wt-a")    // this turn's resolution root
	foreign := filepath.Join(base, "wt-b") // the checkout the poisoned layer resolved
	for _, dir := range []string{root, foreign} {
		if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := &LayerManifest{Layers: []LayerEntry{
		{Files: []LayerFileRecord{{Path: "src/a.go", Hash: "old"}}},
		{Files: []LayerFileRecord{{Path: filepath.Join(foreign, "src", "a.go"), Hash: "new"}}},
	}}
	union := UnionFileRecords(m, root)
	if len(union) != 1 {
		t.Fatalf("union = %+v, want ONE canonical record for the mixed spellings", union)
	}
	if union["src/a.go"].Hash != "new" {
		t.Errorf("union = %+v, want the later (absolute-spelled) record to win under the canonical key", union)
	}
}

// TestCanonicalLayerKey: the canonical file identity is spelling-independent —
// relative, absolute, symlinked, and foreign-checkout spellings of the same
// file collapse to one worktree-relative key; genuinely external files keep a
// stable absolute identity.
func TestCanonicalLayerKey(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "wt")
	if err := os.MkdirAll(filepath.Join(root, "flow", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "flow", "pkg", "a.go")
	if err := os.WriteFile(file, []byte("package pkg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootAlias := filepath.Join(base, "wt-alias")
	if err := os.Symlink(root, rootAlias); err != nil {
		t.Fatal(err)
	}
	// A second checkout of the "same repo" holding the same relative layout.
	foreign := filepath.Join(base, "main-checkout")
	if err := os.MkdirAll(filepath.Join(foreign, "flow", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "flow", "pkg", "a.go"), []byte("package pkg\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const want = "flow/pkg/a.go"
	cases := []struct {
		name, root, path string
	}{
		{"relative spelling", root, "flow/pkg/a.go"},
		{"uncleaned relative spelling", root, "./flow/pkg/a.go"},
		{"absolute under root", root, file},
		{"absolute under symlinked root spelling", rootAlias, filepath.Join(rootAlias, "flow", "pkg", "a.go")},
		{"symlink-alias absolute against real root", root, filepath.Join(rootAlias, "flow", "pkg", "a.go")},
		{"foreign-checkout absolute (job-25 poisoning)", root, filepath.Join(foreign, "flow", "pkg", "a.go")},
	}
	for _, tc := range cases {
		if got := canonicalLayerKey(tc.root, tc.path); got != filepath.FromSlash(want) {
			t.Errorf("%s: canonicalLayerKey(%q, %q) = %q, want %q", tc.name, tc.root, tc.path, got, want)
		}
	}

	// A genuinely external absolute file (no suffix under root) keeps its
	// canonical absolute spelling as a stable identity.
	external := filepath.Join(base, "elsewhere", "doc.md")
	if err := os.MkdirAll(filepath.Dir(external), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := canonicalLayerKey(root, external)
	if !filepath.IsAbs(got) || filepath.Base(got) != "doc.md" {
		t.Errorf("external file key = %q, want a canonical absolute path", got)
	}
	if got != canonicalLayerKey(root, external) {
		t.Errorf("external key not stable")
	}
}

// TestPrepareContextLayers_PoisonedStoreMixedSpellings reproduces the
// oracle-plays job-25 store shape: an existing layer whose files[] records
// carry ANOTHER checkout's absolute paths for the same files the rules
// resolve relatively. The next turn must diff by canonical identity — zero
// false "new" files, no duplicate layer, no removal annotations for the
// absolute spellings — while the already-written layers stay immutable.
func TestPrepareContextLayers_PoisonedStoreMixedSpellings(t *testing.T) {
	f := newLayerFixture(t)
	f.writeRules("src/a.go\nsrc/b.go\n")
	f.run("t1", LayerRefreshNone)

	// A "main checkout" holding the same files at the same relative layout.
	foreign := filepath.Join(filepath.Dir(f.contextDir), "main-checkout")
	if err := os.MkdirAll(filepath.Join(foreign, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.go", "b.go"} {
		src, err := os.ReadFile(filepath.Join(f.contextDir, "src", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(foreign, "src", name), src, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Poison the manifest the way the job-25 turn did: respell layer-0's
	// records as absolute paths under the foreign checkout. Layer ARTIFACTS
	// stay untouched (immutability), only the bookkeeping is poisoned; the
	// root pin is cleared to mimic a store written before the pin existed.
	m := f.manifest()
	for i, rec := range m.Layers[0].Files {
		m.Layers[0].Files[i].Path = filepath.Join(foreign, rec.Path)
	}
	m.Root = ""
	if err := SaveLayerManifest(f.layersDir(), m); err != nil {
		t.Fatal(err)
	}

	res := f.run("t2", LayerRefreshNone)
	if res.AppendedLayer != "" || res.DeltaLayer != "" {
		t.Fatalf("poisoned store duplicated the fileset: %+v", res)
	}
	m2 := f.manifest()
	if len(m2.Layers) != 1 {
		t.Fatalf("layers after t2 = %+v, want just the base (no duplicate layer)", m2.Layers)
	}
	if len(m2.Removals) != 0 {
		t.Errorf("removals = %+v, want none (the absolute spellings are the SAME files)", m2.Removals)
	}
	if m2.Root == "" {
		t.Errorf("root pin not back-filled on the legacy store")
	}
}

// TestPrepareContextLayers_RootPin is the defect-2 guard: a store frozen at
// one resolution root hard-fails a turn resolved from a DIFFERENT checkout
// (never silently extends the lineage), while spelling variants of the same
// root (symlinks) canonicalize equal and proceed without duplicating.
func TestPrepareContextLayers_RootPin(t *testing.T) {
	f := newLayerFixture(t)
	f.run("t1", LayerRefreshNone)
	if m := f.manifest(); m.Root == "" {
		t.Fatal("turn 1 did not record the root pin")
	}
	snap, err := LoadLayerSnapshot(f.planDir, "job-1")
	if err != nil || snap == nil || snap.Root == "" {
		t.Fatalf("snapshot root = (%+v, %v), want the pin mirrored", snap, err)
	}

	// A different checkout with identical content: hard failure.
	other := filepath.Join(filepath.Dir(f.contextDir), "other-checkout")
	if err := os.MkdirAll(filepath.Join(other, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(f.contextDir, "src", "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "src", "a.go"), src, 0o600); err != nil {
		t.Fatal(err)
	}
	p := f.params("t2", LayerRefreshNone)
	p.ContextDir = other
	_, err = PrepareContextLayers(f.ctx, p)
	if err == nil || !strings.Contains(err.Error(), "pinned to resolution root") {
		t.Fatalf("wrong-root turn = %v, want the root-pin hard failure", err)
	}
	if !strings.Contains(err.Error(), "--rebase-context") {
		t.Errorf("root-pin error %v does not name the escape hatch", err)
	}
	if got := len(f.manifest().Layers); got != 1 {
		t.Errorf("wrong-root turn mutated the store: %d layers", got)
	}

	// A symlinked spelling of the SAME root: proceeds, appends nothing.
	alias := filepath.Join(filepath.Dir(f.contextDir), "wt-alias")
	if err := os.Symlink(f.contextDir, alias); err != nil {
		t.Fatal(err)
	}
	p = f.params("t3", LayerRefreshNone)
	p.ContextDir = alias
	res, err := PrepareContextLayers(f.ctx, p)
	if err != nil {
		t.Fatalf("same-root alias turn failed: %v", err)
	}
	if res.AppendedLayer != "" || len(f.manifest().Layers) != 1 {
		t.Errorf("same-root alias turn duplicated the fileset: %+v", res)
	}

	// --rebase-context is the sanctioned re-root: succeeds from the other
	// checkout and re-pins.
	p = f.params("t4", LayerRefreshRebase)
	p.ContextDir = other
	if _, err := PrepareContextLayers(f.ctx, p); err != nil {
		t.Fatalf("rebase from a new root must re-pin, got %v", err)
	}
	if got := f.manifest().Root; got != canonicalLayerRoot(other) {
		t.Errorf("post-rebase root = %q, want %q", got, canonicalLayerRoot(other))
	}
}

// TestPrepareContextLayers_RemovalKeyedCanonically: narrowing the rules on a
// store that captured the dropped file under an absolute spelling records the
// removal under the canonical (worktree-relative) key, once.
func TestPrepareContextLayers_RemovalKeyedCanonically(t *testing.T) {
	f := newLayerFixture(t)
	f.writeRules("src/a.go\nsrc/b.go\n")
	f.run("t1", LayerRefreshNone)

	// Respell src/b.go's record absolutely (under the real context dir, so
	// no foreign checkout is needed for canonicalization).
	m := f.manifest()
	for i, rec := range m.Layers[0].Files {
		if rec.Path == "src/b.go" {
			m.Layers[0].Files[i].Path = filepath.Join(f.contextDir, "src", "b.go")
		}
	}
	if err := SaveLayerManifest(f.layersDir(), m); err != nil {
		t.Fatal(err)
	}

	f.writeRules("src/a.go\n") // drop src/b.go
	f.run("t2", LayerRefreshNone)
	m2 := f.manifest()
	if len(m2.Removals) != 1 || m2.Removals[0].Path != "src/b.go" {
		t.Fatalf("removals = %+v, want exactly src/b.go under its canonical key", m2.Removals)
	}
	// And it is not re-recorded on the next turn.
	f.run("t3", LayerRefreshNone)
	if got := len(f.manifest().Removals); got != 1 {
		t.Errorf("removal re-recorded: %d entries", got)
	}
}

// TestPrepareContextLayers_SnapshotOptOut: context_snapshot: false regenerates
// the store every turn — base hash follows the worktree, no immutability
// violation is reported (e2e 19).
func TestPrepareContextLayers_SnapshotOptOut(t *testing.T) {
	f := newLayerFixture(t)
	p := f.params("t1", LayerRefreshNone)
	p.SnapshotEnabled = false
	if _, err := PrepareContextLayers(f.ctx, p); err != nil {
		t.Fatal(err)
	}
	firstHash := f.manifest().Layers[0].Hash

	f.writeSource("src/a.go", "package src\n\nvar A = 7 // moving worktree\n")
	p = f.params("t2", LayerRefreshNone)
	p.SnapshotEnabled = false
	res, err := PrepareContextLayers(f.ctx, p)
	if err != nil {
		t.Fatalf("opt-out regeneration must not trip the immutability guard: %v", err)
	}
	m := f.manifest()
	if len(m.Layers) != 1 || m.Layers[0].Hash == firstHash {
		t.Errorf("opt-out turn 2 = %+v, want a single regenerated base with a new hash", m.Layers)
	}
	if len(res.LayerPaths) != 1 {
		t.Errorf("opt-out layer paths = %v", res.LayerPaths)
	}
	if !strings.Contains(f.readArtifact("00-base.xml"), "var A = 7") {
		t.Errorf("opt-out base does not track the worktree")
	}
}

// TestPrepareContextLayers_UnreadableFile: a rules-matched file that cannot
// be read is a hard failure (e2e 22), not a silently thinner context.
func TestPrepareContextLayers_UnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 000 is still readable")
	}
	f := newLayerFixture(t)
	if err := os.Chmod(filepath.Join(f.contextDir, "src", "a.go"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(f.contextDir, "src", "a.go"), 0o600) })
	_, err := PrepareContextLayers(f.ctx, f.params("t1", LayerRefreshNone))
	if err == nil || !strings.Contains(err.Error(), "src/a.go") {
		t.Errorf("unreadable rules file → %v, want hard failure naming the file", err)
	}
}

// TestStampTrailingChatDirective covers the refresh-verb stamping used by the
// plan-run flags and chat reopen, and that ParseChatFile surfaces the stamped
// verbs on the turn directive.
func TestStampTrailingChatDirective(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.md")
	content := `---
id: c1
type: chat
---

First question.

<!-- grove: {"id": "aaa111"} -->
## LLM Response (2026-07-05 09:00:00)

Answer.

<!-- grove: {"template": "chat"} -->

Second question after landing changes.
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	stamped, err := StampTrailingChatDirective(path, map[string]interface{}{"append_delta": true})
	if err != nil || !stamped {
		t.Fatalf("stamp = (%v, %v), want (true, nil)", stamped, err)
	}
	after, _ := os.ReadFile(path)
	turns, err := ParseChatFile(after)
	if err != nil {
		t.Fatal(err)
	}
	last := turns[len(turns)-1]
	if last.Speaker != "user" || last.Directive == nil {
		t.Fatalf("last turn = %+v, want a user turn with a directive", last)
	}
	if !last.Directive.AppendDelta || last.Directive.RebaseContext {
		t.Errorf("directive = %+v, want append_delta only", last.Directive)
	}
	if last.Directive.Template != "chat" {
		t.Errorf("stamping dropped the template: %+v", last.Directive)
	}
	// Only the trailing marker was touched.
	if !bytes.Contains(after, []byte(`<!-- grove: {"id": "aaa111"} -->`)) {
		t.Errorf("stamping rewrote a non-trailing marker:\n%s", after)
	}

	// A file with no marker at all: nothing to stamp, no error.
	bare := filepath.Join(dir, "bare.md")
	if err := os.WriteFile(bare, []byte("---\nid: c2\ntype: chat\n---\n\nJust a question.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamped, err = StampTrailingChatDirective(bare, map[string]interface{}{"rebase_context": true})
	if err != nil || stamped {
		t.Errorf("bare stamp = (%v, %v), want (false, nil)", stamped, err)
	}
}

// TestValidateFrozenContextCoverage covers the fire-time empty-freeze gate
// (oracle-plays J1): P1 (declared rules resolve zero files) and P2 (a resolved
// file is captured in no frozen layer) trip; legitimate empty/covered/re-frozen
// turns do not. Each row arranges an on-disk layer store via the engine, then
// calls the gate directly.
func TestValidateFrozenContextCoverage(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, f *layerFixture) *Job
		wantErr string // "" = must pass (no trip)
	}{
		{
			name: "P1 declared rules resolve zero files",
			setup: func(t *testing.T, f *layerFixture) *Job {
				f.writeRules("no/such/*.go\n")
				f.run("t1", LayerRefreshNone) // freezes an empty base
				return &Job{ID: "job-1", RulesFile: "ctx.rules", Filename: "01-chat.md"}
			},
			wantErr: "0 files",
		},
		{
			name: "P2 resolved file captured in no layer",
			setup: func(t *testing.T, f *layerFixture) *Job {
				f.run("t1", LayerRefreshNone) // freezes src/a.go
				m := f.manifest()
				m.Layers[0].Files = nil // the file still resolves but nothing captured it
				if err := SaveLayerManifest(f.layersDir(), m); err != nil {
					t.Fatal(err)
				}
				return &Job{ID: "job-1", RulesFile: "ctx.rules", Filename: "01-chat.md"}
			},
			wantErr: "freeze gap",
		},
		{
			name: "no trip on a normal freeze",
			setup: func(t *testing.T, f *layerFixture) *Job {
				f.run("t1", LayerRefreshNone)
				return &Job{ID: "job-1", RulesFile: "ctx.rules"}
			},
		},
		{
			name: "no trip when the file rides only an inherited layer",
			setup: func(t *testing.T, f *layerFixture) *Job {
				f.run("t1", LayerRefreshNone)
				m := f.manifest()
				m.Layers[0].Source = LayerSourceInherited // union still carries the file
				if err := SaveLayerManifest(f.layersDir(), m); err != nil {
					t.Fatal(err)
				}
				return &Job{ID: "job-1", RulesFile: "ctx.rules"}
			},
		},
		{
			name: "no trip on an empty widen turn",
			setup: func(t *testing.T, f *layerFixture) *Job {
				f.run("t1", LayerRefreshNone)
				f.run("t2", LayerRefreshNone) // rules unchanged: no new layer, all files covered
				return &Job{ID: "job-1", RulesFile: "ctx.rules"}
			},
		},
		{
			name: "no trip when a removed file is no longer resolved",
			setup: func(t *testing.T, f *layerFixture) *Job {
				f.writeRules("src/a.go\nsrc/b.go\n")
				f.run("t1", LayerRefreshNone)
				f.writeRules("src/a.go\n") // drops b.go → removal annotation; a.go still covered
				f.run("t2", LayerRefreshNone)
				return &Job{ID: "job-1", RulesFile: "ctx.rules"}
			},
		},
		{
			name: "no trip with context_snapshot false re-freeze",
			setup: func(t *testing.T, f *layerFixture) *Job {
				p := f.params("t1", LayerRefreshNone)
				p.SnapshotEnabled = false
				if _, err := PrepareContextLayers(f.ctx, p); err != nil {
					t.Fatal(err)
				}
				return &Job{ID: "job-1", RulesFile: "ctx.rules"}
			},
		},
		{
			name: "no trip on empty resolution when no rules are declared",
			setup: func(t *testing.T, f *layerFixture) *Job {
				f.writeRules("no/such/*.go\n")
				f.run("t1", LayerRefreshNone)
				return &Job{ID: "job-1"} // RulesFile == "": P1 does not apply
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newLayerFixture(t)
			job := tt.setup(t, f)
			err := validateFrozenContextCoverage(f.ctx, f.planDir, job, f.contextDir, f.rulesPath)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateFrozenContextCoverage() = %v, want no trip", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateFrozenContextCoverage() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("gate error %q does not contain %q", err.Error(), tt.wantErr)
			}
			if !strings.Contains(err.Error(), "--rebase-context") {
				t.Errorf("gate error %q does not name the recovery verb", err.Error())
			}
		})
	}
}
