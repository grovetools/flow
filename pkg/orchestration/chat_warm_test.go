package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// firedChatFixture builds a chat job with a rules file + two include files, runs
// one mock turn so it owns a real layer store and request manifest, and returns
// the plan/job at rest (pending_user). GROVE_MOCK_LLM_RESPONSE_FILE stays set so
// WarmChatCache short-circuits before runner construction.
func firedChatFixture(t *testing.T) (*Plan, *Job) {
	t.Helper()
	mockResponse := filepath.Join(t.TempDir(), "mock-response.md")
	if err := os.WriteFile(mockResponse, []byte("mock oracle response"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", mockResponse)

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
	if err := os.WriteFile(filepath.Join(plan.Directory, "ref-a.md"), []byte("reference A"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.Directory, "ref-b.md"), []byte("reference B"), 0o600); err != nil {
		t.Fatal(err)
	}
	job.Include = []string{"ref-a.md", "ref-b.md"}

	if err := NewOneShotExecutor(NewMockLLMClient(), nil).Execute(context.Background(), job, plan); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if job.Status != JobStatusPendingUser {
		t.Fatalf("job status = %v, want pending_user", job.Status)
	}
	return plan, job
}

func countRequestManifests(t *testing.T, plan *Plan, jobID string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(plan.Directory, ".artifacts", jobID, "request-manifest-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}

// TestWarmChatCache_MockParityAndReceipt is the happy path: warm reproduces the
// last turn's cached prefix (parity passes), writes exactly one warm receipt,
// writes NO new request manifest, and touches neither the chat body nor the
// frontmatter.
func TestWarmChatCache_MockParityAndReceipt(t *testing.T) {
	plan, job := firedChatFixture(t)

	if got := countRequestManifests(t, plan, job.ID); got != 1 {
		t.Fatalf("request manifests after turn 1 = %d, want 1", got)
	}
	bodyBefore, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}

	result, err := WarmChatCache(context.Background(), job, plan)
	if err != nil {
		t.Fatalf("WarmChatCache: %v", err)
	}
	if !result.Mock || !result.Receipt.ParityOK {
		t.Fatalf("result = %+v, want Mock && ParityOK", result)
	}

	// Exactly one warm receipt was written.
	receipts, err := filepath.Glob(filepath.Join(plan.Directory, ".artifacts", job.ID, "warm-*.json"))
	if err != nil || len(receipts) != 1 {
		t.Fatalf("warm receipts = %v (err=%v), want exactly 1", receipts, err)
	}
	if receipts[0] != result.ReceiptPath {
		t.Errorf("receipt path = %s, want %s", result.ReceiptPath, receipts[0])
	}

	// No new request manifest — warm must never write one.
	if got := countRequestManifests(t, plan, job.ID); got != 1 {
		t.Errorf("request manifests after warm = %d, want 1 (warm writes none)", got)
	}

	// Chat body / frontmatter byte-identical.
	bodyAfter, err := os.ReadFile(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(bodyAfter) != string(bodyBefore) {
		t.Errorf("chat file changed after warm:\nbefore:\n%s\nafter:\n%s", bodyBefore, bodyAfter)
	}
}

// TestWarmChatCache_NeverFiredRefuses: a chat with no prior manifest has nothing
// cached to keep warm.
func TestWarmChatCache_NeverFiredRefuses(t *testing.T) {
	plan, job := newChatJobFixture(t, "", "Please answer.")
	_, err := WarmChatCache(context.Background(), job, plan)
	if err == nil || !strings.Contains(err.Error(), "never fired") {
		t.Fatalf("WarmChatCache err = %v, want a never-fired refusal", err)
	}
}

// TestWarmChatCache_EditedIncludeAborts: an include file edited since the last
// turn changes the context block hash, so warm aborts before the API call.
func TestWarmChatCache_EditedIncludeAborts(t *testing.T) {
	plan, job := firedChatFixture(t)

	// Mutate an include file's bytes.
	if err := os.WriteFile(filepath.Join(plan.Directory, "ref-a.md"), []byte("reference A — EDITED"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := WarmChatCache(context.Background(), job, plan)
	if err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("WarmChatCache err = %v, want a prefix-diverged abort", err)
	}
	// Nothing was written on abort.
	receipts, _ := filepath.Glob(filepath.Join(plan.Directory, ".artifacts", job.ID, "warm-*.json"))
	if len(receipts) != 0 {
		t.Errorf("warm receipts = %v, want none on abort", receipts)
	}
}

// TestWarmChatCache_NonAnthropicRefuses: a last turn recorded under a
// non-Anthropic provider has no ladder cache to keep warm.
func TestWarmChatCache_NonAnthropicRefuses(t *testing.T) {
	plan, job := newChatJobFixture(t, "", "Please answer.")
	// Fabricate a gemini manifest as the job's latest turn.
	if _, err := WriteRequestManifest(plan.Directory, job.ID, "aaa111", RequestManifest{
		TurnID:    "aaa111",
		JobID:     job.ID,
		Model:     "gemini-2.5-pro",
		Provider:  requestManifestProviderGemini,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := WarmChatCache(context.Background(), job, plan)
	if err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("WarmChatCache err = %v, want a non-Anthropic refusal", err)
	}
}

// TestWarmChatCache_ModelMismatchRefuses: the model warm would resolve to must
// equal the manifest's — caches are model-scoped.
func TestWarmChatCache_ModelMismatchRefuses(t *testing.T) {
	plan, job := newChatJobFixture(t, "", "Please answer.")
	// Fabricate an anthropic manifest under a DIFFERENT model than the job
	// resolves to (job has no model → default fallback).
	if _, err := WriteRequestManifest(plan.Directory, job.ID, "bbb222", RequestManifest{
		TurnID:    "bbb222",
		JobID:     job.ID,
		Model:     "claude-some-other-model",
		Provider:  requestManifestProviderAnthropic,
		CacheTTL:  "1h",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := WarmChatCache(context.Background(), job, plan)
	if err == nil || !strings.Contains(err.Error(), "model-scoped") {
		t.Fatalf("WarmChatCache err = %v, want a model-scoped refusal", err)
	}
}

// TestWarmChatCache_NoCacheRefuses: a last turn that ran with caching disabled
// has no cached prefix.
func TestWarmChatCache_NoCacheRefuses(t *testing.T) {
	plan, job := newChatJobFixture(t, "", "Please answer.")
	if _, err := WriteRequestManifest(plan.Directory, job.ID, "ccc333", RequestManifest{
		TurnID:    "ccc333",
		JobID:     job.ID,
		Model:     "claude-x",
		Provider:  requestManifestProviderAnthropic,
		NoCache:   true,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := WarmChatCache(context.Background(), job, plan)
	if err == nil || !strings.Contains(err.Error(), "caching disabled") {
		t.Fatalf("WarmChatCache err = %v, want a no_cache refusal", err)
	}
}
