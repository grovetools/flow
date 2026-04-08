package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/config"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
)

// setupDefaultEnvironment is a helper function to create a standard sandboxed
// test environment with a correctly configured global grove.yml.
// It returns the paths to the project and notebooks directories.
// The homeDir is managed by the harness and available via ctx.HomeDir().
func setupDefaultEnvironment(ctx *harness.Context, projectName string) (projectDir, notebooksRoot string, err error) {
	// 1. Use the harness-provided sandboxed home directory
	homeDir := ctx.HomeDir()
	ctx.Set("home_dir", homeDir) // Store for tests that need to reference it in wrapper scripts

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
	enabled := true
	globalCfg := &config.Config{
		Version:   "1.0",
		Notebooks: notebookConfig,
		Groves: map[string]config.GroveSourceConfig{
			"code": {
				Path:     "~/code", // Use ~ to test home directory expansion
				Enabled:  &enabled,
				Notebook: "default", // This correctly links projects in ~/code to the 'default' notebook.
			},
		},
	}

	err = fs.WriteGroveConfig(groveConfigDir, globalCfg)
	return
}

// setupPlaybookEnvironment wraps setupDefaultEnvironment and materializes a
// minimal playbook at `<notebooksRoot>/workspaces/<projectName>/playbooks/<playbookName>/`.
// The playbook ships two skills (pb-hello, pb-goodbye), one prompt
// (greet.md), and one recipe (test-recipe.md) — just enough surface
// for the playbook CLI, briefing XML, and env injection scenarios to
// exercise every field.
//
// Returns the project dir, the notebooks root, and the absolute path
// to the generated playbook root.
func setupPlaybookEnvironment(ctx *harness.Context, projectName, playbookName string) (projectDir, notebooksRoot, playbookDir string, err error) {
	projectDir, notebooksRoot, err = setupDefaultEnvironment(ctx, projectName)
	if err != nil {
		return
	}

	playbookDir = filepath.Join(notebooksRoot, "workspaces", projectName, "playbooks", playbookName)
	if err = fs.CreateDir(playbookDir); err != nil {
		return
	}

	manifest := fmt.Sprintf(`name = "%s"
version = "1.0.0"
description = "Minimal test playbook"
default_recipe = "test-recipe"
`, playbookName)
	if err = fs.WriteString(filepath.Join(playbookDir, "playbook.toml"), manifest); err != nil {
		return
	}

	helloSkill := `---
name: pb-hello
description: Playbook hello skill used by e2e tests.
---

# Hello
`
	if err = fs.WriteString(filepath.Join(playbookDir, "skills", "pb-hello", "SKILL.md"), helloSkill); err != nil {
		return
	}

	goodbyeSkill := `---
name: pb-goodbye
description: Playbook goodbye skill used by e2e tests.
---

# Goodbye
`
	if err = fs.WriteString(filepath.Join(playbookDir, "skills", "pb-goodbye", "SKILL.md"), goodbyeSkill); err != nil {
		return
	}

	if err = fs.WriteString(filepath.Join(playbookDir, "prompts", "greet.md"), "<!-- purpose: greet the user -->\n\nHello.\n"); err != nil {
		return
	}

	recipeContent := `---
description: Trivial playbook recipe
---

# Test Recipe

Body.
`
	if err = fs.WriteString(filepath.Join(playbookDir, "recipes", "test-recipe.md"), recipeContent); err != nil {
		return
	}

	ctx.Set("playbook_dir", playbookDir)
	ctx.Set("playbook_name", playbookName)
	return
}

// findJobByPrefix finds a job file in the given plan directory that matches the prefix.
// This helper is needed for the session_archiving test which uses specific job filename prefixes.
func findJobByPrefix(planPath, prefix string) (string, error) {
	entries, err := os.ReadDir(planPath)
	if err != nil {
		return "", fmt.Errorf("reading plan directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".md") {
			return filepath.Join(planPath, entry.Name()), nil
		}
	}

	return "", fmt.Errorf("no job file found with prefix %s in %s", prefix, planPath)
}
