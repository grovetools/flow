package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-core/pkg/tmux"
	"github.com/mattsolo1/grove-core/util/sanitize"
	groveexec "github.com/mattsolo1/grove-flow/pkg/exec"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

// NewPlanResumeCmd creates the `plan resume` command.
func NewPlanResumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume <job-file>",
		Short: "Resume a completed interactive agent job",
		Long: `Resumes a completed interactive agent session by finding its native agent session ID
and re-launching the agent in a new tmux window.`,
		Args: cobra.ExactArgs(1),
		RunE: runPlanResume,
	}
	return cmd
}

func runPlanResume(cmd *cobra.Command, args []string) error {
	jobPath := args[0]

	// 1. Load and Validate Job
	planDir := filepath.Dir(jobPath)
	jobFile := filepath.Base(jobPath)

	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	job, found := plan.GetJobByFilename(jobFile)
	if !found {
		return fmt.Errorf("job not found: %s", jobFile)
	}

	if job.Type != orchestration.JobTypeInteractiveAgent && job.Type != orchestration.JobTypeAgent {
		return fmt.Errorf("cannot resume job: only 'interactive_agent' jobs are supported (job type is '%s')", job.Type)
	}
	if job.Status != orchestration.JobStatusCompleted {
		return fmt.Errorf("cannot resume job: status is '%s', must be 'completed'", job.Status)
	}

	// 2. Retrieve Agent Session ID via aglogs
	aglogsCmd := exec.Command("aglogs", "get-session-info", job.FilePath)
	output, err := aglogsCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get session info from aglogs: %w\nOutput: %s", err, string(output))
	}

	var sessionInfo struct {
		AgentSessionID string `json:"agent_session_id"`
		Provider       string `json:"provider"`
	}
	if err := json.Unmarshal(output, &sessionInfo); err != nil {
		return fmt.Errorf("failed to parse session info from aglogs: %w", err)
	}

	if sessionInfo.Provider != "claude" && sessionInfo.Provider != "codex" {
		return fmt.Errorf("resuming sessions is currently only supported for 'claude' and 'codex' providers, but provider is '%s'", sessionInfo.Provider)
	}

	// 3. Update Job Status to 'running'
	persister := orchestration.NewStatePersister()
	if err := persister.UpdateJobStatus(job, orchestration.JobStatusRunning); err != nil {
		return fmt.Errorf("failed to update job status to running: %w", err)
	}
	fmt.Printf("✓ Job status updated to 'running'.\n")

	// 4. Re-launch the Agent in Tmux
	// Load agent config to get default arguments
	appCfg, err := loadFullConfig()
	if err != nil {
		return fmt.Errorf("failed to load agent configuration: %w", err)
	}
	agentArgs := appCfg.Agent.Args

	var resumeCmdParts []string
	if sessionInfo.Provider == "codex" {
		// Codex agent (Anthropic's tool) uses --continue
		resumeCmdParts = append(resumeCmdParts, "codex", "--continue")
		resumeCmdParts = append(resumeCmdParts, agentArgs...)
	} else {
		// Assume 'claude' provider (grove-claude-agent)
		// Use --resume to resume a completed session
		resumeCmdParts = append(resumeCmdParts, "claude", "--resume", sessionInfo.AgentSessionID)
		resumeCmdParts = append(resumeCmdParts, agentArgs...)
	}

	// Create a custom resume function that properly names the window
	err = resumeAgentInTmux(cmd.Context(), plan, job, resumeCmdParts)

	if err != nil {
		// If launching fails, revert the status back to completed
		persister.UpdateJobStatus(job, orchestration.JobStatusCompleted)
		return fmt.Errorf("failed to re-launch agent session: %w", err)
	}

	fmt.Printf("✓ Resumed session for job '%s' in new tmux window.\n", job.Title)
	return nil
}

