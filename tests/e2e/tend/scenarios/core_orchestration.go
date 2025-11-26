package scenarios

import (
	"fmt"
	"path/filepath"

	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

var CoreOrchestrationScenario = harness.NewScenario(
	"core-orchestration",
	"Validates core CLI orchestration logic: plan init, job execution, and status updates.",
	[]string{"core", "cli"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			// Create a sandboxed home directory for global config
			homeDir := ctx.NewDir("home")
			ctx.Set("home_dir", homeDir)
			if err := fs.CreateDir(homeDir); err != nil {
				return err
			}

			// Create a project directory and initialize it as a git repo
			projectDir := ctx.NewDir("my-project")
			ctx.Set("project_dir", projectDir)
			if err := fs.CreateDir(projectDir); err != nil {
				return err
			}
			if _, err := git.SetupTestRepo(projectDir); err != nil {
				return err
			}

			// Configure a centralized notebook in the sandboxed global config
			notebooksRoot := filepath.Join(homeDir, "notebooks")
			if err := fs.CreateDir(notebooksRoot); err != nil {
				return err
			}
			ctx.Set("notebooks_root", notebooksRoot)

			// Create the global config directory
			configDir := filepath.Join(homeDir, ".config", "grove")
			if err := fs.CreateDir(configDir); err != nil {
				return err
			}

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

			globalCfg := &config.Config{
				Version:   "1.0",
				Notebooks: notebookConfig,
			}

			return fs.WriteGroveConfig(configDir, globalCfg)
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "cx"},
		),

		harness.NewStep("Initialize plan from recipe", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Command("flow", "plan", "init", "my-plan", "--recipe", "standard-feature")
			cmd.Dir(projectDir)

			// This command needs the sandboxed home directory to find the global config
			cmd.Env("HOME=" + ctx.GetString("home_dir"))

			result := cmd.Run()
			return result.AssertSuccess()
		}),

		harness.NewStep("Verify plan creation", func(ctx *harness.Context) error {
			notebooksRoot := ctx.GetString("notebooks_root")
			projectName := "my-project"
			planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", "my-plan")
			ctx.Set("plan_path", planPath)

			if err := fs.AssertExists(planPath); err != nil {
				return err
			}
			if err := fs.AssertExists(filepath.Join(planPath, ".grove-plan.yml")); err != nil {
				return err
			}
			return fs.AssertExists(filepath.Join(planPath, "01-spec.md"))
		}),

		harness.NewStep("Verify plan has jobs from recipe", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Verify that jobs from the recipe were created
			expectedJobs := []string{
				"01-spec.md",
				"02-implement.md",
				"03-git-changes.md",
				"04-git-status.md",
				"05-review.md",
			}

			for _, jobFile := range expectedJobs {
				jobPath := filepath.Join(planPath, jobFile)
				if err := fs.AssertExists(jobPath); err != nil {
					return fmt.Errorf("expected job file %s to exist: %w", jobFile, err)
				}

				// Load job to verify it's valid
				_, err := orchestration.LoadJob(jobPath)
				if err != nil {
					return fmt.Errorf("loading job %s: %w", jobFile, err)
				}
			}

			return nil
		}),
	},
)
