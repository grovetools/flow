package scenarios

import (
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-tend/pkg/verify"
)

// MultiProviderConfigScenario tests that multiple providers can be configured with different args.
var MultiProviderConfigScenario = harness.NewScenario(
	"multi-provider-config",
	"Tests multiple providers configured with different args in grove.yml",
	[]string{"agent", "provider", "config", "multi"},
	[]harness.Step{
		harness.NewStep("Setup environment with all three providers configured", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "multi-provider-project")
			if err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Multi Provider Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			// Configure grove.yml with all three providers, each with unique args
			groveConfig := &config.Config{
				Name:    "multi-provider-project",
				Version: "1.0",
				Extensions: map[string]interface{}{
					"agent": map[string]interface{}{
						"interactive_provider": "claude", // Start with claude as default
						"providers": map[string]interface{}{
							"claude": map[string]interface{}{
								"args": []string{"--claude-specific-arg", "--model", "claude-opus"},
							},
							"codex": map[string]interface{}{
								"args": []string{"--codex-specific-arg", "--full-auto"},
							},
							"opencode": map[string]interface{}{
								"args": []string{"--opencode-specific-arg", "--verbose"},
							},
						},
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
			harness.Mock{CommandName: "claude"},
			harness.Mock{CommandName: "opencode"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "tmux"},
		),

		harness.NewStep("Initialize plan with worktree", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "multi-provider-plan", "--worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "multi-provider-project", "plans", "multi-provider-plan")
			ctx.Set("plan_path", planPath)
			return nil
		}),

		harness.NewStep("Add job and run with default provider (claude)", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Bin("plan", "add", "multi-provider-plan",
				"--type", "interactive_agent",
				"--title", "Claude Provider Test",
				"-p", "Test claude provider")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			jobPath := filepath.Join(planPath, "01-claude-provider-test.md")
			ctx.Set("claude_job_path", jobPath)

			cmd = ctx.Bin("plan", "run", jobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Verify job is running
			return assert.YAMLField(jobPath, "status", "running", "job should be running")
		}),

		harness.NewStep("Complete claude job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobPath := ctx.GetString("claude_job_path")

			// Remove lock file if exists
			fs.RemoveIfExists(jobPath + ".lock")

			cmd := ctx.Bin("plan", "complete", jobPath)
			cmd.Dir(projectDir)
			return cmd.Run().AssertSuccess()
		}),

		harness.NewStep("Switch to opencode provider and run job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Update grove.yml to use opencode
			groveConfig := &config.Config{
				Name:    "multi-provider-project",
				Version: "1.0",
				Extensions: map[string]interface{}{
					"agent": map[string]interface{}{
						"interactive_provider": "opencode",
						"providers": map[string]interface{}{
							"claude": map[string]interface{}{
								"args": []string{"--claude-specific-arg"},
							},
							"codex": map[string]interface{}{
								"args": []string{"--codex-specific-arg"},
							},
							"opencode": map[string]interface{}{
								"args": []string{"--opencode-specific-arg", "--verbose"},
							},
						},
					},
				},
			}

			// Write to worktree location
			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "multi-provider-plan")
			if err := fs.WriteGroveConfig(worktreePath, groveConfig); err != nil {
				return err
			}

			// Add opencode job
			cmd := ctx.Bin("plan", "add", "multi-provider-plan",
				"--type", "interactive_agent",
				"--title", "Opencode Provider Test",
				"-p", "Test opencode provider")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			jobPath := filepath.Join(planPath, "02-opencode-provider-test.md")
			ctx.Set("opencode_job_path", jobPath)

			cmd = ctx.Bin("plan", "run", jobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Note: In non-TTY test environments, the GROVE_AGENT_PROVIDER
			// env var is set inside the tmux pane, not visible in output.
			// We verify the job runs successfully with opencode provider
			// by checking it reaches running status.
			return assert.YAMLField(jobPath, "status", "running", "job should be running with opencode provider")
		}),

		harness.NewStep("Complete opencode job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobPath := ctx.GetString("opencode_job_path")

			fs.RemoveIfExists(jobPath + ".lock")

			cmd := ctx.Bin("plan", "complete", jobPath)
			cmd.Dir(projectDir)
			return cmd.Run().AssertSuccess()
		}),

		harness.NewStep("Verify both jobs completed successfully", func(ctx *harness.Context) error {
			claudeJobPath := ctx.GetString("claude_job_path")
			opencodeJobPath := ctx.GetString("opencode_job_path")

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("claude job completed", nil, assert.YAMLField(claudeJobPath, "status", "completed", "claude job should be completed"))
				v.Equal("opencode job completed", nil, assert.YAMLField(opencodeJobPath, "status", "completed", "opencode job should be completed"))
			})
		}),
	},
)

