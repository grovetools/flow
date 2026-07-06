package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- unit-level fixtures ------------------------------------------------

// runEngineAs runs the layer engine for an arbitrary job id + lineage on the
// shared layerFixture world.
func runEngineAs(f *layerFixture, jobID, turnID string, lineage []LineageParent, refresh LayerRefreshMode) (*LayerEngineResult, error) {
	p := f.params(turnID, refresh)
	p.JobID = jobID
	p.Lineage = lineage
	return PrepareContextLayers(f.ctx, p)
}

// mustRunEngineAs is runEngineAs with a fatal on error.
func mustRunEngineAs(f *layerFixture, jobID, turnID string, lineage []LineageParent, refresh LayerRefreshMode) *LayerEngineResult {
	f.t.Helper()
	res, err := runEngineAs(f, jobID, turnID, lineage, refresh)
	if err != nil {
		f.t.Fatalf("PrepareContextLayers(%s/%s): %v", jobID, turnID, err)
	}
	return res
}

// writeParentChatMD writes a completed parent chat .md into the plan dir and
// returns its path.
func writeParentChatMD(f *layerFixture, jobID string) string {
	f.t.Helper()
	content := `---
id: ` + jobID + `
title: parent chat
status: completed
type: chat
---

What is the plan?

<!-- grove: {"id": "aaa111"} -->
## LLM Response (2026-07-05 09:00:00)

The plan is layers.

<!-- grove: {"template": "chat"} -->
`
	path := filepath.Join(f.planDir, jobID+".md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return path
}

// parentOf builds the LineageParent record for a parent whose store lives in
// the fixture plan dir.
func parentOf(f *layerFixture, jobID, mdPath string, modelMatch bool) LineageParent {
	return LineageParent{
		JobID:      jobID,
		Title:      "parent chat",
		FilePath:   mdPath,
		PlanDir:    f.planDir,
		Model:      "claude-parent-model",
		ModelMatch: modelMatch,
	}
}

func loadManifestFor(f *layerFixture, jobID string) *LayerManifest {
	f.t.Helper()
	m, err := LoadLayerManifest(ContextLayersDir(f.planDir, jobID))
	if err != nil {
		f.t.Fatal(err)
	}
	if m == nil {
		f.t.Fatalf("layers.json missing for %s", jobID)
	}
	return m
}

// --- unit tests ----------------------------------------------------------

// TestLineage_InheritTranscriptOwnBase is the e2e-10/11 shape at unit level:
// a child chat's fresh store starts with refs to the parent's artifacts (same
// hashes), then the parent's transcript as a layer document, then an own base
// that excludes files the inherited union already carries (dedup against the
// union). A follow-up turn audits inherited refs cleanly and records no
// spurious removals for inherited-only files.
func TestLineage_InheritTranscriptOwnBase(t *testing.T) {
	f := newLayerFixture(t)

	// Parent job-a freezes src/a.go.
	f.writeRules("src/a.go\n")
	mustRunEngineAs(f, "job-a", "pa", nil, LayerRefreshNone)
	parentManifest := loadManifestFor(f, "job-a")
	parentBaseHash := parentManifest.Layers[0].Hash
	parentBasePath := filepath.Join(ContextLayersDir(f.planDir, "job-a"), "00-base.xml")
	mdPath := writeParentChatMD(f, "job-a")

	// Child job-b: rules overlap the parent (src/a.go) and add src/b.go.
	f.writeRules("src/a.go\nsrc/b.go\n")
	res := mustRunEngineAs(f, "job-b", "t1", []LineageParent{parentOf(f, "job-a", mdPath, true)}, LayerRefreshNone)

	m := loadManifestFor(f, "job-b")
	if len(m.Layers) != 3 {
		t.Fatalf("child layers = %+v, want [inherited, dep-transcript, rules-base]", m.Layers)
	}

	inh := m.Layers[0]
	if inh.Source != LayerSourceInherited || inh.File != parentBasePath || inh.Hash != parentBaseHash {
		t.Errorf("inherited entry = %+v, want a ref to %s with hash %s", inh, parentBasePath, parentBaseHash)
	}
	if inh.InheritedFrom != "job-a/00-base.xml" {
		t.Errorf("inherited_from = %q, want job-a/00-base.xml", inh.InheritedFrom)
	}

	tr := m.Layers[1]
	if tr.Source != LayerSourceDepTranscript || tr.InheritedFrom != "job-a" || tr.File != "01-transcript-job-a.xml" {
		t.Errorf("transcript entry = %+v", tr)
	}
	trData, err := os.ReadFile(filepath.Join(ContextLayersDir(f.planDir, "job-b"), tr.File))
	if err != nil {
		t.Fatal(err)
	}
	transcript := string(trData)
	for _, want := range []string{`source="dep-transcript"`, `job="job-a"`, "What is the plan?", "The plan is layers", `role="assistant"`, `timestamp="2026-07-05 09:00:00"`} {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript layer missing %q:\n%s", want, transcript)
		}
	}
	if strings.Contains(transcript, "<!-- grove:") || strings.Contains(transcript, "awaiting_response") {
		t.Errorf("transcript layer must be cleaned and non-volatile:\n%s", transcript)
	}

	// Own base: the NEXT layer after the lineage, containing ONLY files the
	// inherited union does not already carry.
	base := m.Layers[2]
	if base.Source != LayerSourceRulesBase || base.File != "02-base.xml" {
		t.Errorf("own base entry = %+v", base)
	}
	baseBytes, err := os.ReadFile(filepath.Join(ContextLayersDir(f.planDir, "job-b"), base.File))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(baseBytes), `path="src/a.go"`) {
		t.Errorf("own base duplicated a file the inherited union already carries:\n%s", baseBytes)
	}
	if !strings.Contains(string(baseBytes), `path="src/b.go"`) {
		t.Errorf("own base missing the genuinely-new file:\n%s", baseBytes)
	}

	// Upload order mirrors the manifest.
	if len(res.LayerPaths) != 3 || res.LayerPaths[0] != parentBasePath {
		t.Errorf("LayerPaths = %v, want the inherited ref first", res.LayerPaths)
	}
	if res.SourcesByPath[res.LayerPaths[0]] != LayerSourceInherited ||
		res.SourcesByPath[res.LayerPaths[1]] != LayerSourceDepTranscript ||
		res.SourcesByPath[res.LayerPaths[2]] != LayerSourceRulesBase {
		t.Errorf("SourcesByPath = %v", res.SourcesByPath)
	}

	// Turn 2: lineage already integrated — idempotent, inherited refs audit
	// cleanly, and inherited-only files are NOT annotated as rules removals.
	res2 := mustRunEngineAs(f, "job-b", "t2", []LineageParent{parentOf(f, "job-a", mdPath, true)}, LayerRefreshNone)
	m2 := loadManifestFor(f, "job-b")
	if len(m2.Layers) != 3 || len(res2.LayerPaths) != 3 {
		t.Errorf("turn 2 re-integrated the lineage: %+v", m2.Layers)
	}
	if len(m2.Removals) != 0 {
		t.Errorf("spurious removal annotations for inherited files: %+v", m2.Removals)
	}
}

