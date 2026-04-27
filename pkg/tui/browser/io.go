package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/daemon"
	coreplan "github.com/grovetools/core/pkg/plan"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/core/state"
	"github.com/grovetools/core/util/delegation"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/flow/pkg/planutil"
)

// refreshTickMsg is emitted on the periodic refresh tick.
type refreshTickMsg time.Time

// refreshTick returns a tea.Cmd that emits refreshTickMsg after
// refreshInterval has elapsed.
func refreshTick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return refreshTickMsg(t)
	})
}

// planListLoadCompleteMsg carries the result of an async plans list load.
type planListLoadCompleteMsg struct {
	plans []PlanListItem
	error error
}

// gitLogMsg carries the result of fetching the top-level workspace git log.
type gitLogMsg struct {
	content string
	err     error
}

// repoGitLogMsg carries the result of fetching git log for one repo
// inside an ecosystem plan (when the user is navigating repos).
type repoGitLogMsg struct {
	content string
	err     error
}

// fastForwardMsg carries the result of a rebase/update/merge operation
// kicked off from the browser.
type fastForwardMsg struct {
	err     error
	message string
}

// reviewCompleteMsg carries the result of the external `flow plan review`
// command.
type reviewCompleteMsg struct {
	output string
	err    error
}

func fetchGitLogCmd(plansDir string) tea.Cmd {
	return func() tea.Msg {
		gitRoot, err := git.GetGitRoot(plansDir)
		if err != nil {
			gitRoot, err = git.GetGitRoot(".")
			if err != nil {
				return gitLogMsg{err: fmt.Errorf("not in a git repository: %w", err)}
			}
		}

		cmd := exec.Command("git", "log", "--oneline", "--decorate", "--color=always", "--graph", "--all", "--max-count=20")
		cmd.Dir = gitRoot

		output, err := cmd.CombinedOutput()
		if err != nil {
			return gitLogMsg{err: fmt.Errorf("git log failed: %w: %s", err, string(output))}
		}

		return gitLogMsg{content: string(output)}
	}
}

func fetchRepoGitLogCmd(repoPath string) tea.Cmd {
	return func() tea.Msg {
		if repoPath == "" {
			return repoGitLogMsg{err: fmt.Errorf("no repo path provided")}
		}

		cmd := exec.Command("git", "log", "--oneline", "--decorate", "--color=always", "--graph", "--all", "--max-count=20")
		cmd.Dir = repoPath

		output, err := cmd.CombinedOutput()
		if err != nil {
			return repoGitLogMsg{err: fmt.Errorf("git log failed: %w: %s", err, string(output))}
		}

		return repoGitLogMsg{content: string(output)}
	}
}

func loadPlansListCmd(plansDirectory, cwdGitRoot string, showOnHold bool) tea.Cmd {
	return func() tea.Msg {
		plans, err := loadPlansList(plansDirectory, cwdGitRoot, showOnHold)
		return planListLoadCompleteMsg{plans: plans, error: err}
	}
}

// fetchPlans returns the parsed plan list for plansDirectory. It tries
// the daemon first (where the watcher keeps a pre-parsed snapshot) and
// falls back to a direct filesystem scan if the daemon is unreachable
// or has no cached data for this directory yet.
func fetchPlans(plansDirectory string) ([]*orchestration.Plan, error) {
	client := daemon.New()
	defer client.Close()

	if client.IsRunning() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		body, err := client.GetPlansRaw(ctx, plansDirectory)
		if err == nil && len(body) > 0 && string(body) != "[]" && string(body) != "null" {
			var plans []*orchestration.Plan
			if decodeErr := json.Unmarshal(body, &plans); decodeErr == nil {
				// If the daemon's cached snapshot is missing plan dirs that
				// already exist on disk (e.g. just-created plan that the
				// watcher hasn't picked up yet), fall through to a fresh
				// filesystem scan instead of returning a stale list.
				if !daemonPlansAreStale(plansDirectory, plans) {
					// `Job.Dependencies` is `json:"-"`, so the DAG pointer
					// graph is lost across the socket. Rebuild it locally.
					for _, p := range plans {
						_ = p.ResolveDependencies()
					}
					return plans, nil
				}
			}
		}
	}

	return loadPlansFromDisk(plansDirectory)
}

