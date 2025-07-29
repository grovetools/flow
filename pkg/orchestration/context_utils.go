package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	
	"github.com/mattsolo1/grove-core/git"
)

// GetProjectRoot attempts to find the project root directory
// This is typically where grove.yml is located
func GetProjectRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return cwd, nil
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

// ResolveWorkingDirectory determines the appropriate working directory for command execution
func ResolveWorkingDirectory(plan *Plan) string {
	// If we're in a git repository, use its root
	if gitRoot, err := GetGitRootSafe(plan.Directory); err == nil {
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