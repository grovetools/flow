package scenarios

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

var AgentWorktreeLifecycleScenario = harness.NewScenario(
	"agent-worktree-lifecycle",
	"Tests agent job execution in a git worktree and manual completion.",
	[]string{"core", "cli", "agent", "worktree"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			// Create a sandboxed home directory for global config
			homeDir := ctx.NewDir("home")
			ctx.Set("home_dir", homeDir)
			if err := fs.CreateDir(homeDir); err != nil {
				return err
			}

			// Create a project directory and initialize it as a git repo
			projectDir := ctx.NewDir("worktree-project")
			ctx.Set("project_dir", projectDir)
			if err := fs.CreateDir(projectDir); err != nil {
				return err
			}
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}

			// Create a dummy file for initial commit
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Test Project\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
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
			harness.Mock{CommandName: "grove"}, // Mocks `grove aglogs`
		),

		harness.NewStep("Initialize plan with a worktree", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Command("flow", "plan", "init", "agent-plan", "--worktree")
			cmd.Dir(projectDir)
			cmd.Env("HOME=" + ctx.GetString("home_dir"))
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Verify worktree and branch creation", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "agent-plan")
			if err := fs.AssertExists(worktreePath); err != nil {
				return err
			}

			// Check if branch exists using git branch command
			cmd := exec.Command("git", "branch", "--list", "agent-plan")
			cmd.Dir = projectDir
			output, err := cmd.Output()
			if err != nil {
				return fmt.Errorf("failed to check for branch: %w", err)
			}
			if !strings.Contains(string(output), "agent-plan") {
				return fmt.Errorf("expected branch 'agent-plan' to be created")
			}
			return nil
		}),

		harness.NewStep("Add an interactive_agent job to the plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			projectName := "worktree-project"
			planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", "agent-plan")
			ctx.Set("plan_path", planPath)

			cmd := ctx.Command("flow", "plan", "add", "agent-plan",
				"--type", "interactive_agent",
				"--title", "Implement Task",
				"-p", "Implement a test feature")
			cmd.Dir(projectDir)
			cmd.Env("HOME=" + ctx.GetString("home_dir"))
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Simulate agent launch by setting job to running", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobPath := filepath.Join(planPath, "01-implement-task.md")
			ctx.Set("job_path", jobPath)

			// Read the job file
			content, err := fs.ReadString(jobPath)
			if err != nil {
				return fmt.Errorf("reading job file: %w", err)
			}

			// Replace status: pending with status: running
			updatedContent := strings.Replace(content, "status: pending", "status: running", 1)
			if err := fs.WriteString(jobPath, updatedContent); err != nil {
				return fmt.Errorf("updating job status: %w", err)
			}

			// Create a lock file to simulate an active session
			lockPath := jobPath + ".lock"
			if err := fs.WriteString(lockPath, fmt.Sprintf("pid: 12345\nsession: agent-plan\n")); err != nil {
				return fmt.Errorf("creating lock file: %w", err)
			}

			return nil
		}),

		harness.NewStep("Verify job is in 'running' state", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")

			// Assert job status
			if err := assert.YAMLField(jobPath, "status", "running", "Job status should be 'running'"); err != nil {
				return err
			}

			// Assert lock file exists
			return fs.AssertExists(jobPath + ".lock")
		}),

		harness.NewStep("Complete the job manually", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobPath := ctx.GetString("job_path")

			// Remove the lock file before completing (simulates session ending)
			lockPath := jobPath + ".lock"
			if err := fs.RemoveIfExists(lockPath); err != nil {
				return fmt.Errorf("removing lock file: %w", err)
			}

			// Use the full path to the job file
			cmd := ctx.Command("flow", "plan", "complete", jobPath)
			cmd.Dir(projectDir)
			cmd.Env("HOME=" + ctx.GetString("home_dir"))
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Verify job is 'completed' and cleaned up", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")

			// Assert job status
			if err := assert.YAMLField(jobPath, "status", "completed", "Job status should be 'completed'"); err != nil {
				return err
			}

			// Assert lock file is removed
			if err := fs.AssertNotExists(jobPath + ".lock"); err != nil {
				return err
			}

			// Assert mock transcript was appended
			return fs.AssertContains(jobPath, "## Transcript")
		}),
	},
)
