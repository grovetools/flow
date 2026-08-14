package orchestration

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAddJobNoContextStampsNoRulesFile pins the creation half of the no_context
// contract: a job that declares it carries its own context gets no rules_file
// stamped, so nothing later resolves a path nobody wrote.
func TestAddJobNoContextStampsNoRulesFile(t *testing.T) {
	plan := &Plan{Directory: t.TempDir(), JobsByID: make(map[string]*Job)}
	job := &Job{ID: "self-contained", Title: "Self Contained", Type: JobTypeOneshot, NoContext: true, PromptBody: "Do something"}

	if _, err := AddJob(plan, job); err != nil {
		t.Fatalf("AddJob() error = %v", err)
	}
	if job.RulesFile != "" {
		t.Errorf("RulesFile = %q; want empty for a no_context job", job.RulesFile)
	}
	if _, err := os.Stat(filepath.Join(plan.Directory, "rules")); !os.IsNotExist(err) {
		t.Errorf("rules dir stat = %v; want absent for a no_context job", err)
	}

	// The declaration must survive the frontmatter round trip, or the runner
	// re-reads the job from disk and assembles context anyway.
	reloaded, err := LoadPlan(plan.Directory)
	if err != nil {
		t.Fatalf("LoadPlan() error = %v", err)
	}
	got, ok := reloaded.JobsByID["self-contained"]
	if !ok {
		t.Fatal("reloaded plan is missing the job")
	}
	if !got.NoContext || got.RulesFile != "" {
		t.Errorf("reloaded job NoContext = %v, RulesFile = %q; want true, empty", got.NoContext, got.RulesFile)
	}
}

// TestAddJobRejectsNoContextWithRulesFile: declaring both is a contradiction,
// and silently picking a winner is how a job ends up with context it declined
// (or without context it named).
func TestAddJobRejectsNoContextWithRulesFile(t *testing.T) {
	plan := &Plan{Directory: t.TempDir(), JobsByID: make(map[string]*Job)}
	job := &Job{ID: "contradiction", Title: "Contradiction", Type: JobTypeOneshot, NoContext: true, RulesFile: "custom.rules"}

	_, err := AddJob(plan, job)
	if err == nil {
		t.Fatal("AddJob() = nil error; want a refusal of no_context + rules_file")
	}
	for _, want := range []string{"no_context", "rules_file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("AddJob() error = %q; want it to mention %q", err, want)
		}
	}
}

// contextFileRecorder records the context files each job was dispatched with.
type contextFileRecorder struct {
	mu    sync.Mutex
	files map[string][]string
}

func (c *contextFileRecorder) Complete(_ context.Context, job *Job, _ *Plan, _ string, opts LLMOptions, _ io.Writer) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.files == nil {
		c.files = make(map[string][]string)
	}
	c.files[job.ID] = append([]string{}, opts.ContextFiles...)
	return "answered", nil
}

func (c *contextFileRecorder) get(jobID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.files[jobID]
}

// newNoContextPlanJob writes a job file into a caller-owned plan directory —
// no project config layer, no git repository, no .grove/rules — and returns the
// loaded plan and job.
func newNoContextPlanJob(t *testing.T, frontmatter string) (*Plan, *Job) {
	t.Helper()
	dir := t.TempDir()
	// A CLAUDE.md next to the plan: no_context must decline the ambient file
	// too, not just the generated bundle.
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("AMBIENT_CLAUDE_MARKER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	jobPath := filepath.Join(dir, "01-self-contained.md")
	content := "---\n" +
		"id: self-contained\n" +
		"title: Self Contained\n" +
		"status: pending\n" +
		"type: oneshot\n" +
		frontmatter +
		"---\n" +
		"Everything I need is in this prompt.\n"
	if err := os.WriteFile(jobPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	job, err := LoadJob(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	job.FilePath = jobPath
	job.Filename = filepath.Base(jobPath)
	return &Plan{Directory: dir, JobsByID: map[string]*Job{job.ID: job}}, job
}

// TestOneShotExecutor_NoContextJobRunsWithoutRulesFile is the end-to-end shape
// the LLM-via-flow plugin pattern needs: a self-contained oneshot dispatched
// into a plan directory the caller owns (state dir, no project layer) reaches
// the provider instead of dying in the unauthored-rules funnel — and reaches it
// carrying no repository context at all.
func TestOneShotExecutor_NoContextJobRunsWithoutRulesFile(t *testing.T) {
	plan, job := newNoContextPlanJob(t, "no_context: true\n")

	t.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", filepath.Join(plan.Directory, "mock-response.txt"))

	client := &contextFileRecorder{}
	executor := NewOneShotExecutor(client, &ExecutorConfig{MaxPromptLength: 1_000_000, Timeout: time.Minute, SkipInteractive: true})

	if err := executor.Execute(context.Background(), job, plan); err != nil {
		t.Fatalf("Execute() error = %v; want a no_context oneshot to run", err)
	}
	if files := client.get(job.ID); len(files) != 0 {
		t.Errorf("ContextFiles = %v; want none for a no_context job", files)
	}
	if _, err := os.Stat(filepath.Join(plan.Directory, ".grove", "rules")); !os.IsNotExist(err) {
		t.Errorf(".grove/rules stat = %v; want no_context to author nothing in the caller's plan dir", err)
	}
}

// TestOneShotExecutor_MissingNamedRulesFileStillFails is the other half of the
// contract: no_context is the only way out of context assembly. A job that
// NAMES a rules file that does not exist still hard-fails before the provider
// call, exactly as before.
func TestOneShotExecutor_MissingNamedRulesFileStillFails(t *testing.T) {
	plan, job := newNoContextPlanJob(t, "rules_file: caller-named-missing.rules\n")

	t.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", filepath.Join(plan.Directory, "mock-response.txt"))

	client := &contextFileRecorder{}
	executor := NewOneShotExecutor(client, &ExecutorConfig{MaxPromptLength: 1_000_000, Timeout: time.Minute, SkipInteractive: true})

	err := executor.Execute(context.Background(), job, plan)
	if err == nil {
		t.Fatal("Execute() = nil error; want the missing caller-named rules file to fail")
	}
	if !strings.Contains(err.Error(), "caller-named-missing.rules") {
		t.Errorf("Execute() error = %q; want it to name the unresolvable rules file", err)
	}
	if files := client.get(job.ID); files != nil {
		t.Errorf("provider was called with %v; want no dispatch at all", files)
	}
}
