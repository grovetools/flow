package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/grove-anthropic/pkg/anthropic"
)

// TestChatCacheTTL covers the cache_ttl frontmatter contract (spec 19 D2):
// unset defaults to 1h for chat jobs, 5m/1h pass through, junk fails
// actionably.
func TestChatCacheTTL(t *testing.T) {
	tests := []struct {
		name    string
		ttl     string
		want    string
		wantErr bool
	}{
		{"unset defaults to 1h", "", "1h", false},
		{"5m passes through", "5m", "5m", false},
		{"1h passes through", "1h", "1h", false},
		{"junk rejected", "2h", "", true},
		{"case-sensitive", "5M", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := &Job{CacheTTL: tt.ttl}
			got, err := job.ChatCacheTTL()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ChatCacheTTL(%q) = %q, nil; want error", tt.ttl, got)
				}
				if !strings.Contains(err.Error(), "cache_ttl") {
					t.Errorf("error %v does not mention cache_ttl", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ChatCacheTTL(%q) error: %v", tt.ttl, err)
			}
			if got != tt.want {
				t.Errorf("ChatCacheTTL(%q) = %q, want %q", tt.ttl, got, tt.want)
			}
		})
	}
}

// TestLoadJobCacheTTL verifies cache_ttl round-trips through the YAML loader.
func TestLoadJobCacheTTL(t *testing.T) {
	tmpDir := t.TempDir()
	content := `---
id: ttl-job-1
title: TTL Job
status: pending
type: chat
cache_ttl: 5m
---
Body.`
	path := filepath.Join(tmpDir, "01-ttl-job.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	job, err := LoadJob(path)
	if err != nil {
		t.Fatalf("LoadJob: %v", err)
	}
	if job.CacheTTL != "5m" {
		t.Errorf("job.CacheTTL = %q, want 5m", job.CacheTTL)
	}
	ttl, err := job.ChatCacheTTL()
	if err != nil || ttl != "5m" {
		t.Errorf("ChatCacheTTL() = (%q, %v), want (5m, nil)", ttl, err)
	}
}

// TestLoadJobDetectsLegacyPinnedContext verifies the loader flags (but does
// not fail on) the removed pinned_context key, so plans stay browsable and
// the rejection happens at execution time (spec 19 D5).
func TestLoadJobDetectsLegacyPinnedContext(t *testing.T) {
	tmpDir := t.TempDir()

	withPin := `---
id: pin-job-1
title: Pinned Job
status: pending
type: chat
pinned_context:
  - docs/spec.md
---
Body.`
	pinPath := filepath.Join(tmpDir, "01-pin-job.md")
	if err := os.WriteFile(pinPath, []byte(withPin), 0o600); err != nil {
		t.Fatal(err)
	}
	job, err := LoadJob(pinPath)
	if err != nil {
		t.Fatalf("LoadJob must still load a pinned_context job (rejection is execution-time): %v", err)
	}
	if !job.HasLegacyPinnedContext {
		t.Error("HasLegacyPinnedContext = false, want true")
	}
	rejErr := job.PinnedContextRemovedError()
	if rejErr == nil {
		t.Fatal("PinnedContextRemovedError() = nil, want the actionable rejection")
	}
	for _, want := range []string{"pinned_context", "rules file"} {
		if !strings.Contains(rejErr.Error(), want) {
			t.Errorf("rejection %q does not mention %q", rejErr.Error(), want)
		}
	}

	withoutPin := `---
id: plain-job-1
title: Plain Job
status: pending
type: chat
---
Body.`
	plainPath := filepath.Join(tmpDir, "02-plain-job.md")
	if err := os.WriteFile(plainPath, []byte(withoutPin), 0o600); err != nil {
		t.Fatal(err)
	}
	job, err = LoadJob(plainPath)
	if err != nil {
		t.Fatalf("LoadJob: %v", err)
	}
	if job.HasLegacyPinnedContext {
		t.Error("HasLegacyPinnedContext = true for a job without pinned_context")
	}
	if job.PinnedContextRemovedError() != nil {
		t.Error("PinnedContextRemovedError() != nil for a job without pinned_context")
	}
}

// gitInitDir makes dir a standalone git repo so the executor's project-root
// resolution anchors at the fixture instead of falling back to the test
// binary's cwd repo.
func gitInitDir(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "init", "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
}

// newChatJobFixture writes a chat job file into a fresh plan dir and loads it.
func newChatJobFixture(t *testing.T, frontmatterExtra, body string) (*Plan, *Job) {
	t.Helper()
	tmpDir := t.TempDir()
	plan := &Plan{
		Name:      "test-plan",
		Directory: tmpDir,
		Jobs:      []*Job{},
		JobsByID:  make(map[string]*Job),
	}
	content := "---\nid: chat-job-1\ntitle: chat-job\nstatus: pending\ntype: chat\ntemplate: chat\n" +
		frontmatterExtra + "---\n\n" + body + "\n"
	jobPath := filepath.Join(tmpDir, "01-chat-job.md")
	if err := os.WriteFile(jobPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	job, err := LoadJob(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	job.Filename = filepath.Base(jobPath)
	job.FilePath = jobPath
	plan.Jobs = append(plan.Jobs, job)
	plan.JobsByID[job.ID] = job
	return plan, job
}

// assertJobFileFailed asserts the on-disk job .md ended at status: failed
// (not stuck at running) — the terminal-failure-guard contract.
func assertJobFileFailed(t *testing.T, job *Job) {
	t.Helper()
	if job.Status != JobStatusFailed {
		t.Errorf("job.Status = %v, want failed", job.Status)
	}
	after, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "status: failed") {
		t.Errorf("job file not written to failed status:\n%s", string(after))
	}
	if strings.Contains(string(after), "status: running") {
		t.Errorf("job file stuck at running status:\n%s", string(after))
	}
}

// TestExecuteChatJob_PinnedContextRejected is the D5 rejection path (e2e
// scenario 14 at unit level): a chat job carrying the removed pinned_context
// key must fail the turn with the actionable error AND leave the job file at
// status: failed via the terminal-failure guard.
func TestExecuteChatJob_PinnedContextRejected(t *testing.T) {
	plan, job := newChatJobFixture(t, "pinned_context:\n  - core/logging/logger.go\n", "Please answer.")

	executor := NewOneShotExecutor(NewMockLLMClient(), nil)
	err := executor.Execute(context.Background(), job, plan)
	if err == nil {
		t.Fatal("Execute() = nil error, want the pinned_context rejection")
	}
	for _, want := range []string{"pinned_context", "rules file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Execute() error = %v, want it to mention %q", err, want)
		}
	}
	assertJobFileFailed(t, job)
}

