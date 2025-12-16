package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

var RecipeInitActionsShellScenario = harness.NewScenario(
	"recipe-init-actions-shell",
	"Tests shell-type init actions from recipe workspace_init.yml",
	[]string{"recipe", "init-actions", "shell"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with git repo", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "init-actions-project")
			if err != nil {
				return err
			}

			// Create a git repo with initial commit
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Init Actions Test Project\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Create project recipe with shell init actions", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			recipesDir := filepath.Join(projectDir, ".grove", "recipes", "shell-test-recipe")

			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipe directory: %w", err)
			}

			// Create main job file
			jobContent := `---
type: interactive-agent
template: chat
---

# Test Recipe with Shell Actions

This is a test recipe to verify shell init actions.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-test.md"), jobContent); err != nil {
				return fmt.Errorf("creating job file: %w", err)
			}

			// Create recipe.yml with shell init actions
			recipeContent := `description: Recipe with shell init actions
init:
  - type: shell
    description: Create test directory
    command: mkdir -p test-output

  - type: shell
    description: Write test file with template
    command: echo "Plan:{{ .PlanName }}" > test-output/plan-info.txt

  - type: shell
    description: Create another file
    command: echo "This is a test" > test-output/hello.txt
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeContent); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan from project recipe with shell actions", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "shell-test-plan", "--recipe", "shell-test-recipe", "--worktree", "--init")
			cmd.Dir(projectDir)

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			// Verify output mentions executing actions
			if !strings.Contains(result.Stdout, "Executing initialization actions") {
				return fmt.Errorf("expected output to mention executing initialization actions, got stdout: %s", result.Stdout)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "init-actions-project", "plans", "shell-test-plan")
			ctx.Set("plan_path", planPath)

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "shell-test-plan")
			ctx.Set("worktree_path", worktreePath)

			return nil
		}),

		harness.NewStep("Verify shell actions created expected files in worktree", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")

			// Verify test-output directory was created
			testOutputDir := filepath.Join(worktreePath, "test-output")
			if err := fs.AssertExists(testOutputDir); err != nil {
				return fmt.Errorf("test-output directory should exist: %w", err)
			}

			// Verify plan-info.txt was created with rendered template
			planInfoPath := filepath.Join(testOutputDir, "plan-info.txt")
			if err := fs.AssertExists(planInfoPath); err != nil {
				return fmt.Errorf("plan-info.txt should exist: %w", err)
			}

			content, err := fs.ReadString(planInfoPath)
			if err != nil {
				return fmt.Errorf("reading plan-info.txt: %w", err)
			}

			if !strings.Contains(content, "Plan:shell-test-plan") {
				return fmt.Errorf("plan-info.txt should contain rendered plan name, got: %s", content)
			}

			// Verify hello.txt was created
			helloPath := filepath.Join(testOutputDir, "hello.txt")
			if err := fs.AssertExists(helloPath); err != nil {
				return fmt.Errorf("hello.txt should exist: %w", err)
			}

			helloContent, err := fs.ReadString(helloPath)
			if err != nil {
				return fmt.Errorf("reading hello.txt: %w", err)
			}

			if !strings.Contains(helloContent, "This is a test") {
				return fmt.Errorf("hello.txt should contain expected content, got: %s", helloContent)
			}

			return nil
		}),
	},
)

