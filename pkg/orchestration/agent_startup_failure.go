package orchestration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/grovetools/core/pkg/claudetrust"
	"github.com/grovetools/core/pkg/process"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/util/pathutil"
)

const (
	maxAgentStartupOutputBytes = 32 * 1024
	claudeStartupFailureReason = "provider exited before session start; no hooks fired — likely trust/permission prompt"
)

// agentStartupEvidence is provider-neutral evidence captured before a provider
// has produced a hook-enriched session record. Phase 0 deliberately persists a
// breadcrumb only: callers must not change job status or end daemon sessions.
type agentStartupEvidence struct {
	pid          int
	paneOutput   string
	captureErr   error
	discoveryErr error
}

// appendAgentStartupBreadcrumb records a pre-hook provider exit in the exact
// artifact rendered by the flow `vl` pane. A missing/unknown PID is not proof
// of exit and is therefore left alone.
func appendAgentStartupBreadcrumb(job *Job, plan *Plan, provider string, ev agentStartupEvidence) (bool, error) {
	if ev.pid <= 0 || process.IsProcessAlive(ev.pid) {
		return false, nil
	}
	paneOutput := trimAgentStartupOutput(ev.paneOutput)
	headline := claudeStartupFailureReason
	if provider != "" {
		headline += fmt.Sprintf(" (provider=%s)", provider)
	}
	var details []string
	if paneOutput != "" {
		details = append(details, "Provider startup terminal output:\n"+paneOutput)
	} else if ev.captureErr != nil {
		details = append(details, "Terminal output unavailable: "+ev.captureErr.Error())
	}
	if ev.discoveryErr != nil {
		details = append(details, "Session discovery error: "+ev.discoveryErr.Error())
	}
	if err := persistAgentStartupLog(plan, job, "ERROR", headline, details...); err != nil {
		return false, fmt.Errorf("writing %s startup breadcrumb: %w", provider, err)
	}
	return true, nil
}

// handleAgentStartupFailure is the Phase 1 behavior layered over the Phase 0
// breadcrumb: positive pre-hook death also retires the daemon intent and makes
// the on-disk running job failed. The shared terminal receipt prevents the
// supervisor reporter from emitting a second terminal line/event.
func handleAgentStartupFailure(job *Job, plan *Plan, provider string, ev agentStartupEvidence) (bool, error) {
	handled, breadcrumbErr := appendAgentStartupBreadcrumb(job, plan, provider, ev)
	if !handled {
		return false, breadcrumbErr
	}
	client := terminalSessionClientForJob(job, plan)
	defer client.Close()
	completed, terminalErr := recordInteractiveTerminalOnce(context.Background(), job, plan, 1, "failed", client)
	// A transient reporting failure must remain a pure retry: do not publish
	// terminal frontmatter before the required daemon effect has succeeded.
	// completed=false with no error means another reporter already completed the
	// effects, so the idempotent frontmatter convergence below is still safe.
	if !completed && terminalErr != nil {
		return true, errors.Join(breadcrumbErr, terminalErr)
	}

	diskJob, loadErr := LoadJob(job.FilePath)
	if loadErr != nil {
		return true, errors.Join(breadcrumbErr, terminalErr, loadErr)
	}
	if diskJob.Status != JobStatusRunning {
		return true, errors.Join(breadcrumbErr, terminalErr)
	}
	diskJob.FilePath = job.FilePath
	diskJob.Metadata.LastError = claudeStartupFailureReason
	persister := NewStatePersister()
	var stateErrs []error
	if err := persister.UpdateJobMetadata(diskJob, diskJob.Metadata); err != nil {
		stateErrs = append(stateErrs, err)
	}
	if err := persister.UpdateJobStatus(diskJob, JobStatusFailed); err != nil {
		stateErrs = append(stateErrs, err)
	}
	if err := RemoveLockFile(diskJob.FilePath); err != nil && !os.IsNotExist(err) {
		stateErrs = append(stateErrs, err)
	}
	return true, errors.Join(append([]error{breadcrumbErr, terminalErr}, stateErrs...)...)
}

