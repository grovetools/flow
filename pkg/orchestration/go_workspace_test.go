package orchestration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGoWork(t *testing.T) {
	// Create a temporary directory
	tmpDir := t.TempDir()
	
	// Create a test go.work file
	goWorkContent := `go 1.24.4

use (
	./grove-canopy
	./grove-core
	./grove-flow
)

use ./grove-hooks

replace github.com/fsnotify/fsevents => ./grove-core/internal/fsevents-stub
`
	
	goWorkPath := filepath.Join(tmpDir, "go.work")
	err := os.WriteFile(goWorkPath, []byte(goWorkContent), 0644)
	require.NoError(t, err)
	
	// Parse the file
	config := &GoWorkspaceConfig{
		RootGoWorkPath: goWorkPath,
		WorkspaceRoot:  tmpDir,
	}
	
	err = parseGoWork(goWorkPath, config)
	require.NoError(t, err)
	
	// Verify the parsed content
	assert.Equal(t, "go 1.24.4", config.GoVersion)
	assert.Len(t, config.ModulePaths, 4)
	assert.Contains(t, config.ModulePaths, "./grove-canopy")
	assert.Contains(t, config.ModulePaths, "./grove-core")
	assert.Contains(t, config.ModulePaths, "./grove-flow")
	assert.Contains(t, config.ModulePaths, "./grove-hooks")
}

func TestGenerateWorktreeGoWork(t *testing.T) {
	config := &GoWorkspaceConfig{
		WorkspaceRoot: "/Users/test/grove-ecosystem",
		GoVersion:     "go 1.24.4",
		ModulePaths: []string{
			"./grove-canopy",
			"./grove-core",
			"./grove-flow",
		},
	}
	
	// Test without filter (all modules)
	result := GenerateWorktreeGoWork(config, nil)
	
	expected := `go 1.24.4

use (
	.
	/Users/test/grove-ecosystem/grove-canopy
	/Users/test/grove-ecosystem/grove-core
	/Users/test/grove-ecosystem/grove-flow
)
`
	
	assert.Equal(t, expected, result)
	
	// Test with filter (only required modules)
	resultFiltered := GenerateWorktreeGoWork(config, []string{"grove-core", "grove-flow"})
	
	expectedFiltered := `go 1.24.4

use (
	.
	/Users/test/grove-ecosystem/grove-core
	/Users/test/grove-ecosystem/grove-flow
)
`
	
	assert.Equal(t, expectedFiltered, resultFiltered)
}

func TestFindRootGoWorkspace(t *testing.T) {
	// Create a nested directory structure
	tmpDir := t.TempDir()
	workspaceRoot := tmpDir
	moduleDir := filepath.Join(workspaceRoot, "grove-flow")
	subDir := filepath.Join(moduleDir, "pkg", "orchestration")
	
	err := os.MkdirAll(subDir, 0755)
	require.NoError(t, err)
	
	// Create go.work at the workspace root
	goWorkContent := `go 1.24.4

use (
	./grove-flow
)
`
	goWorkPath := filepath.Join(workspaceRoot, "go.work")
	err = os.WriteFile(goWorkPath, []byte(goWorkContent), 0644)
	require.NoError(t, err)
	
	// Test finding from a subdirectory
	config, err := FindRootGoWorkspace(subDir)
	require.NoError(t, err)
	require.NotNil(t, config)
	
	assert.Equal(t, goWorkPath, config.RootGoWorkPath)
	assert.Equal(t, workspaceRoot, config.WorkspaceRoot)
	assert.Equal(t, "go 1.24.4", config.GoVersion)
	assert.Len(t, config.ModulePaths, 1)
	assert.Equal(t, "./grove-flow", config.ModulePaths[0])
}

func TestParseGoModRequires(t *testing.T) {
	// Create a temporary directory
	tmpDir := t.TempDir()
	
	// Create a test go.mod file
	goModContent := `module github.com/test/my-module

go 1.21

require (
	github.com/mattsolo1/grove-core v0.2.11
	github.com/mattsolo1/grove-tend v0.2.8
	github.com/some/other-module v1.0.0
)
`
	goModPath := filepath.Join(tmpDir, "go.mod")
	err := os.WriteFile(goModPath, []byte(goModContent), 0644)
	require.NoError(t, err)
	
	// Test with workspace modules
	workspaceModules := []string{
		"./grove-core",
		"./grove-tend",
		"./grove-flow",
		"./grove-context",
	}
	
	required, err := parseGoModRequires(goModPath, workspaceModules)
	require.NoError(t, err)
	
	// Should only find grove-core and grove-tend
	assert.Len(t, required, 2)
	assert.Contains(t, required, "grove-core")
	assert.Contains(t, required, "grove-tend")
}

func TestSetupGoWorkspaceForWorktree(t *testing.T) {
	// Create test directory structure
	tmpDir := t.TempDir()
	workspaceRoot := tmpDir
	moduleDir := filepath.Join(workspaceRoot, "grove-flow")
	worktreeDir := filepath.Join(moduleDir, ".grove-worktrees", "test-worktree")
	
	err := os.MkdirAll(moduleDir, 0755)
	require.NoError(t, err)
	err = os.MkdirAll(worktreeDir, 0755)
	require.NoError(t, err)
	
	// Create go.mod to indicate it's a Go project
	goModContent := `module github.com/test/grove-flow

go 1.24.4
`
	err = os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(goModContent), 0644)
	require.NoError(t, err)
	
	// Create go.work at workspace root
	goWorkContent := `go 1.24.4

use (
	./grove-flow
	./grove-core
)
`
	err = os.WriteFile(filepath.Join(workspaceRoot, "go.work"), []byte(goWorkContent), 0644)
	require.NoError(t, err)
	
	// Setup Go workspace for worktree
	err = SetupGoWorkspaceForWorktree(worktreeDir, moduleDir)
	require.NoError(t, err)
	
	// Verify go.work was created in worktree
	worktreeGoWork := filepath.Join(worktreeDir, "go.work")
	assert.FileExists(t, worktreeGoWork)
	
	// Read and verify the content
	content, err := os.ReadFile(worktreeGoWork)
	require.NoError(t, err)
	
	expected := `go 1.24.4

use (
	.
	` + filepath.Join(workspaceRoot, "grove-flow") + `
	` + filepath.Join(workspaceRoot, "grove-core") + `
)
`
	assert.Equal(t, expected, string(content))
}