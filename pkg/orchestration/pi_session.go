package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// piJobSessionDir is Flow-owned and job-scoped. Keeping Pi's transcript below
// the guest's .artifacts/<job-id> root makes it returnable without scanning
// guest HOME and keeps .artifacts outside notebook document sync.
func piJobSessionDir(planDir, jobID string) string {
	return filepath.Join(planDir, ".artifacts", jobID, "sessions")
}

func preparePiJobSessionDir(planDir, jobID string) (string, error) {
	dir := piJobSessionDir(planDir, jobID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create Flow-owned Pi session directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure Flow-owned Pi session directory: %w", err)
	}
	return dir, nil
}

// appendPiJobSessionArgs gives every Pi-family launch path the same Flow-owned
// transcript directory, startup-network policy, and session-scoped project
// trust decision. A Flow dispatch is already an explicit request to run the
// agent in this worktree; --approve prevents that unattended launch from
// stalling at Pi's in-pane trust dialog without mutating the user's persistent
// trust.json. Keeping this in one helper prevents less-common paths (notably
// isolated tmux) from drifting.
func appendPiJobSessionArgs(spec *AgentProviderSpec, planDir, jobID string, args []string) ([]string, error) {
	if spec == nil || spec.PiRuntime == nil {
		return args, nil
	}
	dir, err := preparePiJobSessionDir(planDir, jobID)
	if err != nil {
		return nil, err
	}
	out := append([]string{}, args...)
	out = appendPiProjectTrustArg(out)
	out = append(out, "--session-dir", dir)
	return appendPiOfflineStartupArg(out), nil
}

// piProjectTrustArg is Pi's documented session-scoped project trust override.
// Respect an explicit user choice in provider args, including the short forms.
const piProjectTrustArg = "--approve"

func appendPiProjectTrustArg(args []string) []string {
	for _, arg := range args {
		switch arg {
		case "--approve", "-a", "--no-approve", "-na":
			return args
		}
	}
	return append(args, piProjectTrustArg)
}

// piOfflineStartupArg is Pi's documented switch for "disable startup network
// operations" (the CLI equivalent of PI_OFFLINE=1).
const piOfflineStartupArg = "--offline"

// piOnlineStartupEnv restores Pi's online startup for Flow-launched agents.
// Set it to 1/true/yes to opt a machine back out of the mitigation below.
const piOnlineStartupEnv = "GROVE_FLOW_PI_ONLINE_STARTUP"

// appendPiOfflineStartupArg makes Flow-launched Pi skip its startup network
// operations.
//
// Pi's interactive startup awaits network I/O BEFORE it submits the job's first
// prompt: ModelRuntime.create refreshes the pi.dev model catalog (bounded at
// 15s), and InteractiveMode.init then awaits updateAvailableProviderCount()
// twice, each of which re-refreshes the catalog and re-resolves every provider's
// auth with NO timeout of its own. The only bound left is undici's global idle
// timeout, which Pi configures at 300s. So when a fetch hangs rather than fails
// — a laptop that changed networks, a VPN coming up, DNS blackholing — each of
// those two awaits costs a full 5 minutes with the agent already painted on
// screen, unresponsive, before the briefing prompt is ever sent.
//
// Measured on this ecosystem's own transcripts: 11 of 250 Pi job launches
// stalled between the session header and the first prompt, six of them at ~617s
// = 15s + 2x300s, two at ~316s = 15s + 300s. Reproduced by pointing HTTPS_PROXY
// at a socket that accepts and never answers; the proxy log shows exactly the
// pi.dev connections the arithmetic predicts — t=2.7s (create, aborted at 15s),
// t=17.8s, t=318.8s, and init clearing at ~620s. With --offline not one of them
// is attempted. What --offline does NOT gate is the active provider's own auth
// host, which can hang the same way for one more idle timeout; that half is
// upstream-only and Flow cannot reach it from the launch side.
//
// This does not make the mitigation complete — the real fix belongs upstream,
// where the startup refresh should carry a timeout or not gate the first prompt
// — but it removes the pi.dev half of the stall on every Flow launch path, and
// nothing a Flow job needs depends on it: the job's model comes from --model,
// built-in models resolve without the catalog, and the remote overlay stays
// fresh through ordinary interactive `pi` use.
//
// Skipped when the operator already configured --offline via
// [flow.providers.<pi>] args (no duplicate flag) or opted out via
// GROVE_FLOW_PI_ONLINE_STARTUP.
func appendPiOfflineStartupArg(args []string) []string {
	if isTruthyEnv(os.Getenv(piOnlineStartupEnv)) {
		return args
	}
	for _, arg := range args {
		if arg == piOfflineStartupArg {
			return args
		}
	}
	return append(args, piOfflineStartupArg)
}

// isTruthyEnv reads an opt-in environment flag the way Pi itself does: only
// 1/true/yes count, so an exported-but-empty variable is not an opt-in.
func isTruthyEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// piDiscoveryAfterTime resolves the AfterTime filter for a Pi-family launch's
// transcript discovery.
//
// The filter exists so concurrent launches don't race for "newest file", and it
// compares against the session header's OWN timestamp. That is correct for a
// launch that creates its transcript, and wrong for any launch that opens a
// PRE-EXISTING one: a resumed session, or a `responder: pi-session` chat whose
// seed was synthesized moments before the process started. In both cases the
// header timestamp necessarily predates job.StartTime, so an AfterTime filter
// matches nothing, discovery fails, and the session never gets confirmed with
// the daemon — leaving a perfectly healthy agent invisible to liveness checks,
// live-token collection, and `flow plan complete`.
//
// The filter is safe to drop in exactly those cases because the session
// directory is job-scoped: there is no other session in it to race against.
func piDiscoveryAfterTime(spec *AgentProviderSpec, planDir, jobID string, resuming bool, jobStartTime time.Time) time.Time {
	if spec == nil || spec.PiRuntime == nil {
		return jobStartTime
	}
	if resuming || piJobHasExistingSession(planDir, jobID) {
		return time.Time{}
	}
	return jobStartTime
}

// piJobHasExistingSession reports whether the job's Flow-owned session
// directory already holds a transcript — i.e. this launch is opening one rather
// than creating one.
func piJobHasExistingSession(planDir, jobID string) bool {
	entries, err := os.ReadDir(piJobSessionDir(planDir, jobID))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			return true
		}
	}
	return false
}

// requirePiTranscriptPath prevents a Pi-family session from becoming live in
// groved without its exact transcript path. A pending intent is deliberately
// safer than confirming with an empty path: the latter makes the live-token
// collector invoke global transcript resolution on every retry forever.
func requirePiTranscriptPath(spec *AgentProviderSpec, planDir, jobID, transcriptPath string) error {
	if spec == nil || spec.PiRuntime == nil {
		return nil
	}
	if transcriptPath == "" {
		return fmt.Errorf("Pi transcript was not created in Flow-owned session directory")
	}
	rel, err := filepath.Rel(piJobSessionDir(planDir, jobID), transcriptPath)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Pi transcript %q is outside Flow-owned session directory", transcriptPath)
	}
	return nil
}