// resumeAgentInTmux creates or switches to a tmux session and launches the resumed agent
func resumeAgentInTmux(ctx context.Context, plan *orchestration.Plan, job *orchestration.Job, commandToRun []string) error {
	// Only proceed if we're in a terminal and have tmux
	if os.Getenv("TERM") == "" {
		return fmt.Errorf("not in a terminal")
	}

	// Create tmux client
	tmuxClient, err := tmux.NewClient()
	if err != nil {
		return fmt.Errorf("tmux not available: %w", err)
	}

	// Determine working directory using the canonical logic
	workingDir, err := orchestration.DetermineWorkingDirectory(plan, job)
	if err != nil {
		return fmt.Errorf("failed to determine working directory: %w", err)
	}

	// Get workspace info for session naming (notebook-aware)
	projInfo, err := orchestration.ResolveProjectForSessionNaming(workingDir)
	if err != nil {
		return fmt.Errorf("failed to get workspace info for %s: %w", workingDir, err)
	}

	sessionName := projInfo.Identifier()

	// Check if session already exists
	sessionExists, _ := tmuxClient.SessionExists(ctx, sessionName)

	// Create window name for the resumed job (same pattern as interactive_agent_executor)
	windowName := "job-" + sanitize.SanitizeForTmuxSession(job.Title)

	executor := &groveexec.RealCommandExecutor{}
	commandStr := strings.Join(commandToRun, " ")

	if !sessionExists {
		// Create new session with the resume command
		planName := plan.Name
		setPlanCmd := fmt.Sprintf("flow plan set %s && %s", planName, commandStr)

		panes := []tmux.PaneOptions{
			{
				Command: setPlanCmd,
			},
		}

		opts := tmux.LaunchOptions{
			SessionName:      sessionName,
			WorkingDirectory: workingDir,
			WindowName:       windowName,
			WindowIndex:      2,
			Panes:            panes,
		}

		fmt.Printf("🚀 Creating tmux session '%s' for resumed job...\n", sessionName)
		if err := tmuxClient.Launch(ctx, opts); err != nil {
			return fmt.Errorf("failed to create tmux session: %w", err)
		}

		fmt.Printf("✓ Session '%s' created\n", sessionName)
	} else {
		// Session exists, create a new window for the resumed job
		if err := executor.Execute("tmux", "new-window", "-t", sessionName, "-n", windowName, "-c", workingDir); err != nil {
			fmt.Printf("Note: Could not create new window '%s': %v\n", windowName, err)
		}

		targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)

		// Set the active plan in the worktree
		planName := filepath.Base(plan.Directory)
		setPlanCmd := fmt.Sprintf("flow plan set %s", planName)
		if err := executor.Execute("tmux", "send-keys", "-t", targetPane, setPlanCmd, "C-m"); err != nil {
			return fmt.Errorf("failed to send set plan command: %w", err)
		}

		// Small delay to let the set command complete
		time.Sleep(200 * time.Millisecond)

		// Then run the actual resume command
		if err := executor.Execute("tmux", "send-keys", "-t", targetPane, commandStr, "C-m"); err != nil {
			return fmt.Errorf("failed to send command '%s': %w", commandStr, err)
		}
	}

	// Switch to the session if we're already in tmux
	if os.Getenv("TMUX") != "" {
		fmt.Printf("✓ Switching to session '%s'...\n", sessionName)
		if err := executor.Execute("tmux", "switch-client", "-t", sessionName); err != nil {
			fmt.Printf("Could not switch to session. Attach with: tmux attach -t %s\n", sessionName)
		}
		// Also switch to the new window
		targetPane := fmt.Sprintf("%s:%s", sessionName, windowName)
		if err := executor.Execute("tmux", "select-window", "-t", targetPane); err != nil {
			fmt.Printf("Note: Could not switch to window '%s'\n", windowName)
		}
	} else {
		fmt.Printf("Attach with: tmux attach -t %s\n", sessionName)
	}

	return nil
}
