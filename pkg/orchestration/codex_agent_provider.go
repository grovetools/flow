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
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-core/util/sanitize"
	"github.com/mattsolo1/grove-flow/pkg/exec"
	"github.com/sirupsen/logrus"
)

type CodexAgentProvider struct {
	log  *logrus.Entry
	ulog *grovelogging.UnifiedLogger
}

func NewCodexAgentProvider() *CodexAgentProvider {
	return &CodexAgentProvider{
		log:  grovelogging.NewLogger("grove-flow"),
		ulog: grovelogging.NewUnifiedLogger("grove-flow"),
	}
}

func (p *CodexAgentProvider) Launch(ctx context.Context, job *Job, plan *Plan, workDir string, agentArgs []string, briefingFilePath string) error {
	// Update job status to running
	job.Status = JobStatusRunning
	job.StartTime = time.Now()
	if err := updateJobFile(job); err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}

	// NOTE: Session registration now happens AFTER the agent is launched and PID is discovered
	// to avoid the race condition where PID 0 is registered before the actual process starts.
	// See the synchronous PID discovery below after tmux window creation.

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
	agentCommand, err := p.buildAgentCommand(job, plan, briefingFilePath, agentArgs)
	if err != nil {
		job.Status = JobStatusFailed
		job.EndTime = time.Now()
		return fmt.Errorf("failed to build agent command: %w", err)
	}

	// Create a new window for this specific agent job in the session
	agentWindowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)

	p.ulog.Info("Launching Codex agent in worktree session").
		Field("window", agentWindowName).
		Field("session", sessionName).
		Pretty(theme.IconWorktree + " Launching Codex agent in worktree session").
		Log(ctx)

	// Build new-window command args - add -d flag if in TUI mode to prevent auto-select
	isTUIMode := os.Getenv("GROVE_FLOW_TUI_MODE") == "true"
	newWindowArgs := []string{"new-window"}
	if isTUIMode {
		newWindowArgs = append(newWindowArgs, "-d") // Create window in background (don't select it)
	}
	newWindowArgs = append(newWindowArgs, "-t", sessionName, "-n", agentWindowName, "-c", workDir)

	if err := executor.Execute("tmux", newWindowArgs...); err != nil {
		p.log.WithError(err).Warn("Failed to create agent window, may already exist. Will attempt to use it.")
		// Don't return error, just log and proceed.
	}

	// Set environment variables in the window's shell so they're available to the codex process
	targetPane := fmt.Sprintf("%s:%s", sessionName, agentWindowName)

	// Export environment variables in the window's shell
	// Use separate export commands for shell compatibility (bash/zsh/fish)
	// and properly quote the title to handle spaces and special characters.
	escapedTitle := "'" + strings.ReplaceAll(job.Title, "'", "'\\''") + "'"
	envCommand := fmt.Sprintf("export GROVE_FLOW_JOB_ID='%s'; export GROVE_FLOW_JOB_PATH='%s'; export GROVE_FLOW_PLAN_NAME='%s'; export GROVE_FLOW_JOB_TITLE=%s",
		job.ID, job.FilePath, plan.Name, escapedTitle)
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

	// --- Synchronous PID Discovery and Session Registration ---
	// Wait for Codex process to start and discover its PID before registering
	// This prevents the race condition where PID 0 was registered before the actual process starts
	p.log.WithField("target_pane", targetPane).Info("Starting synchronous Codex PID discovery...")

	// Wait a brief moment for the Codex process to start
	time.Sleep(500 * time.Millisecond)

	var codexPID int
	var pidErr error
	maxPIDRetries := 30
	for i := 0; i < maxPIDRetries; i++ {
		codexPID, pidErr = FindCodexPIDForPane(targetPane)
		if pidErr == nil && codexPID > 0 {
			p.log.WithFields(logrus.Fields{
				"pid":         codexPID,
				"retry_count": i,
			}).Info("Successfully discovered Codex PID")
			break
		}
		p.log.WithFields(logrus.Fields{
			"error":       pidErr,
			"retry_count": i,
			"max_retries": maxPIDRetries,
		}).Debug("Codex PID not found yet, retrying...")
		time.Sleep(1 * time.Second)
	}

	if codexPID == 0 {
		p.log.WithError(pidErr).Error("Failed to find Codex PID after retries - session registration will be skipped")
		// Don't fail the launch - the agent may still be starting
	} else {
		// Now register the session with the discovered PID
		registry, err := sessions.NewFileSystemRegistry()
		if err != nil {
			p.log.WithError(err).Error("Failed to create session registry")
		} else {
			user := os.Getenv("USER")
			if user == "" {
				user = "unknown"
			}
			repo, branch := getGitInfo(workDir)

			// Find latest codex log to get native session ID
			codexSessionsDir := filepath.Join(os.Getenv("HOME"), ".codex", "sessions")
			latestFile, _ := findMostRecentFile(codexSessionsDir, nil)
			codexSessionID := job.ID // Fallback
			transcriptPath := ""
			if latestFile != "" {
				transcriptPath = latestFile
				base := filepath.Base(latestFile)
				parts := strings.Split(strings.TrimSuffix(base, ".jsonl"), "-")
				if len(parts) >= 6 {
					codexSessionID = strings.Join(parts[len(parts)-5:], "-")
				}
			}

			metadata := sessions.SessionMetadata{
				SessionID:        job.ID,
				ClaudeSessionID:  codexSessionID,
				Provider:         "codex",
				PID:              codexPID,
				WorkingDirectory: workDir,
				User:             user,
				Repo:             repo,
				Branch:           branch,
				StartedAt:        time.Now(),
				JobTitle:         job.Title,
				PlanName:         plan.Name,
				JobFilePath:      job.FilePath,
				Type:             "interactive_agent",
				TranscriptPath:   transcriptPath,
			}

			p.log.WithFields(logrus.Fields{
				"session_id": job.ID,
				"pid":        codexPID,
				"provider":   "codex",
			}).Info("Registering codex session with valid PID")

			if err := registry.Register(metadata); err != nil {
				p.log.WithError(err).Error("Failed to register session")
			} else {
				p.log.Info("Successfully registered codex session with valid PID")
			}
		}
	}
	// --- End Synchronous PID Discovery and Session Registration ---

	// Conditionally switch to the agent window (but not when running from TUI)
	// Note: isTUIMode already declared above when building new-window args
	if os.Getenv("TMUX") != "" && !isTUIMode {
		// Check if we are in the correct session before trying to select window
		currentSessionCmd := osexec.Command("tmux", "display-message", "-p", "#S")
		currentSessionOutput, err := currentSessionCmd.Output()
		if err == nil {
			currentSession := strings.TrimSpace(string(currentSessionOutput))
			if currentSession == sessionName {
				// We are in the correct session, just switch to the window
				if err := executor.Execute("tmux", "select-window", "-t", targetPane); err != nil {
					p.log.WithError(err).Warn("Failed to switch to agent window")
				}
			} else {
				// In a different session, print instructions
				p.ulog.Info("Agent started in session").
					Field("session", sessionName).
					Pretty(fmt.Sprintf("   Agent started in session '%s'. To view, run: tmux switch-client -t %s", sessionName, sessionName)).
					Log(ctx)
			}
		} else {
			// Couldn't determine current session, print instructions
			p.log.WithError(err).Warn("Could not get current tmux session")
			p.ulog.Info("Agent started in session").
				Field("session", sessionName).
				Pretty(fmt.Sprintf("   Agent started in session '%s'. To view, run: tmux switch-client -t %s", sessionName, sessionName)).
				Log(ctx)
		}
	} else if !isTUIMode {
		// Not in tmux, print instructions (unless in TUI mode where it's shown in logs)
		p.ulog.Info("Agent session ready").
			Field("session", sessionName).
			Pretty(fmt.Sprintf("   Attach with: tmux attach -t %s", sessionName)).
			Log(ctx)
	}

	if os.Getenv("GROVE_FLOW_TUI_MODE") != "true" {
		p.ulog.Info("").Pretty("").Log(ctx) // blank line
		p.ulog.Info("Task completion instructions").
			Pretty(theme.IconArrow + " When your task is complete, run the following in any terminal:").
			Log(ctx)
		p.ulog.Info("").
			Pretty(fmt.Sprintf("   flow plan complete %s", job.FilePath)).
			Log(ctx)
		p.ulog.Info("").Pretty("").Log(ctx) // blank line
		p.ulog.Info("").
			Pretty("   The session can remain open - the plan will continue automatically.").
			Log(ctx)
	}

	// Return immediately. The lock file indicates the running state.
	return nil
}

