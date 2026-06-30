package orchestration

import (
	"strings"
	"testing"
	"time"
)

func TestBuildCompletionNtfyMessage_Completed(t *testing.T) {
	job := &Job{
		Title:      "Implement notify",
		Repository: "github.com/grovetools/flow",
		Worktree:   "feature-x",
		FilePath:   "/plans/agent-question-state/01-impl.md",
		Duration:   90 * time.Second,
	}

	title, message, tags := buildCompletionNtfyMessage(job, JobStatusCompleted)

	if title != "✅ Implement notify — completed" {
		t.Errorf("unexpected title: %q", title)
	}
	if len(tags) != 1 || tags[0] != "white_check_mark" {
		t.Errorf("unexpected tags: %v", tags)
	}
	wantLines := []string{
		"📂 plan: agent-question-state",
		"🌿 worktree: feature-x",
		"📦 repo: github.com/grovetools/flow",
		"⏱ duration: 1m30s",
	}
	for _, l := range wantLines {
		if !strings.Contains(message, l) {
			t.Errorf("message missing line %q\nmessage:\n%s", l, message)
		}
	}
}

func TestBuildCompletionNtfyMessage_Failed(t *testing.T) {
	job := &Job{Title: "Broken job"}

	title, _, tags := buildCompletionNtfyMessage(job, JobStatusFailed)
	if title != "❌ Broken job — failed" {
		t.Errorf("unexpected title: %q", title)
	}
	if len(tags) != 1 || tags[0] != "rotating_light" {
		t.Errorf("unexpected tags: %v", tags)
	}

	// Abandoned uses the same failed styling.
	titleA, _, _ := buildCompletionNtfyMessage(job, JobStatusAbandoned)
	if titleA != "❌ Broken job — failed" {
		t.Errorf("unexpected abandoned title: %q", titleA)
	}
}

func TestBuildCompletionNtfyMessage_OmitsEmptyAndDuplicateWorktree(t *testing.T) {
	// Worktree equals plan name (the job dir) -> worktree line omitted.
	job := &Job{
		Title:    "Job",
		FilePath: "/plans/myplan/01.md",
		Worktree: "myplan",
	}
	_, message, _ := buildCompletionNtfyMessage(job, JobStatusCompleted)

	if !strings.Contains(message, "📂 plan: myplan") {
		t.Errorf("expected plan line, got:\n%s", message)
	}
	if strings.Contains(message, "🌿 worktree:") {
		t.Errorf("worktree line should be omitted when equal to plan, got:\n%s", message)
	}
	if strings.Contains(message, "📦 repo:") {
		t.Errorf("repo line should be omitted when empty, got:\n%s", message)
	}
	if strings.Contains(message, "⏱ duration:") {
		t.Errorf("duration line should be omitted when zero, got:\n%s", message)
	}
}
