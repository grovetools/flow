package scenarios

import (
	"fmt"
	"path/filepath"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-tend/pkg/verify"
)

// PlanRecipeInheritsDefaultsScenario tests that jobs added via recipe inherit plan config defaults
var PlanRecipeInheritsDefaultsScenario = harness.NewScenario(
	"plan-recipe-inherits-defaults",
	"Tests that recipe jobs inherit default worktree and model values from plan config",
	[]string{"core", "plan", "recipe", "config"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment and create test recipe", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "test-inheritance-project")
			if err != nil {
				return err
			}
			ctx.Set("project_dir", projectDir)
			ctx.Set("notebooks_root", notebooksRoot)

			// Create a test recipe with jobs that don't specify worktree or model
			recipesDir := filepath.Join(ctx.ConfigDir(), "grove", "recipes", "inherit-test-recipe")
			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipes directory: %w", err)
			}

			recipeYaml := `description: Test recipe for config inheritance.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeYaml); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			// Job template without worktree or model specified
			firstJob := `---
title: Inherited Job
type: oneshot
---
This job should inherit plan defaults.`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-inherited-job.md"), firstJob); err != nil {
				return fmt.Errorf("creating recipe job: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan and set default config values", func(ctx *harness.Context) error {
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

			planPath := filepath.Join(notebooksRoot, "workspaces", "test-inheritance-project", "plans", "test-plan")
			ctx.Set("plan_path", planPath)

			// Set default worktree in plan config
			configCmd := ctx.Bin("plan", "config", "test-plan", "--set", "worktree=plan-default-wt")
			configCmd.Dir(projectDir)
			result = configCmd.Run()
			ctx.ShowCommandOutput(configCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan config --set worktree failed: %w", err)
			}

			// Set default model in plan config
			configCmd = ctx.Bin("plan", "config", "test-plan", "--set", "model=plan-default-model")
			configCmd.Dir(projectDir)
			result = configCmd.Run()
			ctx.ShowCommandOutput(configCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan config --set model failed: %w", err)
			}

			return nil
		}),

		harness.NewStep("Add recipe to plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "add", "test-plan", "--recipe", "inherit-test-recipe")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add --recipe failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("shows success message", result.Stdout, "Added 1 jobs from recipe 'inherit-test-recipe'")
			})
		}),

		harness.NewStep("Verify recipe job inherited plan defaults", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobPath := filepath.Join(planPath, "01-inherited-job.md")

			if err := ctx.Check("recipe job created", fs.AssertExists(jobPath)); err != nil {
				return err
			}

			// Load the job and verify it has the inherited values
			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("job inherited worktree from plan", "plan-default-wt", job.Worktree)
				v.Equal("job inherited model from plan", "plan-default-model", job.Model)
				v.Equal("job has correct title", "Inherited Job", job.Title)
			})
		}),
	},
)

// RecipeTemplateOverridesDefaultsScenario tests that explicit recipe values override plan defaults
var RecipeTemplateOverridesDefaultsScenario = harness.NewScenario(
	"recipe-template-overrides-defaults",
	"Tests that job template values in a recipe override plan config defaults",
	[]string{"core", "plan", "recipe", "config"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with override recipe", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "test-override-project")
			if err != nil {
				return err
			}
			ctx.Set("project_dir", projectDir)
			ctx.Set("notebooks_root", notebooksRoot)

			// Create a recipe with mixed explicit and implicit config
			recipesDir := filepath.Join(ctx.ConfigDir(), "grove", "recipes", "override-test-recipe")
			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipes directory: %w", err)
			}

			recipeYaml := `description: Test recipe with template overrides.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeYaml); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			// Job with explicit model override
			overriddenJob := `---
title: Overridden Job
type: oneshot
model: recipe-override-model
---
This job specifies its own model.`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-overridden.md"), overriddenJob); err != nil {
				return fmt.Errorf("creating overridden job: %w", err)
			}

			// Job without any model specified (should inherit)
			inheritedJob := `---
title: Inherited Job
type: oneshot
---
This job should inherit the plan model.`
			if err := fs.WriteString(filepath.Join(recipesDir, "02-inherited.md"), inheritedJob); err != nil {
				return fmt.Errorf("creating inherited job: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan and set default model", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			initCmd := ctx.Bin("plan", "init", "override-plan")
			initCmd.Dir(projectDir)
			result := initCmd.Run()
			ctx.ShowCommandOutput(initCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "test-override-project", "plans", "override-plan")
			ctx.Set("plan_path", planPath)

			// Set default model
			configCmd := ctx.Bin("plan", "config", "override-plan", "--set", "model=plan-default-model")
			configCmd.Dir(projectDir)
			result = configCmd.Run()
			ctx.ShowCommandOutput(configCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan config --set model failed: %w", err)
			}

			return nil
		}),

		harness.NewStep("Add recipe with overrides", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "add", "override-plan", "--recipe", "override-test-recipe")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add --recipe failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("shows success message", result.Stdout, "Added 2 jobs from recipe 'override-test-recipe'")
			})
		}),

		harness.NewStep("Verify overridden job has recipe model", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobPath := filepath.Join(planPath, "01-overridden-job.md")

			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading overridden job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("job kept recipe-specified model", "recipe-override-model", job.Model)
				v.Equal("job has correct title", "Overridden Job", job.Title)
			})
		}),

		harness.NewStep("Verify inherited job has plan model", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobPath := filepath.Join(planPath, "02-inherited-job.md")

			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading inherited job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("job inherited plan model", "plan-default-model", job.Model)
				v.Equal("job has correct title", "Inherited Job", job.Title)
			})
		}),
	},
)