// buildAgentCommand constructs the codex command for the interactive session.
func (p *CodexAgentProvider) buildAgentCommand(job *Job, plan *Plan, briefingFilePath string, agentArgs []string) (string, error) {
	// Pass a simple instruction to read the briefing file.
	// This is cleaner than reading the entire file content into the command.
	// Shell escape the entire briefing file path.
	escapedPath := "'" + strings.ReplaceAll(briefingFilePath, "'", "'\\''") + "'"

	// Build command with agent args
	cmdParts := []string{"codex"}
	cmdParts = append(cmdParts, agentArgs...)

	// Pass instruction to read the briefing file
	return fmt.Sprintf("%s \"Read the briefing file at %s and execute the task.\"", strings.Join(cmdParts, " "), escapedPath), nil
}

// generateSessionName creates a unique session name for the interactive job (notebook-aware).
func (p *CodexAgentProvider) generateSessionName(workDir string) (string, error) {
	projInfo, err := ResolveProjectForSessionNaming(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to get project info for session naming: %w", err)
	}
	return projInfo.Identifier(), nil
}

// discoverAndRegisterSession discovers the codex session ID and registers it with grove-core
func (p *CodexAgentProvider) discoverAndRegisterSession(job *Job, plan *Plan, workDir, targetPane string) {
	// Create debug log file
	debugFile, _ := os.Create(filepath.Join(os.TempDir(), fmt.Sprintf("grove-flow-codex-registration-%s.log", job.ID)))
	if debugFile != nil {
		defer func() {
			debugFile.WriteString(fmt.Sprintf("Goroutine exiting at %s\n", time.Now().Format(time.RFC3339)))
			debugFile.Sync()
			debugFile.Close()
		}()
		debugFile.WriteString(fmt.Sprintf("Starting registration for job %s at %s\n", job.ID, time.Now().Format(time.RFC3339)))
		debugFile.Sync()
	}

	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("❌ Panic in session registration: %v\n", r)
			fmt.Fprintf(os.Stderr, "%s", msg)
			if debugFile != nil {
				debugFile.WriteString(msg)
				debugFile.Sync()
			}
		}
	}()

	p.log.WithFields(logrus.Fields{
		"job_id":      job.ID,
		"plan":        plan.Name,
		"target_pane": targetPane,
	}).Info("Starting codex session discovery and registration")

	// Wait a moment for the log file to be created.
	msg := fmt.Sprintf("⏳ Waiting 2s for Codex log file to be created...\n")
	fmt.Fprintf(os.Stderr, "%s", msg)
	if debugFile != nil {
		debugFile.WriteString(msg)
		debugFile.Sync() // Flush to disk
	}
	time.Sleep(2 * time.Second)

	if debugFile != nil {
		debugFile.WriteString("After sleep, continuing...\n")
		debugFile.Sync()
	}

	codexSessionsDir := filepath.Join(os.Getenv("HOME"), ".codex", "sessions")
	if debugFile != nil {
		debugFile.WriteString(fmt.Sprintf("Looking in: %s\n", codexSessionsDir))
		debugFile.Sync()
	}
	p.log.WithField("sessions_dir", codexSessionsDir).Debug("Looking for codex session files")
	latestFile, err := findMostRecentFile(codexSessionsDir, debugFile)
	if err != nil {
		msg := fmt.Sprintf("❌ Failed to find Codex session file: %v\n", err)
		fmt.Fprintf(os.Stderr, "%s", msg)
		if debugFile != nil {
			debugFile.WriteString(msg)
		}
		p.log.WithError(err).Error("Failed to find most recent codex session file")
		return
	}
	msg = fmt.Sprintf("✓ Found Codex log: %s\n", latestFile)
	fmt.Fprintf(os.Stderr, "%s", msg)
	if debugFile != nil {
		debugFile.WriteString(msg)
	}
	p.log.WithField("latest_file", latestFile).Info("Found codex session file")

	// Parse session ID from filename: rollout-2025-10-20T16-43-18-019a035c-b544-7552-b739-8573c821aaea.jsonl
	base := filepath.Base(latestFile)
	parts := strings.Split(strings.TrimSuffix(base, ".jsonl"), "-")
	if len(parts) < 6 {
		p.log.WithFields(logrus.Fields{
			"filename": base,
			"parts":    len(parts),
		}).Error("Failed to parse session ID from codex log filename")
		return
	}
	// UUID is the last 5 dash-separated segments
	codexSessionID := strings.Join(parts[len(parts)-5:], "-")
	p.log.WithField("codex_session_id", codexSessionID).Info("Parsed codex session ID")

	// Find the PID of the codex process.
	p.ulog.Debug("Looking for Codex PID in pane").
		Field("target_pane", targetPane).
		Log(context.Background())
	pid, err := FindCodexPIDForPane(targetPane)
	if err != nil {
		p.ulog.Error("Failed to find Codex PID").
			Err(err).
			Log(context.Background())
		return
	}
	p.ulog.Debug("Found Codex PID").
		Field("pid", pid).
		Log(context.Background())

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

	p.ulog.Debug("Registering session with registry").
		Field("session_id", codexSessionID).
		Field("pid", pid).
		Log(context.Background())
	if err := registry.Register(metadata); err != nil {
		p.ulog.Error("Failed to register session").
			Err(err).
			Log(context.Background())
	} else {
		p.ulog.Success("Successfully registered Codex session").
			Field("session_id", codexSessionID).
			Field("pid", pid).
			Log(context.Background())
	}
}

