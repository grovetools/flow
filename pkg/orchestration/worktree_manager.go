package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	grovelogging "github.com/mattsolo1/grove-core/logging"
)

var worktreeMgrUlog = grovelogging.NewUnifiedLogger("grove-flow.worktree-manager")

// WorktreeManager handles git worktree lifecycle for job execution.
type WorktreeManager struct {
	baseDir   string
	gitClient GitClient
	logger    Logger
	config    WorktreeConfig
	mu        sync.Mutex // For concurrent operations
}

// WorktreeConfig defines worktree management behavior.
type WorktreeConfig struct {
	AutoCleanup     bool          // Whether to auto-remove worktrees after job completion
	CleanupAge      time.Duration // Age after which stale worktrees are cleaned
	PreserveOnError bool          // Keep worktree if job fails
	CleanupPrompt   bool          // Ask user before cleanup
}

// Worktree represents a git worktree.
type Worktree struct {
	Name      string
	Path      string
	Branch    string
	HEAD      string
	IsLocked  bool
	CreatedAt time.Time
}

// GitClient interface for git operations.
type GitClient interface {
	WorktreeAdd(path, branch string) error
	WorktreeList() ([]Worktree, error)
	WorktreeRemove(name string, force bool) error
	CreateBranch(name string) error
	CurrentBranch() (string, error)
	IsGitRepo() bool
}

// Use the existing Logger interface from orchestrator.go

// WorktreeLock represents a lock on a worktree.
type WorktreeLock struct {
	WorktreeName string
	JobID        string
	LockedAt     time.Time
	PID          int
}

// Default configuration
var DefaultWorktreeConfig = WorktreeConfig{
	AutoCleanup:     true,
	CleanupAge:      24 * time.Hour,
	PreserveOnError: true,
	CleanupPrompt:   false,
}

// NewWorktreeManager creates a new worktree manager.
func NewWorktreeManager(baseDir string, gitClient GitClient, logger Logger) (*WorktreeManager, error) {
	// Validate git repository
	if !gitClient.IsGitRepo() {
		return nil, fmt.Errorf("not a git repository")
	}

	// Create base directory if needed
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("creating base directory: %w", err)
	}

	return &WorktreeManager{
		baseDir:   baseDir,
		gitClient: gitClient,
		logger:    logger,
		config:    DefaultWorktreeConfig,
	}, nil
}

// SetConfig updates the worktree configuration.
func (wm *WorktreeManager) SetConfig(config WorktreeConfig) {
	wm.config = config
}

// CreateWorktree creates a new worktree.
func (wm *WorktreeManager) CreateWorktree(name string, baseBranch string) (string, error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	// Validate name
	if err := validateWorktreeName(name); err != nil {
		return "", fmt.Errorf("invalid worktree name: %w", err)
	}

	// Check if already exists
	worktrees, err := wm.gitClient.WorktreeList()
	if err != nil {
		return "", fmt.Errorf("listing worktrees: %w", err)
	}

	for _, wt := range worktrees {
		if wt.Name == name || strings.HasSuffix(wt.Path, name) {
			return wt.Path, nil // Already exists
		}
	}

	// Create branch name
	branchName := wm.formatBranchName(baseBranch, name)
	
	// Create branch
	if err := wm.gitClient.CreateBranch(branchName); err != nil {
		// Branch might already exist, which is OK
		wm.logger.Debug("branch creation failed (may already exist)", "branch", branchName, "error", err)
	}

	// Create worktree path
	worktreePath := wm.formatWorktreePath(baseBranch, name)

	// Add worktree
	if err := wm.gitClient.WorktreeAdd(worktreePath, branchName); err != nil {
		return "", fmt.Errorf("adding worktree: %w", err)
	}

	wm.logger.Info("created worktree", "name", name, "path", worktreePath, "branch", branchName)
	return worktreePath, nil
}

// GetOrCreateWorktree gets an existing worktree or creates a new one.
func (wm *WorktreeManager) GetOrCreateWorktree(name string) (string, error) {
	// First try to get existing
	worktrees, err := wm.gitClient.WorktreeList()
	if err != nil {
		return "", fmt.Errorf("listing worktrees: %w", err)
	}

	for _, wt := range worktrees {
		if wt.Name == name || strings.HasSuffix(wt.Path, name) {
			wm.logger.Debug("found existing worktree", "name", name, "path", wt.Path)
			
			
			return wt.Path, nil
		}
	}

	// Get current branch as base
	baseBranch, err := wm.gitClient.CurrentBranch()
	if err != nil {
		return "", fmt.Errorf("getting current branch: %w", err)
	}

	// Create new worktree
	return wm.CreateWorktree(name, baseBranch)
}

// RemoveWorktree removes a worktree.
func (wm *WorktreeManager) RemoveWorktree(name string, force bool) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	// Check if locked
	if locked, lock := wm.IsLocked(name); locked && !force {
		return fmt.Errorf("worktree is locked by job %s (PID: %d)", lock.JobID, lock.PID)
	}

	// Remove from git
	if err := wm.gitClient.WorktreeRemove(name, force); err != nil {
		return fmt.Errorf("removing worktree: %w", err)
	}

	// Clean up directory
	worktreePath := filepath.Join(wm.baseDir, name)
	if err := os.RemoveAll(worktreePath); err != nil {
		wm.logger.Error("failed to remove worktree directory", "path", worktreePath, "error", err)
	}

	// Remove lock file if exists
	lockPath := wm.getLockPath(name)
	os.Remove(lockPath)

	wm.logger.Info("removed worktree", "name", name)
	return nil
}