// HoistedAddRecipeInheritsDefaultsScenario tests the top-level 'flow add --recipe' command
var HoistedAddRecipeInheritsDefaultsScenario = harness.NewScenario(
	"hoisted-add-recipe-inherits-defaults",
	"Tests that the hoisted 'flow add --recipe' command correctly inherits plan defaults",
	[]string{"core", "plan", "recipe", "config", "alias"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment and create test recipe", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "test-hoisted-project")
			if err != nil {
				return err
			}
			ctx.Set("project_dir", projectDir)
			ctx.Set("notebooks_root", notebooksRoot)

			// Create a simple test recipe
			recipesDir := filepath.Join(ctx.ConfigDir(), "grove", "recipes", "hoisted-test-recipe")
			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipes directory: %w", err)
			}

			recipeYaml := `description: Test recipe for hoisted command.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeYaml); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			firstJob := `---
title: Hoisted Test Job
type: oneshot
---
Testing hoisted add command.`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-test-job.md"), firstJob); err != nil {
				return fmt.Errorf("creating recipe job: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan with default worktree and model", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			initCmd := ctx.Bin("plan", "init", "hoisted-plan")
			initCmd.Dir(projectDir)
			result := initCmd.Run()
			ctx.ShowCommandOutput(initCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "test-hoisted-project", "plans", "hoisted-plan")
			ctx.Set("plan_path", planPath)

			// Set worktree default
			configCmd := ctx.Bin("plan", "config", "hoisted-plan", "--set", "worktree=hoisted-wt")
			configCmd.Dir(projectDir)
			result = configCmd.Run()
			ctx.ShowCommandOutput(configCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan config --set worktree failed: %w", err)
			}

			// Set model default
			configCmd = ctx.Bin("plan", "config", "hoisted-plan", "--set", "model=hoisted-model")
			configCmd.Dir(projectDir)
			result = configCmd.Run()
			ctx.ShowCommandOutput(configCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan config --set model failed: %w", err)
			}

			return nil
		}),

		harness.NewStep("Add recipe using hoisted 'add' command", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Use top-level 'add' instead of 'plan add'
			cmd := ctx.Bin("add", "hoisted-plan", "--recipe", "hoisted-test-recipe")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("add --recipe failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("shows success message", result.Stdout, "Added 1 jobs from recipe 'hoisted-test-recipe'")
			})
		}),

		harness.NewStep("Verify job inherited plan defaults via hoisted command", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobPath := filepath.Join(planPath, "01-hoisted-test-job.md")

			if err := ctx.Check("job created via hoisted command", fs.AssertExists(jobPath)); err != nil {
				return err
			}

			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("job inherited worktree", "hoisted-wt", job.Worktree)
				v.Equal("job inherited model", "hoisted-model", job.Model)
				v.Equal("job has correct title", "Hoisted Test Job", job.Title)
			})
		}),
	},
)

// RecipeInheritsAllPropertiesScenario tests all inheritable properties
var RecipeInheritsAllPropertiesScenario = harness.NewScenario(
	"recipe-inherits-all-properties",
	"Tests that all inheritable properties (model, worktree, prepend_dependencies) are applied to oneshot jobs",
	[]string{"core", "plan", "recipe", "config"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with multi-property recipe", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "test-all-props-project")
			if err != nil {
				return err
			}
			ctx.Set("project_dir", projectDir)
			ctx.Set("notebooks_root", notebooksRoot)

			// Create a recipe with oneshot jobs
			recipesDir := filepath.Join(ctx.ConfigDir(), "grove", "recipes", "all-props-recipe")
			if err := fs.CreateDir(recipesDir); err != nil {
				return fmt.Errorf("creating recipes directory: %w", err)
			}

			recipeYaml := `description: Test recipe for all inheritable properties.
`
			if err := fs.WriteString(filepath.Join(recipesDir, "recipe.yml"), recipeYaml); err != nil {
				return fmt.Errorf("creating recipe.yml: %w", err)
			}

			// First oneshot job (should inherit all properties)
			firstJob := `---
title: First Job
type: oneshot
---
First job for testing.`
			if err := fs.WriteString(filepath.Join(recipesDir, "01-first.md"), firstJob); err != nil {
				return fmt.Errorf("creating first job: %w", err)
			}

			// Second oneshot job (should also inherit all properties)
			secondJob := `---
title: Second Job
type: oneshot
---
Second job for testing.`
			if err := fs.WriteString(filepath.Join(recipesDir, "02-second.md"), secondJob); err != nil {
				return fmt.Errorf("creating second job: %w", err)
			}

			return nil
		}),

		harness.NewStep("Initialize plan and set all default properties", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			initCmd := ctx.Bin("plan", "init", "all-props-plan")
			initCmd.Dir(projectDir)
			result := initCmd.Run()
			ctx.ShowCommandOutput(initCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "test-all-props-project", "plans", "all-props-plan")
			ctx.Set("plan_path", planPath)

			// Set all inheritable properties
			configCmd := ctx.Bin("plan", "config", "all-props-plan",
				"--set", "worktree=all-wt",
				"--set", "model=all-model",
				"--set", "prepend_dependencies=true")
			configCmd.Dir(projectDir)
			result = configCmd.Run()
			ctx.ShowCommandOutput(configCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan config --set failed: %w", err)
			}

			return nil
		}),

		harness.NewStep("Add recipe with all properties", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "add", "all-props-plan", "--recipe", "all-props-recipe")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add --recipe failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("shows success message", result.Stdout, "Added 2 jobs from recipe 'all-props-recipe'")
			})
		}),

		harness.NewStep("Verify first job inherited all properties", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobPath := filepath.Join(planPath, "01-first-job.md")

			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading first job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("first job inherited worktree", "all-wt", job.Worktree)
				v.Equal("first job inherited model", "all-model", job.Model)
				v.Equal("first job inherited prepend_dependencies", true, job.PrependDependencies)
			})
		}),

		harness.NewStep("Verify second job inherited all properties", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobPath := filepath.Join(planPath, "02-second-job.md")

			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading second job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("second job inherited worktree", "all-wt", job.Worktree)
				v.Equal("second job inherited model", "all-model", job.Model)
				v.Equal("second job inherited prepend_dependencies", true, job.PrependDependencies)
			})
		}),
	},
)
