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

	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/sessions"
	"github.com/grovetools/core/pkg/tmux"
	"github.com/grovetools/core/tui/theme"
	"github.com/grovetools/core/util/sanitize"
	"github.com/grovetools/flow/pkg/exec"
	"github.com/sirupsen/logrus"
)

// OpencodeAgentProvider implements InteractiveAgentProvider for the opencode agent.
type OpencodeAgentProvider struct {
	log  *logrus.Entry
	ulog *grovelogging.UnifiedLogger
}

func NewOpencodeAgentProvider() *OpencodeAgentProvider {
	return &OpencodeAgentProvider{
		log:  grovelogging.NewLogger("grove-flow"),
		ulog: grovelogging.NewUnifiedLogger("grove-flow"),
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

	// --- Synchronous Session Registration ---
	// Register the session BEFORE launching the agent to avoid race conditions.
	// The opencode plugin will enrich this session later with the native session ID.
	registry, err := sessions.NewFileSystemRegistry()
	if err != nil {
		p.log.WithError(err).Error("Failed to create session registry")
	} else {
		user := os.Getenv("USER")
		if user == "" {
			user = "unknown"
		}
		repo, branch := getGitInfo(workDir)

		metadata := sessions.SessionMetadata{
			SessionID:        job.ID,
			ClaudeSessionID:  "", // Empty - plugin will fill this with native opencode session ID
			Provider:         "opencode",
			PID:              0, // Will be updated by plugin
			WorkingDirectory: workDir,
			User:             user,
			Repo:             repo,
			Branch:           branch,
			StartedAt:        time.Now(),
			JobTitle:         job.Title,
			PlanName:         plan.Name,
			JobFilePath:      job.FilePath,
			Type:             "interactive_agent",
		}

		p.log.WithFields(logrus.Fields{
			"session_id": job.ID,
			"provider":   "opencode",
			"work_dir":   workDir,
		}).Info("Registering opencode session synchronously")

		if err := registry.Register(metadata); err != nil {
			p.log.WithError(err).Error("Failed to register session")
		} else {
			p.log.Info("Successfully registered opencode session")
		}
	}
	// --- End Synchronous Session Registration ---

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
	p.ulog.Info("Launching OpenCode agent in worktree session").
		Field("window", agentWindowName).
		Field("session", sessionName).
		Pretty(theme.IconWorktree + " Launching OpenCode agent in worktree session").
		Log(ctx)

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

	// NOTE: Session was registered synchronously above. The opencode plugin will
	// enrich the session with the native session ID when it starts.
	// We no longer need the async discoverAndRegisterSession call.

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
				p.ulog.Info("Agent started in session").
					Field("session", sessionName).
					Pretty(fmt.Sprintf("   Agent started in session '%s'. To view, run: tmux switch-client -t %s", sessionName, sessionName)).
					Log(ctx)
			}
		} else {
			p.log.WithError(err).Warn("Could not get current tmux session")
			p.ulog.Info("Agent started in session").
				Field("session", sessionName).
				Pretty(fmt.Sprintf("   Agent started in session '%s'. To view, run: tmux switch-client -t %s", sessionName, sessionName)).
				Log(ctx)
		}
	} else if !isTUIMode {
		p.ulog.Info("Agent session ready").
			Field("session", sessionName).
			Pretty(fmt.Sprintf("   Attach with: tmux attach -t %s", sessionName)).
			Log(ctx)
	}

	if !isTUIMode {
		p.ulog.Info("").Pretty("").Log(ctx) // blank line
		p.ulog.Info("Task completion instructions").
			Pretty(theme.IconArrow + " When your task is complete, run the following:").
			Log(ctx)
		p.ulog.Info("").
			Pretty(fmt.Sprintf("   flow plan complete %s", job.FilePath)).
			Log(ctx)
	}

	return nil
}

func (p *OpencodeAgentProvider) buildAgentCommand(job *Job, briefingFilePath string, agentArgs []string) (string, error) {
	escapedPath := "'" + strings.ReplaceAll(briefingFilePath, "'", "'\\''") + "'"
	prompt := fmt.Sprintf("Read the briefing file at %s and execute the task.", escapedPath)

	// For interactive_agent jobs, use --prompt to keep opencode running for continued interaction
	// For headless/agent jobs, use run subcommand which exits after completing the task
	if job.Type == JobTypeInteractiveAgent {
		cmdParts := []string{"opencode"}
		cmdParts = append(cmdParts, agentArgs...)
		cmdParts = append(cmdParts, "--prompt", fmt.Sprintf("\"%s\"", prompt))
		return strings.Join(cmdParts, " "), nil
	}

	// Headless mode - use 'run' subcommand
	cmdParts := []string{"opencode", "run"}
	cmdParts = append(cmdParts, agentArgs...)
	return fmt.Sprintf("%s \"%s\"", strings.Join(cmdParts, " "), prompt), nil
}

// generateSessionName creates a unique session name for the interactive job (notebook-aware).
func (p *OpencodeAgentProvider) generateSessionName(workDir string) (string, error) {
	projInfo, err := ResolveProjectForSessionNaming(workDir)
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
		}).Warn("Failed to find opencode PID - will register session without PID")
		// Still register the session with provider=opencode so TUI shows correct provider
		// Use pane PID as fallback
		pid = panePID
	}
	log.WithFields(logrus.Fields{
		"opencode_pid": pid,
		"pane_pid":     panePID,
	}).Info("Using PID for session registration")

	// Opencode stores sessions in ~/.local/share/opencode/storage/session/{project_hash}/ses_*.json
	homeDir, _ := os.UserHomeDir()
	opencodeSessionsDir := filepath.Join(homeDir, ".local", "share", "opencode", "storage", "session")
	log.WithField("sessions_dir", opencodeSessionsDir).Info("Searching for opencode session file")

	latestFile, err := findMostRecentOpencodeFile(opencodeSessionsDir)
	var opencodeSessionID string
	if err != nil {
		log.WithError(err).WithField("sessions_dir", opencodeSessionsDir).Warn("Failed to find opencode session file - will register session without transcript path")
		opencodeSessionID = job.ID // Use job ID as fallback
		latestFile = ""
	} else {
		opencodeSessionID = strings.TrimSuffix(filepath.Base(latestFile), filepath.Ext(latestFile))
	}
	log.WithFields(logrus.Fields{
		"session_file":        latestFile,
		"opencode_session_id": opencodeSessionID,
	}).Info("Session file discovery complete")

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
