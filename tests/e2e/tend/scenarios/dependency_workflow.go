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

var DependencyWorkflowScenario = harness.NewScenario(
	"basic-dependency-workflow",
	"Validates sequential shell job execution with dependencies.",
	[]string{"core", "cli", "dependencies"},
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

		harness.NewStep("Initialize a new plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			projectName := "my-project"
			planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", "basic-plan")
			ctx.Set("plan_path", planPath)

			cmd := ctx.Command("flow", "plan", "init", "basic-plan")
			cmd.Dir(projectDir)
			cmd.Env("HOME=" + ctx.GetString("home_dir"))
			result := cmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("failed to init plan: %w\nOutput: %s", err, result.Stderr)
			}
			return nil
		}),

		harness.NewStep("Add the first shell job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Command("flow", "plan", "add", "basic-plan",
				"--type", "shell",
				"--title", "create-file",
				"-p", "printf 'hello' > output.txt")
			cmd.Dir(projectDir)
			cmd.Env("HOME=" + ctx.GetString("home_dir"))
			result := cmd.Run()
			return result.AssertSuccess()
		}),

		harness.NewStep("Add the second (dependent) shell job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Command("flow", "plan", "add", "basic-plan",
				"--type", "shell",
				"--title", "append-file",
				"-p", "printf ' world' >> output.txt",
				"-d", "01-create-file.md") // Dependency on the first job
			cmd.Dir(projectDir)
			cmd.Env("HOME=" + ctx.GetString("home_dir"))
			result := cmd.Run()
			return result.AssertSuccess()
		}),

		harness.NewStep("Run the first job and verify", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Command("flow", "plan", "run", "--next", "--yes")
			cmd.Dir(projectDir)
			cmd.Env("HOME=" + ctx.GetString("home_dir"))
			result := cmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify file content (shell jobs run in project directory)
			outputFile := filepath.Join(projectDir, "output.txt")
			if err := fs.AssertContains(outputFile, "hello"); err != nil {
				return err
			}
			if err := fs.AssertNotContains(outputFile, "hello world"); err != nil {
				return fmt.Errorf("output file should not contain 'world' yet")
			}

			// Verify job status
			job1, err := orchestration.LoadJob(filepath.Join(planPath, "01-create-file.md"))
			if err != nil {
				return err
			}
			if job1.Status != orchestration.JobStatusCompleted {
				return fmt.Errorf("expected job 1 status to be 'completed', but was '%s'", job1.Status)
			}
			return nil
		}),

		harness.NewStep("Run the second job and verify", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Command("flow", "plan", "run", "--next", "--yes")
			cmd.Dir(projectDir)
			cmd.Env("HOME=" + ctx.GetString("home_dir"))
			result := cmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify final file content (shell jobs run in project directory)
			outputFile := filepath.Join(projectDir, "output.txt")
			if err := fs.AssertContains(outputFile, "hello world"); err != nil {
				return err
			}

			// Verify job status
			job2, err := orchestration.LoadJob(filepath.Join(planPath, "02-append-file.md"))
			if err != nil {
				return err
			}
			if job2.Status != orchestration.JobStatusCompleted {
				return fmt.Errorf("expected job 2 status to be 'completed', but was '%s'", job2.Status)
			}
			return nil
		}),
	},
)