// findMostRecentFile finds the most recently modified file in a directory tree.
func findMostRecentFile(dir string, debugFile *os.File) (string, error) {
	msg := fmt.Sprintf("🔍 Searching for .jsonl files in: %s\n", dir)
	fmt.Fprintf(os.Stderr, "%s", msg)
	if debugFile != nil {
		debugFile.WriteString(msg)
	}

	var latestFile string
	var latestModTime time.Time
	fileCount := 0

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			msg := fmt.Sprintf("  ⚠️  Walk error at %s: %v\n", path, err)
			fmt.Fprintf(os.Stderr, "%s", msg)
			if debugFile != nil {
				debugFile.WriteString(msg)
			}
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".jsonl") {
			fileCount++
			if info.ModTime().After(latestModTime) {
				latestModTime = info.ModTime()
				latestFile = path
				msg := fmt.Sprintf("  📄 Found newer file: %s (modified: %s)\n", filepath.Base(path), info.ModTime().Format("15:04:05"))
				if debugFile != nil {
					debugFile.WriteString(msg)
				}
			}
		}
		return nil
	})

	if err != nil {
		msg := fmt.Sprintf("❌ Walk failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "%s", msg)
		if debugFile != nil {
			debugFile.WriteString(msg)
		}
		return "", err
	}
	if latestFile == "" {
		msg := fmt.Sprintf("❌ No .jsonl files found (searched %d files)\n", fileCount)
		fmt.Fprintf(os.Stderr, "%s", msg)
		if debugFile != nil {
			debugFile.WriteString(msg)
		}
		return "", fmt.Errorf("no jsonl files found in %s", dir)
	}
	msg = fmt.Sprintf("✓ Selected most recent file: %s\n", filepath.Base(latestFile))
	if debugFile != nil {
		debugFile.WriteString(msg)
	}
	return latestFile, nil
}

