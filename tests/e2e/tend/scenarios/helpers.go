package scenarios

import (
	"path/filepath"

	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// setupDefaultEnvironment is a helper function to create a standard sandboxed
// test environment with a correctly configured global grove.yml.
// It returns the paths to the project and notebooks directories.
// The homeDir is managed by the harness and available via ctx.HomeDir().
func setupDefaultEnvironment(ctx *harness.Context, projectName string) (projectDir, notebooksRoot string, err error) {
	// 1. Use the harness-provided sandboxed home directory
	homeDir := ctx.HomeDir()

	// 'code' directory will be our main grove for projects
	codeDir := filepath.Join(homeDir, "code")
	if err = fs.CreateDir(codeDir); err != nil {
		return
	}

	projectDir = filepath.Join(codeDir, projectName)
	ctx.Set("project_dir", projectDir) // Set for reference in tests
	if err = fs.CreateDir(projectDir); err != nil {
		return
	}

	// 2. Initialize project as a git repo and add a basic grove.yml
	if _, err = git.SetupTestRepo(projectDir); err != nil {
		return
	}
	if err = fs.WriteGroveConfig(projectDir, &config.Config{Name: projectName, Version: "1.0"}); err != nil {
		return
	}

	// 3. Configure a centralized notebook location in the sandboxed global config
	notebooksRoot = filepath.Join(homeDir, "notebooks")
	ctx.Set("notebooks_root", notebooksRoot)
	configDir := ctx.ConfigDir() // Use harness-provided config directory
	groveConfigDir := filepath.Join(configDir, "grove")

	notebookConfig := &config.NotebooksConfig{
		Definitions: map[string]*config.Notebook{
			"default": {
				RootDir: notebooksRoot,
			},
		},
		Rules: &config.NotebookRules{
			Default: "default",
		},
	}

	// 4. Create global config with BOTH groves and notebooks, correctly linked.
	globalCfg := &config.Config{
		Version:   "1.0",
		Notebooks: notebookConfig,
		Groves: map[string]config.GroveSourceConfig{
			"code": {
				Path:     "~/code", // Use ~ to test home directory expansion
				Enabled:  true,
				Notebook: "default", // This correctly links projects in ~/code to the 'default' notebook.
			},
		},
	}

	err = fs.WriteGroveConfig(groveConfigDir, globalCfg)
	return
}
