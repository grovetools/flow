package orchestration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/grovetools/core/pkg/sessions/health"
)

// The session-health engine writes a reconciled status into job frontmatter
// when a job's process is lost. Every status it can write must load back, or
// the reconciler bricks the plan it was trying to repair:
//
//	Plan unavailable: load plan misc-fixes: loading job 100-….md:
//	invalid job status: orphaned
//
// This test walks the actual writer→reader round trip rather than asserting a
// hardcoded list, so a new ReconciledStatusFor result in core fails here
// instead of in a user's plan.
func TestReconciledStatusesRoundTripThroughLoader(t *testing.T) {
	// One session type per branch of ReconciledStatusFor.
	for _, sessionType := range []string{
		"chat", "oneshot", "note", "", // turn-based -> interrupted
		"interactive_agent", "isolated_agent", "agent", "headless_agent", // -> orphaned
	} {
		want := health.ReconciledStatusFor(sessionType)

		t.Run(sessionType+"/"+want, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "01-job.md")
			original := "---\nid: job-1\ntitle: Job One\nstatus: running\ntype: oneshot\n---\nbody\n"
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatalf("write job file: %v", err)
			}

			// The reconciler is the real writer used by the daemon reaper
			// and the hooks TUI alike.
			changed, err := ReconcileJobFile(path, want)
			if err != nil {
				t.Fatalf("ReconcileJobFile(%q): %v", want, err)
			}
			if !changed {
				t.Fatalf("ReconcileJobFile(%q) reported no change", want)
			}

			plan, err := LoadPlan(dir)
			if err != nil {
				t.Fatalf("LoadPlan after reconciling to %q: %v", want, err)
			}
			if len(plan.Jobs) != 1 {
				t.Fatalf("loaded %d jobs, want 1", len(plan.Jobs))
			}
			if got := string(plan.Jobs[0].Status); got != want {
				t.Errorf("loaded status = %q, want %q", got, want)
			}

			// The same status must survive a write through the persister,
			// which keeps its own valid-status list.
			if !isValidStatus(JobStatus(want)) {
				t.Errorf("isValidStatus(%q) = false; StatePersister would refuse to write it back", want)
			}
		})
	}
}

// A reconciled job is unreachable by sanctioned tooling if retry refuses it:
// 'plan run' rejects non-pending jobs and hand-editing frontmatter is
// forbidden, so retry has to be the way out.
func TestRetryAcceptsReconciledStatuses(t *testing.T) {
	for _, status := range []JobStatus{JobStatusOrphaned, JobStatusInterrupted} {
		t.Run(string(status), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "01-job.md")
			content := "---\nid: job-1\ntitle: Job One\nstatus: " + string(status) + "\ntype: oneshot\n---\nbody\n"
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write job file: %v", err)
			}

			plan, err := LoadPlan(dir)
			if err != nil {
				t.Fatalf("LoadPlan: %v", err)
			}
			job := plan.Jobs[0]

			// autoRun=false keeps this off the daemon.
			if err := RetryJob(job, plan, false, false); err != nil {
				t.Fatalf("RetryJob from %q: %v", status, err)
			}
			if job.Status != JobStatusPending {
				t.Errorf("in-memory status = %q, want pending", job.Status)
			}

			reloaded, err := LoadPlan(dir)
			if err != nil {
				t.Fatalf("LoadPlan after retry: %v", err)
			}
			if got := reloaded.Jobs[0].Status; got != JobStatusPending {
				t.Errorf("on-disk status = %q, want pending", got)
			}
		})
	}
}