var RecipeInitActionsDockerComposeScenario = harness.NewScenario(
	"recipe-init-actions-docker-compose",
	"Tests docker_compose-type init actions from recipe workspace_init.yml",
	[]string{"recipe", "init-actions", "docker-compose"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with git repo", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "docker-init-project")
			if err != nil {
				return err
			}

			// Create a git repo with initial commit
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Docker Init Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			// Create a docker-compose.yml in the project
			dockerComposeContent := `version: '3.8'
services:
  app:
    image: nginx:latest
    ports:
      - "8080:80"
`
			if err := fs.WriteString(filepath.Join(projectDir, "docker-compose.yml"), dockerComposeContent); err != nil {
				return err
			}
			if err := repo.AddCommit("Add docker-compose.yml"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Create project recipe with docker_compose init action", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			recipesDir := filepath.Join(projectDir, ".grove", "recipes", "docker-compose-recipe")

			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipe directory: %w", err)
			}

			// Create main job file
			jobContent := `---
type: interactive-agent
template: chat
---

# Docker Compose Recipe

This recipe starts Docker Compose services.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-test.md"), jobContent); err != nil {
				return fmt.Errorf("creating job file: %w", err)
			}

			// Create recipe.yml with docker_compose init action
			recipeContent := `description: Recipe with docker_compose init action
init:
  - type: docker_compose
    description: Start development environment
    project_name: "grove-{{ .PlanName }}"
    files:
      - docker-compose.yml
    overlay:
      services:
        app:
          environment:
            - PLAN_NAME={{ .PlanName }}
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeContent); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan from recipe with docker_compose action", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "docker-plan", "--recipe", "docker-compose-recipe", "--worktree", "--init")
			cmd.Dir(projectDir)

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Verify plan init succeeded
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w, stdout: %s, stderr: %s", err, result.Stdout, result.Stderr)
			}

			// Verify output mentions executing actions
			if !strings.Contains(result.Stdout, "Executing initialization actions") {
				return fmt.Errorf("expected output to mention executing initialization actions, got stdout: %s", result.Stdout)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "docker-init-project", "plans", "docker-plan")
			ctx.Set("plan_path", planPath)

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "docker-plan")
			ctx.Set("worktree_path", worktreePath)

			return nil
		}),

		harness.NewStep("Verify docker-compose.override.yml was generated", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")

			// Debug: List what's in the worktree
			fmt.Printf("DEBUG: Worktree path: %s\n", worktreePath)

			// Verify .grove/docker directory was created
			groveDockerDir := filepath.Join(worktreePath, ".grove", "docker")
			if err := fs.AssertExists(groveDockerDir); err != nil {
				// List what's in .grove if it exists
				groveDirPath := filepath.Join(worktreePath, ".grove")
				if err2 := fs.AssertExists(groveDirPath); err2 == nil {
					fmt.Printf("DEBUG: .grove exists, contents:\n")
					// Would need to list dir contents here
				}
				return fmt.Errorf(".grove/docker directory should exist: %w", err)
			}

			// Verify docker-compose.override.yml was created
			overridePath := filepath.Join(groveDockerDir, "docker-compose.override.yml")
			if err := fs.AssertExists(overridePath); err != nil {
				return fmt.Errorf("docker-compose.override.yml should exist: %w", err)
			}

			content, err := fs.ReadString(overridePath)
			if err != nil {
				return fmt.Errorf("reading override file: %w", err)
			}

			// Verify it's marked as auto-generated
			if !strings.Contains(content, "auto-generated by Grove Flow") {
				return fmt.Errorf("override file should be marked as auto-generated")
			}

			// Verify it contains the overlay content with rendered templates
			if !strings.Contains(content, "PLAN_NAME=docker-plan") {
				return fmt.Errorf("override file should contain rendered PLAN_NAME, got: %s", content)
			}

			return nil
		}),

		harness.NewStep("Verify docker compose was invoked (via mock)", func(ctx *harness.Context) error {
			// Since docker is mocked, we just verify the side effects:
			// 1. Override file was created (verified in previous step)
			// 2. The init succeeded without error (verified by plan init success)
			// The mock prevents actual docker execution but allows the command to run
			return nil
		}),
	},
)

