package orchestration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	stubDefaultNotebook(t)
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
	stubDefaultNotebook(t)
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
	nb := stubDefaultNotebook(t)
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
	// The no-op path must stay side-effect free: the git-viewer roll-up
	// re-fires MarkPlanReview on every changes refresh once every file is
	// reviewed, and each one would otherwise shell out to nb.
	if nb.creates != 0 || nb.writes != 0 {
		t.Errorf("already-review no-op touched the notebook: creates=%d writes=%d", nb.creates, nb.writes)
	}
}

func TestMarkPlanReview_WritesTheReviewPacketOnFlip(t *testing.T) {
	nb := stubDefaultNotebook(t)
	dir := writeReviewPlan(t, "model: gemini-3.1-pro-preview\n")

	if err := MarkPlanReview(dir); err != nil {
		t.Fatalf("MarkPlanReview() error = %v", err)
	}

	if nb.creates != 1 {
		t.Fatalf("packet CreateNote called %d times, want 1", nb.creates)
	}
	if len(nb.notes) != 1 {
		t.Fatalf("notebook holds %d notes, want 1", len(nb.notes))
	}
	if got := readPacketFile(t, nb.notes[0].Path); !strings.Contains(got, reviewPacketMarker) {
		t.Errorf("created note is not a review packet:\n%s", got)
	}
}

func TestMarkPlanReviewWithPacket_RefreshesWhenAlreadyReview(t *testing.T) {
	nb := stubDefaultNotebook(t)
	dir := writeReviewPlan(t, "status: review\n")

	outcome, err := MarkPlanReviewWithPacket(dir)
	if err != nil {
		t.Fatalf("MarkPlanReviewWithPacket() error = %v", err)
	}
	if outcome.Flipped {
		t.Error("an already-review plan reported a flip")
	}
	if outcome.PacketErr != nil {
		t.Fatalf("packet error = %v", outcome.PacketErr)
	}
	// The explicit CLI verb refreshes even when the status does not move —
	// this is how re-running `flow plan review` re-checkpoints the snapshot.
	if !outcome.Packet.Created || nb.creates != 1 {
		t.Errorf("packet not written on the already-review path: created=%v creates=%d", outcome.Packet.Created, nb.creates)
	}

	// And again: the second run must be the idempotent no-write refresh.
	second, err := MarkPlanReviewWithPacket(dir)
	if err != nil {
		t.Fatalf("second MarkPlanReviewWithPacket() error = %v", err)
	}
	if second.Packet.Changed || nb.creates != 1 {
		t.Errorf("second refresh changed=%v creates=%d, want false/1", second.Packet.Changed, nb.creates)
	}
}

func TestMarkPlanReview_PacketFailureDoesNotUnflipThePlan(t *testing.T) {
	nb := stubDefaultNotebook(t)
	nb.listErr = errors.New("notebook unavailable")
	dir := writeReviewPlan(t, "model: gemini-3.1-pro-preview\n")

	err := MarkPlanReview(dir)
	if err == nil {
		t.Fatal("a packet failure must be reported loudly")
	}
	if !strings.Contains(err.Error(), "marked for review") {
		t.Errorf("error = %v, want it to say the plan IS marked for review", err)
	}

	reloaded, loadErr := LoadPlan(dir)
	if loadErr != nil {
		t.Fatalf("LoadPlan() error = %v", loadErr)
	}
	if reloaded.Config.Status != "review" {
		t.Errorf("status = %q after a packet failure, want review (the flip must stand)", reloaded.Config.Status)
	}
}

func TestSetHold_RoundTrip(t *testing.T) {
	dir := writeReviewPlan(t, "model: gemini-3.1-pro-preview\n")

	// Set hold: persisted status flips to "hold", other fields preserved.
	if err := SetHold(dir, true); err != nil {
		t.Fatalf("SetHold(true) error = %v", err)
	}
	reloaded, err := LoadPlan(dir)
	if err != nil {
		t.Fatalf("LoadPlan() after hold error = %v", err)
	}
	if reloaded.Config == nil || reloaded.Config.Status != "hold" {
		t.Fatalf("persisted status after hold = %+v, want %q", reloaded.Config, "hold")
	}
	if reloaded.Config.Model != "gemini-3.1-pro-preview" {
		t.Errorf("model not preserved = %q, want %q", reloaded.Config.Model, "gemini-3.1-pro-preview")
	}

	// Clear hold: status resets to "".
	if err := SetHold(dir, false); err != nil {
		t.Fatalf("SetHold(false) error = %v", err)
	}
	reloaded, err = LoadPlan(dir)
	if err != nil {
		t.Fatalf("LoadPlan() after unhold error = %v", err)
	}
	if reloaded.Config == nil || reloaded.Config.Status != "" {
		t.Errorf("persisted status after unhold = %+v, want empty", reloaded.Config)
	}
	if reloaded.Config != nil && reloaded.Config.Model != "gemini-3.1-pro-preview" {
		t.Errorf("model not preserved through unhold = %q", reloaded.Config.Model)
	}
}

func TestSetHold_ClearDoesNotClobberOtherStatus(t *testing.T) {
	dir := writeReviewPlan(t, "status: review\n")

	// Clearing hold on a plan that is not held must leave the status alone.
	if err := SetHold(dir, false); err != nil {
		t.Fatalf("SetHold(false) error = %v", err)
	}
	reloaded, err := LoadPlan(dir)
	if err != nil {
		t.Fatalf("LoadPlan() error = %v", err)
	}
	if reloaded.Config == nil || reloaded.Config.Status != "review" {
		t.Errorf("status after no-op unhold = %+v, want %q", reloaded.Config, "review")
	}
}

func TestSetHold_NoConfigCreatesOne(t *testing.T) {
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

	if err := SetHold(dir, true); err != nil {
		t.Fatalf("SetHold(true) error = %v", err)
	}
	reloaded, err := LoadPlan(dir)
	if err != nil {
		t.Fatalf("LoadPlan() error = %v", err)
	}
	if reloaded.Config == nil || reloaded.Config.Status != "hold" {
		t.Errorf("status = %+v, want %q", reloaded.Config, "hold")
	}
}
