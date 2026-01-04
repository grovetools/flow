package orchestration

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	grovelogging "github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-core/pkg/sessions"
	"github.com/mattsolo1/grove-core/pkg/tmux"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/util/sanitize"
	"github.com/mattsolo1/grove-flow/pkg/exec"
	"github.com/sirupsen/logrus"
)

// OpencodeAgentProvider implements InteractiveAgentProvider for the opencode agent.
type OpencodeAgentProvider struct {
	log       *logrus.Entry
	prettyLog *grovelogging.PrettyLogger
}

func NewOpencodeAgentProvider() *OpencodeAgentProvider {
	return &OpencodeAgentProvider{
		log:       grovelogging.NewLogger("grove-flow"),
		prettyLog: grovelogging.NewPrettyLogger(),
	}
}

// Launch implements the InteractiveAgentProvider interface for opencode.
func (p *OpencodeAgentProvider) Launch(ctx context.Context, job *Job, plan *Plan, workDir string, agentArgs []string, briefingFilePath string) error {
	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	tmuxClient, err := tmux.NewClient()
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("tmux not available: %w", err)
	}

	sessionName, err := p.generateSessionName(workDir)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return err
	}

	sessionExists, _ := tmuxClient.SessionExists(ctx, sessionName)
	executor := &exec.RealCommandExecutor{}

	if !sessionExists {
		opts := tmux.LaunchOptions{
			SessionName:      sessionName,
			WorkingDirectory: workDir,
			WindowName:       "workspace",
			Panes: []tmux.PaneOptions{{Command: ""}},
		}
		p.log.WithField("session", sessionName).Info("Creating new tmux session for opencode job")
		if err := tmuxClient.Launch(ctx, opts); err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to create tmux session: %w", err)
		}

		tmuxPID, err := tmuxClient.GetSessionPID(ctx, sessionName)
		if err != nil {
			return fmt.Errorf("could not get tmux session PID: %w", err)
		}
		if err := CreateLockFile(job.FilePath, tmuxPID); err != nil {
			return fmt.Errorf("failed to create lock file: %w", err)
		}
	} else {
		p.log.WithField("session", sessionName).Info("Using existing session for opencode job")
	}

	agentCommand, err := p.buildAgentCommand(job, briefingFilePath, agentArgs)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to build agent command: %w", err)
	}

	agentWindowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)
	p.log.WithField("window", agentWindowName).Info("Creating window for opencode agent")
	p.prettyLog.InfoPretty(fmt.Sprintf("Creating window '%s' for opencode agent...", agentWindowName))

	isTUIMode := os.Getenv("GROVE_FLOW_TUI_MODE") == "true"
	newWindowArgs := []string{"new-window"}
	if isTUIMode {
		newWindowArgs = append(newWindowArgs, "-d")
	}
	newWindowArgs = append(newWindowArgs, "-t", sessionName, "-n", agentWindowName, "-c", workDir)

	if err := executor.Execute("tmux", newWindowArgs...); err != nil {
		p.log.WithError(err).Warn("Failed to create agent window, may already exist.")
	}

	targetPane := fmt.Sprintf("%s:%s", sessionName, agentWindowName)
	escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
	envCommand := fmt.Sprintf("export GROVE_AGENT_PROVIDER='opencode'; export GROVE_FLOW_JOB_ID='%s'; export GROVE_FLOW_JOB_PATH='%s'; export GROVE_FLOW_PLAN_NAME='%s'; export GROVE_FLOW_JOB_TITLE=%s",
		job.ID, job.FilePath, plan.Name, escapedTitle)
	if err := executor.Execute("tmux", "send-keys", "-t", targetPane, envCommand, "C-m"); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to set environment variables: %w", err)
	}

	time.Sleep(100 * time.Millisecond)

	if err := executor.Execute("tmux", "send-keys", "-t", targetPane, agentCommand, "C-m"); err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to send agent command: %w", err)
	}

	go p.discoverAndRegisterSession(job, plan, workDir, targetPane)

	if os.Getenv("TMUX") != "" && !isTUIMode {
		currentSessionCmd := osexec.Command("tmux", "display-message", "-p", "#S")
		currentSessionOutput, err := currentSessionCmd.Output()
		if err == nil {
			currentSession := strings.TrimSpace(string(currentSessionOutput))
			if currentSession == sessionName {
				if err := executor.Execute("tmux", "select-window", "-t", targetPane); err != nil {
					p.log.WithError(err).Warn("Failed to switch to agent window")
				}
			} else {
				p.prettyLog.InfoPretty(fmt.Sprintf("   Agent started in session '%s'. To view, run: tmux switch-client -t %s", sessionName, sessionName))
			}
		} else {
			p.log.WithError(err).Warn("Could not get current tmux session")
			p.prettyLog.InfoPretty(fmt.Sprintf("   Agent started in session '%s'. To view, run: tmux switch-client -t %s", sessionName, sessionName))
		}
	} else if !isTUIMode {
		p.prettyLog.InfoPretty(fmt.Sprintf("   Attach with: tmux attach -t %s", sessionName))
	}

	if !isTUIMode {
		p.prettyLog.Blank()
		p.prettyLog.InfoPretty("👉 When your task is complete, run the following:")
		p.prettyLog.InfoPretty(fmt.Sprintf("   flow plan complete %s", job.FilePath))
	}

	return nil
}

