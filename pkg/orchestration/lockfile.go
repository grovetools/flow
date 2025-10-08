package orchestration

import (
	"fmt"
	"os"
	"strconv"
)

// lockFileName returns the path for a job's lock file.
func lockFileName(jobFilePath string) string {
	return jobFilePath + ".lock"
}

// CreateLockFile creates a lock file for a job, writing the process ID to it.
func CreateLockFile(jobFilePath string, pid int) error {
	lockFile := lockFileName(jobFilePath)
	content := []byte(strconv.Itoa(pid))
	return os.WriteFile(lockFile, content, 0644)
}

// RemoveLockFile deletes a job's lock file.
func RemoveLockFile(jobFilePath string) error {
	lockFile := lockFileName(jobFilePath)
	// It's not an error if the file doesn't exist.
	err := os.Remove(lockFile)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ReadLockFile reads the PID from a job's lock file.
func ReadLockFile(jobFilePath string) (int, error) {
	lockFile := lockFileName(jobFilePath)
	content, err := os.ReadFile(lockFile)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(string(content))
	if err != nil {
		return 0, fmt.Errorf("invalid PID in lock file: %w", err)
	}
	return pid, nil
}
