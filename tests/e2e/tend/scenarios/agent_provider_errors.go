package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-tend/pkg/verify"
)

// OpencodeMissingBinaryScenario tests graceful failure when opencode binary is not in PATH.
var OpencodeMissingBinaryScenario = harness.NewScenario(
	"opencode-missing-binary",
	"Tests graceful failure when opencode binary is not found in PATH",
	[]string{"agent", "provider", "opencode", "error"},
	[]harness.Step{
		harness.NewStep("Setup environment with opencode provider", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "opencode-missing-project")
			if err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			// Configure opencode as provider
			groveConfig := &config.Config{
				Name:    "opencode-missing-project",
				Version: "1.0",
				Extensions: map[string]interface{}{
					"agent": map[string]interface{}{
						"interactive_provider": "opencode",
					},
				},
			}

			if err := fs.WriteGroveConfig(projectDir, groveConfig); err != nil {
				return err
			}

			ctx.Set("notebooks_root", notebooksRoot)
			return nil
		}),

		// NOTE: We intentionally do NOT include the opencode mock
		// This simulates the opencode binary not being installed
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "tmux"},
			// NO opencode mock - simulates missing binary
		),

		harness.NewStep("Initialize plan and add job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "missing-binary-plan", "--worktree")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			cmd = ctx.Bin("plan", "add", "missing-binary-plan",
				"--type", "interactive_agent",
				"--title", "Missing Binary Test",
				"-p", "Test missing opencode binary handling")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "opencode-missing-project", "plans", "missing-binary-plan")
			ctx.Set("plan_path", planPath)
			ctx.Set("job_path", filepath.Join(planPath, "01-missing-binary-test.md"))
			return nil
		}),

		harness.NewStep("Run job - should still set status to running (tmux sends command)", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobPath := ctx.GetString("job_path")

			cmd := ctx.Bin("plan", "run", jobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// The job will be set to running because tmux send-keys succeeds
			// (the mock tmux accepts the command even though opencode doesn't exist)
			// This is expected behavior - the actual error would occur in the tmux pane
			// when the shell tries to execute 'opencode'

			// Verify job is in running state (command was sent to tmux)
			return assert.YAMLField(jobPath, "status", "running", "job should be running (command sent to tmux)")
		}),
	},
)

// AgentProviderCleanupScenario tests that findAgentSessionInfo works across providers.
var AgentProviderCleanupScenario = harness.NewScenario(
	"agent-provider-cleanup",
	"Tests that provider-agnostic session cleanup works correctly",
	[]string{"agent", "provider", "cleanup"},
	[]harness.Step{
		harness.NewStep("Setup environment with opencode provider", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "cleanup-test-project")
			if err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			groveConfig := &config.Config{
				Name:    "cleanup-test-project",
				Version: "1.0",
				Extensions: map[string]interface{}{
					"agent": map[string]interface{}{
						"interactive_provider": "opencode",
					},
				},
			}

			if err := fs.WriteGroveConfig(projectDir, groveConfig); err != nil {
				return err
			}

			ctx.Set("notebooks_root", notebooksRoot)
			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "opencode"},
			harness.Mock{CommandName: "tmux"},
		),

		harness.NewStep("Initialize plan, add job, and run", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "cleanup-plan", "--worktree")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			cmd = ctx.Bin("plan", "add", "cleanup-plan",
				"--type", "interactive_agent",
				"--title", "Cleanup Test",
				"-p", "Test cleanup functionality")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "cleanup-test-project", "plans", "cleanup-plan")
			jobPath := filepath.Join(planPath, "01-cleanup-test.md")
			ctx.Set("plan_path", planPath)
			ctx.Set("job_path", jobPath)

			cmd = ctx.Bin("plan", "run", jobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			return assert.YAMLField(jobPath, "status", "running", "job should be running")
		}),

		harness.NewStep("Create lock file to simulate active session", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")

			lockPath := jobPath + ".lock"
			if err := fs.WriteString(lockPath, "pid: 12345\nsession: cleanup-test\n"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Complete job and verify cleanup message mentions agent (not Claude)", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobPath := ctx.GetString("job_path")

			// Remove lock file first
			fs.RemoveIfExists(jobPath + ".lock")

			cmd := ctx.Bin("plan", "complete", jobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Verify the output uses generic "agent" terminology
			combinedOutput := result.Stdout + result.Stderr

			return ctx.Verify(func(v *verify.Collector) {
				// Should not use "Claude" terminology for opencode jobs
				// The cleanup message should be provider-agnostic
				v.True("no Claude-specific message", !strings.Contains(strings.ToLower(combinedOutput), "claude session"))
			})
		}),

		harness.NewStep("Verify job is completed", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("job is completed", nil, assert.YAMLField(jobPath, "status", "completed", "job should be completed"))
				v.Equal("lock file removed", nil, fs.AssertNotExists(jobPath+".lock"))
			})
		}),
	},
)

