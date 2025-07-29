package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovepm/grove-flow/pkg/orchestration"
)

type JobsCleanupWorktreesCmd struct {
	Dir   string        `arg:"" help:"Plan directory"`
	Age   time.Duration `flag:"" default:"24h" help:"Remove worktrees older than this"`
	Force bool          `flag:"f" help:"Skip confirmation prompts"`
}

func (c *JobsCleanupWorktreesCmd) Run() error {
	return RunJobsCleanupWorktrees(c)
}

// NullLogger implements the Logger interface with no-op methods.
type NullLogger struct{}

func (n NullLogger) Info(msg string, keysAndValues ...interface{})  {}
func (n NullLogger) Error(msg string, keysAndValues ...interface{}) {}
func (n NullLogger) Debug(msg string, keysAndValues ...interface{}) {}

func RunJobsCleanupWorktrees(cmd *JobsCleanupWorktreesCmd) error {
	// Get repository root
	repoRoot, err := findGitRoot(cmd.Dir)
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}

	// Create git client
	gitClient := orchestration.NewGitClientAdapter(repoRoot)

	// Create worktree manager
	baseDir := filepath.Join(repoRoot, ".grove-worktrees")
	logger := NullLogger{} // Use null logger for CLI
	
	wm, err := orchestration.NewWorktreeManager(baseDir, gitClient, logger)
	if err != nil {
		return fmt.Errorf("failed to create worktree manager: %w", err)
	}

	// List worktrees
	worktrees, err := wm.ListWorktrees()
	if err != nil {
		return fmt.Errorf("failed to list worktrees: %w", err)
	}

	if len(worktrees) == 0 {
		fmt.Println("No worktrees found.")
		return nil
	}

	// Find stale worktrees
	var staleWorktrees []orchestration.Worktree
	now := time.Now()
	
	for _, wt := range worktrees {
		// Check if locked
		if locked, lock := wm.IsLocked(wt.Name); locked {
			fmt.Printf("Skipping locked worktree '%s' (locked by job %s)\n", wt.Name, lock.JobID)
			continue
		}

		// For now, use a simple heuristic: if created more than Age ago
		// In reality, we'd check the directory modification time
		age := now.Sub(wt.CreatedAt)
		if age > cmd.Age {
			staleWorktrees = append(staleWorktrees, wt)
		}
	}

	if len(staleWorktrees) == 0 {
		fmt.Printf("No worktrees older than %s found.\n", cmd.Age)
		return nil
	}

	// Show what will be cleaned up
	fmt.Printf("Found %d stale worktrees:\n", len(staleWorktrees))
	for _, wt := range staleWorktrees {
		fmt.Printf("  - %s (branch: %s)\n", wt.Name, wt.Branch)
	}

	// Confirm unless force
	if !cmd.Force {
		fmt.Print("\nRemove these worktrees? [y/N]: ")
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			fmt.Println("Cleanup cancelled.")
			return nil
		}
	}

	// Perform cleanup
	var cleaned, failed int
	for _, wt := range staleWorktrees {
		if err := wm.RemoveWorktree(wt.Name, cmd.Force); err != nil {
			fmt.Printf("✗ Failed to remove '%s': %v\n", wt.Name, err)
			failed++
		} else {
			fmt.Printf("✓ Removed '%s'\n", wt.Name)
			cleaned++
		}
	}

	fmt.Printf("\nCleanup complete: %d removed, %d failed\n", cleaned, failed)
	return nil
}

// findGitRoot finds the root of the git repository.
func findGitRoot(startPath string) (string, error) {
	path, err := filepath.Abs(startPath)
	if err != nil {
		return "", err
	}

	for {
		// Check if .git exists
		gitPath := filepath.Join(path, ".git")
		if info, err := os.Stat(gitPath); err == nil && info.IsDir() {
			return path, nil
		}

		// Move up one directory
		parent := filepath.Dir(path)
		if parent == path {
			// Reached root without finding .git
			return "", fmt.Errorf("not a git repository")
		}
		path = parent
	}
}