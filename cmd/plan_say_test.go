package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grovetools/flow/pkg/orchestration"
)

// writeChatJob writes a chat job .md into a plan dir and registers it in a plan
// on disk. Returns the plan dir and the job filename.
func writeChatJob(t *testing.T, body string) (planDir, jobFile string) {
	t.Helper()
	planDir = t.TempDir()
	plan := &orchestration.Plan{
		Name: "test-plan",
		Jobs: []*orchestration.Job{
			{ID: "chat-job", Title: "chat-job", Filename: "01-chat-job.md", Type: "chat", Status: "pending_user"},
		},
	}
	if err := orchestration.SavePlan(planDir, plan); err != nil {
		t.Fatal(err)
	}
	// SavePlan writes the job files; overwrite the chat job body with our fixture.
	jobFile = "01-chat-job.md"
	content := "---\nid: chat-job\ntitle: chat-job\nstatus: pending_user\ntype: chat\ntemplate: chat\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(planDir, jobFile), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return planDir, jobFile
}

// TestRunPlanSay covers the CLI core RunPlanSay via the testable struct seam
// (mirroring TestRunPlanAddStep). Text is injected via the Text field so the
// tests do not touch stdin.
func TestRunPlanSay(t *testing.T) {
	t.Run("append to bare trailing marker", func(t *testing.T) {
		planDir, jobFile := writeChatJob(t, "Q\n\n<!-- grove: {\"model\": \"x\"} -->\nAn answer\n\n<!-- grove: {\"template\": \"chat\"} -->")
		c := &PlanSayCmd{
			JobFile: filepath.Join(planDir, jobFile),
			Text:    "Please continue",
		}
		if err := RunPlanSay(c); err != nil {
			t.Fatalf("RunPlanSay: %v", err)
		}
		after, _ := os.ReadFile(filepath.Join(planDir, jobFile))
		if !strings.HasSuffix(strings.TrimSpace(string(after)), "Please continue") {
			t.Errorf("turn not appended as final content:\n%s", string(after))
		}
	})

	t.Run("job not found errors", func(t *testing.T) {
		planDir, _ := writeChatJob(t, "<!-- grove: {\"template\": \"chat\"} -->")
		c := &PlanSayCmd{
			JobFile: filepath.Join(planDir, "99-missing.md"),
			Text:    "hi",
		}
		if err := RunPlanSay(c); err == nil {
			t.Fatal("want a job-not-found error, got nil")
		}
	})

	t.Run("pending turn refuses without force", func(t *testing.T) {
		planDir, jobFile := writeChatJob(t, "<!-- grove: {\"template\": \"chat\"} -->\nAlready pending")
		c := &PlanSayCmd{
			JobFile: filepath.Join(planDir, jobFile),
			Text:    "more",
		}
		if err := RunPlanSay(c); err == nil || !strings.Contains(err.Error(), "already pending") {
			t.Fatalf("want a pending refusal, got %v", err)
		}
	})

	t.Run("force extends pending turn", func(t *testing.T) {
		planDir, jobFile := writeChatJob(t, "<!-- grove: {\"template\": \"chat\"} -->\nAlready pending")
		c := &PlanSayCmd{
			JobFile: filepath.Join(planDir, jobFile),
			Text:    "more",
			Force:   true,
		}
		if err := RunPlanSay(c); err != nil {
			t.Fatalf("RunPlanSay --force: %v", err)
		}
	})
}