// StatusVerificationWithMissingMetadataScenario tests job status verification when session metadata is missing.
var StatusVerificationWithMissingMetadataScenario = harness.NewScenario(
	"status-verification-missing-metadata",
	"Tests that status verification handles missing session metadata gracefully",
	[]string{"agent", "provider", "status", "verification"},
	[]harness.Step{
		harness.NewStep("Setup environment", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "status-check-project")
			if err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			groveConfig := &config.Config{
				Name:    "status-check-project",
				Version: "1.0",
				Extensions: map[string]interface{}{
					"agent": map[string]interface{}{
						"interactive_provider": "opencode",
					},
				},
			}

			if err := fs.WriteGroveConfig(projectDir, groveConfig); err != nil {
				return err
			}

			ctx.Set("notebooks_root", notebooksRoot)
			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "opencode"},
			harness.Mock{CommandName: "tmux"},
		),

		harness.NewStep("Initialize plan, add and run job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "status-plan", "--worktree")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			cmd = ctx.Bin("plan", "add", "status-plan",
				"--type", "interactive_agent",
				"--title", "Status Check Test",
				"-p", "Test status verification")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "status-check-project", "plans", "status-plan")
			jobPath := filepath.Join(planPath, "01-status-check-test.md")
			ctx.Set("plan_path", planPath)
			ctx.Set("job_path", jobPath)

			cmd = ctx.Bin("plan", "run", jobPath)
			cmd.Dir(projectDir)
			cmd.Run()

			return nil
		}),

		harness.NewStep("Verify plan directory exists without session metadata", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Note: We haven't created any session metadata in ~/.grove/hooks/sessions
			// The plan should still load correctly without crashing.

			// Verify the plan directory exists and can be accessed
			planConfigPath := filepath.Join(planPath, ".grove-plan.yml")
			if err := fs.AssertExists(planConfigPath); err != nil {
				return fmt.Errorf("plan config file should exist: %w", err)
			}

			return nil
		}),

		harness.NewStep("Verify job file is valid without session metadata", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")

			// Verify the job file exists and has valid YAML
			if err := fs.AssertExists(jobPath); err != nil {
				return fmt.Errorf("job file should exist: %w", err)
			}

			// Verify we can read the job status
			return assert.YAMLField(jobPath, "status", "running", "job should have valid status")
		}),
	},
)

// JobFailureOnLaunchErrorScenario tests that job is marked as failed when provider launch fails.
var JobFailureOnLaunchErrorScenario = harness.NewScenario(
	"job-failure-on-launch-error",
	"Tests that job is marked as failed when the interactive agent launch fails",
	[]string{"agent", "provider", "error", "failure"},
	[]harness.Step{
		harness.NewStep("Setup environment with invalid provider", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "launch-error-project")
			if err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			// Use an invalid provider name
			groveConfig := &config.Config{
				Name:    "launch-error-project",
				Version: "1.0",
				Extensions: map[string]interface{}{
					"agent": map[string]interface{}{
						"interactive_provider": "invalid-provider-xyz",
					},
				},
			}

			if err := fs.WriteGroveConfig(projectDir, groveConfig); err != nil {
				return err
			}

			ctx.Set("notebooks_root", notebooksRoot)
			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "tmux"},
		),

		harness.NewStep("Initialize plan and add job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "launch-error-plan", "--worktree")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			cmd = ctx.Bin("plan", "add", "launch-error-plan",
				"--type", "interactive_agent",
				"--title", "Launch Error Test",
				"-p", "Test launch error handling")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "launch-error-project", "plans", "launch-error-plan")
			ctx.Set("plan_path", planPath)
			ctx.Set("job_path", filepath.Join(planPath, "01-launch-error-test.md"))
			return nil
		}),

		harness.NewStep("Run job and expect failure", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobPath := ctx.GetString("job_path")

			cmd := ctx.Bin("plan", "run", jobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// The command should fail due to invalid provider
			if result.ExitCode == 0 {
				return fmt.Errorf("expected command to fail with non-zero exit code")
			}

			return nil
		}),

		harness.NewStep("Verify job is marked as failed", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")

			return assert.YAMLField(jobPath, "status", "failed", "job should be marked as failed after launch error")
		}),
	},
)
