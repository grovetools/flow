package orchestration

import (
	"os"
	"path/filepath"
	
	"github.com/grovetools/core/config"
	"github.com/grovetools/core/git"
)

// ExecutionContext provides context about where jobs are being executed from
// This helps resolve the disconnect between plan directories and project directories
type ExecutionContext struct {
	// PlanDirectory is where the plan files are stored
	PlanDirectory string
	
	// ProjectRoot is the root of the project (where grove.yml is located)
	ProjectRoot string
	
	// GitRoot is the root of the git repository
	GitRoot string
	
	// WorkingDirectory is where commands should be executed
	WorkingDirectory string
	
	// Config is the loaded grove configuration
	Config *config.Config
}

// NewExecutionContext creates a new execution context
func NewExecutionContext(planDir string, cfg *config.Config) (*ExecutionContext, error) {
	ctx := &ExecutionContext{
		PlanDirectory: planDir,
		Config:        cfg,
	}
	
	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	ctx.WorkingDirectory = cwd
	
	// Find project root (where grove.yml is)
	if cfg != nil {
		// If we have a config, find where it came from
		configPath, err := config.FindConfigFile(cwd)
		if err == nil {
			ctx.ProjectRoot = filepath.Dir(configPath)
		} else {
			ctx.ProjectRoot = cwd
		}
	} else {
		ctx.ProjectRoot = cwd
	}
	
	// Find git root - try multiple strategies
	// First try from project root
	gitRoot, err := git.GetGitRoot(ctx.ProjectRoot)
	if err != nil {
		// Try from plan directory
		gitRoot, err = git.GetGitRoot(planDir)
		if err != nil {
			// Try from current directory
			gitRoot, err = git.GetGitRoot(cwd)
			if err != nil {
				// No git root found - that's OK for some operations
				ctx.GitRoot = ""
			} else {
				ctx.GitRoot = gitRoot
			}
		} else {
			ctx.GitRoot = gitRoot
		}
	} else {
		ctx.GitRoot = gitRoot
	}
	
	return ctx, nil
}

// ResolvePromptSource resolves a prompt source file path
func (ctx *ExecutionContext) ResolvePromptSource(source string) string {
	// Try multiple resolutions
	candidates := []string{
		// Absolute path
		source,
		// Relative to plan directory
		filepath.Join(ctx.PlanDirectory, source),
		// Relative to project root
		filepath.Join(ctx.ProjectRoot, source),
		// Relative to parent of plan directory (for cross-plan references)
		filepath.Join(filepath.Dir(ctx.PlanDirectory), source),
	}
	
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	
	// Default to plan directory relative
	return filepath.Join(ctx.PlanDirectory, source)
}

// ResolveContextFile resolves a context file path
func (ctx *ExecutionContext) ResolveContextFile() []string {
	candidates := []string{
		// In plan directory
		filepath.Join(ctx.PlanDirectory, ".grove", "context"),
		filepath.Join(ctx.PlanDirectory, "CLAUDE.md"),
		// In project root
		filepath.Join(ctx.ProjectRoot, ".grove", "context"),
		filepath.Join(ctx.ProjectRoot, "CLAUDE.md"),
	}
	
	var found []string
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			found = append(found, candidate)
		}
	}
	
	return found
}

// LogDirectory returns where logs should be written
func (ctx *ExecutionContext) LogDirectory() string {
	// Prefer project root for logs
	if ctx.ProjectRoot != "" {
		return filepath.Join(ctx.ProjectRoot, ".grove", "logs")
	}
	// Fall back to plan directory
	return filepath.Join(ctx.PlanDirectory, ".logs")
}

// WorktreeBaseDirectory returns where worktrees should be created
func (ctx *ExecutionContext) WorktreeBaseDirectory() string {
	if ctx.GitRoot != "" {
		return filepath.Join(ctx.GitRoot, ".grove-worktrees")
	}
	return ""
}