// ListWorktrees returns all worktrees managed by this manager.
func (wm *WorktreeManager) ListWorktrees() ([]Worktree, error) {
	allWorktrees, err := wm.gitClient.WorktreeList()
	if err != nil {
		return nil, fmt.Errorf("listing worktrees: %w", err)
	}

	// Filter by base directory
	var managed []Worktree
	for _, wt := range allWorktrees {
		if strings.HasPrefix(wt.Path, wm.baseDir) {
			managed = append(managed, wt)
		}
	}

	return managed, nil
}

// CleanupStaleWorktrees removes worktrees older than the specified age.
func (wm *WorktreeManager) CleanupStaleWorktrees(age time.Duration) error {
	worktrees, err := wm.ListWorktrees()
	if err != nil {
		return fmt.Errorf("listing worktrees: %w", err)
	}

	now := time.Now()
	var cleaned int

	for _, wt := range worktrees {
		// Skip if locked
		if locked, _ := wm.IsLocked(wt.Name); locked {
			continue
		}

		// Check age (use directory modification time as proxy)
		info, err := os.Stat(wt.Path)
		if err != nil {
			continue
		}

		if now.Sub(info.ModTime()) > age {
			if err := wm.RemoveWorktree(wt.Name, false); err != nil {
				wm.logger.Error("failed to cleanup stale worktree", "name", wt.Name, "error", err)
			} else {
				cleaned++
			}
		}
	}

	wm.logger.Info("cleaned up stale worktrees", "count", cleaned)
	return nil
}

// CleanupJobWorktree cleans up a worktree after job completion.
func (wm *WorktreeManager) CleanupJobWorktree(job *Job) error {
	if !wm.config.AutoCleanup {
		return nil
	}

	if job.Status == JobStatusFailed && wm.config.PreserveOnError {
		wm.logger.Info("preserving worktree due to job failure", "worktree", job.Worktree)
		return nil
	}

	if wm.config.CleanupPrompt {
		ctx := context.Background()
		worktreeMgrUlog.Info("User prompt for worktree cleanup").
			Field("worktree", job.Worktree).
			Field("job_id", job.ID).
			Pretty(fmt.Sprintf("Remove worktree '%s' for completed job? [y/N]: ", job.Worktree)).
			PrettyOnly().
			Log(ctx)
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			return nil
		}
	}

	return wm.RemoveWorktree(job.Worktree, false)
}

// Lock management

// LockWorktree locks a worktree for exclusive use.
func (wm *WorktreeManager) LockWorktree(name string, jobID string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	lockPath := wm.getLockPath(name)
	
	// Check if already locked
	if _, err := os.Stat(lockPath); err == nil {
		return fmt.Errorf("worktree already locked")
	}

	lock := WorktreeLock{
		WorktreeName: name,
		JobID:        jobID,
		LockedAt:     time.Now(),
		PID:          os.Getpid(),
	}

	// Write lock file
	content := fmt.Sprintf("%s\n%d\n%s", lock.JobID, lock.PID, lock.LockedAt.Format(time.RFC3339))
	if err := os.WriteFile(lockPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing lock file: %w", err)
	}

	return nil
}

// UnlockWorktree removes the lock from a worktree.
func (wm *WorktreeManager) UnlockWorktree(name string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	lockPath := wm.getLockPath(name)
	return os.Remove(lockPath)
}

// IsLocked checks if a worktree is locked.
func (wm *WorktreeManager) IsLocked(name string) (bool, *WorktreeLock) {
	lockPath := wm.getLockPath(name)
	
	content, err := os.ReadFile(lockPath)
	if err != nil {
		return false, nil
	}

	parts := strings.Split(string(content), "\n")
	if len(parts) < 3 {
		return false, nil
	}

	var pid int
	fmt.Sscanf(parts[1], "%d", &pid)

	lockedAt, _ := time.Parse(time.RFC3339, parts[2])

	lock := &WorktreeLock{
		WorktreeName: name,
		JobID:        parts[0],
		PID:          pid,
		LockedAt:     lockedAt,
	}

	// Check if process is still alive
	if !isProcessAlive(pid) {
		// Stale lock, remove it
		os.Remove(lockPath)
		return false, nil
	}

	return true, lock
}

// Helper functions

func (wm *WorktreeManager) formatWorktreePath(planName, worktreeName string) string {
	// Pattern: {base-dir}/{plan-name}-worktrees/{worktree-name}
	return filepath.Join(wm.baseDir, fmt.Sprintf("%s-worktrees", sanitizeForPath(planName)), worktreeName)
}

func (wm *WorktreeManager) formatBranchName(planName, worktreeName string) string {
	// Pattern: grove-flow/{plan-name}/{worktree-name}
	return fmt.Sprintf("grove-flow/%s/%s", sanitizeForPath(planName), worktreeName)
}

func (wm *WorktreeManager) getLockPath(name string) string {
	return filepath.Join(wm.baseDir, ".locks", name+".lock")
}

func validateWorktreeName(name string) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}

	if len(name) > 100 {
		return fmt.Errorf("name too long (max 100 characters)")
	}

	// Check for invalid characters
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(name) {
		return fmt.Errorf("name contains invalid characters (use only letters, numbers, hyphens, and underscores)")
	}

	// Check for path traversal
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("name contains path traversal characters")
	}

	return nil
}

func sanitizeForPath(s string) string {
	// Replace spaces and special chars with hyphens
	s = regexp.MustCompile(`[^a-zA-Z0-9-_]+`).ReplaceAllString(s, "-")
	// Remove leading/trailing hyphens
	s = strings.Trim(s, "-")
	// Collapse multiple hyphens
	s = regexp.MustCompile(`-+`).ReplaceAllString(s, "-")
	return s
}

func isProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	
	// On Unix, this doesn't actually check if process exists
	// We need to send signal 0 to check
	err = process.Signal(os.Signal(nil))
	return err == nil
}

