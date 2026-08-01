package orchestration

import "context"

// sessionConfirmation is the handle a provider hands back for the work it
// finishes AFTER the agent pane exists: PID capture via the agentstream
// pidfile, transcript discovery, Pi startup-failure diagnosis, and the daemon
// ConfirmSession call that promotes the pending session intent into a live,
// attachable session.
//
// That work runs in a goroutine because it is legitimately slow —
// agentstream.WaitForPID polls for up to 30s and transcript discovery retries
// ten times at one-second intervals — and neither the status TUI nor groved's
// jobrunner can be held for that long.
//
// The handle exists because the goroutine is only safe in a process that
// outlives it. `flow plan resume` prints two lines and exits; on 2026-08-01 a
// resumed grove-agent (job steward-66dd4eb3) died with the CLI before the
// goroutine was ever scheduled — it never reached its own first log line. The
// session sat at status=pending in the daemon store with a dead PTY that
// treemux's assistant pane could not attach to, the agentstream pidfile was
// never cleaned up, and because handlePiStartupFailure lives on this same path,
// pi's dying words went uncaptured while the job file claimed `running`
// forever. A short-lived caller must wait on this handle before returning.
type sessionConfirmation struct {
	done chan struct{}
	err  error
}

// startSessionConfirmation runs confirm in the background and returns the
// handle to wait on. Callers that never wait see exactly the previous
// behaviour: the confirmation still overlaps the rest of the launch.
func startSessionConfirmation(confirm func() error) *sessionConfirmation {
	c := &sessionConfirmation{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		c.err = confirm()
	}()
	return c
}

// wait blocks until the confirmation settles or ctx is done. A nil receiver
// means no confirmation was ever started — the launch failed before that point,
// or the provider registers sessions some other way — which is nothing to wait
// for rather than an error.
func (c *sessionConfirmation) wait(ctx context.Context) error {
	if c == nil {
		return nil
	}
	select {
	case <-c.done:
		return c.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
