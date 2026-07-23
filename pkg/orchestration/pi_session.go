package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
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
