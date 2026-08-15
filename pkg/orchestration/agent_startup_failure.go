package orchestration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/core/pkg/process"
	"github.com/grovetools/core/pkg/workspace"
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
	canonical, err := filepath.Abs(workDir)
	if err != nil {
		return false, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(canonical); resolveErr == nil {
		canonical = resolved
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return false, err
	}
	var root struct {
		Projects map[string]map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return false, err
	}
	entry := root.Projects[canonical]
	accepted, _ := entry["hasTrustDialogAccepted"].(bool)
	return accepted, nil
}
