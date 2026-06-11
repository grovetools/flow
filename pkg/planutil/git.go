// Package planutil contains helpers shared between flow's CLI and its
// embeddable TUIs for inspecting plan worktrees: merge/ahead-behind status,
// fast-forward rebase/merge operations, and ecosystem repo rollups. These
// were originally private functions inside flow/cmd/plan_tui.go; extracting
// them here lets flow/pkg/tui/browser import them without dragging in the
// cobra command package.
package planutil

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/grovetools/core/git"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/sirupsen/logrus"

	"github.com/grovetools/flow/pkg/orchestration"
)

// EcosystemRepoStatus holds detailed status for a single repo in an
// ecosystem plan.
type EcosystemRepoStatus struct {
	Name        string
	MergeStatus string
	GitStatus   *git.StatusInfo
}

// CommitCount returns the number of commits in a git rev-list range.
func CommitCount(repoPath, revRange string) int {
	cmd := exec.Command("git", "rev-list", "--count", revRange)
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	count := 0
	_, _ = fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &count)
	return count
}

// MergeStatus determines if a branch can be fast-forwarded into main.
// Returns a short human-readable label such as "Synced", "Ready",
// "Needs Rebase", "Conflicts", "Behind", "no branch", or "err".
func MergeStatus(repoPath, branchName string) string {
	if repoPath == "" || branchName == "" {
		return "-"
	}

	branchCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branchName) //nolint:gosec // branchName is internal, not user input
	branchCheckCmd.Dir = repoPath
	if err := branchCheckCmd.Run(); err != nil {
		return "no branch"
	}

	defaultBranch := "main"
	mainCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/main")
	mainCheckCmd.Dir = repoPath
	if err := mainCheckCmd.Run(); err != nil {
		masterCheckCmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/master")
		masterCheckCmd.Dir = repoPath
		if err := masterCheckCmd.Run(); err != nil {
			return "no main"
		}
		defaultBranch = "master"
	}

	aheadCount := CommitCount(repoPath, defaultBranch+".."+branchName)
	behindCount := CommitCount(repoPath, branchName+".."+defaultBranch)

	if aheadCount == 0 && behindCount == 0 {
		return "Synced"
	}
	if aheadCount > 0 && behindCount == 0 {
		return "Ready"
	}
	if aheadCount == 0 && behindCount > 0 {
		return "Behind"
	}

	mergeBaseCmd := exec.Command("git", "merge-base", defaultBranch, branchName)
	mergeBaseCmd.Dir = repoPath
	mergeBaseOutput, err := mergeBaseCmd.Output()
	if err != nil {
		return "err"
	}
	mergeBase := strings.TrimSpace(string(mergeBaseOutput))

	mainRevCmd := exec.Command("git", "rev-parse", defaultBranch)
	mainRevCmd.Dir = repoPath
	mainRevOutput, err := mainRevCmd.Output()
	if err != nil {
		return "err"
	}
	mainRev := strings.TrimSpace(string(mainRevOutput))

	if mergeBase != mainRev {
		mergeTreeCmd := exec.Command("git", "merge-tree", "--write-tree", defaultBranch, branchName)
		mergeTreeCmd.Dir = repoPath
		output, err := mergeTreeCmd.CombinedOutput()
		if err != nil || strings.Contains(string(output), "CONFLICT") {
			return "Conflicts"
		}
		return "Needs Rebase"
	}

	return "Diverged"
}

