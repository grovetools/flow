package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/paths"
	"github.com/sirupsen/logrus"
)

// findAgentSessionInfo finds the PID and session directory for an agent session associated with a job ID.
// It first queries the daemon (which has the correct PID from ConfirmSession), then falls back to
// scanning the filesystem if the daemon is unavailable.
func findAgentSessionInfo(jobID string) (pid int, sessionDir string, err error) {
	log := grovelogging.NewLogger("flow.session.lookup")
	log.WithField("job_id", jobID).Debug("Starting session lookup for job")

	// First try daemon - it has the correct PID from ConfirmSession
	// The filesystem pid.lock can have stale PIDs due to process forking.
	//
	// Host-routed, because ConfirmSession is: the providers register against
	// the daemon owning the interactive host UI, so a scope-resolved lookup
	// here queries a daemon that never saw this session and silently falls
	// through to the filesystem scan (or finds nothing, and the agent is never
	// killed on completion). With no host published this resolves exactly as
	// NewWithAutoStart() did.
	daemonClient := daemon.NewSessionHostClient("")
	if daemonClient != nil {
		daemonRunning := daemonClient.IsRunning()
		log.WithFields(logrus.Fields{
			"job_id":         jobID,
			"daemon_running": daemonRunning,
		}).Debug("Attempting daemon session lookup")

		session, daemonErr := daemonClient.GetSession(context.Background(), jobID)
		if daemonErr == nil && session != nil && session.PID > 0 {
			log.WithFields(logrus.Fields{
				"job_id": jobID,
				"pid":    session.PID,
				"source": "daemon",
			}).Debug("Found session info from daemon")

			// Find sessionDir for backwards compatibility (callers may read metadata from it)
			foundSessionDir := findSessionDirByJobID(jobID)
			daemonClient.Close()
			return session.PID, foundSessionDir, nil
		} else if daemonErr != nil {
			log.WithFields(logrus.Fields{
				"job_id": jobID,
				"error":  daemonErr.Error(),
			}).Debug("Daemon session lookup failed, falling back to filesystem")
		} else if session == nil {
			log.WithFields(logrus.Fields{
				"job_id": jobID,
			}).Debug("Daemon returned nil session, falling back to filesystem")
		} else {
			log.WithFields(logrus.Fields{
				"job_id": jobID,
				"pid":    session.PID,
			}).Debug("Daemon returned session with PID=0, falling back to filesystem")
		}
		daemonClient.Close()
	}

	// Fallback to filesystem scanning
	sessionsDir := filepath.Join(paths.StateDir(), "hooks", "sessions")
	log.WithField("sessions_dir", sessionsDir).Debug("Scanning sessions directory")

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			log.WithField("sessions_dir", sessionsDir).Warn("Sessions directory does not exist")
			return 0, "", fmt.Errorf("sessions directory not found: %s", sessionsDir)
		}
		log.WithError(err).WithField("sessions_dir", sessionsDir).Error("Failed to read sessions directory")
		return 0, "", fmt.Errorf("read sessions directory: %w", err)
	}

	log.WithFields(logrus.Fields{
		"sessions_dir": sessionsDir,
		"entry_count":  len(entries),
	}).Debug("Found session entries")

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		currentSessionDir := filepath.Join(sessionsDir, entry.Name())
		metadataFile := filepath.Join(currentSessionDir, "metadata.json")

		metadataBytes, err := os.ReadFile(metadataFile)
		if err != nil {
			log.WithFields(logrus.Fields{
				"session_dir":   entry.Name(),
				"metadata_file": metadataFile,
				"error":         err.Error(),
			}).Debug("Could not read metadata file, skipping")
			continue
		}

		// Parse full metadata for debugging
		var fullMetadata struct {
			SessionID       string `json:"session_id"`
			ClaudeSessionID string `json:"claude_session_id"`
			Provider        string `json:"provider"`
			PID             int    `json:"pid"`
			JobTitle        string `json:"job_title"`
			PlanName        string `json:"plan_name"`
		}
		if err := json.Unmarshal(metadataBytes, &fullMetadata); err != nil {
			log.WithFields(logrus.Fields{
				"session_dir": entry.Name(),
				"error":       err.Error(),
			}).Debug("Could not parse metadata, skipping")
			continue
		}

		log.WithFields(logrus.Fields{
			"session_dir":         entry.Name(),
			"metadata_session_id": fullMetadata.SessionID,
			"provider":            fullMetadata.Provider,
			"job_title":           fullMetadata.JobTitle,
			"plan_name":           fullMetadata.PlanName,
			"searching_for":       jobID,
			"match":               fullMetadata.SessionID == jobID,
		}).Debug("Checking session metadata against job ID")

		if fullMetadata.SessionID == jobID {
			// Found the session, now get the PID
			log.WithFields(logrus.Fields{
				"job_id":      jobID,
				"session_dir": currentSessionDir,
				"provider":    fullMetadata.Provider,
			}).Debug("Found matching session")

			pidFile := filepath.Join(currentSessionDir, "pid.lock")
			pidBytes, err := os.ReadFile(pidFile)
			if err != nil {
				log.WithFields(logrus.Fields{
					"job_id":   jobID,
					"pid_file": pidFile,
					"error":    err.Error(),
				}).Error("Failed to read PID file for found session")
				return 0, "", fmt.Errorf("read pid file for session %s: %w", jobID, err)
			}

			pidStr := strings.TrimSpace(string(pidBytes))
			pid, err := strconv.Atoi(pidStr)
			if err != nil {
				log.WithFields(logrus.Fields{
					"job_id":  jobID,
					"pid_str": pidStr,
					"error":   err.Error(),
				}).Error("Failed to parse PID from lock file")
				return 0, "", fmt.Errorf("parse pid for session %s: %w", jobID, err)
			}

			// Log at Debug level to avoid log spam from repeated TUI polling
			// (the in-memory deduplication map doesn't work across process boundaries)
			log.WithFields(logrus.Fields{
				"job_id":      jobID,
				"pid":         pid,
				"session_dir": currentSessionDir,
				"provider":    fullMetadata.Provider,
			}).Debug("Found session info for job")

			return pid, currentSessionDir, nil
		}
	}

	log.WithFields(logrus.Fields{
		"job_id":          jobID,
		"sessions_dir":    sessionsDir,
		"entries_checked": len(entries),
	}).Warn("No session found for job ID after checking all entries")

	return 0, "", fmt.Errorf("no session found for job ID: %s", jobID)
}

