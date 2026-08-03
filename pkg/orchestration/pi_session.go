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
// transcript directory. Keeping this in one helper prevents less-common paths
// (notably isolated tmux) from silently falling back to Pi's global session
// directory, where groved would have to resolve the transcript by scanning.
func appendPiJobSessionArgs(spec *AgentProviderSpec, planDir, jobID string, args []string) ([]string, error) {
	if spec == nil || spec.PiRuntime == nil {
		return args, nil
	}
	dir, err := preparePiJobSessionDir(planDir, jobID)
	if err != nil {
		return nil, err
	}
	out := append([]string{}, args...)
	return append(out, "--session-dir", dir), nil
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