// daemonPlansAreStale returns true when the plans directory contains plan
// subdirectories that are not represented in the daemon's snapshot. A plan
// dir is identified by a `.grove-plan.yml` file or any `*.md` job file —
// matching loadPlansFromDisk's recognition rule.
func daemonPlansAreStale(plansDirectory string, plans []*orchestration.Plan) bool {
	entries, err := os.ReadDir(plansDirectory)
	if err != nil {
		return false
	}
	known := make(map[string]struct{}, len(plans))
	for _, p := range plans {
		known[p.Name] = struct{}{}
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, ok := known[entry.Name()]; ok {
			continue
		}
		planPath := filepath.Join(plansDirectory, entry.Name())
		if _, statErr := os.Stat(filepath.Join(planPath, ".grove-plan.yml")); statErr == nil {
			return true
		}
		mdFiles, _ := filepath.Glob(filepath.Join(planPath, "*.md"))
		if len(mdFiles) > 0 {
			return true
		}
	}
	return false
}

// loadPlansFromDisk is the daemon-less fallback: scan the plansDir and
// invoke orchestration.LoadPlan for each subdirectory.
func loadPlansFromDisk(plansDirectory string) ([]*orchestration.Plan, error) {
	entries, err := os.ReadDir(plansDirectory)
	if err != nil {
		return nil, fmt.Errorf("failed to read plans directory %s: %w", plansDirectory, err)
	}
	plans := make([]*orchestration.Plan, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		planPath := filepath.Join(plansDirectory, entry.Name())
		planConfigPath := filepath.Join(planPath, ".grove-plan.yml")
		mdFiles, _ := filepath.Glob(filepath.Join(planPath, "*.md"))
		if _, err := os.Stat(planConfigPath); err != nil && len(mdFiles) == 0 {
			continue
		}
		plan, err := orchestration.LoadPlan(planPath)
		if err != nil {
			continue
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func loadPlansList(plansDirectory, cwdGitRoot string, showOnHold bool) ([]PlanListItem, error) {
	plans, err := fetchPlans(plansDirectory)
	if err != nil {
		return nil, err
	}

	var items []PlanListItem
	// Hoist workspace discovery out of the per-plan loop. It walks the
	// full ecosystem and used to dominate CPU on plans with .Repos set.
	var provider *workspace.Provider

	for _, plan := range plans {
		if plan.Config != nil && plan.Config.Status == "finished" {
			continue
		}
		if !showOnHold && plan.Config != nil && plan.Config.Status == "hold" {
			continue
		}

		planInfo, statErr := os.Stat(plan.Directory)
		var lastUpdated time.Time
		if statErr == nil {
			lastUpdated = planInfo.ModTime()
		} else {
			lastUpdated = time.Now()
		}

		worktree := ""
		notes := ""
		if plan.Config != nil {
			worktree = plan.Config.Worktree
			notes = plan.Config.Notes
		}

		item := PlanListItem{
			Plan:         plan,
			Name:         plan.Name,
			JobCount:     len(plan.Jobs),
			LastUpdated:  lastUpdated,
			Worktree:     worktree,
			Notes:        notes,
			MergeStatus:  "-",
			ReviewStatus: formatConfigStatus(plan.Config),
		}

		if worktree != "" {
			var gitRoot string
			if cwdGitRoot != "" {
				worktreePath := filepath.Join(cwdGitRoot, ".grove-worktrees", worktree)
				if _, err := os.Stat(worktreePath); err == nil {
					gitRoot = cwdGitRoot
				}
			}
			if gitRoot == "" {
				project, err := workspace.GetProjectByPath(plan.Directory)
				if err == nil && project != nil {
					gitRoot = project.Path
				}
			}
			if gitRoot == "" {
				gitRoot, _ = git.GetGitRoot(plan.Directory)
			}
			if gitRoot == "" {
				gitRoot = planutil.FindGitRootForWorktree(plan.Directory, worktree)
			}

			if gitRoot != "" {
				worktreePath := filepath.Join(gitRoot, ".grove-worktrees", worktree)
				if _, statErr := os.Stat(worktreePath); statErr == nil {
					gitStatus, statusErr := git.GetStatus(worktreePath)
					if statusErr == nil {
						gitStatus.AheadCount = planutil.CommitCount(worktreePath, "main..HEAD")
						gitStatus.BehindCount = planutil.CommitCount(worktreePath, "HEAD..main")
						item.GitStatus = gitStatus

						if plan.Config != nil && len(plan.Config.Repos) > 0 {
							if provider == nil {
								p, perr := planutil.DiscoverWorkspaceProvider()
								if perr == nil {
									provider = p
								}
							}
							if provider == nil {
								item.MergeStatus = "err (discovery failed)"
							} else {
								item.EcosystemRepoStatuses, item.MergeStatus = planutil.EcosystemRepoDetails(plan, worktree, provider)
							}
						} else {
							item.MergeStatus = planutil.MergeStatus(gitRoot, worktree)
						}
					}
				}
			}
		}

		statusCounts := make(map[orchestration.JobStatus]int)
		for _, job := range plan.Jobs {
			statusCounts[job.Status]++
		}

		statusParts := make(map[string]int)
		var statusStrParts []string
		if c := statusCounts[orchestration.JobStatusCompleted]; c > 0 {
			statusStrParts = append(statusStrParts, fmt.Sprintf("%d completed", c))
			statusParts["completed"] = c
		}
		if c := statusCounts[orchestration.JobStatusRunning]; c > 0 {
			statusStrParts = append(statusStrParts, fmt.Sprintf("%d running", c))
			statusParts["running"] = c
		}
		if c := statusCounts[orchestration.JobStatusPending] + statusCounts[orchestration.JobStatusPendingUser] + statusCounts[orchestration.JobStatusTodo]; c > 0 {
			statusStrParts = append(statusStrParts, fmt.Sprintf("%d pending", c))
			statusParts["pending"] = c
		}
		if c := statusCounts[orchestration.JobStatusFailed]; c > 0 {
			statusStrParts = append(statusStrParts, fmt.Sprintf("%d failed", c))
			statusParts["failed"] = c
		}
		if c := statusCounts[orchestration.JobStatusBlocked]; c > 0 {
			statusStrParts = append(statusStrParts, fmt.Sprintf("%d blocked", c))
			statusParts["blocked"] = c
		}
		if c := statusCounts[orchestration.JobStatusHold]; c > 0 {
			statusStrParts = append(statusStrParts, fmt.Sprintf("%d on hold", c))
			statusParts["hold"] = c
		}
		if c := statusCounts[orchestration.JobStatusAbandoned]; c > 0 {
			statusStrParts = append(statusStrParts, fmt.Sprintf("%d abandoned", c))
			statusParts["abandoned"] = c
		}
		item.StatusParts = statusParts
		if len(statusStrParts) > 0 {
			item.Status = strings.Join(statusStrParts, ", ")
		} else {
			item.Status = "no jobs"
		}

		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].LastUpdated.After(items[j].LastUpdated)
	})

	return items, nil
}

// formatConfigStatus formats the plan's config status for display in the
// REVIEW column of the browser table.
func formatConfigStatus(config *orchestration.PlanConfig) string {
	if config == nil || config.Status == "" {
		return "-"
	}
	switch config.Status {
	case "review":
		return "Review"
	case "hold":
		return "Hold"
	case "finished":
		return "Finished"
	default:
		return config.Status
	}
}

// executePlanFinish launches `flow plan finish` as a subprocess, first
// setting the active plan so the subprocess picks it up.
func executePlanFinish(plan *orchestration.Plan) tea.Cmd {
	return tea.Sequence(
		func() tea.Msg {
			if err := state.Set(coreplan.StateKey, plan.Name); err != nil {
				return err
			}
			return nil
		},
		tea.ExecProcess(delegation.Command("flow", "plan", "finish"),
			func(err error) tea.Msg { return nil }),
	)
}

// executePlanOpen launches `flow plan open` as a subprocess.
func executePlanOpen(plan *orchestration.Plan) tea.Cmd {
	return tea.Sequence(
		func() tea.Msg {
			if err := state.Set(coreplan.StateKey, plan.Name); err != nil {
				return err
			}
			return nil
		},
		func() tea.Cmd {
			openCmd := delegation.Command("flow", "plan", "open")
			openCmd.Env = append(os.Environ(), "GROVE_FLOW_TUI_MODE=true")
			return tea.ExecProcess(openCmd, func(err error) tea.Msg { return nil })
		}(),
	)
}

// executePlanReview shells out to `grove flow plan review` and returns
// its output via reviewCompleteMsg.
func executePlanReview(plan *orchestration.Plan) tea.Cmd {
	return func() tea.Msg {
		if err := state.Set(coreplan.StateKey, plan.Name); err != nil {
			return reviewCompleteMsg{err: err}
		}
		cmd := exec.Command("grove", "flow", "plan", "review")
		output, err := cmd.CombinedOutput()
		if err != nil {
			outputStr := strings.TrimSpace(string(output))
			if outputStr != "" {
				err = fmt.Errorf("%w: %s", err, outputStr)
			}
		}
		return reviewCompleteMsg{output: string(output), err: err}
	}
}

// executeNewPlan launches `flow plan init --tui` as a subprocess. The
// old plan-list TUI used to swap to the in-process plan_init_tui model,
// but that required cmd-package-internal coupling; delegating keeps this
// package self-contained and matches how finish/open are wired.
func executeNewPlan() tea.Cmd {
	return tea.ExecProcess(delegation.Command("flow", "plan", "init", "--tui"),
		func(err error) tea.Msg { return nil })
}

// fastForwardUpdateCmd rebases a plan's worktree branch onto main. For
// ecosystem plans it walks every repo in the plan; for single-repo plans
// it operates on the current CWD git root.
func fastForwardUpdateCmd(plan PlanListItem) tea.Cmd {
	return func() tea.Msg {
		if plan.Worktree == "" {
			return fastForwardMsg{err: fmt.Errorf("selected plan has no associated worktree")}
		}

		if plan.Plan.Config != nil && len(plan.Plan.Config.Repos) > 0 {
			var results []string
			var errors []string

			provider, err := planutil.DiscoverWorkspaceProvider()
			if err != nil {
				return fastForwardMsg{err: fmt.Errorf("failed to discover workspaces: %w", err)}
			}
			ecosystemRoot, _ := git.GetGitRoot(plan.Plan.Directory)
			localWorkspaces := provider.LocalWorkspacesInEcosystem(ecosystemRoot)

			for _, repoName := range plan.Plan.Config.Repos {
				repoPath, exists := localWorkspaces[repoName]
				if !exists {
					errors = append(errors, fmt.Sprintf("%s: repo not found locally", repoName))
					continue
				}

				worktreePath := filepath.Join(repoPath, ".grove-worktrees", plan.Worktree)
				if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
					continue
				}

				defaultBranch := "main"
				if err := planutil.RebaseWorktreeBranch(worktreePath, defaultBranch); err != nil {
					errors = append(errors, fmt.Sprintf("%s: %v", repoName, err))
				} else {
					results = append(results, repoName)
				}
			}

			var summary strings.Builder
			if len(results) > 0 {
				summary.WriteString(fmt.Sprintf("Successfully updated %d repos: %s. ", len(results), strings.Join(results, ", ")))
			}
			if len(errors) > 0 {
				return fastForwardMsg{err: fmt.Errorf(summary.String()+"Failed to update %d repos: %s", len(errors), strings.Join(errors, "; "))}
			}
			return fastForwardMsg{message: summary.String()}
		}

		gitRoot, err := git.GetGitRoot(".")
		if err != nil {
			return fastForwardMsg{err: fmt.Errorf("not in a git repository: %w", err)}
		}
		worktreePath := filepath.Join(gitRoot, ".grove-worktrees", plan.Worktree)

		defaultBranch := "main"
		if err := planutil.RebaseWorktreeBranch(worktreePath, defaultBranch); err != nil {
			return fastForwardMsg{err: err}
		}

		return fastForwardMsg{message: fmt.Sprintf("Successfully updated branch '%s' from '%s'.", plan.Worktree, defaultBranch)}
	}
}

