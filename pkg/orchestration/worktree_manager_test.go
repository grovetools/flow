package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Mock implementations

type mockGitClient struct {
	worktrees      []Worktree
	currentBranch  string
	addCalled      bool
	removeCalled   bool
	createBranchCalled bool
}

func (m *mockGitClient) WorktreeAdd(path, branch string) error {
	m.addCalled = true
	m.worktrees = append(m.worktrees, Worktree{
		Name:   filepath.Base(path),
		Path:   path,
		Branch: branch,
	})
	return nil
}

func (m *mockGitClient) WorktreeList() ([]Worktree, error) {
	return m.worktrees, nil
}

func (m *mockGitClient) WorktreeRemove(name string, force bool) error {
	m.removeCalled = true
	var updated []Worktree
	for _, wt := range m.worktrees {
		if wt.Name != name && !filepath.HasSuffix(wt.Path, name) {
			updated = append(updated, wt)
		}
	}
	m.worktrees = updated
	return nil
}

func (m *mockGitClient) CreateBranch(name string) error {
	m.createBranchCalled = true
	return nil
}

func (m *mockGitClient) CurrentBranch() (string, error) {
	if m.currentBranch == "" {
		return "main", nil
	}
	return m.currentBranch, nil
}

func (m *mockGitClient) IsGitRepo() bool {
	return true
}

type mockLogger struct {
	logs []string
}

func (m *mockLogger) Info(msg string, keysAndValues ...interface{}) {
	m.logs = append(m.logs, fmt.Sprintf("INFO: %s %v", msg, keysAndValues))
}

func (m *mockLogger) Error(msg string, keysAndValues ...interface{}) {
	m.logs = append(m.logs, fmt.Sprintf("ERROR: %s %v", msg, keysAndValues))
}

func (m *mockLogger) Debug(msg string, keysAndValues ...interface{}) {
	m.logs = append(m.logs, fmt.Sprintf("DEBUG: %s %v", msg, keysAndValues))
}

// Tests

func TestNewWorktreeManager(t *testing.T) {
	tests := []struct {
		name      string
		gitClient GitClient
		wantErr   bool
	}{
		{
			name: "valid git repo",
			gitClient: &mockGitClient{},
			wantErr: false,
		},
		{
			name: "not a git repo",
			gitClient: &mockGitClient{},
			wantErr: false, // mockGitClient always returns true for IsGitRepo
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			logger := &mockLogger{}
			
			wm, err := NewWorktreeManager(dir, tt.gitClient, logger)
			
			if (err != nil) != tt.wantErr {
				t.Errorf("NewWorktreeManager() error = %v, wantErr %v", err, tt.wantErr)
			}
			
			if !tt.wantErr && wm == nil {
				t.Error("expected worktree manager, got nil")
			}
		})
	}
}

func TestCreateWorktree(t *testing.T) {
	tests := []struct {
		name       string
		wtName     string
		baseBranch string
		existing   []Worktree
		wantErr    bool
	}{
		{
			name:       "create new worktree",
			wtName:     "test-wt",
			baseBranch: "main",
			existing:   []Worktree{},
			wantErr:    false,
		},
		{
			name:       "worktree already exists",
			wtName:     "existing",
			baseBranch: "main",
			existing: []Worktree{
				{Name: "existing", Path: "/tmp/existing"},
			},
			wantErr: false, // Should return existing path
		},
		{
			name:       "invalid name",
			wtName:     "test/../../bad",
			baseBranch: "main",
			existing:   []Worktree{},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			gitClient := &mockGitClient{
				worktrees: tt.existing,
			}
			logger := &mockLogger{}
			
			wm, _ := NewWorktreeManager(dir, gitClient, logger)
			
			path, err := wm.CreateWorktree(tt.wtName, tt.baseBranch)
			
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateWorktree() error = %v, wantErr %v", err, tt.wantErr)
			}
			
			if !tt.wantErr {
				if path == "" {
					t.Error("expected path, got empty string")
				}
				
				if tt.name == "create new worktree" && !gitClient.addCalled {
					t.Error("expected WorktreeAdd to be called")
				}
			}
		})
	}
}

