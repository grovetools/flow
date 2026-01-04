package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/mattsolo1/grove-core/cli"
	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-core/pkg/process"
	"github.com/mattsolo1/grove-core/pkg/workspace"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/spf13/cobra"
)

// Command flags
var (
	statusTUI bool // Kept for backwards compatibility; TUI is now always used unless --json is specified
)

// InitPlanStatusFlags initializes the flags for the status command
func InitPlanStatusFlags() {
	// Keep --tui flag for backwards compatibility, but it's now a no-op (TUI is the default)
	planStatusCmd.Flags().BoolVarP(&statusTUI, "tui", "t", false, "Launch interactive TUI (default behavior, kept for backwards compatibility)")
}

// RunPlanStatus implements the status command.
func RunPlanStatus(cmd *cobra.Command, args []string) error {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}

	// Resolve the plan path with active job support
	planPath, err := resolvePlanPathWithActiveJob(dir)
	if err != nil {
		// Smart Redirect: If no active plan is set, redirect to the plan list TUI.
		isNoActiveJobError := strings.Contains(err.Error(), "no active job set")
		isTTY := isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())

		if isNoActiveJobError && isTTY {
			fmt.Println("No active plan set. Launching plan browser...")
			// The runPlanTUI function handles the `flow plan tui` command.
			return runPlanTUI(cmd, []string{}) // Pass empty args to tui command
		}
		return fmt.Errorf("could not resolve plan path: %w", err)
	}

	// Load plan from the resolved directory
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}

	// Build dependency graph
	graph, err := orchestration.BuildDependencyGraph(plan)
	if err != nil {
		return fmt.Errorf("build dependency graph: %w", err)
	}

	// Verify the status of running jobs using PID liveness checks
	// Skip verification if GROVE_SKIP_PID_CHECK is set (useful for tests)
	if os.Getenv("GROVE_SKIP_PID_CHECK") != "true" {
		VerifyRunningJobStatus(plan)
	}

	// Check if JSON output is requested via --json flag
	opts := cli.GetOptions(cmd)
	if opts.JSONOutput {
		// Output JSON and exit (no TUI)
		output, err := formatStatusJSON(plan)
		if err != nil {
			return fmt.Errorf("format JSON output: %w", err)
		}
		fmt.Print(output)
		return nil
	}

	// Always launch TUI for interactive use
	if !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("flow status requires an interactive terminal to launch the TUI")
	}
	return runStatusTUI(plan, graph)
}

// VerifyRunningJobStatus checks the PID liveness for jobs marked as running.
// If a job's process is dead, its status is updated in-memory to "interrupted".
func VerifyRunningJobStatus(plan *orchestration.Plan) {
	// We use a custom "interrupted" status string for display purposes.
	// This is not persisted to disk - it's only updated in memory.
	const JobStatusInterrupted = orchestration.JobStatus("interrupted")

	for _, job := range plan.Jobs {
		if job.Status != orchestration.JobStatusRunning {
			continue
		}

		// Special handling for interactive agent jobs
		if job.Type == orchestration.JobTypeInteractiveAgent || job.Type == orchestration.JobTypeAgent {
			pid, _, err := findAgentSessionInfo(job.ID)
			if err != nil {
				// Give agent jobs a grace period to register with grove-hooks
				// Agents don't register until their first hook call, which can take 5-30 seconds
				gracePeriod := 30 * time.Second
				timeSinceUpdate := time.Since(job.UpdatedAt)

				if timeSinceUpdate < gracePeriod {
					// Job just started, give it time to register
					continue
				}
				// Grace period expired, mark as interrupted
				job.Status = JobStatusInterrupted
				continue
			}
			if !process.IsProcessAlive(pid) {
				// Associated Claude process is dead
				job.Status = JobStatusInterrupted
			}
		} else {
			// Original logic for other job types
			pid, err := orchestration.ReadLockFile(job.FilePath)
			if err != nil || !process.IsProcessAlive(pid) {
				// Lock file missing or process is dead, mark as interrupted.
				job.Status = JobStatusInterrupted
			}
		}
	}
}

// findRootJobs returns jobs with no dependencies.
func findRootJobs(plan *orchestration.Plan) []*orchestration.Job {
	var roots []*orchestration.Job
	for _, job := range plan.Jobs {
		if len(job.Dependencies) == 0 {
			roots = append(roots, job)
		}
	}
	return roots
}

// findAllDependents returns ALL jobs that depend on the given job (not filtered).
func findAllDependents(job *orchestration.Job, plan *orchestration.Plan) []*orchestration.Job {
	var dependents []*orchestration.Job
	for _, candidate := range plan.Jobs {
		for _, dep := range candidate.Dependencies {
			if dep != nil && dep.ID == job.ID {
				dependents = append(dependents, candidate)
				break
			}
		}
	}
	return dependents
}

// Helper functions for rendering styled text
func renderSuccess(text string) string {
	return theme.DefaultTheme.Success.Render(text)
}

func renderError(text string) string {
	return theme.DefaultTheme.Error.Render(text)
}

func renderWarning(text string) string {
	return theme.DefaultTheme.Warning.Render(text)
}

func renderInfo(text string) string {
	return theme.DefaultTheme.Info.Render(text)
}

func renderMuted(text string) string {
	return theme.DefaultTheme.Muted.Render(text)
}