// fastForwardMainCmd fast-forwards main onto the plan's worktree branch
// for each repo in the plan (single-repo or ecosystem).
func fastForwardMainCmd(plan PlanListItem) tea.Cmd {
	return func() tea.Msg {
		if plan.Worktree == "" {
			return fastForwardMsg{err: fmt.Errorf("selected plan has no associated worktree")}
		}

		if plan.Plan.Config != nil && len(plan.Plan.Config.Repos) > 0 {
			var results []string
			var errors []string

			provider, err := planutil.DiscoverWorkspaceProvider()
			if err != nil {
				return fastForwardMsg{err: fmt.Errorf("failed to discover workspaces: %w", err)}
			}
			ecosystemRoot, _ := git.GetGitRoot(plan.Plan.Directory)
			localWorkspaces := provider.LocalWorkspacesInEcosystem(ecosystemRoot)

			for _, repoName := range plan.Plan.Config.Repos {
				repoPath, exists := localWorkspaces[repoName]
				if !exists {
					errors = append(errors, fmt.Sprintf("%s: repo not found locally", repoName))
					continue
				}

				defaultBranch := "main"
				if _, err := os.Stat(filepath.Join(repoPath, ".git", "refs", "heads", "main")); os.IsNotExist(err) {
					if _, err := os.Stat(filepath.Join(repoPath, ".git", "refs", "heads", "master")); err == nil {
						defaultBranch = "master"
					} else {
						errors = append(errors, fmt.Sprintf("%s: no main or master branch", repoName))
						continue
					}
				}

				if err := planutil.RebaseAndMergeRepo(repoPath, plan.Worktree, defaultBranch); err != nil {
					errors = append(errors, fmt.Sprintf("%s: %v", repoName, err))
				} else {
					results = append(results, repoName)
				}
			}

			var summary strings.Builder
			if len(results) > 0 {
				summary.WriteString(fmt.Sprintf("Successfully merged %d repos: %s. ", len(results), strings.Join(results, ", ")))
			}
			if len(errors) > 0 {
				return fastForwardMsg{err: fmt.Errorf(summary.String()+"Failed to merge %d repos: %s", len(errors), strings.Join(errors, "; "))}
			}
			return fastForwardMsg{message: summary.String()}
		}

		gitRoot, err := git.GetGitRoot(".")
		if err != nil {
			return fastForwardMsg{err: fmt.Errorf("not in a git repository: %w", err)}
		}
		_, currentBranch, err := git.GetRepoInfo(gitRoot)
		if err != nil {
			return fastForwardMsg{err: fmt.Errorf("could not determine current branch: %w", err)}
		}

		defaultBranch := "main"
		checkMainCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/main")
		checkMainCmd.Dir = gitRoot
		if checkMainCmd.Run() != nil {
			checkMasterCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/master")
			checkMasterCmd.Dir = gitRoot
			if checkMasterCmd.Run() == nil {
				defaultBranch = "master"
			} else {
				return fastForwardMsg{err: fmt.Errorf("neither 'main' nor 'master' branch found")}
			}
		}

		if currentBranch != defaultBranch {
			return fastForwardMsg{err: fmt.Errorf("must be on '%s' branch to fast-forward. current branch: '%s'", defaultBranch, currentBranch)}
		}

		if err := planutil.RebaseAndMergeRepo(gitRoot, plan.Worktree, defaultBranch); err != nil {
			return fastForwardMsg{err: err}
		}
		return fastForwardMsg{message: fmt.Sprintf("Successfully merged '%s' into '%s' and synchronized the worktree.", plan.Worktree, defaultBranch)}
	}
}
