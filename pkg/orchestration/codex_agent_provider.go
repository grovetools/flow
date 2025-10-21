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

type CodexAgentProvider struct {
	log       *logrus.Entry
	prettyLog *grovelogging.PrettyLogger
}

func NewCodexAgentProvider() *CodexAgentProvider {
	return &CodexAgentProvider{
		log:       grovelogging.NewLogger("grove-flow"),
		prettyLog: grovelogging.NewPrettyLogger(),
	}
}

func (p *CodexAgentProvider) Launch(ctx context.Context, job *Job, plan *Plan, workDir string, agentArgs []string) error {
	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	// Regenerate context before launching the agent
	// We'll use the helper from the oneshot executor
	oneShotExec := NewOneShotExecutor(nil)
	if err := oneShotExec.regenerateContextInWorktree(workDir, "interactive-agent", job, plan); err != nil {
		// A context failure shouldn't block an interactive session, but we should warn the user.
		p.log.WithError(err).Warn("Failed to generate job-specific context for interactive session")
		p.prettyLog.WarnPretty(fmt.Sprintf("Warning: Failed to generate job-specific context: %v", err))
	}

	// Create tmux client
	tmuxClient, err := tmux.NewClient()
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("tmux not available: %w", err)
	}

	// Generate session name
	sessionName, err := p.generateSessionName(workDir)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return err
	}

	// Check if session already exists
	sessionExists, _ := tmuxClient.SessionExists(ctx, sessionName)
	executor := &exec.RealCommandExecutor{}

	if !sessionExists {
		// Create new session with a blank "workspace" window
		opts := tmux.LaunchOptions{
			SessionName:      sessionName,
			WorkingDirectory: workDir,
			WindowName:       "workspace",
			Panes: []tmux.PaneOptions{
				{
					Command: "", // Empty command = default shell
				},
			},
		}
		p.log.WithField("session", sessionName).Info("Creating new tmux session for interactive job")
		if err := tmuxClient.Launch(ctx, opts); err != nil {
			job.Status = JobStatusFailed
			job.EndTime = time.Now()
			return fmt.Errorf("failed to create tmux session: %w", err)
		}

		// Get the tmux session PID and create the lock file.
		tmuxPID, err := tmuxClient.GetSessionPID(ctx, sessionName)
		if err != nil {
			return fmt.Errorf("could not get tmux session PID to create lock file: %w", err)
		}
		if err := CreateLockFile(job.FilePath, tmuxPID); err != nil {
			return fmt.Errorf("failed to create lock file with tmux PID: %w", err)
		}
	} else {
		p.log.WithField("session", sessionName).Info("Using existing session for interactive job")
	}

	// Build agent command (reuse Claude provider's logic but replace "claude" with "codex")
	agentCommand, err := p.buildAgentCommand(job, plan, workDir, agentArgs)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to build agent command: %w", err)
	}

	// Create a new window for this specific agent job in the session
	agentWindowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)

	p.log.WithField("window", agentWindowName).Info("Creating window for agent")
	p.prettyLog.InfoPretty(fmt.Sprintf("Creating window '%s' for agent...", agentWindowName))
	if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", agentWindowName, "-c", workDir); err != nil {
		p.log.WithError(err).Warn("Failed to create agent window, may already exist. Will attempt to use it.")
		// Don't return error, just log and proceed.
	}

	// Set environment variables in the window's shell so they're available to the codex process
	targetPane := fmt.Sprintf("%s:%s", sessionName, agentWindowName)

	// Export environment variables in the window's shell
	envCommand := fmt.Sprintf("export GROVE_FLOW_JOB_ID=%s GROVE_FLOW_JOB_PATH=%s GROVE_FLOW_PLAN_NAME=%s GROVE_FLOW_JOB_TITLE=%s",
		job.ID, job.FilePath, plan.Name, job.Title)
	if err := executor.Execute("tmux", "send-keys", "-t", targetPane, envCommand, "C-m"); err != nil {
		p.log.WithError(err).Error("Failed to set environment variables")
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to set environment variables: %w", err)
	}

	// Small delay to ensure environment variables are set
	time.Sleep(100 * time.Millisecond)

	// Send the agent command to the new window
	if err := executor.Execute("tmux", "send-keys", "-t", targetPane, agentCommand, "C-m"); err != nil {
		p.log.WithError(err).Error("Failed to send agent command")
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to send agent command: %w", err)
	}

	// Discover and register the session in a background goroutine
	go p.discoverAndRegisterSession(job, plan, workDir, targetPane)

	// Switch to the agent window if inside tmux
	if os.Getenv("TMUX") != "" {
		if err := executor.Execute("tmux", "switch-client", "-t", sessionName); err != nil {
			p.log.WithError(err).Warn("Failed to switch to session")
		}
		if err := executor.Execute("tmux", "select-window", "-t", targetPane); err != nil {
			p.log.WithError(err).Warn("Failed to switch to agent window")
		}
	} else {
		p.prettyLog.InfoPretty(fmt.Sprintf("   Attach with: tmux attach -t %s", sessionName))
	}

	p.prettyLog.Blank()
	p.prettyLog.InfoPretty("👉 When your task is complete, run the following in any terminal:")
	p.prettyLog.InfoPretty(fmt.Sprintf("   flow plan complete %s", job.FilePath))
	p.prettyLog.Blank()
	p.prettyLog.InfoPretty("   The session can remain open - the plan will continue automatically.")

	// Return immediately. The lock file indicates the running state.
	return nil
}

