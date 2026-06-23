package orchestration

import (
	"os"
	"path/filepath"
	"testing"
)

// writeReviewPlan creates a minimal plan directory with one job and the given
// .grove-plan.yml contents, returning the plan dir.
func writeReviewPlan(t *testing.T, planConfig string) string {
	t.Helper()
	dir := t.TempDir()

	job := `---
id: job-1
title: First Job
status: pending
type: oneshot
---
Do the thing.`
	if err := os.WriteFile(filepath.Join(dir, "01-job.md"), []byte(job), 0o600); err != nil {
		t.Fatalf("writing job file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".grove-plan.yml"), []byte(planConfig), 0o600); err != nil {
		t.Fatalf("writing plan config: %v", err)
	}
	return dir
}

func TestMarkPlanReview_FlipsStatusAndPersists(t *testing.T) {
	dir := writeReviewPlan(t, "model: gemini-3.1-pro-preview\n")

	if err := MarkPlanReview(dir); err != nil {
		t.Fatalf("MarkPlanReview() error = %v", err)
	}

	// Status must be persisted to disk: reload and verify.
	reloaded, err := LoadPlan(dir)
	if err != nil {
		t.Fatalf("LoadPlan() after review error = %v", err)
	}
	if reloaded.Config == nil {
		t.Fatal("reloaded plan has nil Config")
	}
	if reloaded.Config.Status != "review" {
		t.Errorf("persisted status = %q, want %q", reloaded.Config.Status, "review")
	}
	// Other fields must be preserved through the save.
	if reloaded.Config.Model != "gemini-3.1-pro-preview" {
		t.Errorf("model not preserved = %q, want %q", reloaded.Config.Model, "gemini-3.1-pro-preview")
	}
}

func TestMarkPlanReview_FiresOnReviewHook(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "hook-fired.txt")

	job := `---
id: job-1
title: First Job
status: pending
type: oneshot
---
Do the thing.`
	if err := os.WriteFile(filepath.Join(dir, "01-job.md"), []byte(job), 0o600); err != nil {
		t.Fatalf("writing job file: %v", err)
	}
	planConfig := "hooks:\n  on_review: \"echo {{.PlanName}} > " + marker + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".grove-plan.yml"), []byte(planConfig), 0o600); err != nil {
		t.Fatalf("writing plan config: %v", err)
	}

	if err := MarkPlanReview(dir); err != nil {
		t.Fatalf("MarkPlanReview() error = %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("on_review hook did not create marker file: %v", err)
	}
	// PlanName is the directory base name; the hook templated it in.
	if got := string(data); got == "" {
		t.Errorf("hook marker empty, expected templated PlanName")
	}

	reloaded, err := LoadPlan(dir)
	if err != nil {
		t.Fatalf("LoadPlan() error = %v", err)
	}
	if reloaded.Config.Status != "review" {
		t.Errorf("status = %q, want review", reloaded.Config.Status)
	}
}

func TestMarkPlanReview_IdempotentWhenAlreadyReview(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "hook-fired.txt")

	job := `---
id: job-1
title: First Job
status: pending
type: oneshot
---
Do the thing.`
	if err := os.WriteFile(filepath.Join(dir, "01-job.md"), []byte(job), 0o600); err != nil {
		t.Fatalf("writing job file: %v", err)
	}
	// Already at review, and a hook that would create a marker if it ran.
	planConfig := "status: review\nhooks:\n  on_review: \"touch " + marker + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".grove-plan.yml"), []byte(planConfig), 0o600); err != nil {
		t.Fatalf("writing plan config: %v", err)
	}

	if err := MarkPlanReview(dir); err != nil {
		t.Fatalf("MarkPlanReview() error = %v", err)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Error("on_review hook fired on an already-review plan; expected no-op")
	}
}
