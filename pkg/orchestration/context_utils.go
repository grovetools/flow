package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-core/git"
	"github.com/mattsolo1/grove-core/pkg/workspace"
)

// DetermineWorkingDirectory determines the working directory for a job based on
// its worktree and repository configuration. This is the canonical logic used by
// both interactive agent execution and resume operations.
func DetermineWorkingDirectory(plan *Plan, job *Job) (string, error) {
	gitRoot, err := GetProjectGitRoot(plan.Directory)
	if err != nil {
		return "", fmt.Errorf("could not find project git root: %w", err)
	}

	var workDir string
	if job.Worktree != "" {
		// Check if we're already in the requested worktree
		currentPath := gitRoot
		if strings.HasSuffix(currentPath, filepath.Join(".grove-worktrees", job.Worktree)) {
			workDir = currentPath
		} else {
			// Extract main repository path if we're in a worktree
			actualGitRoot := gitRoot
			if strings.Contains(gitRoot, ".grove-worktrees") {
				parts := strings.Split(gitRoot, ".grove-worktrees")
				if len(parts) > 0 {
					actualGitRoot = strings.TrimSuffix(parts[0], string(filepath.Separator))
				}
			}

			// Construct worktree path
			workDir = filepath.Join(actualGitRoot, ".grove-worktrees", job.Worktree)
		}
	} else {
		// No worktree, use the main git repository root
		workDir = gitRoot
	}

	// Scope to sub-project if job.Repository is set (for ecosystem worktrees)
	workDir = ScopeToSubProject(workDir, job)

	return workDir, nil
}

// ScopeToSubProject adjusts a working directory to point to a sub-project
// within an ecosystem worktree when job.Repository is specified.
// This ensures that context generation, command execution, and agent sessions
// all operate in the correct sub-project directory rather than the ecosystem root.
func ScopeToSubProject(workDir string, job *Job) string {
	if job == nil || job.Repository == "" {
		return workDir
	}

	subProjectPath := filepath.Join(workDir, job.Repository)
	if info, err := os.Stat(subProjectPath); err == nil && info.IsDir() {
		return subProjectPath
	}

	// Sub-project directory doesn't exist, return original workDir
	return workDir
}

// GetProjectRootSafe returns the project root using the workspace model.
// It supports both Grove projects (with grove.yml) and non-Grove repos.
// Falls back to git root or current directory if workspace discovery fails.
func GetProjectRootSafe(startPath string) string {
	// Try workspace discovery first - handles all workspace types including non-Grove repos
	if node, err := workspace.GetProjectByPath(startPath); err == nil {
		return node.Path
	}

	// Fallback to git root
	if root, err := git.GetGitRoot(startPath); err == nil {
		return root
	}

	// Last resort: use current directory
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}

	return startPath
}

// GetProjectRoot attempts to find the project root directory by searching upwards for a grove.yml file.
func GetProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not get current working directory: %w", err)
	}

	startDir := dir // For error message

	for {
		configPath := filepath.Join(dir, "grove.yml")
		if _, err := os.Stat(configPath); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root without finding grove.yml
			return "", fmt.Errorf("could not find project root (grove.yml) searching up from %s", startDir)
		}
		dir = parent
	}
}

// GetGitRootSafe attempts to find git root with multiple fallback strategies
func GetGitRootSafe(planDir string) (string, error) {
	// Strategy 1: Try from plan directory
	if gitRoot, err := git.GetGitRoot(planDir); err == nil {
		return gitRoot, nil
	}

	// Strategy 2: Try from current working directory
	if cwd, err := os.Getwd(); err == nil {
		if gitRoot, err := git.GetGitRoot(cwd); err == nil {
			return gitRoot, nil
		}
	}

	// Strategy 3: If plan directory is in a known structure (e.g., nb repos),
	// try to find git root by looking for grove.yml
	// This handles cases like ~/Documents/nb/repos/myrepo/main/plans
	// where the git root might be at ~/Code/myrepo

	return "", fmt.Errorf("could not find git root from plan directory or current directory")
}

