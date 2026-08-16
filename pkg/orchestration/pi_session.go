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

// piTranscriptLaunch binds transcript discovery to one process launch. Fresh
// launches may only claim files absent before spawn; an explicit resume (or a
// seeded pi-session chat) names the pre-existing transcript it intentionally
// opens.
type piTranscriptLaunch struct {
	baseline         map[string]struct{}
	existingPath     string
	expectedNativeID string
}

// capturePiTranscriptLaunch must run before the agent process is spawned. A
// job-scoped session directory survives retries, so "newest file" alone can
// otherwise confirm a new PID against a prior attempt's transcript while Pi is
// still starting and has not created the current attempt's file yet.
func capturePiTranscriptLaunch(job *Job, planDir, expectedNativeID string) (piTranscriptLaunch, error) {
	launch := piTranscriptLaunch{
		baseline:         make(map[string]struct{}),
		expectedNativeID: strings.TrimSpace(expectedNativeID),
	}
	dir := piJobSessionDir(planDir, job.ID)
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return launch, fmt.Errorf("capturing Pi transcript baseline: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			launch.baseline[entry.Name()] = struct{}{}
		}
	}

	if job.IsPiSessionResponded() {
		desc, descErr := ReadPiSessionDescriptor(planDir, job.ID)
		if descErr != nil {
			return launch, fmt.Errorf("reading Pi session descriptor for transcript discovery: %w", descErr)
		}
		if desc != nil {
			launch.existingPath = desc.SessionFile
		}
	}
	return launch, nil
}

// discoverPiTranscriptForLaunch returns only the transcript owned by launch.
// The explicit-existing branches preserve resume semantics; ordinary retries
// wait until a filename absent from their pre-spawn baseline appears.
func discoverPiTranscriptForLaunch(planDir, jobID string, launch piTranscriptLaunch) (string, error) {
	dir := piJobSessionDir(planDir, jobID)
	if launch.existingPath != "" {
		if _, err := os.Stat(launch.existingPath); err != nil {
			return "", fmt.Errorf("explicit Pi session transcript %s: %w", launch.existingPath, err)
		}
		return launch.existingPath, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("reading Pi session directory: %w", err)
	}
	var latestPath string
	var latestModTime time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if launch.expectedNativeID != "" {
			if piNativeSessionID(path) == launch.expectedNativeID {
				return path, nil
			}
			continue
		}
		if _, existed := launch.baseline[entry.Name()]; existed {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		if latestPath == "" || info.ModTime().After(latestModTime) {
			latestPath = path
			latestModTime = info.ModTime()
		}
	}
	if launch.expectedNativeID != "" {
		return "", fmt.Errorf("Pi session %s not found in %s", launch.expectedNativeID, dir)
	}
	if latestPath == "" {
		return "", fmt.Errorf("no new Pi session files found in %s", dir)
	}
	return latestPath, nil
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
