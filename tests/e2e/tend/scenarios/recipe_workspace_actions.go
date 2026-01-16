package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
)

var RecipeInitFlagScenario = harness.NewScenario(
	"recipe-init-flag",
	"Tests that init actions only run with --init flag",
	[]string{"recipe", "init-flag", "workspace-actions"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with git repo", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "init-flag-project")
			if err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Init Flag Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Create recipe with new init/actions format", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			recipesDir := filepath.Join(projectDir, ".grove", "recipes", "init-flag-recipe")

			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipe directory: %w", err)
			}

			jobContent := `---
type: interactive-agent
template: chat
---

# Test Recipe
Test recipe for init flag.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-test.md"), jobContent); err != nil {
				return fmt.Errorf("creating job file: %w", err)
			}

			// Create recipe.yml with init and named actions
			recipeContent := `description: Recipe with init and named actions

init:
  - type: shell
    description: Init action
    command: echo "Init action ran" > init-ran.txt

actions:
  start-dev:
    - type: shell
      description: Start development
      command: echo "Dev started" > dev-started.txt
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeContent); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan WITHOUT --init flag", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "no-init-plan", "--recipe", "init-flag-recipe", "--worktree")
			cmd.Dir(projectDir)

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			// Verify init actions did NOT run
			if strings.Contains(result.Stdout, "Executing initialization actions") {
				return fmt.Errorf("init actions should not run without --init flag")
			}

			// Should show tip about running init actions
			if !strings.Contains(result.Stdout, "flow plan action init") {
				return fmt.Errorf("should show tip about running init actions")
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "init-flag-project", "plans", "no-init-plan")
			ctx.Set("plan_path", planPath)

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "no-init-plan")
			ctx.Set("worktree_path", worktreePath)

			return nil
		}),

		harness.NewStep("Verify init action did NOT create files", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")

			// Verify init-ran.txt does NOT exist
			initRanPath := filepath.Join(worktreePath, "init-ran.txt")
			if err := fs.AssertExists(initRanPath); err == nil {
				return fmt.Errorf("init-ran.txt should not exist without --init flag")
			}

			// Verify named action files don't exist either
			devStartedPath := filepath.Join(worktreePath, "dev-started.txt")
			if err := fs.AssertExists(devStartedPath); err == nil {
				return fmt.Errorf("dev-started.txt should not exist without running action")
			}

			return nil
		}),

		harness.NewStep("Verify .grove-plan.yml contains recipe field", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			planConfigPath := filepath.Join(planPath, ".grove-plan.yml")

			content, err := fs.ReadString(planConfigPath)
			if err != nil {
				return fmt.Errorf("reading .grove-plan.yml: %w", err)
			}

			if !strings.Contains(content, "recipe: init-flag-recipe") {
				return fmt.Errorf(".grove-plan.yml should contain recipe field, got: %s", content)
			}

			return nil
		}),

		harness.NewStep("Initialize another plan WITH --init flag", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "init", "with-init-plan", "--recipe", "init-flag-recipe", "--worktree", "--init")
			cmd.Dir(projectDir)

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init with --init failed: %w", err)
			}

			// Verify init actions DID run
			if !strings.Contains(result.Stdout, "Executing initialization actions") {
				return fmt.Errorf("init actions should run with --init flag")
			}

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "with-init-plan")
			ctx.Set("with_init_worktree", worktreePath)

			return nil
		}),

		harness.NewStep("Verify init action DID create files with --init flag", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("with_init_worktree")

			// Verify init-ran.txt exists
			initRanPath := filepath.Join(worktreePath, "init-ran.txt")
			if err := fs.AssertExists(initRanPath); err != nil {
				return fmt.Errorf("init-ran.txt should exist with --init flag: %w", err)
			}

			content, err := fs.ReadString(initRanPath)
			if err != nil {
				return fmt.Errorf("reading init-ran.txt: %w", err)
			}

			if !strings.Contains(content, "Init action ran") {
				return fmt.Errorf("init-ran.txt should contain expected content, got: %s", content)
			}

			return nil
		}),
	},
)