// GetProjectGitRoot returns the git root for the project associated with a plan.
// If the plan is inside a notebook, it returns the associated project's path.
// Otherwise, it falls back to GetGitRootSafe.
func GetProjectGitRoot(planDir string) (string, error) {
	// First check if the plan directory is inside a notebook
	if project, notebookRoot, _ := workspace.GetProjectFromNotebookPath(planDir); notebookRoot != "" && project != nil {
		// Plan is in a notebook - use the associated project's path
		return project.Path, nil
	}

	// Normal case - get git root from plan directory
	return GetGitRootSafe(planDir)
}

// ResolveProjectForSessionNaming resolves the appropriate project for tmux session naming.
// If workDir is inside a notebook, it returns the associated project.
// Otherwise, it returns the project at workDir.
func ResolveProjectForSessionNaming(workDir string) (*workspace.WorkspaceNode, error) {
	// First check if we're in a notebook
	if project, notebookRoot, _ := workspace.GetProjectFromNotebookPath(workDir); notebookRoot != "" && project != nil {
		return project, nil
	}
	// Normal case - get project at workDir
	return workspace.GetProjectByPath(workDir)
}

// ResolveWorkingDirectory determines the appropriate working directory for command execution
func ResolveWorkingDirectory(plan *Plan) string {
	// If we're in a git repository, use its root (notebook-aware)
	if gitRoot, err := GetProjectGitRoot(plan.Directory); err == nil {
		return gitRoot
	}
	
	// Otherwise use current working directory
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	
	// Last resort: use plan directory
	return plan.Directory
}

// ResolveLogDirectory determines where log files should be written
func ResolveLogDirectory(plan *Plan, job *Job) string {
	// Try to use project root first
	if cwd, err := os.Getwd(); err == nil {
		logDir := filepath.Join(cwd, ".grove", "logs", plan.Name)
		if err := os.MkdirAll(logDir, 0755); err == nil {
			return logDir
		}
	}
	
	// Fall back to plan directory
	return filepath.Join(plan.Directory, ".logs")
}

// ResolvePromptSource resolves a prompt source file with multiple strategies
func ResolvePromptSource(source string, plan *Plan) (string, error) {
	// If absolute path, use as-is
	if filepath.IsAbs(source) {
		return source, nil
	}
	
	// Try multiple resolution strategies
	candidates := []string{
		// Relative to plan directory
		filepath.Join(plan.Directory, source),
		// Relative to parent of plan directory (for sibling plans)
		filepath.Join(filepath.Dir(plan.Directory), source),
		// Relative to current working directory
		source,
	}
	
	// If we can determine a project root, also try that
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, source))
	}
	
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	
	return "", fmt.Errorf("could not find prompt source: %s", source)
}

// FindContextFiles looks for context files in multiple locations
func FindContextFiles(plan *Plan) []string {
	var contextFiles []string
	
	candidates := []string{
		// In plan directory
		filepath.Join(plan.Directory, ".grove", "context"),
		filepath.Join(plan.Directory, "CLAUDE.md"),
	}
	
	// Also check current working directory / project root
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, ".grove", "context"),
			filepath.Join(cwd, "CLAUDE.md"),
		)
	}
	
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			contextFiles = append(contextFiles, candidate)
		}
	}
	
	return contextFiles
}

// ResolveTemplate resolves a template file path
func ResolveTemplate(templateName string, plan *Plan) (string, error) {
	// If it looks like a path, resolve it as a prompt source
	if strings.Contains(templateName, "/") || strings.HasSuffix(templateName, ".md") {
		return ResolvePromptSource(templateName, plan)
	}
	
	// Otherwise, look for built-in templates
	// First check plan directory for custom templates
	customTemplate := filepath.Join(plan.Directory, "templates", templateName+".md")
	if _, err := os.Stat(customTemplate); err == nil {
		return customTemplate, nil
	}
	
	// Check for built-in templates
	builtinTemplate := filepath.Join("internal", "orchestration", "builtin_templates", templateName+".md")
	if _, err := os.Stat(builtinTemplate); err == nil {
		return builtinTemplate, nil
	}
	
	// Try as a plain file in the plan directory
	plainFile := filepath.Join(plan.Directory, templateName+".md")
	if _, err := os.Stat(plainFile); err == nil {
		return plainFile, nil
	}
	
	return "", fmt.Errorf("template not found: %s", templateName)
}