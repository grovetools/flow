package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/workspace"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
)

// expectedWorktreePath returns the on-disk location a worktree named name of
// the repository rooted at gitRoot is expected to occupy, for use in e2e
// assertions.
//
//	sibling=false (legacy in-repo layout):
//	    <gitRoot>/.grove-worktrees/<name>
//	sibling=true (XDG out-of-repo layout, --sibling-workspaces plans):
//	    <ctx.DataDir()>/grove/worktrees/<DirIdentifier(gitRoot)>/<name>
//
// The XDG identifier is computed via core's workspace.DirIdentifier — the
// SAME function the production code uses — so the expectation never drifts
// from the real layout and is never reconstructed from HOME. ctx.DataDir() is
// the sandboxed XDG_DATA_HOME, so this maps to paths.WorktreesDir()/<id>/<name>
// inside the sandbox.
func expectedWorktreePath(ctx *harness.Context, gitRoot, name string, sibling bool) string {
	if sibling {
		return filepath.Join(ctx.DataDir(), "grove", "worktrees", workspace.DirIdentifier(gitRoot), name)
	}
	return filepath.Join(gitRoot, ".grove-worktrees", name)
}

// setupEcosystemEnvironment builds a sandboxed ecosystem whose root and child
// repos are addressed by their REALPATH, then returns the realpath ecosystem
// git root, a map of child-repo name -> realpath dir, and the notebooks root.
//
// Why realpath: core's workspace.SetupSubmodules keeps only discovered
// workspaces whose parent EqualFold-matches the ecosystem git root
// (submodules.go direct-child filter). On macOS the harness sandbox lives
// under /var/folders/... which symlinks to /private/var/folders/...;
// `git rev-parse --show-toplevel` (the git root) returns the /private spelling
// while workspace discovery walks the raw grove-source path. The two spellings
// fail EqualFold and every sibling repo is silently dropped — the Phase 4
// trap. Pointing the grove source at the resolved path makes both sides agree.
// (The durable fix is a NormalizeForLookup-based comparison in core's
// SetupSubmodules; this helper pins the desired behavior from the flow side.)
//
// Each child repo is enumerated under the ecosystem grove.yml `workspaces`
// key so discovery promotes these otherwise grove-config-less repos
// (discover.go zero-footprint child path), and is initialized as an
// independent git repo with a single README commit so `git worktree add` can
// link it. Callers may add and commit further files before creating worktrees.
func setupEcosystemEnvironment(ctx *harness.Context, ecosystemName string, repos []string) (gitRoot string, repoDirs map[string]string, notebooksRoot string, err error) {
	// Resolve the sandbox home so every path below matches git's realpath.
	homeDir := ctx.HomeDir()
	if resolved, rerr := filepath.EvalSymlinks(homeDir); rerr == nil {
		homeDir = resolved
	}
	ctx.Set("home_dir", homeDir)

	codeDir := filepath.Join(homeDir, "code")
	if err = fs.CreateDir(codeDir); err != nil {
		return
	}

	gitRoot = filepath.Join(codeDir, ecosystemName)
	if err = fs.CreateDir(gitRoot); err != nil {
		return
	}
	ctx.Set("project_dir", gitRoot)

	// Ecosystem root: git repo + grove.yml enumerating the child workspaces so
	// discovery promotes them.
	ecoRepo, gerr := git.SetupTestRepo(gitRoot)
	if gerr != nil {
		err = gerr
		return
	}
	if err = fs.WriteString(filepath.Join(gitRoot, "README.md"), fmt.Sprintf("# %s\n", ecosystemName)); err != nil {
		return
	}
	if err = fs.WriteGroveConfig(gitRoot, &config.Config{Name: ecosystemName, Version: "1.0", Workspaces: repos}); err != nil {
		return
	}
	if err = ecoRepo.AddCommit("Initial ecosystem commit"); err != nil {
		return
	}

	// Child repos: each an independent git repo with one commit.
	repoDirs = make(map[string]string, len(repos))
	for _, repo := range repos {
		repoDir := filepath.Join(gitRoot, repo)
		if err = fs.CreateDir(repoDir); err != nil {
			return
		}
		childRepo, cerr := git.SetupTestRepo(repoDir)
		if cerr != nil {
			err = cerr
			return
		}
		if err = fs.WriteString(filepath.Join(repoDir, "README.md"), fmt.Sprintf("# %s\n", repo)); err != nil {
			return
		}
		if err = childRepo.AddCommit("Initial " + repo + " commit"); err != nil {
			return
		}
		repoDirs[repo] = repoDir
	}

	// Global config: grove source "code" pinned to the REALPATH code dir (an
	// absolute path, not ~/code, so discovery walks the resolved spelling),
	// linked to a centralized notebook.
	notebooksRoot = filepath.Join(homeDir, "notebooks")
	ctx.Set("notebooks_root", notebooksRoot)
	groveConfigDir := filepath.Join(ctx.ConfigDir(), "grove")
	enabled := true
	globalCfg := &config.Config{
		Version: "1.0",
		Notebooks: &config.NotebooksConfig{
			Definitions: map[string]*config.Notebook{
				"default": {RootDir: notebooksRoot},
			},
			Rules: &config.NotebookRules{Default: "default"},
		},
		Groves: map[string]config.GroveSourceConfig{
			"code": {
				Path:     codeDir,
				Enabled:  &enabled,
				Notebook: "default",
			},
		},
	}
	err = fs.WriteGroveConfig(groveConfigDir, globalCfg)
	return
}

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
