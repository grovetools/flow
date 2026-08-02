package plan_finish

import (
	"errors"
	"strings"
	"testing"

	"github.com/grovetools/core/pkg/worktreeregistry"
	"github.com/grovetools/core/util/pathutil"

	"github.com/grovetools/flow/pkg/orchestration"
)

// addReviewState puts live review marks and a checklist on the fixture's
// registry entry — the SessionState the tombstone is about to strip.
func addReviewState(t *testing.T, container string) {
	t.Helper()
	if err := worktreeregistry.Update(pathutil.WorktreeID(container), func(e *worktreeregistry.Entry) {
		if e.SessionState == nil {
			e.SessionState = map[string]any{}
		}
		e.SessionState["review:core/a.txt"] = map[string]any{"lastReviewedBlobHash": "hash-a"}
		e.SessionState["checklist:cl-1"] = map[string]any{"id": "cl-1", "title": "verify"}
	}); err != nil {
		t.Fatal(err)
	}
}

// recordingPacketWriter is the test seam over orchestration.WriteReviewPacket.
type recordingPacketWriter struct {
	calls  []orchestration.ReviewPacketRequest
	result orchestration.ReviewPacketResult
	err    error
}

func (w *recordingPacketWriter) write(req orchestration.ReviewPacketRequest) (orchestration.ReviewPacketResult, error) {
	w.calls = append(w.calls, req)
	return w.result, w.err
}

func TestReviewCheckpointRunsBeforeTheTombstoneAndEveryRetirement(t *testing.T) {
	bctx, container, _ := setupProvenancePlan(t)
	addReviewState(t, container)

	result, err := BuildItems(bctx, Options{})
	if err != nil {
		t.Fatalf("BuildItems: %v", err)
	}

	checkpoint := indexOf(t, result.Items, ItemReviewCheckpoint)
	tombstone := indexOf(t, result.Items, ItemTombstoneRegistry)
	if checkpoint < 0 {
		t.Fatal("review checkpoint item missing from the built list")
	}
	// The tombstone STRIPS SessionState; checkpointing it afterwards would
	// snapshot nothing.
	if checkpoint > tombstone {
		t.Errorf("checkpoint (index %d) runs after the tombstone (index %d)", checkpoint, tombstone)
	}
	for _, later := range []string{
		ItemArchiveWorktree, ItemPruneWorktree, ItemDeleteLocalBranch, ItemArchivePlan,
	} {
		if at := indexOf(t, result.Items, later); at >= 0 && at < checkpoint {
			t.Errorf("%s (index %d) runs before the review checkpoint (index %d)", later, at, checkpoint)
		}
	}
}

func TestReviewCheckpointItemWritesThroughTheSeam(t *testing.T) {
	bctx, container, planPath := setupProvenancePlan(t)
	addReviewState(t, container)

	writer := &recordingPacketWriter{result: orchestration.ReviewPacketResult{
		Path: "/notes/review/packet.md", Changed: true,
	}}
	bctx.ReviewPacketWriter = writer.write

	result, err := BuildItems(bctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	item := ItemsByID(result.Items, ItemReviewCheckpoint)
	if item == nil {
		t.Fatal("review checkpoint item not built")
	}
	if !item.IsAvailable {
		t.Fatalf("item should be available with live review state, status = %q", item.Status)
	}
	if !strings.Contains(item.Status, "1 file mark(s)") || !strings.Contains(item.Status, "1 checklist(s)") {
		t.Errorf("status = %q, want it to report the live counts", item.Status)
	}
	if err := item.Action(); err != nil {
		t.Fatalf("checkpoint action: %v", err)
	}

	if len(writer.calls) != 1 {
		t.Fatalf("packet writer called %d times, want 1", len(writer.calls))
	}
	req := writer.calls[0]
	if req.PlanDir != planPath {
		t.Errorf("PlanDir = %q, want %q", req.PlanDir, planPath)
	}
	if req.ContainerPath != container {
		t.Errorf("ContainerPath = %q, want the resolved worktree %q", req.ContainerPath, container)
	}
	if req.Trigger != orchestration.ReviewTriggerFinish {
		t.Errorf("Trigger = %q, want %q", req.Trigger, orchestration.ReviewTriggerFinish)
	}
	if req.Plan == nil {
		t.Error("the loaded plan was not passed through; the packet would reload it needlessly")
	}
}

func TestReviewCheckpointFailureBlocksTheTombstone(t *testing.T) {
	bctx, container, _ := setupProvenancePlan(t)
	addReviewState(t, container)

	writer := &recordingPacketWriter{err: errors.New("notebook unavailable")}
	bctx.ReviewPacketWriter = writer.write

	result, err := BuildItems(bctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ItemsByID(result.Items, ItemReviewCheckpoint).Action(); err == nil {
		t.Fatal("a failed checkpoint must fail the item")
	}

	// The whole point: the tombstone strips SessionState, so it must refuse
	// after a failed checkpoint rather than delete un-saved review state.
	tombstoneErr := ItemsByID(result.Items, ItemTombstoneRegistry).Action()
	if tombstoneErr == nil {
		t.Fatal("the tombstone ran after a failed review checkpoint")
	}
	if !strings.Contains(tombstoneErr.Error(), "checkpoint") {
		t.Errorf("tombstone error = %v, want it to name the failed checkpoint", tombstoneErr)
	}

	entry, err := worktreeregistry.Load(pathutil.WorktreeID(container))
	if err != nil {
		t.Fatalf("registry entry unreadable: %v", err)
	}
	if entry.IsFinished() {
		t.Error("the entry was tombstoned despite the failed checkpoint")
	}
	if _, ok := entry.SessionState["review:core/a.txt"]; !ok {
		t.Error("the review mark was stripped despite the failed checkpoint")
	}
}

func TestReviewCheckpointIsInertWithoutLiveReviewState(t *testing.T) {
	// setupProvenancePlan's SessionState holds only unrelated keys.
	bctx, _, _ := setupProvenancePlan(t)
	writer := &recordingPacketWriter{}
	bctx.ReviewPacketWriter = writer.write

	result, err := BuildItems(bctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	item := ItemsByID(result.Items, ItemReviewCheckpoint)
	if item.IsAvailable {
		t.Errorf("no live review state must leave the item inert, status = %q", item.Status)
	}
	if !strings.HasPrefix(item.Status, "None") {
		t.Errorf("status = %q, want a None… status", item.Status)
	}

	// And the tombstone is not gated by an item that never ran.
	if err := ItemsByID(result.Items, ItemTombstoneRegistry).Action(); err != nil {
		t.Errorf("tombstone blocked without any checkpoint failure: %v", err)
	}
}

func TestReviewCheckpointSkippedByOption(t *testing.T) {
	bctx, container, _ := setupProvenancePlan(t)
	addReviewState(t, container)
	writer := &recordingPacketWriter{}
	bctx.ReviewPacketWriter = writer.write

	result, err := BuildItems(bctx, Options{NoReviewCheckpoint: true})
	if err != nil {
		t.Fatal(err)
	}
	item := ItemsByID(result.Items, ItemReviewCheckpoint)
	if item.IsAvailable {
		t.Errorf("--no-review-checkpoint must make the item unavailable, status = %q", item.Status)
	}
	if err := item.Action(); err != nil {
		t.Fatal(err)
	}
	if len(writer.calls) != 0 {
		t.Error("--no-review-checkpoint must not write a packet")
	}
}
