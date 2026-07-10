package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// backdateManifests rewrites every request manifest in an artifact dir to the
// given CreatedAt, so the staleness scan reads deterministic timestamps.
func backdateManifests(t *testing.T, artifactDir string, at time.Time) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(artifactDir, "request-manifest-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatalf("no request manifests in %s to back-date", artifactDir)
	}
	for _, p := range matches {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var m RequestManifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		m.CreatedAt = at
		out, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, out, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func artifactDirOf(plan *Plan, jobID string) string {
	return filepath.Join(plan.Directory, ".artifacts", jobID)
}

// TestLastCacheActivity_MaxAcrossLineage: a child chat's last activity is the
// max RequestManifest.CreatedAt over its OWN dir AND its lineage parent's dir —
// so a parent request more recent than the child's own turn is what warms the
// shared prefix and drives the staleness clock.
func TestLastCacheActivity_MaxAcrossLineage(t *testing.T) {
	plan, parent, child := newLineageExecutorFixture(t, "", "")

	// Run the child's turn so it owns a manifest + inherited layer store.
	if err := NewOneShotExecutor(NewMockLLMClient(), nil).Execute(context.Background(), child, plan); err != nil {
		t.Fatalf("child turn: %v", err)
	}

	now := time.Now().UTC()
	childAt := now.Add(-70 * time.Minute)
	parentAt := now.Add(-30 * time.Minute) // more recent than the child's own turn
	backdateManifests(t, artifactDirOf(plan, child.ID), childAt)
	backdateManifests(t, artifactDirOf(plan, parent.ID), parentAt)

	last, sources, ok := LastCacheActivity(plan.Directory, child)
	if !ok {
		t.Fatal("LastCacheActivity ok = false, want activity")
	}
	if last.Sub(parentAt).Abs() > time.Second {
		t.Errorf("last activity = %v, want the parent's %v (the max across the chain)", last, parentAt)
	}
	// Sources span both the child's own dir and the parent's.
	var sawChild, sawParent bool
	for _, s := range sources {
		if s.JobID == child.ID {
			sawChild = true
		}
		if s.JobID == parent.ID {
			sawParent = true
		}
	}
	if !sawChild || !sawParent {
		t.Errorf("sources missing a dir: sawChild=%v sawParent=%v (%d sources)", sawChild, sawParent, len(sources))
	}
}

// TestLastCacheActivity_WarmReceiptCounts: a warm receipt refreshes the activity
// clock, and one placed in a parent's dir (more recent than any manifest) wins.
func TestLastCacheActivity_WarmReceiptCounts(t *testing.T) {
	plan, parent, child := newLineageExecutorFixture(t, "", "")
	if err := NewOneShotExecutor(NewMockLLMClient(), nil).Execute(context.Background(), child, plan); err != nil {
		t.Fatalf("child turn: %v", err)
	}

	now := time.Now().UTC()
	backdateManifests(t, artifactDirOf(plan, child.ID), now.Add(-70*time.Minute))
	backdateManifests(t, artifactDirOf(plan, parent.ID), now.Add(-60*time.Minute))

	// A warm receipt on the PARENT, more recent than every manifest.
	warmAt := now.Add(-5 * time.Minute)
	if _, err := writeWarmReceipt(plan.Directory, parent.ID, WarmReceipt{CreatedAt: warmAt, Model: "claude-x", ParityOK: true}); err != nil {
		t.Fatal(err)
	}

	last, sources, ok := LastCacheActivity(plan.Directory, child)
	if !ok {
		t.Fatal("LastCacheActivity ok = false, want activity")
	}
	if last.Sub(warmAt).Abs() > time.Second {
		t.Errorf("last activity = %v, want the warm receipt's %v", last, warmAt)
	}
	var sawWarm bool
	for _, s := range sources {
		if s.Kind == "warm" {
			sawWarm = true
		}
	}
	if !sawWarm {
		t.Error("no warm-kind source found; warm receipts must count as activity")
	}
}

// TestLastCacheActivity_OwnDirOnly: a standalone chat (no lineage) reports its
// own manifest's timestamp, and none when it never fired.
func TestLastCacheActivity_OwnDirOnly(t *testing.T) {
	plan, job := firedChatFixture(t)

	at := time.Now().UTC().Add(-40 * time.Minute)
	backdateManifests(t, artifactDirOf(plan, job.ID), at)

	last, _, ok := LastCacheActivity(plan.Directory, job)
	if !ok {
		t.Fatal("LastCacheActivity ok = false, want activity")
	}
	if last.Sub(at).Abs() > time.Second {
		t.Errorf("last activity = %v, want own manifest's %v", last, at)
	}

	// A fresh, never-fired chat (its own plan dir) reports no activity.
	freshPlan, freshJob := newChatJobFixture(t, "", "hi")
	if _, _, ok := LastCacheActivity(freshPlan.Directory, freshJob); ok {
		t.Error("LastCacheActivity ok = true for a never-fired chat, want false")
	}
}

// TestChatCacheStaleness_ThresholdAndTokens: past the 50-min (1h TTL) threshold
// the warning fires with a lineage-prefix token figure; fresh activity stays
// quiet.
func TestChatCacheStaleness_ThresholdAndTokens(t *testing.T) {
	plan, job := firedChatFixture(t)

	// Fresh: just fired, so no warning.
	if msg, stale := ChatCacheStaleness(plan.Directory, job); stale {
		t.Errorf("fresh chat flagged stale: %q", msg)
	}

	// Back-date past the 50-min threshold.
	backdateManifests(t, artifactDirOf(plan, job.ID), time.Now().UTC().Add(-74*time.Minute))
	msg, stale := ChatCacheStaleness(plan.Directory, job)
	if !stale {
		t.Fatal("stale chat not flagged")
	}
	for _, want := range []string{"cache-touching activity", "TTL 1h", "tokens"} {
		if !strings.Contains(msg, want) {
			t.Errorf("staleness message %q missing %q", msg, want)
		}
	}
}
