package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/grovetools/agentlogs/pkg/agentstream"
	"github.com/grovetools/core/pkg/paths"
)

const supervisedExitReceiptName = "supervised-exit.json"

type sessionEnder interface {
	EndSession(context.Context, string, string, string) error
}

// ReportInteractiveAgentExit owns the terminal handoff for an interactive
// provider supervised by agentstream. A zero provider exit only retires the
// daemon session row; it never completes job frontmatter.
func ReportInteractiveAgentExit(ctx context.Context, planDir, jobID, expectedAttemptID string, exitCode int) error {
	plan, err := LoadPlan(planDir)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	job, ok := plan.GetJobByID(jobID)
	if !ok {
		return fmt.Errorf("job %q not found in plan %s", jobID, planDir)
	}
	client := terminalSessionClientForJob(job, plan)
	defer client.Close()
	return reportInteractiveAgentExit(ctx, job, plan, expectedAttemptID, exitCode, client)
}

func reportInteractiveAgentExit(ctx context.Context, job *Job, plan *Plan, expectedAttemptID string, exitCode int, ender sessionEnder) error {
	// The reporter may run after a retry has already started. Its command was
	// constructed for the provider's original attempt, so it must never load a
	// reused job ID and terminate the newer attempt. Empty remains legacy-only
	// compatibility for supervisors launched before attempt propagation landed.
	if expectedAttemptID != "" && job.AttemptID != expectedAttemptID {
		return nil
	}

	outcome := "exited"
	if exitCode != 0 {
		outcome = "interrupted"
	}
	won, terminalErr := recordInteractiveTerminalOnce(ctx, job, plan, exitCode, outcome, ender)
	if !won || exitCode == 0 {
		return terminalErr
	}

	enriched, classifyErr := currentAttemptHookEnriched(job)
	if classifyErr != nil {
		// Absence must be positively established before changing authoritative
		// frontmatter. An unreadable registry is uncertainty, not startup death.
		return errors.Join(terminalErr, fmt.Errorf("classify current session enrichment: %w", classifyErr))
	}
	if enriched {
		return terminalErr
	}

	diskJob, loadErr := LoadJob(job.FilePath)
	if loadErr != nil {
		return errors.Join(terminalErr, fmt.Errorf("reload job before startup-failure transition: %w", loadErr))
	}
	if diskJob.Status != JobStatusRunning {
		return terminalErr
	}
	diskJob.FilePath = job.FilePath
	summary := fmt.Sprintf("provider exited with code %d before session hooks enriched this attempt", exitCode)
	diskJob.Metadata.LastError = summary
	persister := NewStatePersister()
	var errs []error
	if err := persister.UpdateJobMetadata(diskJob, diskJob.Metadata); err != nil {
		errs = append(errs, fmt.Errorf("record startup exit: %w", err))
	}
	if err := persister.UpdateJobStatus(diskJob, JobStatusFailed); err != nil {
		errs = append(errs, fmt.Errorf("mark startup exit failed: %w", err))
	}
	if err := RemoveLockFile(diskJob.FilePath); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("remove startup lock: %w", err))
	}
	return errors.Join(append([]error{terminalErr}, errs...)...)
}

// recordInteractiveTerminalOnce shares one attempt-scoped receipt between the
// supervisor reporter and startup-failure handlers. The daemon independently
// makes repeated EndSession calls harmless; this receipt additionally keeps
// job.log to one supervised terminal line.
func recordInteractiveTerminalOnce(ctx context.Context, job *Job, plan *Plan, exitCode int, outcome string, ender sessionEnder) (bool, error) {
	if job == nil || plan == nil {
		return false, fmt.Errorf("terminal report requires job and plan")
	}
	dir := filepath.Join(plan.Directory, ".artifacts", job.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	receiptPath := filepath.Join(dir, supervisedExitReceiptName)
	receipt := struct {
		JobID     string    `json:"job_id"`
		ExitCode  int       `json:"exit_code"`
		Outcome   string    `json:"outcome"`
		CreatedAt time.Time `json:"created_at"`
	}{job.ID, exitCode, outcome, time.Now().UTC()}
	data, _ := json.Marshal(receipt)
	f, err := os.OpenFile(receiptPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = f.Write(append(data, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(receiptPath)
		return false, err
	}
	if closeErr != nil {
		_ = os.Remove(receiptPath)
		return false, closeErr
	}

	var errs []error
	if err := persistAgentStartupLog(plan, job, "INFO", fmt.Sprintf("provider exited, code %d, supervised", exitCode)); err != nil {
		errs = append(errs, err)
	}
	if ender != nil {
		endCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if err := ender.EndSession(endCtx, job.ID, job.AttemptID, outcome); err != nil {
			errs = append(errs, fmt.Errorf("end daemon session: %w", err))
		}
		cancel()
	}
	// Do not unlink the PID receipt here. agentstream removes acknowledged
	// receipts after reporting and deliberately retains an unacknowledged
	// fast-exit receipt until discovery or the next attempt's PreparePIDFile.
	return true, errors.Join(errs...)
}

// currentAttemptHookEnriched distinguishes a positively empty registry from an
// unavailable registry. findVerifiedJobSession already enforces exact job path,
// native id, non-empty transcript, and attempt freshness.
func currentAttemptHookEnriched(job *Job) (bool, error) {
	_, err := findVerifiedJobSession(job)
	if err == nil {
		return true, nil
	}
	base := filepath.Join(paths.StateDir(), "hooks", "sessions")
	if _, statErr := os.Stat(base); statErr != nil && os.IsNotExist(statErr) {
		return false, nil
	}
	// Binding rejection is positive absence only when the registry itself is
	// fully readable. A corrupt/unreadable record could be the current attempt,
	// so uncertainty must not change authoritative frontmatter.
	entries, readErr := os.ReadDir(base)
	if readErr != nil {
		return false, readErr
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, metadataErr := os.ReadFile(filepath.Join(base, entry.Name(), "metadata.json"))
		if metadataErr != nil {
			return false, metadataErr
		}
		if !json.Valid(data) {
			return false, fmt.Errorf("invalid session metadata %s", entry.Name())
		}
	}
	return false, nil
}

// InteractiveExitReporterCommand constructs the opaque reporter command passed
// into agentstream's flow-agnostic supervisor.
func buildSupervisedInteractiveCommand(job *Job, plan *Plan, agentCommand string) (string, error) {
	if err := os.Remove(filepath.Join(plan.Directory, ".artifacts", job.ID, supervisedExitReceiptName)); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("prepare supervised exit receipt: %w", err)
	}
	if err := agentstream.PreparePIDFile(job.ID); err != nil {
		return "", fmt.Errorf("prepare provider pid receipt: %w", err)
	}
	return agentstream.BuildSupervisedAgentCommand(
		job.ID,
		"aglogs _supervise-agent",
		agentCommand,
		InteractiveExitReporterCommand(job, plan),
	), nil
}

func withInlineSupervisorEnv(envPrefix, command string) string {
	if envPrefix == "" {
		return command
	}
	// An assignment preceding a compound `rm && exec` list applies only to rm.
	// Put the whole supervisor command under one shell so launch environment is
	// inherited by supervisor, provider, and reporter.
	return envPrefix + "sh -c " + shellSingleQuote(command)
}

func InteractiveExitReporterCommand(job *Job, plan *Plan) string {
	command := "flow agent exited --job " + shellSingleQuote(job.ID) +
		" --plan " + shellSingleQuote(plan.Directory)
	if job.AttemptID != "" {
		command += " --attempt " + shellSingleQuote(job.AttemptID)
	}
	return command + " --exit-code"
}
