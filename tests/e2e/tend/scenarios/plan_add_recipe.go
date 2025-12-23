package scenarios

import (
	"fmt"
	"path/filepath"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-tend/pkg/verify"
)

var PlanAddRecipeScenario = harness.NewScenario(
	"plan-add-recipe",
	"Tests the `flow plan add --recipe` command to add recipe jobs to an existing plan with correct dependency remapping.",
	[]string{"core", "plan", "recipe", "cli"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment and create test recipe", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "test-recipe-project")
			if err != nil {
				return err
			}
			ctx.Set("project_dir", projectDir)
			ctx.Set("notebooks_root", notebooksRoot)

			// Create a test recipe with two jobs where second depends on first
			recipesDir := filepath.Join(ctx.ConfigDir(), "grove", "recipes", "e2e-test-recipe")
			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipes directory: %w", err)
			}

			// Create recipe.yml
			recipeYaml := `description: An e2e test recipe for testing plan add --recipe functionality.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeYaml); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			// Create first recipe job
			firstJob := `---
title: Recipe First Step
type: oneshot
---
This is the first step of the recipe.`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-first-step.md"), firstJob); err != nil {
				return fmt.Errorf("creating first recipe job: %w", err)
			}

			// Create second recipe job that depends on the first
			secondJob := `---
title: Recipe Second Step
type: oneshot
depends_on:
  - 01-first-step.md
---
This is the second step, depending on the first.`
			if err := fs.WriteString(filepath.Join(recipesDir, "02-second-step.md"), secondJob); err != nil {
				return fmt.Errorf("creating second recipe job: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan and add base job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			// Initialize a new plan
			initCmd := ctx.Bin("plan", "init", "test-plan")
			initCmd.Dir(projectDir)
			result := initCmd.Run()
			ctx.ShowCommandOutput(initCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			// Add an initial job to the plan
			addCmd := ctx.Bin("plan", "add", "test-plan", "--title", "initial-job", "-p", "do something")
			addCmd.Dir(projectDir)
			result = addCmd.Run()
			ctx.ShowCommandOutput(addCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "test-recipe-project", "plans", "test-plan")
			ctx.Set("plan_path", planPath)

			// Verify initial job was created
			initialJobPath := filepath.Join(planPath, "01-initial-job.md")
			if err := fs.AssertExists(initialJobPath); err != nil {
				return fmt.Errorf("initial job not created: %w", err)
			}

			return nil
		}),

		harness.NewStep("Add recipe to plan with dependency on initial job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Add the recipe to the existing plan, making it depend on the initial job
			cmd := ctx.Bin("plan", "add", "test-plan", "--recipe", "e2e-test-recipe", "--depends-on", "01-initial-job.md")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add --recipe failed: %w", err)
			}

			// Verify success message
			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("shows success message", result.Stdout, "Added 2 jobs from recipe 'e2e-test-recipe'")
				v.Contains("shows first job filename", result.Stdout, "02-recipe-first-step.md")
				v.Contains("shows second job filename", result.Stdout, "03-recipe-second-step.md")
			})
		}),

		harness.NewStep("Verify recipe jobs were created with correct numbering", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Check that the new files have been created with correct sequential numbering
			if err := ctx.Check("first recipe job created", fs.AssertExists(filepath.Join(planPath, "02-recipe-first-step.md"))); err != nil {
				return err
			}

			if err := ctx.Check("second recipe job created", fs.AssertExists(filepath.Join(planPath, "03-recipe-second-step.md"))); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Verify first recipe job has correct external dependency", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			firstRecipeJobPath := filepath.Join(planPath, "02-recipe-first-step.md")

			// Load the job and verify its dependencies
			job, err := orchestration.LoadJob(firstRecipeJobPath)
			if err != nil {
				return fmt.Errorf("loading first recipe job: %w", err)
			}

			// The first recipe job (root of recipe) should now depend on the initial job
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("first recipe job has one dependency", 1, len(job.DependsOn))
				if len(job.DependsOn) > 0 {
					v.Equal("first recipe job depends on initial job", "01-initial-job.md", job.DependsOn[0])
				}
				v.Equal("first recipe job has correct title", "Recipe First Step", job.Title)
				v.Equal("first recipe job has correct type", orchestration.JobTypeOneshot, job.Type)
			})
		}),

		harness.NewStep("Verify second recipe job has remapped internal dependency", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			secondRecipeJobPath := filepath.Join(planPath, "03-recipe-second-step.md")

			// Load the job and verify its dependencies
			job, err := orchestration.LoadJob(secondRecipeJobPath)
			if err != nil {
				return fmt.Errorf("loading second recipe job: %w", err)
			}

			// The second recipe job should depend on the first recipe job with the NEW filename
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("second recipe job has one dependency", 1, len(job.DependsOn))
				if len(job.DependsOn) > 0 {
					v.Equal("second recipe job depends on remapped first job", "02-recipe-first-step.md", job.DependsOn[0])
				}
				v.Equal("second recipe job has correct title", "Recipe Second Step", job.Title)
				v.Equal("second recipe job has correct type", orchestration.JobTypeOneshot, job.Type)
			})
		}),

		harness.NewStep("Verify job content was preserved", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Check that job prompt bodies were preserved
			firstJobPath := filepath.Join(planPath, "02-recipe-first-step.md")
			firstJobContent, err := fs.ReadString(firstJobPath)
			if err != nil {
				return fmt.Errorf("reading first recipe job: %w", err)
			}

			secondJobPath := filepath.Join(planPath, "03-recipe-second-step.md")
			secondJobContent, err := fs.ReadString(secondJobPath)
			if err != nil {
				return fmt.Errorf("reading second recipe job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("first job contains original content", firstJobContent, "This is the first step of the recipe.")
				v.Contains("second job contains original content", secondJobContent, "This is the second step, depending on the first.")
			})
		}),
	},
)

var PlanAddRecipeAliasScenario = harness.NewScenario(
	"plan-add-recipe-alias",
	"Tests the `flow add --recipe` command (top-level alias) to ensure both CLI entry points work.",
	[]string{"core", "plan", "recipe", "cli", "alias"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment and create test recipe", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "test-alias-project")
			if err != nil {
				return err
			}
			ctx.Set("project_dir", projectDir)
			ctx.Set("notebooks_root", notebooksRoot)

			// Create a test recipe
			recipesDir := filepath.Join(ctx.ConfigDir(), "grove", "recipes", "alias-test-recipe")
			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipes directory: %w", err)
			}

			recipeYaml := `description: Test recipe for alias command.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeYaml); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			firstJob := `---
title: Alias Test Job
type: oneshot
---
Testing the alias command.`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-test-job.md"), firstJob); err != nil {
				return fmt.Errorf("creating recipe job: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan using top-level init", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			// Initialize a new plan
			initCmd := ctx.Bin("plan", "init", "another-plan")
			initCmd.Dir(projectDir)
			result := initCmd.Run()
			ctx.ShowCommandOutput(initCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "test-alias-project", "plans", "another-plan")
			ctx.Set("plan_path", planPath)

			return nil
		}),

		harness.NewStep("Add recipe using top-level 'add' alias", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Use the top-level 'add' command (alias for 'plan add')
			cmd := ctx.Bin("add", "another-plan", "--recipe", "alias-test-recipe")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("add --recipe alias failed: %w", err)
			}

			// Verify success message
			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("shows success message for alias", result.Stdout, "Added 1 jobs from recipe 'alias-test-recipe'")
			})
		}),

		harness.NewStep("Verify recipe job was created via alias", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			if err := ctx.Check("recipe job created via alias", fs.AssertExists(filepath.Join(planPath, "01-alias-test-job.md"))); err != nil {
				return err
			}

			// Load and verify job content
			job, err := orchestration.LoadJob(filepath.Join(planPath, "01-alias-test-job.md"))
			if err != nil {
				return fmt.Errorf("loading job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("job has correct title", "Alias Test Job", job.Title)
				v.Contains("job has correct content", job.PromptBody, "Testing the alias command.")
			})
		}),
	},
)