// RebaseWorktreeBranch rebases a worktree's branch onto the default branch.
// Must be called with worktreePath set to the worktree directory so git
// commands run inside the worktree's working copy.
func RebaseWorktreeBranch(worktreePath, defaultBranch string) error {
	cmd := exec.Command("git", "rebase", defaultBranch)
	cmd.Dir = worktreePath
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to rebase worktree branch: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

// RebaseAndMergeRepo fast-forwards repoPath's default branch to the tip of
// worktreeBranch, then resets the associated worktree's working copy to
// match. This is the "merge to main" path used by the browser TUI and the
// `flow plan merge-worktree` command.
func RebaseAndMergeRepo(repoPath, worktreeBranch, defaultBranch string) error {
	checkoutCmd := exec.Command("git", "checkout", defaultBranch)
	checkoutCmd.Dir = repoPath
	if output, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to checkout %s: %s", defaultBranch, strings.TrimSpace(string(output)))
	}

	mergeCmd := exec.Command("git", "merge", "--ff-only", worktreeBranch)
	mergeCmd.Dir = repoPath
	if output, err := mergeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fast-forward merge failed: %s", strings.TrimSpace(string(output)))
	}

	if worktreePath, ok := workspace.FindWorktreePath(repoPath, worktreeBranch); ok {
		resetCmd := exec.Command("git", "reset", "--hard", defaultBranch)
		resetCmd.Dir = worktreePath
		if output, err := resetCmd.CombinedOutput(); err != nil {
			fmt.Printf("Warning: failed to sync worktree branch '%s': %s\n", worktreeBranch, strings.TrimSpace(string(output)))
		}
	}

	return nil
}

// EcosystemRepoDetails fetches detailed git and merge status for each repo
// in an ecosystem plan, along with a rollup summary string suitable for
// display in a list.
func EcosystemRepoDetails(plan *orchestration.Plan, worktree string, provider *workspace.Provider) ([]EcosystemRepoStatus, string) {
	if provider == nil {
		return nil, "err (no provider)"
	}
	ecosystemRoot, _ := git.GetGitRoot(plan.Directory)
	localWorkspaces := provider.LocalWorkspacesInEcosystem(ecosystemRoot)

	var details []EcosystemRepoStatus
	statusCounts := make(map[string]int)

	for _, repoName := range plan.Config.Repos {
		repoPath, exists := localWorkspaces[repoName]
		if !exists {
			details = append(details, EcosystemRepoStatus{Name: repoName, MergeStatus: "not found"})
			statusCounts["err"]++
			continue
		}

		gitStatus, _ := git.GetStatus(repoPath)
		mergeStatus := MergeStatus(repoPath, worktree)
		details = append(details, EcosystemRepoStatus{
			Name:        repoName,
			MergeStatus: mergeStatus,
			GitStatus:   gitStatus,
		})
		statusCounts[mergeStatus]++
	}

	var summaryStatus string
	switch {
	case statusCounts["Conflicts"] > 0:
		summaryStatus = fmt.Sprintf("%d Conflicts", statusCounts["Conflicts"])
	case statusCounts["Needs Rebase"] > 0:
		summaryStatus = fmt.Sprintf("%d Rebase", statusCounts["Needs Rebase"])
	case statusCounts["Ready"] > 0:
		summaryStatus = fmt.Sprintf("%d Ready", statusCounts["Ready"])
	case statusCounts["Merged"] == len(plan.Config.Repos):
		summaryStatus = "Merged"
	case statusCounts["err"] > 0:
		summaryStatus = "err"
	default:
		summaryStatus = "Mixed"
	}

	return details, summaryStatus
}

// FindGitRootForWorktree attempts to locate the repository root that owns
// a given worktree name, starting from planPath. Used by the browser TUI
// to render ahead/behind counts for plan list entries.
func FindGitRootForWorktree(planPath, worktreeName string) string {
	project, err := workspace.GetProjectByPath(planPath)
	if err != nil || project == nil {
		return ""
	}

	// Probe the candidate owner roots (this project, then its parent/ecosystem
	// roots) for an existing worktree of the requested name. The first root
	// that owns the worktree wins. Layout knowledge lives in the core helpers,
	// so this resolves legacy and XDG worktrees alike.
	for _, root := range []string{project.Path, project.ParentProjectPath, project.RootEcosystemPath} {
		if root == "" {
			continue
		}
		if _, ok := workspace.FindWorktreePath(root, worktreeName); ok {
			return root
		}
	}

	return ""
}

// DiscoverWorkspaceProvider is a convenience wrapper that runs workspace
// discovery with a quiet logger and returns a Provider. Callers that
// already have a provider should skip this.
func DiscoverWorkspaceProvider() (*workspace.Provider, error) {
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)
	discoveryService := workspace.NewDiscoveryService(logger)
	discoveryResult, err := discoveryService.DiscoverAll()
	if err != nil {
		return nil, err
	}
	return workspace.NewProvider(discoveryResult), nil
}
