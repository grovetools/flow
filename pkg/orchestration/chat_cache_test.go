package orchestration

import (
	"context"
	"encoding/json"
	"os"
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
// asserts the per-turn request manifest (spec 19 D9) is written next to the
// briefing file, describing the ladder assembly: include files ride as layer
// documents with the breakpoint (default ttl 1h) on the LAST layer, followed
// by the volatile turn block.
func TestExecuteChatJob_WritesRequestManifest(t *testing.T) {
	mockResponse := filepath.Join(t.TempDir(), "mock-response.md")
	if err := os.WriteFile(mockResponse, []byte("mock oracle response"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", mockResponse)

	plan, job := newChatJobFixture(t, "", "Please answer.")

	// Two include files so the manifest has a multi-layer document region.
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

	// Structural shape (the test env may sweep a repo-level rules file into a
	// leading hot-context layer, so assert structure, not exact counts): every
	// entry before the final turn block is a layer; the breakpoint (default
	// ttl 1h) sits on the LAST layer only; the include files ride as the two
	// trailing layers in declared order.
	n := len(manifest.Entries)
	if n < 3 {
		t.Fatalf("entries = %+v, want at least [layer, layer, turn]", manifest.Entries)
	}
	lastLayer := n - 2
	for i, e := range manifest.Entries {
		wantKind := "layer"
		if i == n-1 {
			wantKind = "turn"
		}
		if e.Kind != wantKind {
			t.Errorf("entry %d kind = %q, want %q", i, e.Kind, wantKind)
		}
		if e.ContentHash == "" || e.TokenEstimate < 0 {
			t.Errorf("entry %d missing content hash / token estimate: %+v", i, e)
		}
		wantBP := i == lastLayer
		if e.Breakpoint != wantBP {
			t.Errorf("entry %d breakpoint = %v, want %v", i, e.Breakpoint, wantBP)
		}
		wantTTL := ""
		if wantBP {
			wantTTL = "1h"
		}
		if e.TTL != wantTTL {
			t.Errorf("entry %d ttl = %q, want %q", i, e.TTL, wantTTL)
		}
	}
	if manifest.Entries[lastLayer-1].Path != inc1 || manifest.Entries[lastLayer].Path != inc2 {
		t.Errorf("trailing layer paths = (%q, %q), want (%q, %q)",
			manifest.Entries[lastLayer-1].Path, manifest.Entries[lastLayer].Path, inc1, inc2)
	}
	if manifest.Entries[n-1].Path != "" {
		t.Errorf("turn entry has a path: %q", manifest.Entries[n-1].Path)
	}

	// The turn hash must cover the full assembled prompt (briefing bytes).
	briefing := filepath.Join(plan.Directory, ".artifacts", job.ID, "briefing-"+manifest.TurnID+".xml")
	if _, err := os.Stat(briefing); err != nil {
		t.Errorf("manifest turn id %q does not correspond to a briefing file: %v", manifest.TurnID, err)
	}
}