// TestExecuteChatJob_InvalidCacheTTLFails asserts a junk cache_ttl fails the
// turn actionably (and terminally) instead of silently degrading caching.
func TestExecuteChatJob_InvalidCacheTTLFails(t *testing.T) {
	plan, job := newChatJobFixture(t, "cache_ttl: 2h\n", "Please answer.")

	executor := NewOneShotExecutor(NewMockLLMClient(), nil)
	err := executor.Execute(context.Background(), job, plan)
	if err == nil {
		t.Fatal("Execute() = nil error, want the cache_ttl rejection")
	}
	if !strings.Contains(err.Error(), "cache_ttl") {
		t.Errorf("Execute() error = %v, want it to mention cache_ttl", err)
	}
	assertJobFileFailed(t, job)
}

// TestExecuteChatJob_WritesRequestManifest runs a full mock chat turn and
// asserts the per-turn request manifest (spec 19 D9) describes the P3 ladder
// assembly: the layer engine's frozen 00-base.xml is the layer region with
// the breakpoint (default ttl 1h), include files follow as plain context
// documents (they become transcript layers in P5 and sit under the history
// breakpoint once P4 lands), then the volatile turn block.
func TestExecuteChatJob_WritesRequestManifest(t *testing.T) {
	mockResponse := filepath.Join(t.TempDir(), "mock-response.md")
	if err := os.WriteFile(mockResponse, []byte("mock oracle response"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", mockResponse)

	// A rules file pins the layer store to a deterministic single-file sweep
	// (without one, notebook-resolved rules in the test env could leak in),
	// and git-init makes the plan dir its own project root (otherwise the
	// executor's cwd fallback would resolve the test binary's repo).
	plan, job := newChatJobFixture(t, "rules_file: ctx.rules\n", "Please answer.")
	gitInitDir(t, plan.Directory)
	if err := os.MkdirAll(filepath.Join(plan.Directory, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.Directory, "src", "a.go"), []byte("package src\n\nvar A = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.Directory, "ctx.rules"), []byte("src/a.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Two include files so the manifest has a post-layer context region.
	inc1 := filepath.Join(plan.Directory, "ref-a.md")
	inc2 := filepath.Join(plan.Directory, "ref-b.md")
	if err := os.WriteFile(inc1, []byte("reference A"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inc2, []byte("reference B"), 0o600); err != nil {
		t.Fatal(err)
	}
	job.Include = []string{"ref-a.md", "ref-b.md"}

	executor := NewOneShotExecutor(NewMockLLMClient(), nil)
	if err := executor.Execute(context.Background(), job, plan); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(plan.Directory, ".artifacts", job.ID, "request-manifest-*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly 1 request manifest, got %v (err=%v)", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var manifest RequestManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	if manifest.Provider != requestManifestProviderMock {
		t.Errorf("Provider = %q, want %q", manifest.Provider, requestManifestProviderMock)
	}
	if manifest.CacheLayout != anthropic.CacheLayoutLadder {
		t.Errorf("CacheLayout = %q, want ladder", manifest.CacheLayout)
	}
	if manifest.CacheTTL != "1h" {
		t.Errorf("CacheTTL = %q, want the 1h chat default", manifest.CacheTTL)
	}
	if manifest.JobID != job.ID || manifest.TurnID == "" {
		t.Errorf("manifest identity = (%q, %q), want job %q with a turn id", manifest.JobID, manifest.TurnID, job.ID)
	}

	// Exact P3 shape: [layer 00-base *1h][context inc1][context inc2][turn].
	wantBase := filepath.Join(plan.Directory, ".artifacts", job.ID, "context-layers", "00-base.xml")
	want := []struct {
		kind, path, source string
		breakpoint         bool
		ttl                string
	}{
		{"layer", wantBase, "rules-base", true, "1h"},
		{"context", inc1, "", false, ""},
		{"context", inc2, "", false, ""},
		{"turn", "", "", false, ""},
	}
	if len(manifest.Entries) != len(want) {
		t.Fatalf("entries = %+v, want %d blocks", manifest.Entries, len(want))
	}
	for i, w := range want {
		e := manifest.Entries[i]
		if e.Kind != w.kind || e.Path != w.path || e.Source != w.source || e.Breakpoint != w.breakpoint || e.TTL != w.ttl {
			t.Errorf("entry %d = %+v, want %+v", i, e, w)
		}
		if e.ContentHash == "" {
			t.Errorf("entry %d missing content hash", i)
		}
	}

	// The manifest's layer hash matches layers.json (D9 ledger honesty), and
	// the turn id corresponds to a briefing file.
	layerManifest, err := LoadLayerManifest(ContextLayersDir(plan.Directory, job.ID))
	if err != nil || layerManifest == nil || len(layerManifest.Layers) != 1 {
		t.Fatalf("layers.json = (%+v, %v), want a single frozen base", layerManifest, err)
	}
	if manifest.Entries[0].ContentHash != layerManifest.Layers[0].Hash {
		t.Errorf("manifest layer hash %s != layers.json hash %s", manifest.Entries[0].ContentHash, layerManifest.Layers[0].Hash)
	}
	if _, err := os.Stat(LayerSnapshotPath(plan.Directory, job.ID)); err != nil {
		t.Errorf("snapshot.json missing: %v", err)
	}
	briefing := filepath.Join(plan.Directory, ".artifacts", job.ID, "briefing-"+manifest.TurnID+".xml")
	if _, err := os.Stat(briefing); err != nil {
		t.Errorf("manifest turn id %q does not correspond to a briefing file: %v", manifest.TurnID, err)
	}
}

// TestExecuteChatJob_LayerEngineTwoTurns is the executor-level layer-engine
// walkthrough (spec 19 §2): turn 1 freezes the layer store; the user widens
// the rules file and runs turn 2 → exactly one rules-diff layer with only the
// new files is appended, the base stays byte-identical, and the manifest
// shows the breakpoint moved to the appended (last) layer.
func TestExecuteChatJob_LayerEngineTwoTurns(t *testing.T) {
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

	executor := NewOneShotExecutor(NewMockLLMClient(), nil)
	if err := executor.Execute(context.Background(), job, plan); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if job.Status != JobStatusPendingUser {
		t.Fatalf("turn 1 status = %v, want pending_user", job.Status)
	}

	layersDir := ContextLayersDir(plan.Directory, job.ID)
	m1, err := LoadLayerManifest(layersDir)
	if err != nil || m1 == nil || len(m1.Layers) != 1 {
		t.Fatalf("turn 1 layers.json = (%+v, %v), want the frozen base", m1, err)
	}
	baseHash := m1.Layers[0].Hash

	manifestsAfterTurn1, _ := filepath.Glob(filepath.Join(plan.Directory, ".artifacts", job.ID, "request-manifest-*.json"))
	if len(manifestsAfterTurn1) != 1 {
		t.Fatalf("turn 1 manifests = %v", manifestsAfterTurn1)
	}

	// Widen the rules (the ONLY context surface the user touches) and append
	// the next user turn below the trailing marker the response left behind.
	writeSrc("ctx.rules", "src/a.go\nsrc/b.go\n")
	chatContent, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	chatContent = append(chatContent, []byte("\nNow widen the discussion to b.\n")...)
	if err := os.WriteFile(job.FilePath, chatContent, 0o600); err != nil {
		t.Fatal(err)
	}
	job2, err := LoadJob(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	job2.Filename = job.Filename
	job2.FilePath = job.FilePath
	if err := executor.Execute(context.Background(), job2, plan); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	// Layer store: base untouched, exactly one appended rules-diff layer
	// containing ONLY the new file.
	m2, err := LoadLayerManifest(layersDir)
	if err != nil || m2 == nil {
		t.Fatal(err)
	}
	if len(m2.Layers) != 2 {
		t.Fatalf("turn 2 layers = %+v, want base + rules-diff", m2.Layers)
	}
	if m2.Layers[0].Hash != baseHash {
		t.Errorf("base layer hash changed across the widening turn")
	}
	if m2.Layers[1].Source != LayerSourceRulesDiff {
		t.Errorf("appended layer source = %q", m2.Layers[1].Source)
	}
	addedData, err := os.ReadFile(filepath.Join(layersDir, m2.Layers[1].File))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(addedData), `path="src/b.go"`) || strings.Contains(string(addedData), `path="src/a.go"`) {
		t.Errorf("rules-diff layer must contain only the new file:\n%s", addedData)
	}

	// Turn-2 manifest: [layer base][layer add *1h][turn] — the breakpoint
	// sits on the LAST layer, and layer-0's uploaded bytes are the same
	// frozen artifact turn 1 wrote (cache-read, not rewrite).
	manifests, _ := filepath.Glob(filepath.Join(plan.Directory, ".artifacts", job.ID, "request-manifest-*.json"))
	if len(manifests) != 2 {
		t.Fatalf("manifests after turn 2 = %v", manifests)
	}
	var turn2 *RequestManifest
	for _, path := range manifests {
		if path == manifestsAfterTurn1[0] {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var m RequestManifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		turn2 = &m
	}
	if turn2 == nil {
		t.Fatal("turn 2 manifest not found")
	}
	if len(turn2.Entries) != 3 {
		t.Fatalf("turn 2 entries = %+v, want [layer, layer, turn]", turn2.Entries)
	}
	e0, e1, e2 := turn2.Entries[0], turn2.Entries[1], turn2.Entries[2]
	if e0.Kind != "layer" || e0.Source != "rules-base" || e0.Breakpoint || e0.ContentHash != baseHash {
		t.Errorf("entry 0 = %+v, want the frozen base (no breakpoint, hash %s)", e0, baseHash)
	}
	if e1.Kind != "layer" || e1.Source != "rules-diff" || !e1.Breakpoint || e1.TTL != "1h" {
		t.Errorf("entry 1 = %+v, want the rules-diff layer carrying the 1h breakpoint", e1)
	}
	if e1.ContentHash != m2.Layers[1].Hash {
		t.Errorf("manifest diff-layer hash %s != layers.json %s", e1.ContentHash, m2.Layers[1].Hash)
	}
	if e2.Kind != "turn" || e2.Breakpoint {
		t.Errorf("entry 2 = %+v, want the volatile turn block", e2)
	}
}

// TestExecuteChatJob_RefreshVerbsMutuallyExclusive: a turn directive carrying
// both refresh verbs fails the turn actionably (and terminally via the
// failure guard) instead of guessing which refresh the user meant.
func TestExecuteChatJob_RefreshVerbsMutuallyExclusive(t *testing.T) {
	mockResponse := filepath.Join(t.TempDir(), "mock-response.md")
	if err := os.WriteFile(mockResponse, []byte("mock oracle response"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", mockResponse)

	body := "<!-- grove: {\"template\": \"chat\", \"append_delta\": true, \"rebase_context\": true} -->\n\nPlease answer."
	plan, job := newChatJobFixture(t, "", body)
	gitInitDir(t, plan.Directory)

	executor := NewOneShotExecutor(NewMockLLMClient(), nil)
	err := executor.Execute(context.Background(), job, plan)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("Execute() = %v, want the mutually-exclusive rejection", err)
	}
	assertJobFileFailed(t, job)
}