// TestLineage_GitDeltaOnLineage is e2e 12 at unit level: a file captured by
// the inherited lineage changes between the parent completing and the child's
// turn 1 → the child gets an auto delta layer listing exactly the changed
// files, supersede-annotated, before its own rules layers.
func TestLineage_GitDeltaOnLineage(t *testing.T) {
	f := newLayerFixture(t)

	f.writeRules("src/a.go\n")
	mustRunEngineAs(f, "job-a", "pa", nil, LayerRefreshNone)
	mdPath := writeParentChatMD(f, "job-a")

	// The impl lands: an inherited file changes on disk.
	f.writeSource("src/a.go", "package src\n\nvar A = 999 // landed after parent completed\n")

	// Child rules are disjoint from the parent's.
	f.writeRules("src/b.go\n")
	mustRunEngineAs(f, "job-c", "t1", []LineageParent{parentOf(f, "job-a", mdPath, true)}, LayerRefreshNone)

	m := loadManifestFor(f, "job-c")
	if len(m.Layers) != 4 {
		t.Fatalf("child layers = %+v, want [inherited, transcript, git-delta, rules-base]", m.Layers)
	}
	delta := m.Layers[2]
	if delta.Source != LayerSourceGitDelta || !strings.HasPrefix(delta.File, "02-delta-") {
		t.Fatalf("delta entry = %+v", delta)
	}
	if len(delta.Supersedes) != 1 || delta.Supersedes[0] != "src/a.go" {
		t.Errorf("delta supersedes = %v, want exactly [src/a.go]", delta.Supersedes)
	}
	deltaBytes, err := os.ReadFile(filepath.Join(ContextLayersDir(f.planDir, "job-c"), delta.File))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(deltaBytes), "var A = 999") || !strings.Contains(string(deltaBytes), `supersedes="src/a.go"`) {
		t.Errorf("delta layer missing the changed content or annotation:\n%s", deltaBytes)
	}
	if strings.Contains(string(deltaBytes), `path="src/b.go"`) {
		t.Errorf("delta layer captured an unchanged/unrelated file:\n%s", deltaBytes)
	}
	if base := m.Layers[3]; base.Source != LayerSourceRulesBase || base.File != "03-base.xml" {
		t.Errorf("own base entry = %+v", base)
	}

	// Turn 2: no spurious removal annotations for inherited-only files (they
	// were never in this job's rules).
	mustRunEngineAs(f, "job-c", "t2", []LineageParent{parentOf(f, "job-a", mdPath, true)}, LayerRefreshNone)
	if m2 := loadManifestFor(f, "job-c"); len(m2.Removals) != 0 {
		t.Errorf("inherited-only files annotated as removals: %+v", m2.Removals)
	}
}

