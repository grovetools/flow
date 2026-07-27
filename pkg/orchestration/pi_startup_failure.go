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

// handlePiStartupFailure turns a zero-transcript Pi launch into a durable job
// failure when the process has exited (or Pi explicitly reported an extension
// loading failure). Pi can fail before it creates its JSONL session, so without
// this bridge the async discovery error only reaches Flow's internal log and the
// status TUI leaves the job looking running forever.
func handlePiStartupFailure(job *Job, plan *Plan, pid int, paneOutput string, discoveryErr error) (bool, error) {
	paneOutput = trimPiStartupOutput(paneOutput)
	summary, extensionFailure := piExtensionFailureSummary(paneOutput)
	processExited := pid <= 0 || !process.IsProcessAlive(pid)
	if !extensionFailure && !processExited {
		return false, nil
	}

	if summary == "" {
		summary = "Pi exited during startup before creating a transcript"
	}
	if discoveryErr != nil && !extensionFailure {
		summary += ": " + discoveryErr.Error()
	}

	// An extension error can leave Pi waiting at a startup screen. Stop that
	// process before clearing the job lock so a retry cannot overlap it.
	if extensionFailure && pid > 0 && process.IsProcessAlive(pid) {
		if proc, err := os.FindProcess(pid); err == nil {
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