func TestGetOrCreateWorktree(t *testing.T) {
	dir := t.TempDir()
	gitClient := &mockGitClient{
		currentBranch: "feature",
	}
	logger := &mockLogger{}
	
	wm, _ := NewWorktreeManager(dir, gitClient, logger)
	
	// First call should create
	path1, err := wm.GetOrCreateWorktree("test-wt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	
	if !gitClient.addCalled {
		t.Error("expected WorktreeAdd to be called")
	}
	
	// Second call should return existing
	gitClient.addCalled = false
	path2, err := wm.GetOrCreateWorktree("test-wt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	
	if gitClient.addCalled {
		t.Error("expected WorktreeAdd NOT to be called for existing worktree")
	}
	
	if path1 != path2 {
		t.Errorf("expected same path, got %s and %s", path1, path2)
	}
}

func TestRemoveWorktree(t *testing.T) {
	dir := t.TempDir()
	gitClient := &mockGitClient{
		worktrees: []Worktree{
			{Name: "test-wt", Path: filepath.Join(dir, "test-wt")},
		},
	}
	logger := &mockLogger{}
	
	wm, _ := NewWorktreeManager(dir, gitClient, logger)
	
	// Create lock directory
	os.MkdirAll(filepath.Join(dir, ".locks"), 0755)
	
	// Test normal removal
	err := wm.RemoveWorktree("test-wt", false)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	
	if !gitClient.removeCalled {
		t.Error("expected WorktreeRemove to be called")
	}
}

func TestLockManagement(t *testing.T) {
	dir := t.TempDir()
	gitClient := &mockGitClient{}
	logger := &mockLogger{}
	
	wm, _ := NewWorktreeManager(dir, gitClient, logger)
	
	// Create lock directory
	os.MkdirAll(filepath.Join(dir, ".locks"), 0755)
	
	// Test locking
	err := wm.LockWorktree("test-wt", "job-123")
	if err != nil {
		t.Fatalf("failed to lock worktree: %v", err)
	}
	
	// Check if locked
	locked, lock := wm.IsLocked("test-wt")
	if !locked {
		t.Error("expected worktree to be locked")
	}
	if lock.JobID != "job-123" {
		t.Errorf("expected job ID 'job-123', got %s", lock.JobID)
	}
	
	// Try to lock again
	err = wm.LockWorktree("test-wt", "job-456")
	if err == nil {
		t.Error("expected error when locking already locked worktree")
	}
	
	// Unlock
	err = wm.UnlockWorktree("test-wt")
	if err != nil {
		t.Fatalf("failed to unlock worktree: %v", err)
	}
	
	// Check if unlocked
	locked, _ = wm.IsLocked("test-wt")
	if locked {
		t.Error("expected worktree to be unlocked")
	}
}

func TestCleanupStaleWorktrees(t *testing.T) {
	dir := t.TempDir()
	
	// Create old worktree directory
	oldWT := filepath.Join(dir, "old-wt")
	os.MkdirAll(oldWT, 0755)
	
	// Set modification time to past
	oldTime := time.Now().Add(-48 * time.Hour)
	os.Chtimes(oldWT, oldTime, oldTime)
	
	gitClient := &mockGitClient{
		worktrees: []Worktree{
			{Name: "old-wt", Path: oldWT},
			{Name: "new-wt", Path: filepath.Join(dir, "new-wt")},
		},
	}
	logger := &mockLogger{}
	
	wm, _ := NewWorktreeManager(dir, gitClient, logger)
	
	// Cleanup worktrees older than 24 hours
	err := wm.CleanupStaleWorktrees(24 * time.Hour)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	
	// Check that old worktree was removed
	if !gitClient.removeCalled {
		t.Error("expected old worktree to be removed")
	}
}

func TestCleanupJobWorktree(t *testing.T) {
	tests := []struct {
		name           string
		job            *Job
		config         WorktreeConfig
		expectRemoval  bool
	}{
		{
			name: "auto cleanup disabled",
			job: &Job{
				Worktree: "test-wt",
				Status:   JobStatusCompleted,
			},
			config: WorktreeConfig{
				AutoCleanup: false,
			},
			expectRemoval: false,
		},
		{
			name: "preserve on error",
			job: &Job{
				Worktree: "test-wt",
				Status:   JobStatusFailed,
			},
			config: WorktreeConfig{
				AutoCleanup:     true,
				PreserveOnError: true,
			},
			expectRemoval: false,
		},
		{
			name: "cleanup on success",
			job: &Job{
				Worktree: "test-wt",
				Status:   JobStatusCompleted,
			},
			config: WorktreeConfig{
				AutoCleanup:     true,
				PreserveOnError: true,
			},
			expectRemoval: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			gitClient := &mockGitClient{
				worktrees: []Worktree{
					{Name: tt.job.Worktree, Path: filepath.Join(dir, tt.job.Worktree)},
				},
			}
			logger := &mockLogger{}
			
			wm, _ := NewWorktreeManager(dir, gitClient, logger)
			wm.SetConfig(tt.config)
			
			err := wm.CleanupJobWorktree(tt.job)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			
			if tt.expectRemoval && !gitClient.removeCalled {
				t.Error("expected worktree to be removed")
			}
			if !tt.expectRemoval && gitClient.removeCalled {
				t.Error("expected worktree NOT to be removed")
			}
		})
	}
}

func TestValidateWorktreeName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid name", "test-worktree_123", false},
		{"empty name", "", true},
		{"too long", string(make([]byte, 101)), true},
		{"path traversal", "../bad", true},
		{"forward slash", "test/bad", true},
		{"backslash", "test\\bad", true},
		{"special chars", "test@#$", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWorktreeName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateWorktreeName() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}