// TestLineage_ModelMismatch is e2e 13 at unit level: a parent on a different
// effective model contributes NO inherited refs — the child starts fresh
// layers (transcript + full own base), a warning lands in the job log, and
// the turn succeeds.
func TestLineage_ModelMismatch(t *testing.T) {
	f := newLayerFixture(t)

	f.writeRules("src/a.go\n")
	mustRunEngineAs(f, "job-a", "pa", nil, LayerRefreshNone)
	mdPath := writeParentChatMD(f, "job-a")

	f.writeRules("src/a.go\nsrc/b.go\n")
	res := mustRunEngineAs(f, "job-b", "t1", []LineageParent{parentOf(f, "job-a", mdPath, false)}, LayerRefreshNone)

	m := loadManifestFor(f, "job-b")
	for _, e := range m.Layers {
		if e.Source == LayerSourceInherited {
			t.Errorf("model mismatch must not inherit refs: %+v", e)
		}
	}
	if len(m.Layers) != 2 || m.Layers[0].Source != LayerSourceDepTranscript || m.Layers[1].Source != LayerSourceRulesBase {
		t.Fatalf("layers = %+v, want [dep-transcript, rules-base]", m.Layers)
	}
	// The fresh base carries the FULL rules sweep (nothing was inherited).
	baseBytes, err := os.ReadFile(filepath.Join(ContextLayersDir(f.planDir, "job-b"), m.Layers[1].File))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`path="src/a.go"`, `path="src/b.go"`} {
		if !strings.Contains(string(baseBytes), want) {
			t.Errorf("fresh base missing %s:\n%s", want, baseBytes)
		}
	}
	if !strings.Contains(f.logBuf.String(), "model") || !strings.Contains(f.logBuf.String(), "no inherited refs") {
		t.Errorf("job log missing the model-mismatch warning:\n%s", f.logBuf.String())
	}
	if len(res.LayerPaths) != 2 {
		t.Errorf("LayerPaths = %v", res.LayerPaths)
	}
}