// findDescendantPID recursively finds a descendant process with a given name.
func findDescendantPID(parentPID int, targetComm string) (int, error) {
	// 1. Get all processes
	cmd := osexec.Command("ps", "-o", "pid,ppid,comm")
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	// 2. Build a process tree (map of ppid to children) and a pid-to-command map
	tree := make(map[int][]int)
	pidToComm := make(map[int]string)
	lines := strings.Split(string(output), "\n")
	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			pid, _ := strconv.Atoi(fields[0])
			ppid, _ := strconv.Atoi(fields[1])
			comm := fields[2]
			tree[ppid] = append(tree[ppid], pid)
			pidToComm[pid] = comm
		}
	}

	// 3. Traverse from parentPID using breadth-first search
	queue := []int{parentPID}
	visited := make(map[int]bool)

	for len(queue) > 0 {
		currentPID := queue[0]
		queue = queue[1:]

		if visited[currentPID] {
			continue
		}
		visited[currentPID] = true

		// Check if the current process is the target
		if comm, ok := pidToComm[currentPID]; ok && strings.Contains(comm, targetComm) {
			return currentPID, nil
		}

		// Add children to the queue
		if children, ok := tree[currentPID]; ok {
			queue = append(queue, children...)
		}
	}

	return 0, fmt.Errorf("descendant process '%s' not found for parent PID %d", targetComm, parentPID)
}

// FindCodexPIDForPane finds the PID of the 'codex' process running within a specific tmux pane
// by traversing the process tree from the pane's shell.
func FindCodexPIDForPane(targetPane string) (int, error) {
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

	// Find the 'codex' process that is a descendant of that shell.
	return findDescendantPID(shellPID, "codex")
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