// findSessionDirByJobID scans the sessions directory to find the session directory
// for a given job ID. Returns empty string if not found.
func findSessionDirByJobID(jobID string) string {
	sessionsDir := filepath.Join(paths.StateDir(), "hooks", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return ""
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		currentSessionDir := filepath.Join(sessionsDir, entry.Name())
		metadataFile := filepath.Join(currentSessionDir, "metadata.json")

		metadataBytes, err := os.ReadFile(metadataFile)
		if err != nil {
			continue
		}

		var metadata struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			continue
		}

		if metadata.SessionID == jobID {
			return currentSessionDir
		}
	}

	return ""
}

// findProcessByJobID uses pgrep to find a process with the job ID in its command line.
// This handles cases where Claude's node process forks/respawns with a new PID.
// Returns 0 if no process found. The calling flow process is excluded: its own
// argv can contain the job ID (e.g. `flow plan retry <job-id>`), and matching
// ourselves would report a live agent that isn't there.
func findProcessByJobID(jobID string) int {
	cmd := exec.Command("pgrep", "-f", jobID)
	output, err := cmd.Output()
	if err != nil {
		return 0
	}

	self := os.Getpid()
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil || pid == self {
			continue
		}
		return pid
	}
	return 0
}

// getWorktreePathFromSession reads the working_directory from the session metadata.
func getWorktreePathFromSession(jobID string) (string, error) {
	_, sessionDir, err := findAgentSessionInfo(jobID)
	if err != nil {
		return "", err
	}

	metadataFile := filepath.Join(sessionDir, "metadata.json")
	metadataBytes, err := os.ReadFile(metadataFile)
	if err != nil {
		return "", fmt.Errorf("could not read metadata from found session: %w", err)
	}

	var metadata struct {
		WorkingDirectory string `json:"working_directory"`
	}
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return "", fmt.Errorf("could not parse metadata from found session: %w", err)
	}

	return metadata.WorkingDirectory, nil
}
