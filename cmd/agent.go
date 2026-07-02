package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/daemon"
	"github.com/grovetools/core/pkg/models"
	"github.com/grovetools/core/pkg/mux"
	"github.com/grovetools/core/pkg/worktreeregistry"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
)

// NewAgentCmd returns the root `flow agent` command for managing interactive agents.
func NewAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage interactive agents running in tmux",
		Long: `Commands for interacting with interactive agents running in tmux sessions.

Provides read, send, status, list, and transcript subcommands for
coordinator-to-agent communication.

Commands that target a specific agent accept any of:
  - Unified target: --at <feature> <job>  (plan name, container path, or
    <container-id>/<name> — the same --at every flow subcommand uses; the
    canonical, disambiguated form)
  - Positional args: <slug> <job>  (resolved via flow plan/job; an ambiguous
    slug errors with the --at candidate forms rather than picking one)
  - Direct tmux target: -t "session:window"  (standard tmux syntax)

The job is reconciled to its live daemon session id, so a job whose session is
hash-suffixed (e.g. "coord-9a146245") still resolves from plan+job.`,
		Example: `  # Quick workflow: check status, read output, send instruction
  flow agent status my-feature impl-feature
  flow agent read my-feature impl-feature -n 50
  flow agent send my-feature impl-feature 'Fix the failing test in pkg/api'

  # Unified --at targeting (disambiguates a plan name shared by many worktrees)
  flow agent read --at my-feature impl-feature -n 50
  flow agent status --at host-id/my-feature coord

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
	cmd.AddCommand(newAgentClawCmd())
	cmd.AddCommand(newAgentUnclawCmd())
	cmd.AddCommand(newAgentDetachCmd())
	cmd.AddCommand(newAgentAttachCmd())
	cmd.AddCommand(newAgentKillCmd())

	return cmd
}

// resolveAgentTarget loads the plan and job from a slug and job identifier and
// reconciles the job to its live daemon session id. The job can be specified by
// filename or title.
//
// Plan resolution is ctx-aware: when a unified `--at <feature>` target is
// present on ctx it pins the plan dir directly (the same mechanism every other
// `flow plan` subcommand uses), so `flow agent read --at <feature> <job>` is the
// canonical, disambiguated address. A bare positional slug still routes through
// the legacy cwd/workspace resolver, but an ambiguous one errors with the
// concrete `--at <container-id>/<name>` candidate forms instead of silently
// picking a plan.
func resolveAgentTarget(ctx context.Context, slug, jobID string) (*orchestration.Plan, *orchestration.Job, error) {
	planDir, err := resolvePlanPathCtx(ctx, slug, ".")
	if err != nil {
		if cands := planTargetCandidates(slug); len(cands) > 1 {
			return nil, nil, fmt.Errorf("plan %q is ambiguous; disambiguate with one of:\n  --at %s",
				slug, strings.Join(cands, "\n  --at "))
		}
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

	// Reconcile the on-disk job.ID to the live daemon session id. The daemon
	// registers an interactive agent under a hash-suffixed id (e.g.
	// "coord-9a146245") that differs from job.ID; every capture/send/status/kill
	// path keys on job.ID, so without this they miss the session and fall through
	// to the (often wrong) tmux path. Mutating the freshly-loaded in-memory job is
	// safe — it lives only for this CLI invocation.
	job.ID = reconcileSessionID(plan, job)

	return plan, job, nil
}

// reconcileSessionID maps a resolved job to the live daemon session id by
// consulting the daemon registry the same way `flow agent list` does, scoped to
// this plan. Returns the registry id of the best-matching session (preferring a
// live one over a terminal one), or job.ID unchanged when the daemon is
// unreachable or nothing matches — preserving the bare-id / tmux escape paths.
func reconcileSessionID(plan *orchestration.Plan, job *orchestration.Job) string {
	client := daemon.NewWithAutoStart()
	defer client.Close()

	sessions, err := client.GetSessions(context.Background())
	if err != nil {
		return job.ID
	}

	var fallback string
	for _, s := range sessions {
		// Scope to this plan when both sides are known, so a job title that
		// collides across plans cannot grab the wrong session.
		if plan != nil && plan.Name != "" && s.PlanName != "" && s.PlanName != plan.Name {
			continue
		}
		if !matchesJob(s, job) {
			continue
		}
		if s.Status != "completed" && s.Status != "failed" && s.Status != "stopped" {
			return s.ID
		}
		if fallback == "" {
			fallback = s.ID
		}
	}
	if fallback != "" {
		return fallback
	}
	return job.ID
}

// planTargetCandidates returns the canonical `--at <container-id>/<name>` forms
// for every registered worktree whose plan name equals slug. Used to turn an
// ambiguous bare slug into an actionable disambiguation error.
func planTargetCandidates(slug string) []string {
	if slug == "" {
		return nil
	}
	entries, err := worktreeregistry.ListAll()
	if err != nil {
		return nil
	}
	var forms []string
	for _, e := range entries {
		if e == nil || e.Plan != slug || e.AbsPath == "" || e.IsArchived() {
			continue
		}
		containerID := filepath.Base(filepath.Dir(e.AbsPath))
		name := filepath.Base(e.AbsPath)
		forms = append(forms, fmt.Sprintf("%s/%s", containerID, name))
	}
	return forms
}

// resolveAgentTargetArgs is the positional-args front door to
// resolveAgentTarget: it splits args into (slug, job) honoring a unified `--at`
// target on ctx, then resolves and reconciles. Used by the simple agent
// subcommands that take exactly a target (no trailing message).
func resolveAgentTargetArgs(ctx context.Context, args []string) (*orchestration.Plan, *orchestration.Job, error) {
	slug, jobID, err := agentSlugJob(ctx, args)
	if err != nil {
		return nil, nil, err
	}
	return resolveAgentTarget(ctx, slug, jobID)
}

// agentSlugJob splits positional args into (slug, job) honoring a unified
// `--at` target on ctx. With `--at` the plan is already pinned, so callers pass
// only <job>; without it the canonical <slug> <job> pair is required.
func agentSlugJob(ctx context.Context, args []string) (slug, job string, err error) {
	if _, ok := TargetFromContext(ctx); ok {
		if len(args) < 1 {
			return "", "", fmt.Errorf("provide <job> (plan targeted via --at)")
		}
		return "", args[0], nil
	}
	if len(args) < 2 {
		return "", "", fmt.Errorf("provide <slug> <job>, --at <plan> <job>, or -t <session:window>")
	}
	return args[0], args[1], nil
}

// agentCmdLogger returns a structured logger for CLI agent commands.
func agentCmdLogger() *logrus.Entry {
	return grovelogging.NewLogger("flow.cmd.agent")
}

// captureAgentOutput captures output from an interactive agent and returns the last N lines.
// If plan and job are non-nil, delegates to the orchestration helper (which tries daemon
// native-PTY capture first, then falls back to tmux). If both are nil, uses a direct tmux
// capture of targetPane — this preserves the `-t <tmux-target>` CLI escape hatch.
func captureAgentOutput(plan *orchestration.Plan, job *orchestration.Job, targetPane string, lines int) (string, error) {
	delegation := "direct_tmux"
	if plan != nil && job != nil {
		delegation = "orchestration"
	}
	agentCmdLogger().WithFields(logrus.Fields{
		"delegation": delegation,
		"target":     targetPane,
	}).Debug("captureAgentOutput called")

	var output string
	if plan != nil && job != nil {
		out, err := orchestration.CaptureInteractiveAgentOutput(plan, job)
		if err != nil {
			return "", err
		}
		output = out
	} else {
		engine, err := mux.DetectMuxEngine(context.Background())
		if err != nil {
			return "", fmt.Errorf("mux engine not available: %w", err)
		}
		out, err := engine.CapturePane(context.Background(), targetPane)
		if err != nil {
			return "", fmt.Errorf("failed to capture pane: %w", err)
		}
		output = out
	}

	split := strings.Split(strings.TrimSpace(output), "\n")
	start := len(split) - lines
	if start < 0 {
		start = 0
	}
	return strings.Join(split[start:], "\n"), nil
}

// sendToAgent sends input text to an interactive agent.
// If plan and job are non-nil, delegates to the orchestration helper (native-PTY first,
// tmux fallback). Otherwise falls back to a direct tmux send against targetPane, reading
// input_mode from the flow config for vim-mode escape handling.
func sendToAgent(plan *orchestration.Plan, job *orchestration.Job, targetPane, input string) error {
	delegation := "direct_tmux"
	if plan != nil && job != nil {
		delegation = "orchestration"
	}
	agentCmdLogger().WithFields(logrus.Fields{
		"delegation": delegation,
		"target":     targetPane,
	}).Debug("sendToAgent called")

	if plan != nil && job != nil {
		return orchestration.SendInputToInteractiveAgent(plan, job, input)
	}

	// Direct path (for `-t <target>`).
	flowCfgPtr, err := orchestration.LoadFlowConfigDefault()
	if err != nil {
		return err
	}
	flowCfg := *flowCfgPtr

	inputMode := "vim"
	providerName := "claude"
	if flowCfg.InteractiveProvider != "" {
		providerName = flowCfg.InteractiveProvider
	}
	if providerCfg, ok := flowCfg.Providers[providerName]; ok && providerCfg.InputMode != "" {
		inputMode = providerCfg.InputMode
	}

	engine, err := mux.DetectMuxEngine(context.Background())
	if err != nil {
		return fmt.Errorf("mux engine not available: %w", err)
	}

	ctx := context.Background()

	if inputMode == "vim" {
		if err := engine.SendKeys(ctx, targetPane, "Escape", "i", input); err != nil {
			return fmt.Errorf("failed to send input: %w", err)
		}
	} else {
		if err := engine.SendKeys(ctx, targetPane, input); err != nil {
			return fmt.Errorf("failed to send input: %w", err)
		}
	}

	if err := engine.SendKeys(ctx, targetPane, "C-m"); err != nil {
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
			var plan *orchestration.Plan
			var job *orchestration.Job
			targetPane := target
			if target == "" {
				slug, jobID, perr := agentSlugJob(cmd.Context(), args)
				if perr != nil {
					return perr
				}
				var err error
				plan, job, err = resolveAgentTarget(cmd.Context(), slug, jobID)
				if err != nil {
					return err
				}
				targetPane = ""
			}

			output, err := captureAgentOutput(plan, job, targetPane, lines)
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

// buildSenderHeader constructs a [from: ...] header identifying the sender.
// It reads GROVE_FLOW_JOB_ID and GROVE_FLOW_JOB_TITLE env vars first.
// If those aren't set, falls back to the current tmux session:window.
func buildSenderHeader() string {
	jobID := os.Getenv("GROVE_FLOW_JOB_ID")
	jobTitle := os.Getenv("GROVE_FLOW_JOB_TITLE")
	planName := os.Getenv("GROVE_FLOW_PLAN_NAME")

	if jobID != "" {
		// Build tmux address from plan/job info
		var tmuxAddr string
		if planName != "" && jobTitle != "" {
			// Reconstruct the likely tmux target
			tmuxAddr = planName + ":job-" + jobTitle
		}
		label := jobID
		if jobTitle != "" {
			label = jobTitle
		}
		if tmuxAddr != "" {
			return fmt.Sprintf("[from: %s (%s)]", label, tmuxAddr)
		}
		return fmt.Sprintf("[from: %s]", label)
	}

	// Fallback: try to get current session:window via the mux engine.
	engine, engineErr := mux.DetectMuxEngine(context.Background())
	if engineErr == nil {
		// Use a best-effort capture to identify current session.
		if sessions, listErr := engine.ListSessions(context.Background()); listErr == nil && len(sessions) > 0 {
			return fmt.Sprintf("[from: %s]", sessions[0].Name)
		}
	}

	return ""
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

A [from: ...] header is automatically prepended to identify the sender,
using the GROVE_FLOW_JOB_ID env var or the current tmux pane identity.

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
			var plan *orchestration.Plan
			var job *orchestration.Job
			var targetPane, input string

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
				// Honor --at: with a unified target the plan is pinned, so the
				// positionals are <job> [message...]; otherwise <slug> <job>
				// [message...].
				var slug, jobID string
				var msgArgs []string
				if _, ok := TargetFromContext(cmd.Context()); ok {
					if len(args) < 1 {
						return fmt.Errorf("provide <job> [message] (plan targeted via --at)")
					}
					jobID, msgArgs = args[0], args[1:]
				} else {
					if len(args) < 2 {
						return fmt.Errorf("provide <slug> <job> [message], --at <plan> <job> [message], or -t <session:window>")
					}
					slug, jobID, msgArgs = args[0], args[1], args[2:]
				}
				var err error
				plan, job, err = resolveAgentTarget(cmd.Context(), slug, jobID)
				if err != nil {
					return err
				}
				if fileFlag != "" {
					b, readErr := os.ReadFile(fileFlag)
					if readErr != nil {
						return fmt.Errorf("could not read file '%s': %w", fileFlag, readErr)
					}
					input = string(b)
				} else if len(msgArgs) > 0 {
					input = strings.Join(msgArgs, " ")
				} else {
					return fmt.Errorf("must provide either a message argument or --file")
				}
			}

			// Prepend sender header
			if header := buildSenderHeader(); header != "" {
				input = header + "\n" + input
			}

			if err := sendToAgent(plan, job, targetPane, input); err != nil {
				return err
			}

			displayTarget := targetPane
			if displayTarget == "" && plan != nil && job != nil {
				if pane, err := orchestration.ResolveInteractiveAgentPane(plan, job); err == nil {
					displayTarget = pane
				} else {
					displayTarget = job.Title
				}
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Message sent to %s\n", displayTarget)

			if waitFlag {
				fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for agent to become idle...\n")
				time.Sleep(2 * time.Second)
				for {
					out, capErr := captureAgentOutput(plan, job, targetPane, 30)
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
	Status string `json:"status"` // "idle", "working", "pending_user", "disconnected", or "unknown"
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
			var plan *orchestration.Plan
			var job *orchestration.Job
			targetPane := target
			displayTarget := target
			if target == "" {
				slug, jobID, perr := agentSlugJob(cmd.Context(), args)
				if perr != nil {
					return perr
				}
				var err error
				plan, job, err = resolveAgentTarget(cmd.Context(), slug, jobID)
				if err != nil {
					return err
				}
				targetPane = ""
				if pane, paneErr := orchestration.ResolveInteractiveAgentPane(plan, job); paneErr == nil {
					displayTarget = pane
				} else {
					displayTarget = job.Title
				}
			}

			status := AgentStatus{Target: displayTarget}

			// Consult the authoritative daemon session status before scraping
			// the tmux pane. The daemon registry (the same source of truth
			// `flow agent list` uses) knows when an agent is blocked waiting on
			// the user (pending_user) — a state the pane scrape would mislabel
			// as "working". Fetch the session once and reuse it for both the
			// pending_user short-circuit and the capture-error fallback below.
			var sess *models.Session
			if job != nil {
				client := daemon.NewWithAutoStart()
				s, err := client.GetSession(context.Background(), job.ID)
				client.Close()
				if err == nil {
					sess = s
				}
			}

			if sess != nil && sess.Status == "pending_user" {
				// Agent is blocked waiting on the user; the pane scrape can't
				// see this, so trust the daemon and skip it.
				status.Status = "pending_user"
			} else {
				output, capErr := captureAgentOutput(plan, job, targetPane, 30)
				if capErr != nil {
					// Capture failed. Consult the daemon registry to distinguish
					// a genuinely gone agent (→ disconnected) from a running
					// agent whose capture hit a transport error (→ unknown), so
					// status agrees with list.
					status.Status = "disconnected"
					if sess != nil && sess.Status != "completed" && sess.Status != "failed" {
						status.Status = "unknown"
					}
				} else if isAgentIdle(output) {
					status.Status = "idle"
				} else {
					status.Status = "working"
				}
			}

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(status)
			}

			fmt.Printf("%s: %s\n", displayTarget, status.Status)
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
			client := daemon.NewWithAutoStart()
			defer client.Close()

			sessions, err := client.GetSessions(ctx)
			if err != nil {
				return fmt.Errorf("could not query sessions: %w", err)
			}

			var active []AgentListEntry
			for _, s := range sessions {
				// Include pending_user so agents blocked waiting on the user
				// stay visible instead of silently dropping off the list.
				if (s.Status == "running" || s.Status == "pending_user") && s.PlanName != "" {
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
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, jobID, perr := agentSlugJob(cmd.Context(), args)
			if perr != nil {
				return perr
			}
			plan, job, err := resolveAgentTarget(cmd.Context(), slug, jobID)
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

func newAgentKillCmd() *cobra.Command {
	var sessionID string
	cmd := &cobra.Command{
		Use:   "kill [<slug> <job>]",
		Short: "Terminate a running interactive agent and close its pane",
		Long: `Sends SIGTERM to the agent process, closes its out-of-process PTY,
