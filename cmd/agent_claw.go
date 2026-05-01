package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	notifications "github.com/grovetools/notify"
	notifyconfig "github.com/grovetools/notify/pkg/config"
	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
)

func newAgentClawCmd() *cobra.Command {
	var idleMinutes int
	var prompt string

	cmd := &cobra.Command{
		Use:   "claw <slug> <job>",
		Short: "Enable channels and autonomous mode on an interactive agent",
		Long: `Enable Signal channel and autonomous idle pinging on a running interactive agent.

This turns a standard interactive agent into a "claw" agent that can:
- Receive and send Signal messages
- Get periodic idle pings when inactive`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			plan, job, err := resolveAgentTarget(args[0], args[1])
			if err != nil {
				return err
			}

			client := daemon.NewWithAutoStart()
			defer client.Close()

			// Enable signal channel
			if err := client.UpdateSessionChannels(ctx, job.ID, []string{"signal"}); err != nil {
				return fmt.Errorf("failed to enable channels: %w", err)
			}

			// Enable autonomous pinger
			if err := client.UpdateSessionAutonomous(ctx, job.ID, &models.AutonomousConfig{
				Enabled:     true,
				IdleMinutes: idleMinutes,
				Prompt:      prompt,
			}); err != nil {
				return fmt.Errorf("failed to enable autonomous: %w", err)
			}

			// Resolve tmux target and notify the agent about its new capabilities
			targetPane, err := orchestration.ResolveInteractiveAgentPane(plan, job)
			if err == nil && targetPane != "" {
				// Update daemon with tmux target — harmless fallback; job 42's Mux
				// dispatch handles the primary routing via Session.Mux.
				_ = client.UpdateSessionTmuxTarget(ctx, job.ID, targetPane)

				notifyCfg := notifyconfig.Load()
				instructions := notifications.AgentInstructions(notifyCfg, []string{"signal"})
				if instructions != "" {
					msg := fmt.Sprintf("System: Signal messaging and autonomous mode have been enabled for this session.\n\n%s", instructions)
					grovelogging.NewLogger("flow.cmd.claw").WithFields(map[string]interface{}{
						"job_id":  job.ID,
						"msg_len": len(msg),
					}).Info("Injecting claw bootstrap instructions")
					_ = orchestration.SendInputToInteractiveAgent(plan, job, msg)
				}
			}

			// Update job frontmatter with channels + autonomous
			job.Channels = []string{"signal"}
			job.Autonomous = &models.AutonomousConfig{
				Enabled:     true,
				IdleMinutes: idleMinutes,
				Prompt:      prompt,
			}
			writeClawFrontmatter(job)

			fmt.Printf("Claw enabled for %s (signal + autonomous, idle=%dm)\n", job.Title, idleMinutes)
			return nil
		},
	}

	cmd.Flags().IntVar(&idleMinutes, "idle", 15, "Minutes of inactivity before idle ping")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Custom idle ping prompt (default: check for new work)")

	return cmd
}

func newAgentUnclawCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unclaw <slug> <job>",
		Short: "Disable channels and autonomous mode on an interactive agent",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			_, job, err := resolveAgentTarget(args[0], args[1])
			if err != nil {
				return err
			}

			client := daemon.NewWithAutoStart()
			defer client.Close()

			if err := client.UpdateSessionChannels(ctx, job.ID, nil); err != nil {
				return fmt.Errorf("failed to disable channels: %w", err)
			}

			if err := client.UpdateSessionAutonomous(ctx, job.ID, &models.AutonomousConfig{
				Enabled: false,
			}); err != nil {
				return fmt.Errorf("failed to disable autonomous: %w", err)
			}

			// Strip claw frontmatter so the claw is not rehydrated on daemon restart
			if job.FilePath != "" {
				content, err := os.ReadFile(job.FilePath)
				if err == nil {
					updates := map[string]any{"channels": []string{}}
					if newContent, err := orchestration.UpdateFrontmatter(content, updates); err == nil {
						s := string(newContent)
						if idx := strings.Index(s, "autonomous:\n"); idx >= 0 {
							end := idx
							lines := strings.Split(s[idx:], "\n")
							end += len(lines[0]) + 1
							for i := 1; i < len(lines); i++ {
								if strings.HasPrefix(lines[i], "  ") {
									end += len(lines[i]) + 1
								} else {
									break
								}
							}
							s = s[:idx] + s[end:]
						}
						_ = os.WriteFile(job.FilePath, []byte(s), 0o600)
					}
				}
			}

			fmt.Printf("Claw disabled for %s\n", job.Title)
			return nil
		},
	}
}

func newAgentDetachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "detach <slug> <job>",
		Short: "Pop an agent into its own tmux session",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			_, job, err := resolveAgentTarget(args[0], args[1])
			if err != nil {
				return err
			}

			client := daemon.NewWithAutoStart()
			defer client.Close()

			// Get current session
			session, err := client.GetSession(ctx, job.ID)
			if err != nil || session == nil {
				return fmt.Errorf("session not found for job %s", job.ID)
			}

			currentTarget := session.TmuxTarget
			if currentTarget == "" {
				return fmt.Errorf("no tmux target found for session")
			}

			// Break pane into its own session
			breakCmd := exec.Command("tmux", "break-pane", "-d", "-s", currentTarget)
			if output, err := breakCmd.CombinedOutput(); err != nil {
				return fmt.Errorf("tmux break-pane failed: %w (%s)", err, string(output))
			}

			// Discover new target
			displayCmd := exec.Command("tmux", "display-message", "-p", "-t", currentTarget, "#{session_name}:#{window_name}")
			output, err := displayCmd.Output()
			if err != nil {
				// Fallback: the window name should still be the same
				return fmt.Errorf("could not determine new target: %w", err)
			}
			newTarget := strings.TrimSpace(string(output))

			// Update daemon
			if err := client.UpdateSessionTmuxTarget(ctx, job.ID, newTarget); err != nil {
				return fmt.Errorf("failed to update tmux target: %w", err)
			}

			fmt.Printf("Agent detached: %s → %s\n", currentTarget, newTarget)
			return nil
		},
	}
}

func newAgentAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <slug> <job>",
		Short: "Move a detached agent back into the project session",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			_, job, err := resolveAgentTarget(args[0], args[1])
			if err != nil {
				return err
			}

			client := daemon.NewWithAutoStart()
			defer client.Close()

			session, err := client.GetSession(ctx, job.ID)
			if err != nil || session == nil {
				return fmt.Errorf("session not found for job %s", job.ID)
			}

			currentTarget := session.TmuxTarget
			if currentTarget == "" {
				return fmt.Errorf("no tmux target found for session")
			}

			// Move window back to project session
			// The project session name is derived from the working directory
			moveCmd := exec.Command("tmux", "move-window", "-s", currentTarget, "-t", ":")
			if output, err := moveCmd.CombinedOutput(); err != nil {
				return fmt.Errorf("tmux move-window failed: %w (%s)", err, string(output))
			}

			// Discover new target
			displayCmd := exec.Command("tmux", "display-message", "-p", "#{session_name}:#{window_name}")
			output, err := displayCmd.Output()
			if err != nil {
				return fmt.Errorf("could not determine new target: %w", err)
			}
			newTarget := strings.TrimSpace(string(output))

			if err := client.UpdateSessionTmuxTarget(ctx, job.ID, newTarget); err != nil {
				return fmt.Errorf("failed to update tmux target: %w", err)
			}

			fmt.Printf("Agent attached: %s → %s\n", currentTarget, newTarget)
			return nil
		},
	}
}

// writeClawFrontmatter writes channels + autonomous to a job's frontmatter using
// the simple key-value updater for channels and manual YAML for autonomous.
func writeClawFrontmatter(job *orchestration.Job) {
	if job.FilePath == "" {
		return
	}
	content, err := os.ReadFile(job.FilePath)
	if err != nil {
		return
	}

	// Use UpdateFrontmatter for channels (simple string array — works fine)
	updates := map[string]any{
		"channels": job.Channels,
	}
	newContent, err := orchestration.UpdateFrontmatter(content, updates)
	if err != nil {
		return
	}

	// For autonomous (nested struct), do a simple string insertion since
	// UpdateFrontmatter doesn't handle nested maps properly.
	if job.Autonomous != nil && job.Autonomous.Enabled {
		s := string(newContent)

		// Remove existing autonomous block first (prevent duplicates)
		if idx := strings.Index(s, "autonomous:\n"); idx >= 0 {
			end := idx + len("autonomous:\n")
			remaining := s[end:]
			for _, line := range strings.SplitAfter(remaining, "\n") {
				if strings.HasPrefix(line, "  ") {
					end += len(line)
				} else {
					break
				}
			}
			s = s[:idx] + s[end:]
		}

		autoYAML := fmt.Sprintf("autonomous:\n  enabled: true\n  idle_minutes: %d", job.Autonomous.IdleMinutes)
		if job.Autonomous.Prompt != "" {
			autoYAML += fmt.Sprintf("\n  prompt: %q", job.Autonomous.Prompt)
		}
		// Insert before the closing ---
		if idx := strings.LastIndex(s, "\n---\n"); idx >= 0 {
			s = s[:idx] + "\n" + autoYAML + s[idx:]
		}
		newContent = []byte(s)
	}

	_ = os.WriteFile(job.FilePath, newContent, 0o600)
}
