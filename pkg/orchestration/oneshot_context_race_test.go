package orchestration

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingLLMClient captures, per job ID, the concatenated contents of the
// context files it was handed. It lets the concurrency test observe exactly
// which context payload each job would have uploaded.
type recordingLLMClient struct {
	mu       sync.Mutex
	recorded map[string]string
}

func (c *recordingLLMClient) Complete(_ context.Context, job *Job, _ *Plan, _ string, opts LLMOptions, _ io.Writer) (string, error) {
	var sb strings.Builder
	for _, f := range opts.ContextFiles {
		if b, err := os.ReadFile(f); err == nil {
			sb.Write(b)
		}
	}
	c.mu.Lock()
	if c.recorded == nil {
		c.recorded = make(map[string]string)
	}
	c.recorded[job.ID] = sb.String()
	c.mu.Unlock()
	return "ok", nil
}

func (c *recordingLLMClient) get(jobID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recorded[jobID]
}

// TestOneShotExecutor_ConcurrentJobsGetOwnContext dispatches two oneshot jobs
// in the same plan concurrently, each with its own rules file selecting a
// distinct source file, and asserts each job uploads ONLY its own context.
//
// Before the job-scoped context fix, both jobs generated into the shared
// plan-scoped <plan>/context/generated/context path, so the second generation
// clobbered the first and both jobs uploaded whichever payload landed on disk
// last — this test would fail (cross-job context leak).
func TestOneShotExecutor_ConcurrentJobsGetOwnContext(t *testing.T) {
	tmpDir := t.TempDir()

	// Make tmpDir a git repo so GetProjectGitRoot resolves the job's working
	// directory to tmpDir (otherwise it falls back to the test process cwd and
	// the relative rules patterns below wouldn't match the source files).
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", tmpDir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Two distinct source files, each carrying a unique marker.
	if err := os.WriteFile(filepath.Join(tmpDir, "alpha.txt"), []byte("ALPHA_CONTEXT_MARKER"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "beta.txt"), []byte("BETA_CONTEXT_MARKER"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Per-job rules files, each selecting only its own source file.
	if err := os.WriteFile(filepath.Join(tmpDir, "alpha.rules"), []byte("alpha.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "beta.rules"), []byte("beta.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan := &Plan{Directory: tmpDir, JobsByID: make(map[string]*Job)}

	mkJob := func(id, rules string) *Job {
		jobPath := filepath.Join(tmpDir, id+".md")
		content := "---\n" +
			"id: " + id + "\n" +
			"title: " + id + "\n" +
			"status: pending\n" +
			"type: oneshot\n" +
			"rules_file: " + rules + "\n" +
			"---\n" +
			"Do the thing.\n"
		if err := os.WriteFile(jobPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		job, err := LoadJob(jobPath)
		if err != nil {
			t.Fatal(err)
		}
		job.FilePath = jobPath
		job.Filename = id + ".md"
		return job
	}

	jobA := mkJob("job-alpha", "alpha.rules")
	jobB := mkJob("job-beta", "beta.rules")

	// Enabling mock mode routes the LLM call through e.llmClient.Complete,
	// where our recording client observes the per-job context files.
	t.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", filepath.Join(tmpDir, "mock-response.txt"))

	client := &recordingLLMClient{}
	config := &ExecutorConfig{MaxPromptLength: 1_000_000, Timeout: time.Minute, SkipInteractive: true}
	executor := NewOneShotExecutor(client, config)

	ctx := context.Background()
	var wg sync.WaitGroup
	for _, job := range []*Job{jobA, jobB} {
		wg.Add(1)
		go func(j *Job) {
			defer wg.Done()
			if err := executor.Execute(ctx, j, plan); err != nil {
				t.Errorf("Execute(%s) error: %v", j.ID, err)
			}
		}(job)
	}
	wg.Wait()

	alphaCtx := client.get("job-alpha")
	betaCtx := client.get("job-beta")

	if !strings.Contains(alphaCtx, "ALPHA_CONTEXT_MARKER") {
		t.Errorf("job-alpha context missing its own marker; got: %q", alphaCtx)
	}
	if strings.Contains(alphaCtx, "BETA_CONTEXT_MARKER") {
		t.Errorf("job-alpha context leaked job-beta content (cross-job race not fixed)")
	}
	if !strings.Contains(betaCtx, "BETA_CONTEXT_MARKER") {
		t.Errorf("job-beta context missing its own marker; got: %q", betaCtx)
	}
	if strings.Contains(betaCtx, "ALPHA_CONTEXT_MARKER") {
		t.Errorf("job-beta context leaked job-alpha content (cross-job race not fixed)")
	}
}