and marks the session as interrupted in the daemon registry.

The treemux pane closes automatically once the PTY receives EOF —
no daemon restart required.

Targets a single agent by plan slug and job name, OR — with --id — by the
daemon session id shown in 'flow agent list'. The --id form talks straight
to the daemon registry and does NOT resolve the on-disk plan, so it can reap
an ORPHANED agent whose plan has already been finished/archived (the case
where '<slug> <job>' fails with "could not resolve plan").`,
		Example: `  # Kill the coordinator agent for a plan
  flow agent kill agent-lifecycle-reaping coordinate-agent-lifecycle-reaping

  # Kill an orphan by its session id (plan already gone)
  flow agent kill --id coordinate-my-feature-701748e2`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate the target shape BEFORE touching the daemon so bad
			// invocations don't auto-start a daemon as a side effect.
			if sessionID != "" && len(args) > 0 {
				return fmt.Errorf("pass either --id or <slug> <job>, not both")
			}
			var slug, jobID string
			if sessionID == "" {
				var perr error
				slug, jobID, perr = agentSlugJob(cmd.Context(), args)
				if perr != nil {
					return perr
				}
			}

			ctx := context.Background()
			client := daemon.NewWithAutoStart()
			defer client.Close()

			// --id bypasses plan resolution entirely: kill straight by the
			// daemon's session id. This is the orphan escape hatch — it works
			// even after the plan has been finished/archived.
			if sessionID != "" {
				if err := client.KillSession(ctx, sessionID); err != nil {
					return fmt.Errorf("kill agent %q: %w", sessionID, err)
				}
				fmt.Printf("Agent session %q killed.\n", sessionID)
				return nil
			}

			_, job, err := resolveAgentTarget(cmd.Context(), slug, jobID)
			if err != nil {
				return err
			}

			if err := client.KillSession(ctx, job.ID); err != nil {
				return fmt.Errorf("kill agent: %w", err)
			}

			fmt.Printf("Agent %q killed.\n", job.Title)
			return nil
		},
	}
	cmd.Flags().StringVar(&sessionID, "id", "", "Kill by daemon session id (bypasses plan resolution; reaps orphans)")
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