var RecipePlanActionCommandScenario = harness.NewScenario(
	"recipe-plan-action-command",
	"Tests flow plan action command for on-demand actions",
	[]string{"recipe", "plan-action", "workspace-actions"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with git repo", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "plan-action-project")
			if err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Plan Action Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Create recipe with named actions", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			recipesDir := filepath.Join(projectDir, ".grove", "recipes", "action-recipe")

			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipe directory: %w", err)
			}

			jobContent := `---
type: interactive-agent
template: chat
---

# Action Recipe
Test recipe for plan action command.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-test.md"), jobContent); err != nil {
				return fmt.Errorf("creating job file: %w", err)
			}

			// Create recipe.yml with init and named actions
			recipeContent := `description: Recipe with named actions

init:
  - type: shell
    description: Init setup
    command: echo "Init complete" > init-complete.txt

actions:
  start-dev:
    - type: shell
      description: Start development server
      command: echo "Dev server started" > dev-server.txt

  seed-db:
    - type: shell
      description: Seed database
      command: echo "Database seeded" > db-seeded.txt

  run-tests:
    - type: shell
      description: Run test suite
      command: echo "Tests passed" > test-results.txt
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeContent); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan without running init", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "action-plan", "--recipe", "action-recipe", "--worktree")
			cmd.Dir(projectDir)

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "plan-action-project", "plans", "action-plan")
			ctx.Set("plan_path", planPath)

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "action-plan")
			ctx.Set("worktree_path", worktreePath)

			return nil
		}),

		harness.NewStep("Run init action using flow plan action init", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Bin("plan", "action", "init", filepath.Base(planPath))
			cmd.Dir(projectDir)

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("flow plan action init failed: %w", err)
			}

			if !strings.Contains(result.Stdout, "Executing init actions") {
				return fmt.Errorf("should show executing init actions message")
			}

			return nil
		}),

		harness.NewStep("Verify init action created expected file", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")

			initCompletePath := filepath.Join(worktreePath, "init-complete.txt")
			if err := fs.AssertExists(initCompletePath); err != nil {
				return fmt.Errorf("init-complete.txt should exist: %w", err)
			}

			content, err := fs.ReadString(initCompletePath)
			if err != nil {
				return fmt.Errorf("reading init-complete.txt: %w", err)
			}

			if !strings.Contains(content, "Init complete") {
				return fmt.Errorf("init-complete.txt should contain expected content, got: %s", content)
			}

			return nil
		}),

		harness.NewStep("Run named action: start-dev", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Bin("plan", "action", "start-dev", filepath.Base(planPath))
			cmd.Dir(projectDir)

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("flow plan action start-dev failed: %w", err)
			}

			if !strings.Contains(result.Stdout, "Executing action 'start-dev'") {
				return fmt.Errorf("should show executing action message")
			}

			return nil
		}),

		harness.NewStep("Verify start-dev action created expected file", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")

			devServerPath := filepath.Join(worktreePath, "dev-server.txt")
			if err := fs.AssertExists(devServerPath); err != nil {
				return fmt.Errorf("dev-server.txt should exist: %w", err)
			}

			content, err := fs.ReadString(devServerPath)
			if err != nil {
				return fmt.Errorf("reading dev-server.txt: %w", err)
			}

			if !strings.Contains(content, "Dev server started") {
				return fmt.Errorf("dev-server.txt should contain expected content, got: %s", content)
			}

			return nil
		}),

		harness.NewStep("Run another named action: seed-db", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Bin("plan", "action", "seed-db", filepath.Base(planPath))
			cmd.Dir(projectDir)

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("flow plan action seed-db failed: %w", err)
			}

			return nil
		}),

		harness.NewStep("Verify seed-db action created expected file", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")

			dbSeededPath := filepath.Join(worktreePath, "db-seeded.txt")
			if err := fs.AssertExists(dbSeededPath); err != nil {
				return fmt.Errorf("db-seeded.txt should exist: %w", err)
			}

			return nil
		}),

		harness.NewStep("Try to run non-existent action", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Bin("plan", "action", "non-existent", filepath.Base(planPath))
			cmd.Dir(projectDir)

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Should fail with appropriate error message
			if result.ExitCode == 0 {
				return fmt.Errorf("non-existent action should fail")
			}

			if !strings.Contains(result.Stderr, "not found") {
				return fmt.Errorf("should show action not found error")
			}

			return nil
		}),
	},
)