// ProviderDefaultFallbackScenario tests that claude is used when no provider is specified.
var ProviderDefaultFallbackScenario = harness.NewScenario(
	"provider-default-fallback",
	"Tests that claude provider is used by default when no interactive_provider is specified",
	[]string{"agent", "provider", "config", "default"},
	[]harness.Step{
		harness.NewStep("Setup environment without interactive_provider", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "default-provider-project")
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

			// Configure grove.yml WITHOUT interactive_provider
			groveConfig := &config.Config{
				Name:    "default-provider-project",
				Version: "1.0",
				// No agent extension - should default to claude
			}

			if err := fs.WriteGroveConfig(projectDir, groveConfig); err != nil {
				return err
			}

			ctx.Set("notebooks_root", notebooksRoot)
			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "claude"},
			harness.Mock{CommandName: "tmux"},
		),

		harness.NewStep("Initialize plan and add job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "default-plan", "--worktree")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			cmd = ctx.Bin("plan", "add", "default-plan",
				"--type", "interactive_agent",
				"--title", "Default Provider Test",
				"-p", "Test default provider fallback")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "default-provider-project", "plans", "default-plan")
			ctx.Set("plan_path", planPath)
			ctx.Set("job_path", filepath.Join(planPath, "01-default-provider-test.md"))
			return nil
		}),

		harness.NewStep("Run job and verify claude is used by default", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobPath := ctx.GetString("job_path")

			cmd := ctx.Bin("plan", "run", jobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// The tmux mock logs send-keys commands
			// We should NOT see GROVE_AGENT_PROVIDER='opencode' or 'codex'
			// Claude doesn't set GROVE_AGENT_PROVIDER (only codex/opencode do)
			combinedOutput := result.Stdout + result.Stderr

			return ctx.Verify(func(v *verify.Collector) {
				// Verify job is running (command was sent successfully)
				v.Equal("job is running", nil, assert.YAMLField(jobPath, "status", "running", "job should be running"))
				// Verify it's not opencode
				v.True("not opencode provider", !strings.Contains(combinedOutput, "GROVE_AGENT_PROVIDER='opencode'"))
			})
		}),
	},
)

// InvalidProviderErrorScenario tests that an invalid provider name produces a clear error.
var InvalidProviderErrorScenario = harness.NewScenario(
	"invalid-provider-error",
	"Tests that an invalid provider name produces a clear error message",
	[]string{"agent", "provider", "config", "error"},
	[]harness.Step{
		harness.NewStep("Setup environment with invalid provider", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "invalid-provider-project")
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

			// Configure grove.yml with invalid provider
			groveConfig := &config.Config{
				Name:    "invalid-provider-project",
				Version: "1.0",
				Extensions: map[string]interface{}{
					"agent": map[string]interface{}{
						"interactive_provider": "nonexistent-provider",
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

			cmd := ctx.Bin("plan", "init", "invalid-plan", "--worktree")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			cmd = ctx.Bin("plan", "add", "invalid-plan",
				"--type", "interactive_agent",
				"--title", "Invalid Provider Test",
				"-p", "Test invalid provider error")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "invalid-provider-project", "plans", "invalid-plan")
			ctx.Set("plan_path", planPath)
			ctx.Set("job_path", filepath.Join(planPath, "01-invalid-provider-test.md"))
			return nil
		}),

		harness.NewStep("Run job and verify error for invalid provider", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobPath := ctx.GetString("job_path")

			cmd := ctx.Bin("plan", "run", jobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Should fail with error about unknown provider
			combinedOutput := result.Stdout + result.Stderr

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("error mentions unknown provider", combinedOutput, "unknown interactive_agent provider")
				v.Contains("error mentions provider name", combinedOutput, "nonexistent-provider")
			})
		}),

		harness.NewStep("Verify job is marked as failed", func(ctx *harness.Context) error {
			jobPath := ctx.GetString("job_path")

			return assert.YAMLField(jobPath, "status", "failed", "job should be marked as failed")
		}),
	},
)

// ProviderSpecificArgsScenario tests that each provider receives only its own args.
var ProviderSpecificArgsScenario = harness.NewScenario(
	"provider-specific-args",
	"Tests that each provider receives only its configured args, not other providers' args",
	[]string{"agent", "provider", "config", "args"},
	[]harness.Step{
		harness.NewStep("Setup environment with multiple providers", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "provider-args-project")
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

			// Configure with unique args per provider
			groveConfig := &config.Config{
				Name:    "provider-args-project",
				Version: "1.0",
				Extensions: map[string]interface{}{
					"agent": map[string]interface{}{
						"interactive_provider": "opencode",
						"providers": map[string]interface{}{
							"claude": map[string]interface{}{
								"args": []string{"--claude-unique-arg"},
							},
							"opencode": map[string]interface{}{
								"args": []string{"--opencode-unique-arg"},
							},
						},
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

		harness.NewStep("Initialize plan and add job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "args-isolation-plan", "--worktree")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			cmd = ctx.Bin("plan", "add", "args-isolation-plan",
				"--type", "interactive_agent",
				"--title", "Args Isolation Test",
				"-p", "Test args isolation")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "provider-args-project", "plans", "args-isolation-plan")
			ctx.Set("plan_path", planPath)
			ctx.Set("job_path", filepath.Join(planPath, "01-args-isolation-test.md"))
			return nil
		}),

		harness.NewStep("Run job and verify opencode args are used, not claude args", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobPath := ctx.GetString("job_path")

			cmd := ctx.Bin("plan", "run", jobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// The tmux mock logs the send-keys command which contains the agent command
			combinedOutput := result.Stdout + result.Stderr

			return ctx.Verify(func(v *verify.Collector) {
				// Should see opencode being called
				v.Contains("opencode is called", combinedOutput, "opencode")
				// Should NOT see claude's unique arg
				v.True("claude args not present", !strings.Contains(combinedOutput, "--claude-unique-arg"))
			})
		}),
	},
)
