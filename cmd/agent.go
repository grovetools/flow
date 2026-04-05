package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/pkg/tmux"
	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

// NewAgentCmd returns the root `flow agent` command for managing interactive agents.
func NewAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage interactive agents running in tmux",
		Long: `Commands for interacting with interactive agents running in tmux sessions.

Provides read, send, status, list, and transcript subcommands for
coordinator-to-agent communication.

Commands that target a specific agent accept either:
  - Positional args: <slug> <job>  (resolved via flow plan/job)
  - Direct tmux target: -t "session:window"  (standard tmux syntax)`,
		Example: `  # Quick workflow: check status, read output, send instruction
  flow agent status my-feature impl-feature
  flow agent read my-feature impl-feature -n 50
  flow agent send my-feature impl-feature 'Fix the failing test in pkg/api'

  # Direct tmux targeting (bypass plan/job resolution)
  flow agent read -t "grovetools_my-feature:job-impl-feature"
  flow agent send -t "grovetools_my-feature:kitchen-env" 'check your work'

  # List all running agents across plans
  flow agent list --json`,
	}

	cmd.AddCommand(newAgentReadCmd())
	cmd.AddCommand(newAgentSendCmd())
	cmd.AddCommand(newAgentStatusCmd())
	cmd.AddCommand(newAgentListCmd())
	cmd.AddCommand(newAgentTranscriptCmd())

	return cmd
}