var RecipeDockerComposePortRemovalScenario = harness.NewScenario(
	"recipe-docker-compose-port-removal",
	"Tests that ports: [] in overlay removes port bindings from base compose file",
	[]string{"recipe", "docker-compose", "port-removal"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with git repo", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "port-removal-project")
			if err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Port Removal Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			// Create docker-compose.yml with ports
			dockerComposeContent := `version: '3.8'

services:
  postgres:
    image: postgres:15-alpine
    ports:
      - "5432:5432"
    environment:
      POSTGRES_USER: testuser
      POSTGRES_PASSWORD: testpass

  nginx:
    image: nginx:alpine
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

		harness.NewStep("Create recipe with ports: [] overlay", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			recipesDir := filepath.Join(projectDir, ".grove", "recipes", "port-removal-recipe")

			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipe directory: %w", err)
			}

			jobContent := `---
type: interactive-agent
template: chat
---

# Port Removal Recipe
Test recipe for port removal.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-test.md"), jobContent); err != nil {
				return fmt.Errorf("creating job file: %w", err)
			}

			// Create recipe.yml with ports: [] to remove port bindings
			recipeContent := `description: Recipe that removes port bindings

init:
  - type: docker_compose
    description: Start services without port bindings
    project_name: "port-test-{{ .PlanName }}"
    files:
      - docker-compose.yml
    overlay:
      services:
        postgres:
          ports: []  # Remove port bindings
        nginx:
          ports: []  # Remove port bindings
          labels:
            - "traefik.enable=true"
            - "traefik.http.routers.nginx-{{ .PlanName }}.rule=Host(` + "`nginx.{{ .PlanName }}.localhost`" + `)"
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeContent); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan with --init to trigger Docker Compose action", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "port-removal-plan", "--recipe", "port-removal-recipe", "--worktree", "--init")
			cmd.Dir(projectDir)

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "port-removal-project", "plans", "port-removal-plan")
			ctx.Set("plan_path", planPath)

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "port-removal-plan")
			ctx.Set("worktree_path", worktreePath)

			return nil
		}),

		harness.NewStep("Verify sanitized compose file was created", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")

			groveDockerDir := filepath.Join(worktreePath, ".grove", "docker")
			if err := fs.AssertExists(groveDockerDir); err != nil {
				return fmt.Errorf(".grove/docker directory should exist: %w", err)
			}

			// Check for sanitized base file
			sanitizedPath := filepath.Join(groveDockerDir, "docker-compose.base.sanitized.yml")
			if err := fs.AssertExists(sanitizedPath); err != nil {
				return fmt.Errorf("sanitized compose file should exist: %w", err)
			}

			content, err := fs.ReadString(sanitizedPath)
			if err != nil {
				return fmt.Errorf("reading sanitized file: %w", err)
			}

			// Verify it's marked as auto-generated
			if !strings.Contains(content, "auto-generated by Grove Flow") {
				return fmt.Errorf("sanitized file should be marked as auto-generated")
			}

			// Verify ports were removed from postgres service
			if strings.Contains(content, "5432:5432") {
				return fmt.Errorf("sanitized file should not contain postgres port mapping")
			}

			// Verify ports were removed from nginx service
			if strings.Contains(content, "8080:80") {
				return fmt.Errorf("sanitized file should not contain nginx port mapping")
			}

			// Verify services still exist
			if !strings.Contains(content, "postgres:") {
				return fmt.Errorf("sanitized file should still contain postgres service")
			}
			if !strings.Contains(content, "nginx:") {
				return fmt.Errorf("sanitized file should still contain nginx service")
			}

			return nil
		}),

		harness.NewStep("Verify overlay file contains new labels", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")

			overridePath := filepath.Join(worktreePath, ".grove", "docker", "docker-compose.override.yml")
			if err := fs.AssertExists(overridePath); err != nil {
				return fmt.Errorf("override file should exist: %w", err)
			}

			content, err := fs.ReadString(overridePath)
			if err != nil {
				return fmt.Errorf("reading override file: %w", err)
			}

			// Verify Traefik labels were added
			if !strings.Contains(content, "traefik.enable=true") {
				return fmt.Errorf("override file should contain Traefik labels")
			}

			// Verify rendered plan name in labels
			if !strings.Contains(content, "port-removal-plan") {
				return fmt.Errorf("override file should contain rendered plan name in labels")
			}

			return nil
		}),
	},
)