// persistAgentStartupLog is the shared durable launch-path writer used by all
// providers. Keeping GetJobLogPath and append semantics here makes startup
// diagnostics symmetrical without coupling Claude to Pi's status transition.
func persistAgentStartupLog(plan *Plan, job *Job, level, headline string, details ...string) error {
	logPath, err := GetJobLogPath(plan, job)
	if err != nil {
		return err
	}
	line := fmt.Sprintf("%s %s %s\n", time.Now().Format(time.RFC3339), level, headline)
	for _, detail := range details {
		if detail != "" {
			line += detail + "\n"
		}
	}
	return appendFile(logPath, line)
}

func trimAgentStartupOutput(output string) string {
	output = strings.TrimSpace(output)
	if len(output) <= maxAgentStartupOutputBytes {
		return output
	}
	return "[earlier terminal output truncated]\n" + output[len(output)-maxAgentStartupOutputBytes:]
}

// warnIfClaudeFolderUntrusted implements the observability-only half of the
// Claude trust preflight. It never blocks or seeds trust in Phase 0.
func warnIfClaudeFolderUntrusted(job *Job, plan *Plan, workDir string) error {
	trusted, err := claudeFolderTrusted(workDir)
	if err == nil && trusted {
		return nil
	}
	managed := workspace.WorktreeManagesTrust(workDir, nil)
	fix := "accept the Claude folder-trust prompt once, or enable [claude] manageTrust = true in a trusted grove.toml"
	if managed {
		fix = "[claude] manageTrust is enabled; run grove workspace preparation/reconcile and verify ~/.claude.json"
	}
	logPath, pathErr := GetJobLogPath(plan, job)
	if pathErr != nil {
		return pathErr
	}
	reason := "folder trust is missing"
	if err != nil {
		reason = "folder trust could not be verified: " + err.Error()
	}
	line := fmt.Sprintf("%s WARN Claude %s for %s; launch may stop at the trust dialog; fix: %s\n",
		time.Now().Format(time.RFC3339), reason, workDir, fix)
	if err := appendFile(logPath, line); err != nil {
		return fmt.Errorf("writing Claude trust preflight warning: %w", err)
	}
	return nil
}

func claudeFolderTrusted(workDir string) (bool, error) {
	canonical, err := pathutil.CanonicalPath(workDir)
	if err != nil {
		return false, err
	}
	return claudetrust.IsTrusted(canonical)
}

// enforceClaudeFolderTrust prevents Claude from entering an interactive trust
// dialog before hooks can identify the session. It must run before status,
// intent, pane, lock, or process side effects.
func enforceClaudeFolderTrust(ctx context.Context, job *Job, plan *Plan, workDir string) error {
	canonical, err := pathutil.CanonicalPath(workDir)
	if err != nil {
		return refuseClaudeTrust(job, plan, workDir, fmt.Errorf("canonicalize cwd: %w", err))
	}
	trusted, checkErr := claudetrust.IsTrusted(canonical)
	if checkErr == nil && trusted {
		return nil
	}
	if !workspace.WorktreeManagesTrust(canonical, nil) {
		if checkErr != nil {
			return refuseClaudeTrust(job, plan, canonical, fmt.Errorf("verify trust: %w", checkErr))
		}
		return refuseClaudeTrust(job, plan, canonical, nil)
	}

	seedErr := claudetrust.SeedTrust(canonical)
	if seedErr != nil {
		// Sandboxed launchers may not write ~/.claude.json directly. Delegate to
		// the existing managed daemon path rather than adding another writer.
		seedErr = NewTrustSeedFallback(plan.Directory)(ctx, canonical)
	}
	trusted, checkErr = claudetrust.IsTrusted(canonical)
	if seedErr != nil || checkErr != nil || !trusted {
		return refuseClaudeTrust(job, plan, canonical, errors.Join(seedErr, checkErr, fmt.Errorf("managed trust seed did not establish the exact cwd key")))
	}
	return nil
}

func refuseClaudeTrust(job *Job, plan *Plan, workDir string, cause error) error {
	fix := "accept Claude folder trust manually, or enable [claude] manageTrust = true in a trusted grove.toml"
	message := fmt.Sprintf("refusing Claude launch: folder trust is missing for %s; fix: %s", workDir, fix)
	if cause != nil {
		message += "; error: " + cause.Error()
	}
	if logErr := persistAgentStartupLog(plan, job, "ERROR", message); logErr != nil {
		return errors.Join(errors.New(message), logErr)
	}
	return errors.New(message)
}