// resolveAgentTarget loads the plan and job from a slug and job identifier.
// The job can be specified by filename or title.
func resolveAgentTarget(slug, jobID string) (*orchestration.Plan, *orchestration.Job, error) {
	planDir, err := resolvePlanPath(slug, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("could not resolve plan '%s': %w", slug, err)
	}

	plan, err := orchestration.LoadPlan(planDir)
	if err != nil {
		return nil, nil, fmt.Errorf("could not load plan from '%s': %w", planDir, err)
	}

	// Try by filename first (with or without .md extension)
	job, found := plan.GetJobByFilename(jobID)
	if !found {
		job, found = plan.GetJobByFilename(jobID + ".md")
	}
	if !found {
		job, found = plan.GetJobByID(jobID)
	}
	if !found {
		for _, j := range plan.Jobs {
			if j.Title == jobID {
				job = j
				found = true
				break
			}
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("job '%s' not found in plan '%s'", jobID, slug)
	}

	return plan, job, nil
}

// resolveTmuxTarget returns the tmux target pane string.
// If directTarget is set, returns it directly. Otherwise resolves from slug+job.
func resolveTmuxTarget(directTarget string, args []string) (string, error) {
	if directTarget != "" {
		return directTarget, nil
	}
	if len(args) < 2 {
		return "", fmt.Errorf("provide either -t <session:window> or <slug> <job>")
	}
	plan, job, err := resolveAgentTarget(args[0], args[1])
	if err != nil {
		return "", err
	}
	return orchestration.ResolveInteractiveAgentPane(plan, job)
}

// capturePaneOutput captures output from a tmux pane and returns the last N lines.
func capturePaneOutput(targetPane string, lines int) (string, error) {
	tmuxClient, err := tmux.NewClient()
	if err != nil {
		return "", fmt.Errorf("tmux not available: %w", err)
	}

	output, err := tmuxClient.CapturePane(context.Background(), targetPane)
	if err != nil {
		return "", fmt.Errorf("failed to capture pane: %w", err)
	}

	split := strings.Split(strings.TrimSpace(output), "\n")
	start := len(split) - lines
	if start < 0 {
		start = 0
	}
	return strings.Join(split[start:], "\n"), nil
}

// sendToPane sends input text to a tmux pane, respecting input_mode config.
func sendToPane(targetPane, input string) error {
	// Read flow config for input_mode
	coreCfg, cfgErr := config.LoadDefault()
	if cfgErr != nil {
		coreCfg = &config.Config{}
	}
	var flowCfg orchestration.FlowConfig
	coreCfg.UnmarshalExtension("flow", &flowCfg)

	inputMode := "vim"
	providerName := "claude"
	if flowCfg.InteractiveProvider != "" {
		providerName = flowCfg.InteractiveProvider
	}
	if providerCfg, ok := flowCfg.Providers[providerName]; ok && providerCfg.InputMode != "" {
		inputMode = providerCfg.InputMode
	}

	tmuxClient, err := tmux.NewClient()
	if err != nil {
		return fmt.Errorf("tmux not available: %w", err)
	}

	ctx := context.Background()

	if inputMode == "vim" {
		if err := tmuxClient.SendKeys(ctx, targetPane, "Escape", "i", input); err != nil {
			return fmt.Errorf("failed to send input: %w", err)
		}
	} else {
		if err := tmuxClient.SendKeys(ctx, targetPane, input); err != nil {
			return fmt.Errorf("failed to send input: %w", err)
		}
	}

	if err := tmuxClient.SendKeys(ctx, targetPane, "C-m"); err != nil {
		return fmt.Errorf("failed to send submit key: %w", err)
	}
	return nil
}

// isAgentIdle checks the last few lines of agent output for idle indicators.
func isAgentIdle(output string) bool {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	start := len(lines) - 10
	if start < 0 {
		start = 0
	}
	for _, l := range lines[start:] {
		if strings.Contains(l, "INSERT") || strings.Contains(l, "Tokens:") || strings.Contains(l, "❯") {
			return true
		}
	}
	return false
}

func newAgentReadCmd() *cobra.Command {
	var lines int
	var target string

	cmd := &cobra.Command{
		Use:   "read [<slug> <job>]",
		Short: "Read recent output from an agent's tmux pane",
		Long: `Captures the visible output from an interactive agent's tmux pane
and prints the last N lines (default 30).

Target the agent by plan slug + job, or directly by tmux target.`,
		Example: `  # Read last 30 lines (default) from an agent
  flow agent read my-feature impl-feature

  # Read last 100 lines
  flow agent read my-feature impl-feature -n 100

  # Read using direct tmux target (session:window syntax)
  flow agent read -t "grovetools_my-feature:job-impl-feature"
  flow agent read -t "grovetools_my-feature:kitchen-env" -n 50

  # Pipe output to grep for specific patterns
  flow agent read my-feature impl-feature -n 200 | grep -i error`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetPane, err := resolveTmuxTarget(target, args)
			if err != nil {
				return err
			}

			output, err := capturePaneOutput(targetPane, lines)
			if err != nil {
				return err
			}

			fmt.Println(output)
			return nil
		},
	}

	cmd.Flags().IntVarP(&lines, "lines", "n", 30, "Number of lines to return")
	cmd.Flags().StringVarP(&target, "target", "t", "", "Direct tmux target (session:window)")
	return cmd
}

