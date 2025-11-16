package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Test helper functions
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\nOutput: %s", args, err, output)
	}
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")
}

func addFile(t *testing.T, dir, filename, content string) {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write %s: %v", filename, err)
	}
	runGit(t, dir, "add", filename)
}

func commit(t *testing.T, dir, message string) {
	t.Helper()
	runGit(t, dir, "commit", "-m", message)
}

func createBranch(t *testing.T, dir, branch string) {
	t.Helper()
	runGit(t, dir, "branch", branch)
}

func checkout(t *testing.T, dir, branch string) {
	t.Helper()
	runGit(t, dir, "checkout", branch)
}

func createWorktree(t *testing.T, dir, branch, path string) {
	t.Helper()
	runGit(t, dir, "worktree", "add", "-b", branch, path)
}

// TestGetMergeStatus tests the various merge status scenarios
func TestGetMergeStatus(t *testing.T) {
	// Create a temporary directory for our test repository
	tempDir, err := os.MkdirTemp("", "merge-status-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Initialize a git repository
	initRepo(t, tempDir)

	// Create initial commit on main
	addFile(t, tempDir, "README.md", "# Test")
	commit(t, tempDir, "Initial commit")

	t.Run("Synced - branch identical to main", func(t *testing.T) {
		// Create a branch that's identical to main
		branchName := "synced-branch"
		createBranch(t, tempDir, branchName)

		status := getMergeStatus(tempDir, branchName)
		if status != "Synced" {
			t.Errorf("Expected 'Synced', got '%s'", status)
		}
	})

	t.Run("Ready - branch ahead of main", func(t *testing.T) {
		// Create a branch with a commit ahead of main
		branchName := "ready-branch"
		createBranch(t, tempDir, branchName)
		checkout(t, tempDir, branchName)

		// Add a commit
		addFile(t, tempDir, "feature.txt", "feature")
		commit(t, tempDir, "Add feature")

		// Switch back to main for the status check
		checkout(t, tempDir, "main")

		status := getMergeStatus(tempDir, branchName)
		if status != "Ready" {
			t.Errorf("Expected 'Ready', got '%s'", status)
		}
	})

	t.Run("Behind - branch behind main", func(t *testing.T) {
		// Create a branch, then advance main
		branchName := "behind-branch"
		createBranch(t, tempDir, branchName)

		// Make sure we're on main
		checkout(t, tempDir, "main")

		// Add a commit to main
		addFile(t, tempDir, "update.txt", "update")
		commit(t, tempDir, "Update main")

		status := getMergeStatus(tempDir, branchName)
		if status != "Behind" {
			t.Errorf("Expected 'Behind', got '%s'", status)
		}
	})

	t.Run("Needs Rebase - branch diverged from main", func(t *testing.T) {
		// Create a branch with a commit
		branchName := "diverged-branch"
		createBranch(t, tempDir, branchName)
		checkout(t, tempDir, branchName)

		// Add a commit on the branch
		addFile(t, tempDir, "diverged.txt", "diverged")
		commit(t, tempDir, "Diverged commit")

		// Switch to main and add a different commit
		checkout(t, tempDir, "main")
		addFile(t, tempDir, "main-diverged.txt", "main diverged")
		commit(t, tempDir, "Main diverged commit")

		status := getMergeStatus(tempDir, branchName)
		if status != "Needs Rebase" {
			t.Errorf("Expected 'Needs Rebase', got '%s'", status)
		}
	})

	t.Run("no branch - branch doesn't exist", func(t *testing.T) {
		status := getMergeStatus(tempDir, "nonexistent-branch")
		if status != "no branch" {
			t.Errorf("Expected 'no branch', got '%s'", status)
		}
	})

	t.Run("empty inputs", func(t *testing.T) {
		status := getMergeStatus("", "")
		if status != "-" {
			t.Errorf("Expected '-', got '%s'", status)
		}

		status = getMergeStatus(tempDir, "")
		if status != "-" {
			t.Errorf("Expected '-', got '%s'", status)
		}

		status = getMergeStatus("", "branch")
		if status != "-" {
			t.Errorf("Expected '-', got '%s'", status)
		}
	})
}

// TestGetCommitCount tests the commit counting helper function
func TestGetCommitCount(t *testing.T) {
	// Create a temporary directory for our test repository
	tempDir, err := os.MkdirTemp("", "commit-count-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Initialize a git repository
	initRepo(t, tempDir)

	// Create initial commit
	addFile(t, tempDir, "README.md", "# Test")
	commit(t, tempDir, "Initial commit")

	// Create a branch and add commits
	createBranch(t, tempDir, "test-branch")
	checkout(t, tempDir, "test-branch")

	// Add 3 commits on test-branch
	for i := 1; i <= 3; i++ {
		filename := "file" + string(rune('0'+i)) + ".txt"
		addFile(t, tempDir, filename, "content")
		commit(t, tempDir, "Commit "+string(rune('0'+i)))
	}

	// test-branch should be 3 commits ahead of main
	count := getCommitCount(tempDir, "main..test-branch")
	if count != 3 {
		t.Errorf("Expected 3 commits ahead, got %d", count)
	}

	// main should be 0 commits ahead of test-branch
	count = getCommitCount(tempDir, "test-branch..main")
	if count != 0 {
		t.Errorf("Expected 0 commits ahead, got %d", count)
	}

	// Switch to main and add a commit
	checkout(t, tempDir, "main")
	addFile(t, tempDir, "main-file.txt", "main content")
	commit(t, tempDir, "Main commit")

	// main should be 1 commit ahead of test-branch
	count = getCommitCount(tempDir, "test-branch..main")
	if count != 1 {
		t.Errorf("Expected 1 commit ahead on main, got %d", count)
	}

	// test-branch should still be 3 commits ahead
	count = getCommitCount(tempDir, "main..test-branch")
	if count != 3 {
		t.Errorf("Expected 3 commits ahead on test-branch, got %d", count)
	}
}

// TestRebaseWorktreeBranch tests the worktree rebase functionality
func TestRebaseWorktreeBranch(t *testing.T) {
	// Create a temporary directory for our test
	tempDir, err := os.MkdirTemp("", "rebase-worktree-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Initialize a git repository
	initRepo(t, tempDir)

	// Create initial commit
	addFile(t, tempDir, "README.md", "# Test")
	commit(t, tempDir, "Initial commit")

	// Create a worktree
	worktreePath := filepath.Join(tempDir, ".grove-worktrees", "test-worktree")
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		t.Fatalf("Failed to create worktrees dir: %v", err)
	}
	createWorktree(t, tempDir, "test-worktree", worktreePath)

	// Add a commit in the worktree
	addFile(t, worktreePath, "feature.txt", "feature")
	commit(t, worktreePath, "Add feature")

	// Add a commit to main
	addFile(t, tempDir, "update.txt", "update")
	commit(t, tempDir, "Update main")

	// Rebase the worktree branch
	if err := rebaseWorktreeBranch(worktreePath, "main"); err != nil {
		t.Fatalf("Failed to rebase worktree: %v", err)
	}

	// Verify the worktree has both commits
	// The feature commit should be on top of the main update
	count := getCommitCount(worktreePath, "main..HEAD")
	if count != 1 {
		t.Errorf("Expected worktree to be 1 commit ahead after rebase, got %d", count)
	}

	count = getCommitCount(worktreePath, "HEAD..main")
	if count != 0 {
		t.Errorf("Expected worktree to not be behind main after rebase, got %d", count)
	}
}