// buildAgentCommand constructs the codex command for the interactive session.
func (p *CodexAgentProvider) buildAgentCommand(job *Job, plan *Plan, worktreePath string, agentArgs []string) (string, error) {
	// Build instruction for the agent
	var instructionBuilder strings.Builder

	// Add dependency files if the job has dependencies
	if len(job.Dependencies) > 0 {
		var depFiles []string
		for _, dep := range job.Dependencies {
			if dep != nil && dep.FilePath != "" {
				depFiles = append(depFiles, dep.FilePath)
			}
		}

		if job.PrependDependencies {
			// New logic: Emphasize dependencies
			instructionBuilder.WriteString(fmt.Sprintf("CRITICAL CONTEXT: Before you do anything else, you MUST read and fully understand the content of the following files in order. They provide the primary context and requirements for your task: %s. ", strings.Join(depFiles, ", ")))
			instructionBuilder.WriteString(fmt.Sprintf("After you have processed that context, execute the agent job defined in %s. ", job.FilePath))
		} else {
			// Original logic
			instructionBuilder.WriteString(fmt.Sprintf("Read the file %s and execute the agent job defined there. ", job.FilePath))
			instructionBuilder.WriteString("For additional context from previous jobs, also read: ")
			instructionBuilder.WriteString(strings.Join(depFiles, ", "))
			instructionBuilder.WriteString(". ")
		}
	} else {
		// No dependencies, original logic
		instructionBuilder.WriteString(fmt.Sprintf("Read the file %s and execute the agent job defined there. ", job.FilePath))
	}

	// Add context files if specified
	if len(job.PromptSource) > 0 {
		instructionBuilder.WriteString("Also read these context files: ")
		var contextFiles []string
		for _, source := range job.PromptSource {
			resolved, err := ResolvePromptSource(source, plan)
			relPath := source // fallback
			if err == nil {
				if p, err := filepath.Rel(worktreePath, resolved); err == nil {
					relPath = p
				} else {
					relPath = resolved
				}
			}
			contextFiles = append(contextFiles, relPath)
		}
		instructionBuilder.WriteString(strings.Join(contextFiles, ", "))
	}

	instruction := instructionBuilder.String()

	// Shell escape the instruction
	escapedInstruction := "'" + strings.ReplaceAll(instruction, "'", "'\\''") + "'"

	// Build command with agent args - use "codex" instead of "claude"
	cmdParts := []string{"codex"}
	if job.AgentContinue {
		cmdParts = append(cmdParts, "--continue")
		cmdParts = append(cmdParts, agentArgs...)
		return fmt.Sprintf("echo %s | %s", escapedInstruction, strings.Join(cmdParts, " ")), nil
	}

	cmdParts = append(cmdParts, agentArgs...)
	cmdParts = append(cmdParts, escapedInstruction)

	return strings.Join(cmdParts, " "), nil
}

// generateSessionName creates a unique session name for the interactive job.
func (p *CodexAgentProvider) generateSessionName(workDir string) (string, error) {
	projInfo, err := workspace.GetProjectByPath(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to get project info for session naming: %w", err)
	}
	return projInfo.Identifier(), nil
}