func newAgentSendCmd() *cobra.Command {
	var fileFlag string
	var waitFlag bool
	var target string

	cmd := &cobra.Command{
		Use:   "send [<slug> <job>] [message]",
		Short: "Send a message to an interactive agent",
		Long: `Sends a message or instruction to an interactive agent via tmux.
The message can be provided as an argument or read from a file.

By default, the command returns immediately after sending (fire-and-forget).
Use --wait to block until the agent becomes idle again.

Target the agent by plan slug + job, or directly by tmux target.`,
		Example: `  # Send a simple message
  flow agent send my-feature impl-feature 'Fix the failing test in pkg/api'

  # Send multi-line instructions from a file
  flow agent send my-feature impl-feature --file /tmp/instructions.md

  # Send and wait for the agent to finish before returning
  flow agent send my-feature impl-feature --wait 'Run make test'

  # Direct tmux target (skip plan/job resolution)
  flow agent send -t "grovetools_my-feature:job-impl" 'Fix the test'
  flow agent send -t "grovetools_my-feature:kitchen-env" --file todo.md

  # Broadcast to multiple agents
  flow agent send -t "session:agent-1" 'commit your work'
  flow agent send -t "session:agent-2" 'commit your work'`,
		Args: cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			var targetPane, input string
			var err error

			if target != "" {
				targetPane = target
				// With -t, all positional args are the message
				if fileFlag != "" {
					b, readErr := os.ReadFile(fileFlag)
					if readErr != nil {
						return fmt.Errorf("could not read file '%s': %w", fileFlag, readErr)
					}
					input = string(b)
				} else if len(args) > 0 {
					input = strings.Join(args, " ")
				} else {
					return fmt.Errorf("must provide either a message argument or --file")
				}
			} else {
				if len(args) < 2 {
					return fmt.Errorf("provide either -t <session:window> or <slug> <job> [message]")
				}
				targetPane, err = resolveTmuxTarget("", args[:2])
				if err != nil {
					return err
				}
				if fileFlag != "" {
					b, readErr := os.ReadFile(fileFlag)
					if readErr != nil {
						return fmt.Errorf("could not read file '%s': %w", fileFlag, readErr)
					}
					input = string(b)
				} else if len(args) >= 3 {
					input = strings.Join(args[2:], " ")
				} else {
					return fmt.Errorf("must provide either a message argument or --file")
				}
			}

			if err := sendToPane(targetPane, input); err != nil {
				return err
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "Message sent to %s\n", targetPane)

			if waitFlag {
				fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for agent to become idle...\n")
				time.Sleep(2 * time.Second)
				for {
					out, capErr := capturePaneOutput(targetPane, 30)
					if capErr == nil && isAgentIdle(out) {
						fmt.Fprintf(cmd.ErrOrStderr(), "Agent is idle.\n")
						break
					}
					time.Sleep(2 * time.Second)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&fileFlag, "file", "", "Read message from a file")
	cmd.Flags().BoolVarP(&waitFlag, "wait", "w", false, "Wait for the agent to finish before exiting")
	cmd.Flags().StringVarP(&target, "target", "t", "", "Direct tmux target (session:window)")
	return cmd
}

// AgentStatus represents the detected state of an agent.
type AgentStatus struct {
	Status string `json:"status"` // "idle", "working", or "disconnected"
	Target string `json:"target"`
}

func newAgentStatusCmd() *cobra.Command {
	var jsonOut bool
	var target string

	cmd := &cobra.Command{
		Use:   "status [<slug> <job>]",
		Short: "Check if an agent is idle, working, or disconnected",
		Long: `Reads the agent's tmux pane and analyzes the last few lines to determine
whether the agent is idle (waiting for input), working (processing), or disconnected.`,
		Example: `  # Check if agent is idle, working, or disconnected
  flow agent status my-feature impl-feature

  # Machine-readable JSON output
  flow agent status my-feature impl-feature --json

  # Check status via direct tmux target
  flow agent status -t "grovetools_my-feature:job-impl"
  flow agent status -t "grovetools_my-feature:kitchen-env" --json

  # Use in scripts to wait for idle
  while [ "$(flow agent status my-feature impl --json | jq -r .status)" != "idle" ]; do sleep 5; done`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetPane, err := resolveTmuxTarget(target, args)
			if err != nil {
				return err
			}

			output, capErr := capturePaneOutput(targetPane, 30)

			status := AgentStatus{Target: targetPane}

			if capErr != nil {
				status.Status = "disconnected"
			} else if isAgentIdle(output) {
				status.Status = "idle"
			} else {
				status.Status = "working"
			}

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(status)
			}

			fmt.Printf("%s: %s\n", targetPane, status.Status)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().StringVarP(&target, "target", "t", "", "Direct tmux target (session:window)")
	return cmd
}

// AgentListEntry represents a running interactive agent for list output.
type AgentListEntry struct {
	PlanName  string `json:"plan_name"`
	JobTitle  string `json:"job_title"`
	Provider  string `json:"provider,omitempty"`
	Status    string `json:"status"`
	SessionID string `json:"session_id,omitempty"`
}

func newAgentListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all running interactive agents",
		Long: `Queries the daemon for active sessions and lists all running interactive agents.
Shows the plan name, job title, provider, and status for each agent.`,
		Example: `  # List all running interactive agents
  flow agent list

  # Machine-readable JSON output
  flow agent list --json

  # Count running agents
  flow agent list --json | jq length

  # Get tmux targets for all agents in a plan
  flow agent list --json | jq -r '.[] | select(.plan_name=="my-feature") | .session_id'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			client := daemon.New()
			defer client.Close()

			sessions, err := client.GetSessions(ctx)
			if err != nil {
				return fmt.Errorf("could not query sessions: %w", err)
			}

			var active []AgentListEntry
			for _, s := range sessions {
				if s.Status == "running" && s.PlanName != "" {
					active = append(active, AgentListEntry{
						PlanName:  s.PlanName,
						JobTitle:  s.JobTitle,
						Provider:  s.Provider,
						Status:    s.Status,
						SessionID: s.ID,
					})
				}
			}

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(active)
			}

			if len(active) == 0 {
				fmt.Println("No running interactive agents found.")
				return nil
			}

			fmt.Printf("%-30s %-30s %-10s %-10s\n", "PLAN", "JOB", "PROVIDER", "STATUS")
			for _, a := range active {
				fmt.Printf("%-30s %-30s %-10s %-10s\n", a.PlanName, a.JobTitle, a.Provider, a.Status)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func newAgentTranscriptCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "transcript <slug> <job>",
		Short: "Show the full transcript for an agent session",
		Long: `Retrieves the transcript for an interactive agent session.

First attempts to read the transcript file registered with the daemon.
Falls back to the job log in the plan's .artifacts directory.`,
		Example: `  # View full agent transcript
  flow agent transcript my-feature impl-feature

  # Pipe to pager for long transcripts
  flow agent transcript my-feature impl-feature | less

  # Search transcript for specific output
  flow agent transcript my-feature impl-feature | grep -A5 "error"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, job, err := resolveAgentTarget(args[0], args[1])
			if err != nil {
				return err
			}

			// Try daemon sessions for transcript path
			ctx := context.Background()
			client := daemon.New()
			defer client.Close()

			if transcriptPath := findTranscriptPath(ctx, client, job); transcriptPath != "" {
				content, err := os.ReadFile(transcriptPath)
				if err == nil {
					fmt.Print(string(content))
					return nil
				}
			}

			// Fallback to job log
			logPath, err := orchestration.GetJobLogPath(plan, job)
			if err != nil {
				return fmt.Errorf("could not determine log path: %w", err)
			}

			content, err := os.ReadFile(logPath)
			if err != nil {
				return fmt.Errorf("no transcript available (tried daemon and %s): %w", logPath, err)
			}

			fmt.Print(string(content))
			return nil
		},
	}

	return cmd
}

// findTranscriptPath queries the daemon for a session matching the job and returns
// the transcript path if available.
func findTranscriptPath(ctx context.Context, client daemon.Client, job *orchestration.Job) string {
	sessions, err := client.GetSessions(ctx)
	if err != nil {
		return ""
	}

	for _, s := range sessions {
		if matchesJob(s, job) {
			return getTranscriptFromSession(s)
		}
	}
	return ""
}

// matchesJob checks if a daemon session corresponds to a given job.
func matchesJob(s *models.Session, job *orchestration.Job) bool {
	if s.JobFilePath != "" && strings.HasSuffix(s.JobFilePath, job.Filename) {
		return true
	}
	if s.JobTitle != "" && s.JobTitle == job.Title {
		return true
	}
	return false
}

// getTranscriptFromSession extracts the transcript path from session metadata.
func getTranscriptFromSession(s *models.Session) string {
	if s.WorkingDirectory == "" {
		return ""
	}
	return ""
}