func (p *OpencodeAgentProvider) buildAgentCommand(job *Job, briefingFilePath string, agentArgs []string) (string, error) {
	escapedPath := "'" + strings.ReplaceAll(briefingFilePath, "'", "'\\''") + "'"
	cmdParts := []string{"opencode", "run"}
	cmdParts = append(cmdParts, agentArgs...)
	return fmt.Sprintf("%s \"Read the briefing file at %s and execute the task.\"", strings.Join(cmdParts, " "), escapedPath), nil
}

func (p *OpencodeAgentProvider) generateSessionName(workDir string) (string, error) {
	projInfo, err := workspace.GetProjectByPath(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to get project info: %w", err)
	}
	return projInfo.Identifier(), nil
}

func (p *OpencodeAgentProvider) discoverAndRegisterSession(job *Job, plan *Plan, workDir, targetPane string) {
	log := grovelogging.NewLogger("flow.opencode.session")
	log.WithFields(logrus.Fields{
		"job_id":      job.ID,
		"job_title":   job.Title,
		"target_pane": targetPane,
		"work_dir":    workDir,
	}).Debug("Starting opencode session discovery")

	time.Sleep(2 * time.Second) // Wait for agent to start

	panePID := getPanePID(targetPane)
	log.WithFields(logrus.Fields{
		"target_pane": targetPane,
		"pane_pid":    panePID,
	}).Debug("Got pane PID, searching for opencode descendant process")

	pid, err := findDescendantPID(panePID, "opencode")
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"pane_pid": panePID,
		}).Error("Failed to find opencode PID - descendant process not found")
		return
	}
	log.WithFields(logrus.Fields{
		"opencode_pid": pid,
		"pane_pid":     panePID,
	}).Debug("Found opencode process PID")

	// Opencode stores sessions in ~/.local/share/opencode/storage/session/{project_hash}/ses_*.json
	homeDir, _ := os.UserHomeDir()
	opencodeSessionsDir := filepath.Join(homeDir, ".local", "share", "opencode", "storage", "session")
	log.WithField("sessions_dir", opencodeSessionsDir).Debug("Searching for opencode session file")

	latestFile, err := findMostRecentOpencodeFile(opencodeSessionsDir)
	if err != nil {
		log.WithError(err).WithField("sessions_dir", opencodeSessionsDir).Error("Failed to find opencode session file")
		return
	}
	opencodeSessionID := strings.TrimSuffix(filepath.Base(latestFile), filepath.Ext(latestFile))
	log.WithFields(logrus.Fields{
		"session_file":       latestFile,
		"opencode_session_id": opencodeSessionID,
	}).Debug("Found opencode session file")

	// Get git info
	repo, branch := getGitInfo(workDir)

	metadata := sessions.SessionMetadata{
		SessionID:        job.ID,
		ClaudeSessionID:  opencodeSessionID,
		Provider:         "opencode",
		PID:              pid,
		WorkingDirectory: workDir,
		User:             os.Getenv("USER"),
		Repo:             repo,
		Branch:           branch,
		StartedAt:        time.Now(),
		JobTitle:         job.Title,
		PlanName:         plan.Name,
		JobFilePath:      job.FilePath,
		Type:             "interactive_agent",
		TranscriptPath:   latestFile,
	}

	// Log all metadata fields before registration
	log.WithFields(logrus.Fields{
		"session_id":         metadata.SessionID,
		"claude_session_id":  metadata.ClaudeSessionID,
		"provider":           metadata.Provider,
		"pid":                metadata.PID,
		"working_directory":  metadata.WorkingDirectory,
		"user":               metadata.User,
		"repo":               metadata.Repo,
		"branch":             metadata.Branch,
		"started_at":         metadata.StartedAt,
		"job_title":          metadata.JobTitle,
		"plan_name":          metadata.PlanName,
		"job_file_path":      metadata.JobFilePath,
		"type":               metadata.Type,
		"transcript_path":    metadata.TranscriptPath,
	}).Info("Registering opencode session with grove-hooks registry")

	registry, err := sessions.NewFileSystemRegistry()
	if err != nil {
		log.WithError(err).Error("Failed to create session registry")
		return
	}
	if err := registry.Register(metadata); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"session_id": metadata.SessionID,
			"provider":   metadata.Provider,
		}).Error("Failed to register opencode session")
	} else {
		log.WithFields(logrus.Fields{
			"session_id":         job.ID,
			"opencode_session_id": opencodeSessionID,
			"pid":                pid,
			"provider":           "opencode",
		}).Info("Successfully registered opencode session")
	}
}

// getPanePID gets the PID of a tmux pane
func getPanePID(targetPane string) int {
	cmd := osexec.Command("tmux", "display-message", "-p", "-t", targetPane, "#{pane_pid}")
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	shellPID, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0
	}
	return shellPID
}

// findMostRecentOpencodeFile finds the most recently modified file in a directory
func findMostRecentOpencodeFile(dir string) (string, error) {
	var latestFile string
	var latestModTime time.Time
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.ModTime().After(latestModTime) {
			latestModTime = info.ModTime()
			latestFile = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if latestFile == "" {
		return "", fmt.Errorf("no files found in %s", dir)
	}
	return latestFile, nil
}

// FindOpencodePIDForPane finds the PID of the 'opencode' process running within a specific tmux pane
func FindOpencodePIDForPane(targetPane string) (int, error) {
	cmd := osexec.Command("tmux", "display-message", "-p", "-t", targetPane, "#{pane_pid}")
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to get pane PID: %w", err)
	}

	shellPID, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0, fmt.Errorf("failed to parse pane PID: %w", err)
	}

	return findDescendantPID(shellPID, "opencode")
}
