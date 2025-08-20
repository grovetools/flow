package orchestration

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GoWorkspaceConfig handles Go workspace configuration for worktrees.
type GoWorkspaceConfig struct {
	// Path to the root go.work file
	RootGoWorkPath string
	// Directory containing the root go.work file
	WorkspaceRoot string
	// Go version from the root go.work file
	GoVersion string
	// List of module paths from the root go.work file
	ModulePaths []string
}

// FindRootGoWorkspace searches for a go.work file by walking up the directory tree
// from the given start path.
func FindRootGoWorkspace(startPath string) (*GoWorkspaceConfig, error) {
	// Convert to absolute path
	absPath, err := filepath.Abs(startPath)
	if err != nil {
		return nil, fmt.Errorf("getting absolute path: %w", err)
	}

	// Walk up the directory tree
	currentPath := absPath
	for {
		goWorkPath := filepath.Join(currentPath, "go.work")
		if _, err := os.Stat(goWorkPath); err == nil {
			// Found go.work file
			config := &GoWorkspaceConfig{
				RootGoWorkPath: goWorkPath,
				WorkspaceRoot:  currentPath,
			}
			
			// Parse the go.work file
			if err := parseGoWork(goWorkPath, config); err != nil {
				return nil, fmt.Errorf("parsing go.work: %w", err)
			}
			
			return config, nil
		}

		// Move up one directory
		parent := filepath.Dir(currentPath)
		if parent == currentPath {
			// Reached root directory
			break
		}
		currentPath = parent
	}

	return nil, nil // No go.work file found
}

// parseGoWork parses a go.work file and extracts version and module paths.
func parseGoWork(path string, config *GoWorkspaceConfig) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening go.work file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	inUseBlock := false
	
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		
		// Check for go version
		if strings.HasPrefix(line, "go ") {
			config.GoVersion = line
			continue
		}
		
		// Check for use block start
		if strings.HasPrefix(line, "use (") || line == "use (" {
			inUseBlock = true
			continue
		}
		
		// Check for use block end
		if inUseBlock && line == ")" {
			inUseBlock = false
			continue
		}
		
		// Parse use directives
		if inUseBlock {
			// Just trim whitespace, keep the path as-is
			modulePath := strings.TrimSpace(line)
			if modulePath != "" {
				config.ModulePaths = append(config.ModulePaths, modulePath)
			}
		} else if strings.HasPrefix(line, "use ") {
			// Single line use directive
			modulePath := strings.TrimSpace(strings.TrimPrefix(line, "use"))
			if modulePath != "" {
				config.ModulePaths = append(config.ModulePaths, modulePath)
			}
		}
	}
	
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading go.work file: %w", err)
	}
	
	return nil
}

// GenerateWorktreeGoWork generates a go.work file content for a worktree.
// If requiredModules is provided, only those modules will be included.
func GenerateWorktreeGoWork(config *GoWorkspaceConfig, requiredModules []string) string {
	var sb strings.Builder
	
	// Write go version
	sb.WriteString(config.GoVersion)
	sb.WriteString("\n\n")
	
	// Write use block
	sb.WriteString("use (\n")
	
	// Always include current directory
	sb.WriteString("\t.\n")
	
	// If requiredModules is specified, only include those
	if len(requiredModules) > 0 {
		// Create a map for quick lookup
		requiredMap := make(map[string]bool)
		for _, mod := range requiredModules {
			requiredMap[mod] = true
		}
		
		// Add absolute paths for required modules only
		for _, modulePath := range config.ModulePaths {
			// Extract module name from path (e.g., "./grove-core" -> "grove-core")
			moduleName := filepath.Base(modulePath)
			if requiredMap[moduleName] {
				absPath := filepath.Join(config.WorkspaceRoot, modulePath)
				sb.WriteString(fmt.Sprintf("\t%s\n", absPath))
			}
		}
	} else {
		// No filter specified, include all modules (backward compatibility)
		for _, modulePath := range config.ModulePaths {
			absPath := filepath.Join(config.WorkspaceRoot, modulePath)
			sb.WriteString(fmt.Sprintf("\t%s\n", absPath))
		}
	}
	
	sb.WriteString(")\n")
	
	return sb.String()
}

// parseGoModRequires parses a go.mod file and returns the list of required local modules.
// It looks for require statements that match modules in the workspace.
func parseGoModRequires(goModPath string, workspaceModules []string) ([]string, error) {
	file, err := os.Open(goModPath)
	if err != nil {
		return nil, fmt.Errorf("opening go.mod: %w", err)
	}
	defer file.Close()

	// Create a map of workspace modules for quick lookup
	// Map both full module names and their base names
	moduleMap := make(map[string]string)
	for _, modPath := range workspaceModules {
		moduleName := filepath.Base(modPath)
		moduleMap[moduleName] = moduleName
		// Also try to match by full module path in case it's referenced that way
		if strings.HasPrefix(modPath, "./") {
			moduleMap[modPath[2:]] = moduleName
		}
	}

	var requiredModules []string
	scanner := bufio.NewScanner(file)
	inRequireBlock := false
	
	// Regex to match module names in require statements
	// Matches patterns like: github.com/mattsolo1/grove-core v0.2.11
	moduleRegex := regexp.MustCompile(`^\s*([^\s]+)\s+v[\d\.]+`)
	
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		
		// Check for require block start
		if strings.HasPrefix(line, "require (") || line == "require (" {
			inRequireBlock = true
			continue
		}
		
		// Check for require block end
		if inRequireBlock && line == ")" {
			inRequireBlock = false
			continue
		}
		
		// Parse require directives
		if inRequireBlock || strings.HasPrefix(line, "require ") {
			// Extract module name from the line
			var moduleLine string
			if strings.HasPrefix(line, "require ") {
				moduleLine = strings.TrimPrefix(line, "require ")
			} else {
				moduleLine = line
			}
			
			matches := moduleRegex.FindStringSubmatch(moduleLine)
			if len(matches) > 1 {
				moduleName := matches[1]
				// Check if this module is in our workspace
				for _, workspaceModule := range moduleMap {
					if strings.Contains(moduleName, workspaceModule) {
						requiredModules = append(requiredModules, workspaceModule)
						break
					}
				}
			}
		}
	}
	
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading go.mod: %w", err)
	}
	
	return requiredModules, nil
}

// SetupGoWorkspaceForWorktree checks if the current project uses Go workspaces
// and if so, creates an appropriate go.work file in the worktree.
func SetupGoWorkspaceForWorktree(worktreePath string, gitRoot string) error {
	// Check if this is a Go project (has go.mod)
	goModPath := filepath.Join(gitRoot, "go.mod")
	if _, err := os.Stat(goModPath); os.IsNotExist(err) {
		// Not a Go project, nothing to do
		return nil
	}
	
	// Find root go.work file
	config, err := FindRootGoWorkspace(gitRoot)
	if err != nil {
		return fmt.Errorf("finding root go.work: %w", err)
	}
	
	if config == nil {
		// No go.work file found, nothing to do
		return nil
	}
	
	// Parse go.mod to find required modules
	requiredModules, err := parseGoModRequires(goModPath, config.ModulePaths)
	if err != nil {
		// If we can't parse go.mod, fall back to including all modules
		requiredModules = nil
	}
	
	// Generate worktree-specific go.work content
	content := GenerateWorktreeGoWork(config, requiredModules)
	
	// Write go.work file to worktree
	worktreeGoWorkPath := filepath.Join(worktreePath, "go.work")
	if err := os.WriteFile(worktreeGoWorkPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing go.work to worktree: %w", err)
	}
	
	return nil
}