// discoverAndRegisterSession discovers the codex session ID and registers it with grove-core
func (p *CodexAgentProvider) discoverAndRegisterSession(job *Job, plan *Plan, workDir, targetPane string) {
	// Wait a moment for the log file to be created.
	time.Sleep(2 * time.Second)

	codexSessionsDir := filepath.Join(os.Getenv("HOME"), ".codex", "sessions")
	latestFile, err := findMostRecentFile(codexSessionsDir)
	if err != nil {
		p.log.WithError(err).Error("Failed to find most recent codex session file")
		return
	}

	// Parse session ID from filename: rollout-2025-10-20T16-43-18-019a035c-b544-7552-b739-8573c821aaea.jsonl
	base := filepath.Base(latestFile)
	parts := strings.Split(strings.TrimSuffix(base, ".jsonl"), "-")
	if len(parts) < 6 {
		p.log.Error("Failed to parse session ID from codex log filename")
		return
	}
	// UUID is the last 5 dash-separated segments
	codexSessionID := strings.Join(parts[len(parts)-5:], "-")

	// Find the PID of the codex process.
	pid, err := findCodexPIDForWindow(targetPane)
	if err != nil {
		p.log.WithError(err).Error("Failed to find codex PID")
		return
	}

	// Register the session using the core registry.
	registry, err := sessions.NewFileSystemRegistry()
	if err != nil {
		p.log.WithError(err).Error("Failed to create session registry")
		return
	}

	// Get user info
	user := os.Getenv("USER")
	if user == "" {
		user = "unknown"
	}

	// Get git info
	repo, branch := getGitInfo(workDir)

	metadata := sessions.SessionMetadata{
		SessionID:        job.ID,
		ClaudeSessionID:  codexSessionID, // Store the native agent ID
		Provider:         "codex",
		PID:              pid,
		WorkingDirectory: workDir,
		User:             user,
		Repo:             repo,
		Branch:           branch,
		StartedAt:        time.Now(),
		JobTitle:         job.Title,
		PlanName:         plan.Name,
		JobFilePath:      job.FilePath,
		TranscriptPath:   latestFile,
	}

	if err := registry.Register(metadata); err != nil {
		p.log.WithError(err).Error("Failed to register codex session")
	} else {
		p.log.WithFields(logrus.Fields{
			"session_id": codexSessionID,
			"pid":        pid,
		}).Info("Successfully registered codex session")
	}
}

// findMostRecentFile finds the most recently modified file in a directory tree.
func findMostRecentFile(dir string) (string, error) {
	var latestFile string
	var latestModTime time.Time

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".jsonl") {
			if info.ModTime().After(latestModTime) {
				latestModTime = info.ModTime()
				latestFile = path
			}
		}
		return nil
	})

	if err != nil {
		return "", err
	}
	if latestFile == "" {
		return "", fmt.Errorf("no jsonl files found in %s", dir)
	}
	return latestFile, nil
}

// findCodexPIDForWindow finds the PID of the 'codex' process running within a specific tmux pane.
func findCodexPIDForWindow(targetPane string) (int, error) {
	// Use tmux display-message to get the pane PID
	cmd := osexec.Command("tmux", "display-message", "-p", "-t", targetPane, "#{pane_pid}")
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to get pane PID: %w", err)
	}

	shellPID, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0, fmt.Errorf("failed to parse pane PID: %w", err)
	}

	// Find the 'codex' process that is a child of that shell.
	cmd = osexec.Command("ps", "-o", "pid,ppid,comm")
	output, err = cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to run 'ps': %w", err)
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			// Check if the command name contains 'codex'
			if strings.Contains(fields[2], "codex") {
				ppid, _ := strconv.Atoi(fields[1])
				// If its parent is our shell, we've found our process.
				if ppid == shellPID {
					pid, _ := strconv.Atoi(fields[0])
					return pid, nil
				}
			}
		}
	}

	return 0, fmt.Errorf("could not find a 'codex' process as a child of shell with PID %d", shellPID)
}

// getGitInfo returns the repo name and current branch for the given directory
func getGitInfo(workDir string) (repo string, branch string) {
	// Get repo name from git config
	cmd := osexec.Command("git", "-C", workDir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	if err == nil {
		repoURL := strings.TrimSpace(string(output))
		// Extract repo name from URL (e.g., "github.com/user/repo.git" -> "repo")
		parts := strings.Split(repoURL, "/")
		if len(parts) > 0 {
			repo = strings.TrimSuffix(parts[len(parts)-1], ".git")
		}
	}

	// Get current branch
	cmd = osexec.Command("git", "-C", workDir, "branch", "--show-current")
	output, err = cmd.Output()
	if err == nil {
		branch = strings.TrimSpace(string(output))
	}

	return
}