// TestLineage_MultiParentDedup: diamond lineage (child depends on A and on B,
// where B itself inherited A) includes each artifact exactly once — first
// occurrence wins, in depends_on order.
func TestLineage_MultiParentDedup(t *testing.T) {
	f := newLayerFixture(t)

	// Parent A freezes src/a.go.
	f.writeRules("src/a.go\n")
	mustRunEngineAs(f, "job-a", "pa", nil, LayerRefreshNone)
	aBaseHash := loadManifestFor(f, "job-a").Layers[0].Hash
	aMD := writeParentChatMD(f, "job-a")

	// Parent B inherits A and adds src/b.go.
	f.writeRules("src/a.go\nsrc/b.go\n")
	mustRunEngineAs(f, "job-b", "pb", []LineageParent{parentOf(f, "job-a", aMD, true)}, LayerRefreshNone)
	bManifest := loadManifestFor(f, "job-b")
	if len(bManifest.Layers) != 3 {
		t.Fatalf("parent B layers = %+v", bManifest.Layers)
	}
	bMD := writeParentChatMD(f, "job-b")

	// Child depends on both A and B.
	f.writeSource("src/c.go", "package src\n\nvar C = 3\n")
	f.writeRules("src/a.go\nsrc/b.go\nsrc/c.go\n")
	mustRunEngineAs(f, "job-c", "t1", []LineageParent{
		parentOf(f, "job-a", aMD, true),
		parentOf(f, "job-b", bMD, true),
	}, LayerRefreshNone)

	m := loadManifestFor(f, "job-c")
	hashCount := make(map[string]int)
	for _, e := range m.Layers {
		hashCount[e.Hash]++
	}
	if hashCount[aBaseHash] != 1 {
		t.Errorf("A's base appears %d times in the child manifest, want exactly 1 (first occurrence wins):\n%+v", hashCount[aBaseHash], m.Layers)
	}
	// Every one of B's layers rides too (B's inherited copy of A's base was
	// the duplicate; its transcript-of-A and own base are unique artifacts).
	for _, be := range bManifest.Layers[1:] {
		if hashCount[be.Hash] != 1 {
			t.Errorf("B's layer %s (hash %s) appears %d times, want 1", be.File, be.Hash, hashCount[be.Hash])
		}
	}
	// A's base (via A) precedes B's contributions (depends_on order).
	if m.Layers[0].Hash != aBaseHash || m.Layers[0].InheritedFrom != "job-a/00-base.xml" {
		t.Errorf("first child layer = %+v, want A's base inherited via A", m.Layers[0])
	}
	// Own base: only the genuinely-new file.
	own := m.Layers[len(m.Layers)-1]
	if own.Source != LayerSourceRulesBase {
		t.Fatalf("last layer = %+v, want the own rules base", own)
	}
	ownBytes, err := os.ReadFile(filepath.Join(ContextLayersDir(f.planDir, "job-c"), own.File))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ownBytes), `path="src/c.go"`) ||
		strings.Contains(string(ownBytes), `path="src/a.go"`) ||
		strings.Contains(string(ownBytes), `path="src/b.go"`) {
		t.Errorf("own base must contain only src/c.go:\n%s", ownBytes)
	}
}