// colorizeStatus returns a colored status icon.
func colorizeStatus(status orchestration.JobStatus) string {
	switch status {
	case orchestration.JobStatusCompleted:
		return theme.DefaultTheme.Success.Render(theme.IconStatusCompleted)
	case orchestration.JobStatusRunning:
		return theme.DefaultTheme.Warning.Render(theme.IconStatusRunning)
	case orchestration.JobStatusFailed:
		return theme.DefaultTheme.Error.Render(theme.IconStatusFailed)
	case orchestration.JobStatusBlocked:
		return theme.DefaultTheme.Error.Render(theme.IconStatusBlocked)
	case orchestration.JobStatusNeedsReview:
		return theme.DefaultTheme.Info.Render(theme.IconStatusNeedsReview)
	case orchestration.JobStatusPendingUser:
		return theme.DefaultTheme.Info.Render(theme.IconStatusPendingUser)
	case orchestration.JobStatusPendingLLM:
		return theme.DefaultTheme.Warning.Render(theme.IconHeadlessAgent)
	case "interrupted": // Jobs that were running but process is dead
		return theme.DefaultTheme.Magenta.Render(theme.IconStatusInterrupted)
	case orchestration.JobStatusTodo:
		return theme.DefaultTheme.Info.Render(theme.IconStatusTodo)
	case orchestration.JobStatusHold:
		return theme.DefaultTheme.Warning.Render(theme.IconStatusHold)
	case orchestration.JobStatusAbandoned:
		return theme.DefaultTheme.Muted.Render(theme.IconStatusAbandoned)
	default: // Pending
		return theme.IconPending
	}
}

// WorktreeStatus represents git and worktree information for JSON output
type WorktreeStatus struct {
	Name         string          `json:"name"`
	Branch       string          `json:"branch,omitempty"`
	GitStatus    *GitStatusInfo  `json:"git_status,omitempty"`
	MergeStatus  string          `json:"merge_status"`
	ReviewStatus string          `json:"review_status"`
}

// GitStatusInfo contains git repository status information
type GitStatusInfo struct {
	Clean        bool   `json:"clean"`
	AheadCount   int    `json:"ahead_count"`
	BehindCount  int    `json:"behind_count"`
	HasUntracked bool   `json:"has_untracked"`
	HasModified  bool   `json:"has_modified"`
	HasStaged    bool   `json:"has_staged"`
}

// getWorktreeStatus retrieves worktree and git status information for a plan
func getWorktreeStatus(plan *orchestration.Plan) (*WorktreeStatus, error) {
	if plan.Config == nil || plan.Config.Worktree == "" {
		return nil, fmt.Errorf("no worktree configured")
	}

	worktreeName := plan.Config.Worktree
	status := &WorktreeStatus{
		Name:         worktreeName,
		Branch:       worktreeName, // Branch name typically matches worktree name
		MergeStatus:  "-",
		ReviewStatus: "-",
	}

	// Try to get git root from current directory first
	gitRoot, err := git.GetGitRoot(".")
	if err != nil {
		gitRoot = "" // We'll try to find it another way
	}

	// Build worktree path and check if it exists
	var worktreePath string
	if gitRoot != "" {
		worktreePath = filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
		if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
			// Worktree not found at this git root
			gitRoot = ""
		}
	}

	// If we couldn't find the worktree from CWD, try using the plan's directory
	// to infer the workspace and find the git root
	if gitRoot == "" {
		// Try to get workspace for this plan
		project, err := workspace.GetProjectByPath(plan.Directory)
		if err == nil && project != nil {
			gitRoot = project.Path
			worktreePath = filepath.Join(gitRoot, ".grove-worktrees", worktreeName)
			if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
				// Still not found
				return status, nil
			}
		} else {
			// Can't find worktree
			return status, nil
		}
	}

	// Get git status for the worktree
	gitStatus, err := git.GetStatus(worktreePath)
	if err == nil {
		// Override ahead/behind counts to compare against local main, not upstream
		gitStatus.AheadCount = getCommitCount(worktreePath, "main..HEAD")
		gitStatus.BehindCount = getCommitCount(worktreePath, "HEAD..main")

		status.GitStatus = &GitStatusInfo{
			Clean:        !gitStatus.IsDirty,
			AheadCount:   gitStatus.AheadCount,
			BehindCount:  gitStatus.BehindCount,
			HasUntracked: gitStatus.UntrackedCount > 0,
			HasModified:  gitStatus.ModifiedCount > 0,
			HasStaged:    gitStatus.StagedCount > 0,
		}

		// Determine merge status
		status.MergeStatus = getMergeStatus(gitRoot, worktreeName)
	}

	// Determine review status based on plan config
	if plan.Config.Status == "review" {
		status.ReviewStatus = "In Progress"
	} else if plan.Config.Status == "finished" {
		status.ReviewStatus = "Finished"
	} else {
		status.ReviewStatus = "Not Started"
	}

	return status, nil
}

// formatStatusJSON creates JSON output.
func formatStatusJSON(plan *orchestration.Plan) (string, error) {
	// Create a structure for JSON output with git/worktree info
	output := struct {
		Plan         string               `json:"plan"`
		Jobs         []*orchestration.Job `json:"jobs"`
		Stats        map[string]int       `json:"statistics"`
		Worktree     *WorktreeStatus      `json:"worktree,omitempty"`
	}{
		Plan:  plan.Name,
		Jobs:  plan.Jobs,
		Stats: make(map[string]int),
	}

	// Calculate statistics
	for _, job := range plan.Jobs {
		output.Stats[string(job.Status)]++
	}
	output.Stats["total"] = len(plan.Jobs)
	output.Stats["completed"] = output.Stats["completed"]

	// Add worktree status if available
	if plan.Config != nil && plan.Config.Worktree != "" {
		worktreeStatus, err := getWorktreeStatus(plan)
		if err == nil {
			output.Worktree = worktreeStatus
		}
	}

	// Marshal with indentation
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return "", err
	}

	return string(data), nil
}
