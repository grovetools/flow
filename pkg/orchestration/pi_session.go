package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