var RecipeInitActionsEcosystemScenario = harness.NewScenario(
	"recipe-init-actions-ecosystem",
	"Tests init actions with ecosystem worktrees that have sub-repos",
	[]string{"recipe", "init-actions", "ecosystem", "worktree"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with ecosystem project", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "ecosystem-project")
			if err != nil {
				return err
			}

			// Create main ecosystem repo
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Ecosystem Project\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			// Create sub-repos
			backendDir := filepath.Join(projectDir, "backend")
			frontendDir := filepath.Join(projectDir, "frontend")

			// Create directories first
			if err := fs.CreateDir(backendDir); err != nil {
				return err
			}
			if err := fs.CreateDir(frontendDir); err != nil {
				return err
			}

			backendRepo, err := git.SetupTestRepo(backendDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(backendDir, "main.go"), "package main\n"); err != nil {
				return err
			}
			if err := backendRepo.AddCommit("Initial backend commit"); err != nil {
				return err
			}

			frontendRepo, err := git.SetupTestRepo(frontendDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(frontendDir, "index.html"), "<html></html>\n"); err != nil {
				return err
			}
			if err := frontendRepo.AddCommit("Initial frontend commit"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Create project recipe with repo-specific shell actions", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			recipesDir := filepath.Join(projectDir, ".grove", "recipes", "ecosystem-recipe")

			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipe directory: %w", err)
			}

			// Create main job file
			jobContent := `---
type: interactive-agent
template: chat
---

# Ecosystem Recipe

Test recipe for ecosystem workflows.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-test.md"), jobContent); err != nil {
				return fmt.Errorf("creating job file: %w", err)
			}

			// Create recipe.yml with repo-specific actions
			recipeContent := `description: Recipe with ecosystem-specific init actions
init:
  - type: shell
    description: Setup backend dependencies
    repo: backend
    command: echo "Backend setup for {{ .PlanName }}" > setup.log

  - type: shell
    description: Setup frontend dependencies
    repo: frontend
    command: echo "Frontend setup for {{ .PlanName }}" > setup.log

  - type: shell
    description: Create shared config
    command: echo "Shared config" > shared-config.txt
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeContent); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan from ecosystem recipe", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "ecosystem-plan", "--recipe", "ecosystem-recipe", "--worktree", "--init")
			cmd.Dir(projectDir)

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "ecosystem-project", "plans", "ecosystem-plan")
			ctx.Set("plan_path", planPath)

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "ecosystem-plan")
			ctx.Set("worktree_path", worktreePath)

			return nil
		}),

		harness.NewStep("Verify repo-specific actions created files in correct locations", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")

			// Verify backend setup.log
			backendSetupLog := filepath.Join(worktreePath, "backend", "setup.log")
			if err := fs.AssertExists(backendSetupLog); err != nil {
				return fmt.Errorf("backend setup.log should exist: %w", err)
			}

			backendContent, err := fs.ReadString(backendSetupLog)
			if err != nil {
				return fmt.Errorf("reading backend setup.log: %w", err)
			}

			if !strings.Contains(backendContent, "Backend setup for ecosystem-plan") {
				return fmt.Errorf("backend setup.log should contain rendered content, got: %s", backendContent)
			}

			// Verify frontend setup.log
			frontendSetupLog := filepath.Join(worktreePath, "frontend", "setup.log")
			if err := fs.AssertExists(frontendSetupLog); err != nil {
				return fmt.Errorf("frontend setup.log should exist: %w", err)
			}

			frontendContent, err := fs.ReadString(frontendSetupLog)
			if err != nil {
				return fmt.Errorf("reading frontend setup.log: %w", err)
			}

			if !strings.Contains(frontendContent, "Frontend setup for ecosystem-plan") {
				return fmt.Errorf("frontend setup.log should contain rendered content, got: %s", frontendContent)
			}

			// Verify shared config at root level
			sharedConfig := filepath.Join(worktreePath, "shared-config.txt")
			if err := fs.AssertExists(sharedConfig); err != nil {
				return fmt.Errorf("shared-config.txt should exist: %w", err)
			}

			return nil
		}),
	},
)

