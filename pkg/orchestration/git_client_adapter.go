package orchestration

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-core/git"
)

// GitClientAdapter adapts the git.WorktreeManager to the GitClient interface.
type GitClientAdapter struct {
	wtManager  *git.WorktreeManager
	repoPath   string
}

// NewGitClientAdapter creates a new git client adapter.
func NewGitClientAdapter(repoPath string) *GitClientAdapter {
	return &GitClientAdapter{
		wtManager: git.NewWorktreeManager(),
		repoPath:  repoPath,
	}
}

// WorktreeAdd adds a new worktree.
func (g *GitClientAdapter) WorktreeAdd(path, branch string) error {
	ctx := context.Background()
	
	// Check if branch exists
	cmd := exec.CommandContext(ctx, "git", "-C", g.repoPath, "rev-parse", "--verify", branch)
	createBranch := cmd.Run() != nil
	
	return g.wtManager.CreateWorktree(ctx, g.repoPath, path, branch, createBranch)
}

// WorktreeList lists all worktrees.
func (g *GitClientAdapter) WorktreeList() ([]Worktree, error) {
	ctx := context.Background()
	
	gitWorktrees, err := g.wtManager.ListWorktrees(ctx, g.repoPath)
	if err != nil {
		return nil, err
	}
	
	var worktrees []Worktree
	for _, gw := range gitWorktrees {
		wt := Worktree{
			Name:      filepath.Base(gw.Path),
			Path:      gw.Path,
			Branch:    gw.Branch,
			HEAD:      gw.Head,
			IsLocked:  false, // Git worktree info doesn't track lock status
			CreatedAt: time.Now(), // Git doesn't track creation time
		}
		worktrees = append(worktrees, wt)
	}
	
	return worktrees, nil
}

// WorktreeRemove removes a worktree.
func (g *GitClientAdapter) WorktreeRemove(name string, force bool) error {
	ctx := context.Background()
	
	// Find worktree path by name
	worktrees, err := g.WorktreeList()
	if err != nil {
		return err
	}
	
	var path string
	for _, wt := range worktrees {
		if wt.Name == name || strings.HasSuffix(wt.Path, name) {
			path = wt.Path
			break
		}
	}
	
	if path == "" {
		return fmt.Errorf("worktree not found: %s", name)
	}
	
	// Remove worktree
	err = g.wtManager.RemoveWorktree(ctx, g.repoPath, path)
	if err != nil && force {
		// Force removal
		cmd := exec.CommandContext(ctx, "git", "-C", g.repoPath, "worktree", "remove", "--force", path)
		return cmd.Run()
	}
	
	return err
}

// CreateBranch creates a new branch.
func (g *GitClientAdapter) CreateBranch(name string) error {
	ctx := context.Background()
	
	// Create branch from current HEAD
	cmd := exec.CommandContext(ctx, "git", "-C", g.repoPath, "branch", name)
	if err := cmd.Run(); err != nil {
		// Check if branch already exists
		checkCmd := exec.CommandContext(ctx, "git", "-C", g.repoPath, "rev-parse", "--verify", name)
		if checkCmd.Run() == nil {
			// Branch exists, that's OK
			return nil
		}
		return err
	}
	
	return nil
}

// CurrentBranch returns the current branch name.
func (g *GitClientAdapter) CurrentBranch() (string, error) {
	ctx := context.Background()
	
	cmd := exec.CommandContext(ctx, "git", "-C", g.repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	
	return strings.TrimSpace(string(output)), nil
}

// IsGitRepo checks if the path is a git repository.
func (g *GitClientAdapter) IsGitRepo() bool {
	ctx := context.Background()
	
	cmd := exec.CommandContext(ctx, "git", "-C", g.repoPath, "rev-parse", "--git-dir")
	return cmd.Run() == nil
}