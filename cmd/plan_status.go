package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/cli"
	"github.com/grovetools/core/git"
	grovelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/planutil"
)

// Command flags
var (
	statusTUI     bool   // Kept for backwards compatibility; TUI is now always used unless --json is specified
	planStatusDir string // Workspace or plan directory context
)

// InitPlanStatusFlags initializes the flags for the status command
func InitPlanStatusFlags() {
	// Keep --tui flag for backwards compatibility, but it's now a no-op (TUI is the default)
	planStatusCmd.Flags().BoolVarP(&statusTUI, "tui", "t", false, "Launch interactive TUI (default behavior, kept for backwards compatibility)")
	planStatusCmd.Flags().StringVarP(&planStatusDir, "dir", "d", "", "Workspace or plan directory context (defaults to current directory)")
}

// RunPlanStatus implements the status command.
func RunPlanStatus(cmd *cobra.Command, args []string) error {
	opts := cli.GetOptions(cmd)
	if opts.JSONOutput {
		// Machine output owns stdout. Status verification may emit lifecycle logs
		// (for example when a stale running job is marked interrupted), so route
		// those logs to stderr for the full command, including plan loading and
		// verification—not only while encoding the final envelope.
		previousOutput := grovelogging.SwapGlobalOutput(os.Stderr)
		defer grovelogging.SetGlobalOutput(previousOutput)
	}

	var planName string
	if len(args) > 0 {
		planName = args[0]
	}

	// Use --dir flag value, defaulting to current directory
	contextDir := planStatusDir
	if contextDir == "" {
		contextDir = "."
	}

	// Resolve the plan path with active job support and context directory.
	// The Ctx variant honors --at when set, else falls back to the legacy path.
	planPath, err := resolvePlanPathWithActiveJobCtx(cmd.Context(), planName, contextDir)
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
		orchestration.VerifyRunningJobStatus(plan)
	}

	// Check if JSON output is requested via --json flag
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

// WorktreeStatus represents git and worktree information for JSON output
type WorktreeStatus struct {
	Name         string         `json:"name"`
	Branch       string         `json:"branch,omitempty"`
	GitStatus    *GitStatusInfo `json:"git_status,omitempty"`
	MergeStatus  string         `json:"merge_status"`
	ReviewStatus string         `json:"review_status"`
}

// GitStatusInfo contains git repository status information
type GitStatusInfo struct {
	Clean        bool `json:"clean"`
	AheadCount   int  `json:"ahead_count"`
	BehindCount  int  `json:"behind_count"`
	HasUntracked bool `json:"has_untracked"`
	HasModified  bool `json:"has_modified"`
	HasStaged    bool `json:"has_staged"`
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
		if found, ok := workspace.FindWorktreePath(gitRoot, worktreeName); ok {
			worktreePath = found
		} else {
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
			if found, ok := workspace.FindWorktreePath(gitRoot, worktreeName); ok {
				worktreePath = found
			} else {
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
		gitStatus.AheadCount = planutil.CommitCount(worktreePath, "main..HEAD")
		gitStatus.BehindCount = planutil.CommitCount(worktreePath, "HEAD..main")

		status.GitStatus = &GitStatusInfo{
			Clean:        !gitStatus.IsDirty,
			AheadCount:   gitStatus.AheadCount,
			BehindCount:  gitStatus.BehindCount,
			HasUntracked: gitStatus.UntrackedCount > 0,
			HasModified:  gitStatus.ModifiedCount > 0,
			HasStaged:    gitStatus.StagedCount > 0,
		}

		// Determine merge status
		status.MergeStatus = planutil.MergeStatus(gitRoot, worktreeName)
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

// JobOutput wraps orchestration.Job for JSON serialization, adding failure details.
type JobOutput struct {
	*orchestration.Job
	LastError     string                             `json:"last_error,omitempty"`
	LogPath       string                             `json:"log_path,omitempty"`
	SkillFidelity []orchestration.SkillFidelityState `json:"skill_fidelity,omitempty"`
}

// formatStatusJSON creates JSON output.
func formatStatusJSON(plan *orchestration.Plan) (string, error) {
	// Create wrapper jobs with error details for failed/interrupted jobs
	jobOutputs := make([]JobOutput, 0, len(plan.Jobs))
	for _, job := range plan.Jobs {
		jo := JobOutput{Job: job}

		if job.Status == orchestration.JobStatusFailed || job.Status == "interrupted" {
			// Set error message
			if job.Metadata.LastError != "" {
				jo.LastError = job.Metadata.LastError
			} else if job.Status == "interrupted" {
				jo.LastError = "Job was interrupted (process died or session ended unexpectedly)"
			} else {
				jo.LastError = "Job execution failed without recording a specific error"
			}

			// Set log path (relative to cwd when possible)
			if logPath, err := orchestration.GetJobLogPath(plan, job); err == nil {
				if cwd, err := os.Getwd(); err == nil {
					if relPath, err := filepath.Rel(cwd, logPath); err == nil {
						jo.LogPath = relPath
					} else {
						jo.LogPath = logPath
					}
				} else {
					jo.LogPath = logPath
				}
			}
		}
		// Populate skill fidelity from status.json files if job has a skill sequence
		if len(job.SkillSequence) > 0 {
			artifactDir := filepath.Join(plan.Directory, ".artifacts", job.ID)
			if states, err := orchestration.ReadSkillFidelityStates(artifactDir); err == nil && len(states) > 0 {
				jo.SkillFidelity = states
			}
		}

		jobOutputs = append(jobOutputs, jo)
	}

	// Create a structure for JSON output with git/worktree info
	output := struct {
		Plan     string          `json:"plan"`
		Jobs     []JobOutput     `json:"jobs"`
		Stats    map[string]int  `json:"statistics"`
		Worktree *WorktreeStatus `json:"worktree,omitempty"`
	}{
		Plan:  plan.Name,
		Jobs:  jobOutputs,
		Stats: make(map[string]int),
	}

	// Calculate statistics
	for _, job := range plan.Jobs {
		output.Stats[string(job.Status)]++
	}
	output.Stats["total"] = len(plan.Jobs)

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
