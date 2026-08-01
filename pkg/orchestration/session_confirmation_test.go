package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/grovetools/agentlogs/pkg/agentstream"
	"github.com/grovetools/core/pkg/daemon"
)

// TestPreparedResumeConfirmsSessionBeforeReturningToACallerThatExits is the
// orphaned-goroutine regression, observed live on 2026-08-01 with job
// steward-66dd4eb3.
//
// `flow plan resume` launched a native grove-agent pane, printed two lines and
// exited. LaunchPrepared had handed the rest of the lifecycle — PID capture,
// transcript discovery, Pi startup-failure diagnosis, and ConfirmSession — to a
// bare `go func()`, and the process died before the runtime ever scheduled it:
// the goroutine's own first log line never appeared, and the agentstream pidfile
// was still on disk hours later. The daemon was left holding the session at
// status=pending with a dead PTY that treemux's assistant pane could not attach
// to, while pi's startup error went unrecorded and the job file still claimed
// `running`.
//
// The pidfile is written on a delay here because that is what makes the bug
// reachable in the first place: the wrapper only writes it once the pane has
// actually spawned, so discovery is still inside agentstream.WaitForPID at the
// moment Launch returns.
func TestPreparedResumeConfirmsSessionBeforeReturningToACallerThatExits(t *testing.T) {
	f := newHostedLaunchFixture(t)
	t.Setenv(daemon.HostSocketEnv, f.host.socketPath)

	// A transcript discovery can resolve, so the only thing standing between
	// the goroutine and ConfirmSession is the pidfile wait below.
	projects := filepath.Join(f.home, ".claude", "projects", agentstream.SanitizePathForClaude(f.workDir))
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatalf("mkdir projects: %v", err)
	}
	transcriptPath := filepath.Join(projects, "0199f81a-dead-beef-0000-000000000002.jsonl")
	line := fmt.Sprintf("{\"timestamp\":%q}\n", time.Now().Add(time.Minute).UTC().Format(time.RFC3339))
	if err := os.WriteFile(transcriptPath, []byte(line), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(agentstream.PidFilePath(f.job.ID)), 0o755); err != nil {
		t.Fatalf("mkdir runtime: %v", err)
	}
	f.job.StartTime = time.Now().Add(-time.Minute)

	pidWritten := make(chan struct{})
	go func() {
		defer close(pidWritten)
		time.Sleep(500 * time.Millisecond)
		_ = os.WriteFile(agentstream.PidFilePath(f.job.ID), []byte("4242\n"), 0o600)
	}()
	t.Cleanup(func() { <-pidWritten })

	spec, ok := LookupAgentProvider("claude")
	if !ok {
		t.Fatal("claude provider spec missing from registry")
	}
	prepared := &PreparedInteractiveAgentResume{
		provider:         NewGrovetermAgentProvider(spec, false, "native"),
		job:              f.job,
		plan:             f.plan,
		workDir:          f.workDir,
		shellCommand:     "claude --resume native-1",
		expectedNativeID: "native-1",
	}

	if err := prepared.Launch(context.Background()); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// This is the exact instant the CLI used to print its last line and exit.
	// If confirmation had already landed the test would prove nothing.
	if _, _, hc := f.host.counts(); hc != 0 {
		t.Fatalf("host daemon already had %d confirmations when Launch returned; the fixture is not reproducing the window the bug lived in", hc)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := prepared.AwaitSessionConfirmation(ctx); err != nil {
		t.Fatalf("AwaitSessionConfirmation: %v", err)
	}

	_, _, hc := f.host.counts()
	if hc != 1 {
		t.Fatalf("host daemon received %d confirmations once the resume path was ready to exit, want 1: the session would be left pending forever", hc)
	}
	f.host.mu.Lock()
	confirm := f.host.confirms[0]
	f.host.mu.Unlock()
	if confirm.JobID != f.job.ID {
		t.Fatalf("confirm JobID = %q, want %q", confirm.JobID, f.job.ID)
	}
	if confirm.PID != 4242 {
		t.Fatalf("confirm PID = %d, want the pidfile's 4242", confirm.PID)
	}
	if confirm.TranscriptPath != transcriptPath {
		t.Fatalf("confirm TranscriptPath = %q, want %q", confirm.TranscriptPath, transcriptPath)
	}
}

// TestSessionConfirmationWaitPropagatesFailureAndCancellation pins the two
// answers the resume caller has to distinguish: a confirmation that ran and
// failed (report it, the agent is still live) versus one that outlived its
// bound (report that, and never block a terminal indefinitely).
func TestSessionConfirmationWaitPropagatesFailureAndCancellation(t *testing.T) {
	t.Run("nil handle is nothing to wait for", func(t *testing.T) {
		var c *sessionConfirmation
		if err := c.wait(context.Background()); err != nil {
			t.Fatalf("wait() on nil handle = %v, want nil", err)
		}
	})

	t.Run("propagates the confirmation error", func(t *testing.T) {
		want := fmt.Errorf("confirm session: daemon unreachable")
		c := startSessionConfirmation(func() error { return want })
		if err := c.wait(context.Background()); err != want {
			t.Fatalf("wait() = %v, want %v", err, want)
		}
	})

	t.Run("returns when the caller's bound expires", func(t *testing.T) {
		release := make(chan struct{})
		c := startSessionConfirmation(func() error {
			<-release
			return nil
		})
		defer close(release)

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := c.wait(ctx); err != context.DeadlineExceeded {
			t.Fatalf("wait() = %v, want context.DeadlineExceeded", err)
		}
	})
}
