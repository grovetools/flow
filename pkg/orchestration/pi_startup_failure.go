package orchestration

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/grovetools/core/pkg/process"
)

const maxPiStartupOutputBytes = 32 * 1024

// piStartupEvidence is everything session discovery learned about a Pi launch
// that produced no transcript. captureErr is carried alongside paneOutput on
// purpose: on this path "there is no terminal output" is itself a finding, and
// the reason (typically the PTY being gone) is the most useful thing we can
// tell the user when Pi died before writing anything we can read.
type piStartupEvidence struct {
	pid          int    // 0 when the pidfile never landed — unknown, not dead
	paneOutput   string // last useful pane snapshot, "" when capture never succeeded
	captureErr   error  // why paneOutput is empty
	discoveryErr error  // why transcript discovery gave up
}

// handlePiStartupFailure turns a zero-transcript Pi launch into a durable job
// failure when there is real evidence the launch failed. Pi can fail before it
// creates its JSONL session, so without this bridge the async discovery error
// only reaches Flow's internal log and the status TUI leaves the job looking
// running forever.
//
// Evidence means a PID we discovered that is no longer alive, or an explicit
// startup error in the captured terminal output. An unknown PID on its own is
// inconclusive — the pidfile may simply not have landed for a healthy but slow
// Pi — and marking those jobs failed would kill working sessions. The instant
// death that leaves no pidfile is still caught, because the pane watcher runs
// from spawn and its capture carries Pi's error text (or, failing that, the
// capture error proving the pane is gone).
func handlePiStartupFailure(job *Job, plan *Plan, ev piStartupEvidence) (bool, error) {
	paneOutput := trimPiStartupOutput(ev.paneOutput)
	summary, errKind := piStartupErrorSummary(paneOutput)
	extensionFailure := errKind == piStartupErrorExtension
	// Only a PID we actually discovered can be observed dead. pid <= 0 means
	// "unknown", which is not the same as "exited".
	processExited := ev.pid > 0 && !process.IsProcessAlive(ev.pid)
	if errKind == piStartupErrorNone && !processExited {
		return false, nil
	}

	if summary == "" {
		summary = "Pi exited during startup before creating a transcript"
	}
	if ev.discoveryErr != nil && errKind == piStartupErrorNone {
		summary += ": " + ev.discoveryErr.Error()
	}
	// Never swallow the capture failure: without it a reader cannot tell an
	// empty terminal from a terminal we failed to read.
	if paneOutput == "" {
		summary += " (" + piPaneUnavailableReason(ev.captureErr) + ")"
	}

	// An extension error can leave Pi waiting at a startup screen. Stop that
	// process before clearing the job lock so a retry cannot overlap it.
	if extensionFailure && ev.pid > 0 && process.IsProcessAlive(ev.pid) {
		if proc, err := os.FindProcess(ev.pid); err == nil {
			_ = proc.Signal(os.Interrupt)
		}
	}

	failureText := "**Pi failed during startup before a transcript was created.**\n\n" + summary
	if paneOutput != "" {
		failureText += "\n\nTerminal output:\n\n    " + strings.ReplaceAll(paneOutput, "\n", "\n    ")
	}

	var persistenceErrs []error
	logPath, err := GetJobLogPath(plan, job)
	if err != nil {
		persistenceErrs = append(persistenceErrs, err)
	} else {
		logLine := fmt.Sprintf("%s ERROR %s\n", time.Now().Format(time.RFC3339), summary)
		if paneOutput != "" {
			logLine += "Pi startup terminal output:\n" + paneOutput + "\n"
		}
		if err := appendFile(logPath, logLine); err != nil {
			persistenceErrs = append(persistenceErrs, fmt.Errorf("writing Pi startup failure log: %w", err))
		}
	}

	// Each surface is best-effort, but none may prevent the terminal status
	// transition: a broken artifact write must not recreate the stuck-running
	// bug this path exists to fix.
	persister := NewStatePersister()
	job.Metadata.LastError = summary
	if err := persister.UpdateJobMetadata(job, job.Metadata); err != nil {
		persistenceErrs = append(persistenceErrs, fmt.Errorf("recording Pi startup error: %w", err))
	}
	if _, err := persister.UpdateJobTranscript(job, failureText, false); err != nil {
		persistenceErrs = append(persistenceErrs, fmt.Errorf("recording Pi startup failure transcript: %w", err))
	}
	if err := persister.UpdateJobStatus(job, JobStatusFailed); err != nil {
		persistenceErrs = append(persistenceErrs, fmt.Errorf("marking Pi startup failure: %w", err))
	}
	if err := RemoveLockFile(job.FilePath); err != nil && !os.IsNotExist(err) {
		persistenceErrs = append(persistenceErrs, fmt.Errorf("removing Pi startup lock: %w", err))
	}
	return true, errors.Join(persistenceErrs...)
}

// piStartupErrorKind classifies what the captured pane output proves about a
// launch. Only piStartupErrorExtension triggers the SIGINT cleanup, because
// only that failure leaves Pi parked on a startup screen instead of exiting.
type piStartupErrorKind int

const (
	piStartupErrorNone piStartupErrorKind = iota
	piStartupErrorExtension
	piStartupErrorFatal
)

// piStartupErrorSummary extracts the first line of captured output that proves
// Pi failed to start. Extension failures win over the generic patterns because
// they carry their own remediation (and their own cleanup).
func piStartupErrorSummary(output string) (string, piStartupErrorKind) {
	if summary, ok := piExtensionFailureSummary(output); ok {
		return summary, piStartupErrorExtension
	}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lower, "error:"),
			strings.HasPrefix(lower, "fatal:"),
			strings.Contains(lower, "cannot find module"),
			strings.Contains(lower, "command not found"),
			strings.Contains(lower, "permission denied"):
			return "Pi startup error: " + trimmed, piStartupErrorFatal
		}
	}
	return "", piStartupErrorNone
}

// piPaneUnavailableReason renders why there is no terminal output to show. The
// underlying error (e.g. "pty session ... not found") is the evidence that the
// pane died with Pi, so it is reported verbatim rather than dropped.
func piPaneUnavailableReason(captureErr error) string {
	if captureErr != nil {
		return "terminal output unavailable: " + captureErr.Error()
	}
	return "terminal output unavailable: pane capture returned no output"
}

func piExtensionFailureSummary(output string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "failed to load extension") || strings.Contains(lower, "extension failed to load") {
			return "Pi startup error: " + trimmed, true
		}
	}
	return "", false
}

func trimPiStartupOutput(output string) string {
	output = strings.TrimSpace(output)
	if len(output) <= maxPiStartupOutputBytes {
		return output
	}
	return "[earlier terminal output truncated]\n" + output[len(output)-maxPiStartupOutputBytes:]
}

func appendFile(path, text string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(text)
	return err
}
