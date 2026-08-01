package orchestration

import (
	"context"

	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/pkg/mux"
)

// TmuxStampVerdict says whether a resolved tmux pane name may be recorded on a
// daemon session as its delivery route.
type TmuxStampVerdict string

const (
	// TmuxStampRecord — the session is genuinely tmux-hosted.
	TmuxStampRecord TmuxStampVerdict = "record"
	// TmuxStampSkip — the session's PTY lives out of process (treemux/tuimux);
	// a tmux target would be fiction.
	TmuxStampSkip TmuxStampVerdict = "skip"
	// TmuxStampVerify — nothing is known about the host, so the pane has to
	// prove it exists before its name is recorded.
	TmuxStampVerify TmuxStampVerdict = "verify"
)

// ClassifyTmuxStamp decides whether a session may carry a tmux pane target.
//
// ResolveInteractiveAgentPane SYNTHESIZES a name from the project and job
// title — it never asks tmux whether that pane exists. Recording it
// unconditionally (what `flow agent claw` used to do, on the theory that it was
// a harmless fallback) is how a treemux-hosted agent ended up with a tmux
// address as its only delivery route: inbound Signal messages then ran
// send-keys against a pane that had never existed, and the operator saw a bare
// 500 from the daemon that owned the agent.
//
// The reason string is for logs — it names the evidence, not the outcome.
func ClassifyTmuxStamp(session *models.Session) (TmuxStampVerdict, string) {
	if session == nil {
		return TmuxStampVerify, "no daemon session record"
	}
	if session.PtyID != "" {
		return TmuxStampSkip, "session has an out-of-process PTY (pty_id " + session.PtyID + ")"
	}
	switch session.Mux {
	case models.MuxTreemux, models.MuxTuimux:
		return TmuxStampSkip, "session mux is " + session.Mux
	case models.MuxTmux:
		return TmuxStampRecord, "session mux is tmux"
	}
	return TmuxStampVerify, "session records no mux"
}

// tmuxPaneIsLive is a seam for tests.
var tmuxPaneIsLive = func(ctx context.Context, target string) bool {
	// Ask tmux specifically, not the ambient mux: this question is only ever
	// "is there a real tmux pane behind this synthesized name", and `flow` is
	// usually itself running inside a treemux/tuimux pane, whose engine would
	// answer about a different address space entirely.
	engine, err := mux.NewTmuxEngineWithSocket(mux.GetTmuxSocketPath())
	if err != nil {
		return false
	}
	exists, err := engine.PaneExists(ctx, target)
	return err == nil && exists
}

// TmuxTargetRecorder is the slice of the daemon client this needs.
type TmuxTargetRecorder interface {
	UpdateSessionTmuxTarget(ctx context.Context, jobID, tmuxTarget string) error
}

// RecordTmuxTargetIfTmuxHosted records targetPane as jobID's tmux delivery
// route, but only when the session really is tmux-hosted. It reports whether
// the target was recorded, alongside the reason either way.
func RecordTmuxTargetIfTmuxHosted(ctx context.Context, client TmuxTargetRecorder, jobID, targetPane string, session *models.Session) (bool, string) {
	if targetPane == "" {
		return false, "no tmux target resolved"
	}
	verdict, reason := ClassifyTmuxStamp(session)
	if verdict == TmuxStampVerify {
		if !tmuxPaneIsLive(ctx, targetPane) {
			return false, reason + "; no live tmux pane at " + targetPane
		}
		verdict, reason = TmuxStampRecord, reason+"; verified a live tmux pane"
	}
	if verdict != TmuxStampRecord {
		return false, reason
	}
	if err := client.UpdateSessionTmuxTarget(ctx, jobID, targetPane); err != nil {
		return false, "recording tmux target failed: " + err.Error()
	}
	return true, reason
}

// RecordTmuxTargetIfTmuxHostedLogged is RecordTmuxTargetIfTmuxHosted plus the
// log line — the form both claw entry points (CLI and status TUI) want.
func RecordTmuxTargetIfTmuxHostedLogged(ctx context.Context, client TmuxTargetRecorder, jobID, targetPane string, session *models.Session) bool {
	recorded, reason := RecordTmuxTargetIfTmuxHosted(ctx, client, jobID, targetPane, session)
	logTmuxStampDecision(jobID, targetPane, recorded, reason)
	return recorded
}

// logTmuxStampDecision emits the decision at a level matched to how surprising
// it is, so a claw that declines to record a target says so in the log rather
// than looking like it did nothing.
func logTmuxStampDecision(jobID, targetPane string, recorded bool, reason string) {
	logger := grovelogging.NewLogger("flow.orchestration.claw").WithFields(map[string]interface{}{
		"job_id":      jobID,
		"tmux_target": targetPane,
		"recorded":    recorded,
		"reason":      reason,
	})
	if recorded {
		logger.Debug("Recorded tmux delivery target")
		return
	}
	logger.Info("Skipped recording tmux delivery target")
}