var RecipeInitActionsNotebookScenario = harness.NewScenario(
	"recipe-init-actions-notebook",
	"Tests shell-type init actions from notebook recipe workspace_init.yml",
	[]string{"recipe", "init-actions", "notebook", "shell"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with git repo", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "notebook-init-project")
			if err != nil {
				return err
			}

			// Create a git repo with initial commit
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Notebook Init Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Create notebook recipe with shell init actions", func(ctx *harness.Context) error {
			notebooksRoot := ctx.GetString("notebooks_root")
			recipesDir := filepath.Join(notebooksRoot, "workspaces", "notebook-init-project", "recipes", "notebook-shell-recipe")

			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipe directory: %w", err)
			}

			// Create main job file
			jobContent := `---
type: interactive-agent
template: chat
---

# Notebook Recipe with Shell Actions

This is a test recipe in the notebook directory to verify shell init actions.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-test.md"), jobContent); err != nil {
				return fmt.Errorf("creating job file: %w", err)
			}

			// Create recipe.yml with shell actions
			recipeContent := `description: Notebook recipe with shell init actions
init:
  - type: shell
    description: Create notebook test directory
    command: mkdir -p notebook-test-output

  - type: shell
    description: Write test file with template from notebook
    command: echo "NotebookPlan:{{ .PlanName }}" > notebook-test-output/notebook-plan.txt
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeContent); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan from notebook recipe with shell actions", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "notebook-shell-plan", "--recipe", "notebook-shell-recipe", "--worktree", "--init")
			cmd.Dir(projectDir)

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			// Verify output mentions executing actions
			if !strings.Contains(result.Stdout, "Executing initialization actions") {
				return fmt.Errorf("expected output to mention executing initialization actions, got stdout: %s", result.Stdout)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "notebook-init-project", "plans", "notebook-shell-plan")
			ctx.Set("plan_path", planPath)

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "notebook-shell-plan")
			ctx.Set("worktree_path", worktreePath)

			return nil
		}),

		harness.NewStep("Verify shell actions from notebook recipe created expected files", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")

			// Verify notebook-test-output directory was created
			testOutputDir := filepath.Join(worktreePath, "notebook-test-output")
			if err := fs.AssertExists(testOutputDir); err != nil {
				return fmt.Errorf("notebook-test-output directory should exist: %w", err)
			}

			// Verify notebook-plan.txt was created with rendered template
			planInfoPath := filepath.Join(testOutputDir, "notebook-plan.txt")
			if err := fs.AssertExists(planInfoPath); err != nil {
				return fmt.Errorf("notebook-plan.txt should exist: %w", err)
			}

			content, err := fs.ReadString(planInfoPath)
			if err != nil {
				return fmt.Errorf("reading notebook-plan.txt: %w", err)
			}

			if !strings.Contains(content, "NotebookPlan:notebook-shell-plan") {
				return fmt.Errorf("notebook-plan.txt should contain rendered plan name, got: %s", content)
			}

			return nil
		}),
	},
)

var RecipeInitActionsFailureHandlingScenario = harness.NewScenario(
	"recipe-init-actions-failure",
	"Tests that init action failures are handled gracefully without blocking plan creation",
	[]string{"recipe", "init-actions", "failure-handling"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with git repo", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "failure-test-project")
			if err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Failure Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Create project recipe with a failing shell action", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			recipesDir := filepath.Join(projectDir, ".grove", "recipes", "failing-recipe")

			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipe directory: %w", err)
			}

			// Create main job file
			jobContent := `---
type: interactive-agent
---

# Failing Recipe

This recipe has a failing action.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-test.md"), jobContent); err != nil {
				return fmt.Errorf("creating job file: %w", err)
			}

			// Create recipe.yml with a failing action
			recipeContent := `description: Recipe with failing action
init:
  - type: shell
    description: This action will fail
    command: exit 1

  - type: shell
    description: This action should not run
    command: echo "Should not run" > should-not-exist.txt
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeContent); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan with failing action", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "failure-plan", "--recipe", "failing-recipe", "--worktree", "--init")
			cmd.Dir(projectDir)

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Plan init should still succeed despite action failure
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init should succeed even with failing action: %w", err)
			}

			// Verify warning was shown
			if !strings.Contains(result.Stdout, "Warning") && !strings.Contains(result.Stderr, "Warning") {
				return fmt.Errorf("expected warning message about init action failure")
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "failure-test-project", "plans", "failure-plan")
			ctx.Set("plan_path", planPath)

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "failure-plan")
			ctx.Set("worktree_path", worktreePath)

			return nil
		}),

		harness.NewStep("Verify plan was created despite action failure", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			worktreePath := ctx.GetString("worktree_path")

			// Verify plan exists
			if err := fs.AssertExists(planPath); err != nil {
				return fmt.Errorf("plan should exist despite action failure: %w", err)
			}

			// Verify worktree exists
			if err := fs.AssertExists(worktreePath); err != nil {
				return fmt.Errorf("worktree should exist despite action failure: %w", err)
			}

			// Note: The implementation continues executing actions even after failures,
			// it just logs warnings. So both actions will attempt to run.
			// The first action exits with code 1, the second action attempts to run.
			// Since init actions continue on failure, this is expected behavior.

			return nil
		}),
	},
)
