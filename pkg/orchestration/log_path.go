package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
)

// GetJobLogPath returns the path to the log file for a given job.
// The log files are stored in <plan.Directory>/.artifacts/<job.ID>/job.log
// and the directory is created if it doesn't exist.
func GetJobLogPath(plan *Plan, job *Job) (string, error) {
	if plan == nil {
		return "", fmt.Errorf("plan cannot be nil")
	}
	if job == nil {
		return "", fmt.Errorf("job cannot be nil")
	}

	jobArtifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
	if err := os.MkdirAll(jobArtifactDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create job artifact directory: %w", err)
	}

	logPath := filepath.Join(jobArtifactDir, "job.log")
	return logPath, nil
}