// TestLineage_BrokenParentArtifact: a parent whose artifacts were rebased or
// pruned is a broken lineage — assembly fails loudly with the actionable
// --rebase-context suggestion, both at inheritance time and on later-turn
// audits of already-inherited refs.
func TestLineage_BrokenParentArtifact(t *testing.T) {
	f := newLayerFixture(t)

	f.writeRules("src/a.go\n")
	mustRunEngineAs(f, "job-a", "pa", nil, LayerRefreshNone)
	mdPath := writeParentChatMD(f, "job-a")
	parentBase := filepath.Join(ContextLayersDir(f.planDir, "job-a"), "00-base.xml")

	// Case 1: parent artifact tampered BEFORE the child ever inherits.
	if err := os.WriteFile(parentBase, []byte("rebased"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runEngineAs(f, "job-b", "t1", []LineageParent{parentOf(f, "job-a", mdPath, true)}, LayerRefreshNone)
	if err == nil || !strings.Contains(err.Error(), "job-a") || !strings.Contains(err.Error(), "--rebase-context") {
		t.Fatalf("broken parent at inheritance → %v, want an actionable lineage error", err)
	}

	// Restore, inherit cleanly, then break the parent AFTER inheritance: the
	// child's next-turn audit must catch it.
	f.writeRules("src/a.go\n") // re-render identical parent base bytes
	data, _, rerr := renderLayerXML(f.contextDir, false, 0, LayerSourceRulesBase, []string{"src/a.go"}, nil, "")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if err := os.WriteFile(parentBase, data, 0o600); err != nil {
		t.Fatal(err)
	}
	mustRunEngineAs(f, "job-c", "t1", []LineageParent{parentOf(f, "job-a", mdPath, true)}, LayerRefreshNone)
	if err := os.Remove(parentBase); err != nil {
		t.Fatal(err)
	}
	_, err = runEngineAs(f, "job-c", "t2", []LineageParent{parentOf(f, "job-a", mdPath, true)}, LayerRefreshNone)
	if err == nil || !strings.Contains(err.Error(), "inherited") || !strings.Contains(err.Error(), "--rebase-context") {
		t.Fatalf("broken parent after inheritance → %v, want the inherited-audit error", err)
	}
}

// TestLineage_ReopenAddsNewParent: a chat whose store already exists picks up
// a newly-completed dep append-only — the existing base is untouched and the
// new parent's lineage lands after it.
func TestLineage_ReopenAddsNewParent(t *testing.T) {
	f := newLayerFixture(t)

	// Child turn 1: no lineage yet.
	f.writeRules("src/b.go\n")
	mustRunEngineAs(f, "job-b", "t1", nil, LayerRefreshNone)
	ownBaseHash := loadManifestFor(f, "job-b").Layers[0].Hash

	// A dep completes between turns; parent A froze src/a.go.
	f.writeRules("src/a.go\n")
	mustRunEngineAs(f, "job-a", "pa", nil, LayerRefreshNone)
	mdPath := writeParentChatMD(f, "job-a")

	// Child turn 2 (reopen shape): lineage integrates append-only.
	f.writeRules("src/b.go\n")
	res := mustRunEngineAs(f, "job-b", "t2", []LineageParent{parentOf(f, "job-a", mdPath, true)}, LayerRefreshNone)

	m := loadManifestFor(f, "job-b")
	if len(m.Layers) != 3 {
		t.Fatalf("layers after reopen = %+v, want [own base, inherited, transcript]", m.Layers)
	}
	if m.Layers[0].Hash != ownBaseHash {
		t.Errorf("reopen mutated the existing base")
	}
	if m.Layers[1].Source != LayerSourceInherited || m.Layers[2].Source != LayerSourceDepTranscript {
		t.Errorf("appended lineage = %+v", m.Layers[1:])
	}
	if len(res.LayerPaths) != 3 {
		t.Errorf("LayerPaths = %v", res.LayerPaths)
	}

	// Turn 3: idempotent.
	mustRunEngineAs(f, "job-b", "t3", []LineageParent{parentOf(f, "job-a", mdPath, true)}, LayerRefreshNone)
	if got := len(loadManifestFor(f, "job-b").Layers); got != 3 {
		t.Errorf("lineage re-integrated on turn 3: %d layers", got)
	}
}

// TestLineageEffectiveModel covers the parent-model resolution precedence
// used by the lineage guard.
func TestLineageEffectiveModel(t *testing.T) {
	plan := &Plan{}
	if got := lineageEffectiveModel(&Job{Model: "gemini-2.5-pro"}, plan); got != "gemini-2.5-pro" {
		t.Errorf("frontmatter model = %q", got)
	}
	plan.Config = &PlanConfig{Model: "gemini-2.5-flash"}
	if got := lineageEffectiveModel(&Job{}, plan); got != "gemini-2.5-flash" {
		t.Errorf("plan config model = %q", got)
	}
	plan.Config = nil
	if got := lineageEffectiveModel(&Job{}, plan); got == "" {
		t.Error("default model resolution returned empty")
	}
}

// --- executor-level tests (mock LLM) --------------------------------------

// newLineageExecutorFixture builds a git-inited plan with a completed parent
// chat (real layer store, built by executing a mock turn) and a dependent
// child chat with inline: dependencies set — the exact shape the lineage path
// must supersede.
func newLineageExecutorFixture(t *testing.T, parentExtra, childExtra string) (*Plan, *Job, *Job) {
	t.Helper()
	mockResponse := filepath.Join(t.TempDir(), "mock-response.md")
	if err := os.WriteFile(mockResponse, []byte("mock oracle response"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", mockResponse)

	tmpDir := t.TempDir()
	gitInitDir(t, tmpDir)
	writeFile := func(rel, content string) {
		t.Helper()
		path := filepath.Join(tmpDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("src/a.go", "package src\n\nvar A = 1\n")
	writeFile("src/b.go", "package src\n\nvar B = 2\n")
	writeFile("parent.rules", "src/a.go\n")
	writeFile("child.rules", "src/a.go\nsrc/b.go\n")

	writeFile("01-parent.md", "---\nid: parent-chat-1\ntitle: parent chat\nstatus: pending\ntype: chat\ntemplate: chat\nrules_file: parent.rules\n"+
		parentExtra+"---\n\nDesign the feature.\n")
	writeFile("02-child.md", "---\nid: child-chat-1\ntitle: child chat\nstatus: pending\ntype: chat\ntemplate: chat\nrules_file: child.rules\ndepends_on:\n  - parent-chat-1\ninline:\n  - dependencies\n"+
		childExtra+"---\n\nImplement phase 1.\n")

	plan := &Plan{
		Name:      "lineage-plan",
		Directory: tmpDir,
		JobsByID:  make(map[string]*Job),
	}
	loadInto := func(rel string) *Job {
		t.Helper()
		job, err := LoadJob(filepath.Join(tmpDir, rel))
		if err != nil {
			t.Fatal(err)
		}
		job.Filename = filepath.Base(rel)
		job.FilePath = filepath.Join(tmpDir, rel)
		plan.Jobs = append(plan.Jobs, job)
		plan.JobsByID[job.ID] = job
		return job
	}
	parent := loadInto("01-parent.md")
	child := loadInto("02-child.md")

	// Run the parent's turn for real (mock LLM) so it owns a genuine layer
	// store, then complete it.
	executor := NewOneShotExecutor(NewMockLLMClient(), nil)
	if err := executor.Execute(context.Background(), parent, plan); err != nil {
		t.Fatalf("parent turn: %v", err)
	}
	parentContent, err := os.ReadFile(parent.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := UpdateFrontmatter(parentContent, map[string]interface{}{"status": "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent.FilePath, completed, 0o600); err != nil {
		t.Fatal(err)
	}
	parent.Status = JobStatusCompleted

	if err := plan.ResolveDependencies(); err != nil {
		t.Fatal(err)
	}
	return plan, parent, child
}

// readTurnManifests loads all request manifests of a job, keyed by path.
func readTurnManifests(t *testing.T, plan *Plan, jobID string) []RequestManifest {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(plan.Directory, ".artifacts", jobID, "request-manifest-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []RequestManifest
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var m RequestManifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// TestExecuteChatJob_CrossJobLineage is the executor-level walkthrough of
// spec 19 e2e 10–11: the child chat's turn 1 starts its layer sequence with
// refs to the parent's artifacts (same hashes), carries the parent's body as
// a dep-transcript layer instead of prompt text (no <prepended_dependency>
// in the briefing despite inline: dependencies), and the request manifest
// records the full lineage order.
func TestExecuteChatJob_CrossJobLineage(t *testing.T) {
	plan, parent, child := newLineageExecutorFixture(t, "", "")

	executor := NewOneShotExecutor(NewMockLLMClient(), nil)
	if err := executor.Execute(context.Background(), child, plan); err != nil {
		t.Fatalf("child turn: %v", err)
	}
	if child.Status != JobStatusPendingUser {
		t.Fatalf("child status = %v, want pending_user", child.Status)
	}

	parentManifest, err := LoadLayerManifest(ContextLayersDir(plan.Directory, parent.ID))
	if err != nil || parentManifest == nil || len(parentManifest.Layers) != 1 {
		t.Fatalf("parent layers.json = (%+v, %v)", parentManifest, err)
	}
	parentBaseHash := parentManifest.Layers[0].Hash

	childManifest, err := LoadLayerManifest(ContextLayersDir(plan.Directory, child.ID))
	if err != nil || childManifest == nil {
		t.Fatal(err)
	}
	if len(childManifest.Layers) != 3 {
		t.Fatalf("child layers = %+v, want [inherited, dep-transcript, rules-base]", childManifest.Layers)
	}
	inh, tr, base := childManifest.Layers[0], childManifest.Layers[1], childManifest.Layers[2]
	if inh.Source != LayerSourceInherited || inh.Hash != parentBaseHash {
		t.Errorf("inherited entry = %+v, want the parent base hash %s", inh, parentBaseHash)
	}
	wantRef := filepath.Join(ContextLayersDir(plan.Directory, parent.ID), "00-base.xml")
	if inh.File != wantRef {
		t.Errorf("inherited ref = %q, want the parent's artifact %q (a ref, not a copy)", inh.File, wantRef)
	}
	if tr.Source != LayerSourceDepTranscript || tr.InheritedFrom != parent.ID {
		t.Errorf("transcript entry = %+v", tr)
	}
	trBytes, err := os.ReadFile(filepath.Join(ContextLayersDir(plan.Directory, child.ID), tr.File))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Design the feature.", "mock oracle response", `job="parent-chat-1"`} {
		if !strings.Contains(string(trBytes), want) {
			t.Errorf("transcript layer missing %q:\n%s", want, trBytes)
		}
	}
	if base.Source != LayerSourceRulesBase {
		t.Errorf("own base entry = %+v", base)
	}
	baseBytes, err := os.ReadFile(filepath.Join(ContextLayersDir(plan.Directory, child.ID), base.File))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(baseBytes), `path="src/a.go"`) || !strings.Contains(string(baseBytes), `path="src/b.go"`) {
		t.Errorf("own base must dedup against the inherited union:\n%s", baseBytes)
	}

	// e2e 11: the briefing XML carries NO <prepended_dependency> for the chat
	// dep and no inlined parent body — the transcript layer is the vehicle.
	manifests := readTurnManifests(t, plan, child.ID)
	if len(manifests) != 1 {
		t.Fatalf("child manifests = %d, want 1", len(manifests))
	}
	briefing, err := os.ReadFile(filepath.Join(plan.Directory, ".artifacts", child.ID, "briefing-"+manifests[0].TurnID+".xml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(briefing), "<prepended_dependency") {
		t.Errorf("briefing still inlines the chat dep:\n%s", briefing)
	}
	if strings.Contains(string(briefing), "Design the feature.") {
		t.Errorf("parent body leaked into the prompt text:\n%s", briefing)
	}

	// e2e 10: manifest layer region = parent sequence + transcript + own
	// layers, same hashes, breakpoint on the LAST layer.
	var layerEntries []RequestManifestEntry
	for _, e := range manifests[0].Entries {
		if e.Kind == "layer" {
			layerEntries = append(layerEntries, e)
		}
	}
	if len(layerEntries) != 3 {
		t.Fatalf("manifest layer entries = %+v, want 3", layerEntries)
	}
	wantSources := []string{LayerSourceInherited, LayerSourceDepTranscript, LayerSourceRulesBase}
	for i, e := range layerEntries {
		if e.Source != wantSources[i] {
			t.Errorf("layer entry %d source = %q, want %q", i, e.Source, wantSources[i])
		}
		if e.Breakpoint != (i == len(layerEntries)-1) {
			t.Errorf("layer entry %d breakpoint = %v (the save point rides the LAST layer)", i, e.Breakpoint)
		}
	}
	if layerEntries[0].ContentHash != parentBaseHash {
		t.Errorf("manifest inherited hash %s != parent base hash %s", layerEntries[0].ContentHash, parentBaseHash)
	}
}

// TestExecuteChatJob_LineageGitDelta is e2e 12 at executor level: a commit
// lands between the parent completing and the child's turn 1 → the child's
// store carries an auto git-delta layer listing exactly the changed file.
func TestExecuteChatJob_LineageGitDelta(t *testing.T) {
	plan, _, child := newLineageExecutorFixture(t, "", "")

	// The impl lands after the parent completed.
	if err := os.WriteFile(filepath.Join(plan.Directory, "src", "a.go"), []byte("package src\n\nvar A = 42 // landed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	executor := NewOneShotExecutor(NewMockLLMClient(), nil)
	if err := executor.Execute(context.Background(), child, plan); err != nil {
		t.Fatalf("child turn: %v", err)
	}

	childManifest, err := LoadLayerManifest(ContextLayersDir(plan.Directory, child.ID))
	if err != nil || childManifest == nil {
		t.Fatal(err)
	}
	var delta *LayerEntry
	for i := range childManifest.Layers {
		if childManifest.Layers[i].Source == LayerSourceGitDelta {
			if delta != nil {
				t.Fatalf("more than one delta layer: %+v", childManifest.Layers)
			}
			delta = &childManifest.Layers[i]
		}
	}
	if delta == nil {
		t.Fatalf("no git-delta layer in %+v", childManifest.Layers)
	}
	if len(delta.Supersedes) != 1 || delta.Supersedes[0] != "src/a.go" {
		t.Errorf("delta supersedes = %v, want exactly the changed file", delta.Supersedes)
	}
	deltaBytes, err := os.ReadFile(filepath.Join(ContextLayersDir(plan.Directory, child.ID), delta.File))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(deltaBytes), "var A = 42") {
		t.Errorf("delta layer missing the landed change:\n%s", deltaBytes)
	}
	// The own rules base still dedups src/a.go (it is in the inherited
	// union), so the changed file travels ONLY via the delta layer.
	own := childManifest.Layers[len(childManifest.Layers)-1]
	ownBytes, err := os.ReadFile(filepath.Join(ContextLayersDir(plan.Directory, child.ID), own.File))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ownBytes), `path="src/a.go"`) {
		t.Errorf("own base duplicated the delta'd file:\n%s", ownBytes)
	}
}

// TestExecuteChatJob_LineageModelMismatch is e2e 13 at executor level: the
// parent ran on a different model → the child starts fresh (no inherited
// refs), the transcript still rides as a layer, and the turn succeeds.
func TestExecuteChatJob_LineageModelMismatch(t *testing.T) {
	plan, _, child := newLineageExecutorFixture(t, "model: gemini-2.5-pro\n", "")

	executor := NewOneShotExecutor(NewMockLLMClient(), nil)
	if err := executor.Execute(context.Background(), child, plan); err != nil {
		t.Fatalf("child turn must succeed on model mismatch: %v", err)
	}
	if child.Status != JobStatusPendingUser {
		t.Fatalf("child status = %v, want pending_user", child.Status)
	}

	childManifest, err := LoadLayerManifest(ContextLayersDir(plan.Directory, child.ID))
	if err != nil || childManifest == nil {
		t.Fatal(err)
	}
	sources := make([]string, 0, len(childManifest.Layers))
	for _, e := range childManifest.Layers {
		if e.Source == LayerSourceInherited {
			t.Errorf("model mismatch must not inherit refs: %+v", e)
		}
		sources = append(sources, e.Source)
	}
	if len(sources) != 2 || sources[0] != LayerSourceDepTranscript || sources[1] != LayerSourceRulesBase {
		t.Fatalf("layers = %v, want [dep-transcript, rules-base]", sources)
	}
	// The fresh base carries the full child sweep — nothing was inherited.
	baseBytes, err := os.ReadFile(filepath.Join(ContextLayersDir(plan.Directory, child.ID), childManifest.Layers[1].File))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`path="src/a.go"`, `path="src/b.go"`} {
		if !strings.Contains(string(baseBytes), want) {
			t.Errorf("fresh base missing %s:\n%s", want, baseBytes)
		}
	}
}