var PlanAddRecipeWithVariablesScenario = harness.NewScenario(
	"plan-add-recipe-with-vars",
	"Tests the `flow plan add --recipe` command with --recipe-vars for template variable substitution.",
	[]string{"core", "plan", "recipe", "cli", "templates"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment and create templated recipe", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "test-vars-project")
			if err != nil {
				return err
			}
			ctx.Set("project_dir", projectDir)
			ctx.Set("notebooks_root", notebooksRoot)

			// Create a test recipe with template variables
			recipesDir := filepath.Join(ctx.ConfigDir(), "grove", "recipes", "vars-test-recipe")
			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipes directory: %w", err)
			}

			recipeYaml := `description: Test recipe with template variables.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeYaml); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			// Create a job that uses template variables
			templatedJob := `---
title: Test {{.Vars.feature_name}}
type: oneshot
---
Implementing feature: {{.Vars.feature_name}}
For plan: {{.PlanName}}
Priority: {{.Vars.priority}}`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-templated-job.md"), templatedJob); err != nil {
				return fmt.Errorf("creating templated job: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			initCmd := ctx.Bin("plan", "init", "vars-test-plan")
			initCmd.Dir(projectDir)
			result := initCmd.Run()
			ctx.ShowCommandOutput(initCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "test-vars-project", "plans", "vars-test-plan")
			ctx.Set("plan_path", planPath)

			return nil
		}),

		harness.NewStep("Add recipe with template variables", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Add recipe with --recipe-vars
			cmd := ctx.Bin("plan", "add", "vars-test-plan", "--recipe", "vars-test-recipe",
				"--recipe-vars", "feature_name=Authentication",
				"--recipe-vars", "priority=high")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add --recipe with vars failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("shows success message", result.Stdout, "Added 1 jobs from recipe 'vars-test-recipe'")
			})
		}),

		harness.NewStep("Verify template variables were substituted", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobPath := filepath.Join(planPath, "01-test-authentication.md")

			if err := ctx.Check("templated job created", fs.AssertExists(jobPath)); err != nil {
				return err
			}

			// Load job and verify template substitution
			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("title has substituted variable", "Test Authentication", job.Title)
				v.Contains("prompt has feature_name substituted", job.PromptBody, "Implementing feature: Authentication")
				v.Contains("prompt has plan name substituted", job.PromptBody, "For plan: vars-test-plan")
				v.Contains("prompt has priority substituted", job.PromptBody, "Priority: high")
			})
		}),
	},
)
