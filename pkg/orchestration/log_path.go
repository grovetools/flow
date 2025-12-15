package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
)

// GetJobLogPath returns the path to the log file for a given job.
// The log files are stored in <plan.Directory>/.artifacts/logs/jobs/<job.ID>.log
// and the directory is created if it doesn't exist.
func GetJobLogPath(plan *Plan, job *Job) (string, error) {
	if plan == nil {
		return "", fmt.Errorf("plan cannot be nil")
	}
	if job == nil {
		return "", fmt.Errorf("job cannot be nil")
	}

	logsDir := filepath.Join(plan.Directory, ".artifacts", "logs", "jobs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create logs directory: %w", err)
	}

	logPath := filepath.Join(logsDir, fmt.Sprintf("%s.log", job.ID))
	return logPath, nil
}
