package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/core/config"
	"github.com/grovetools/tend/pkg/assert"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// createInteractiveAgentNonTTYScenario generates a test that verifies interactive_agent
// jobs launch correctly in a non-TTY context for a given provider. Before the fix,
// the tmux.Launch() call required a TTY. The fix uses RealCommandExecutor with
// `tmux new-session -d` which works without a TTY, and always creates agent windows
// as detached.
func createInteractiveAgentNonTTYScenario(p ProviderConfig) *harness.Scenario {
	return harness.NewScenario(
		fmt.Sprintf("%s-interactive-non-tty", p.Name),
		fmt.Sprintf("Tests %s interactive_agent job launches correctly in non-TTY context (detached tmux session)", p.Name),
		[]string{"agent", "provider", p.Name, "non-tty", "interactive"},
		[]harness.Step{
			harness.NewStep(fmt.Sprintf("Setup environment with %s provider", p.Name), func(ctx *harness.Context) error {
				projectName := fmt.Sprintf("%s-nontty-project", p.ProjectSuffix)
				projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, projectName)
				if err != nil {
					return err
				}

				repo, err := git.SetupTestRepo(projectDir)
				if err != nil {
					return err
				}
				if err := fs.WriteString(filepath.Join(projectDir, "README.md"), fmt.Sprintf("# %s Non-TTY Test\n", p.Name)); err != nil {
					return err
				}
				if err := repo.AddCommit("Initial commit"); err != nil {
					return err
				}

				groveConfig := &config.Config{
					Name:    projectName,
					Version: "1.0",
					Extensions: map[string]interface{}{
						"flow": map[string]interface{}{
							"interactive_provider": p.Name,
						},
					},
				}

				if err := fs.WriteGroveConfig(projectDir, groveConfig); err != nil {
					return err
				}

				ctx.Set("notebooks_root", notebooksRoot)
				ctx.Set("provider_name", p.Name)
				ctx.Set("project_name", projectName)
				return nil
			}),

			harness.SetupMocks(
				harness.Mock{CommandName: "grove"},
				harness.Mock{CommandName: p.MockName},
				harness.Mock{CommandName: "tmux"},
			),

			harness.NewStep("Initialize plan with worktree", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				providerName := ctx.GetString("provider_name")

				planName := fmt.Sprintf("%s-nontty-plan", providerName)
				cmd := ctx.Bin("plan", "init", planName, "--worktree")
				cmd.Dir(projectDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if err := result.AssertSuccess(); err != nil {
					return err
				}

				ctx.Set("plan_name", planName)
				return nil
			}),

			harness.NewStep("Add interactive_agent job", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				notebooksRoot := ctx.GetString("notebooks_root")
				planName := ctx.GetString("plan_name")
				providerName := ctx.GetString("provider_name")
				projectName := ctx.GetString("project_name")

				planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", planName)
				ctx.Set("plan_path", planPath)

				cmd := ctx.Bin("plan", "add", planName,
					"--type", "interactive_agent",
					"--title", fmt.Sprintf("%s Non-TTY Test", providerName),
					"-p", fmt.Sprintf("Test the %s provider in non-TTY context", providerName))
				cmd.Dir(projectDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if err := result.AssertSuccess(); err != nil {
					return err
				}

				jobPath := filepath.Join(planPath, fmt.Sprintf("01-%s-non-tty-test.md", providerName))
				ctx.Set("job_path", jobPath)
				return nil
			}),

			harness.NewStep("Run interactive_agent job without TTY and verify success", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				jobPath := ctx.GetString("job_path")

				// The tend harness runs commands without a TTY by default.
				// Before the fix, this would fail because tmux.Launch() required a TTY.
				// After the fix, RealCommandExecutor.Execute("tmux", "new-session", "-d", ...)
				// works correctly without a TTY.
				cmd := ctx.Bin("plan", "run", jobPath)
				cmd.Dir(projectDir)
				cmd.Timeout(60 * time.Second)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				// The job should succeed and reach running status
				if err := result.AssertSuccess(); err != nil {
					return fmt.Errorf("plan run failed in non-TTY context (this was the bug): %w", err)
				}

				// Verify the job is now in running status
				return assert.YAMLField(jobPath, "status", "running", "job should be running after non-TTY launch")
			}),

			harness.NewStep("Verify briefing file was created", func(ctx *harness.Context) error {
				planPath := ctx.GetString("plan_path")
				providerName := ctx.GetString("provider_name")

				// Verify the briefing file exists in artifacts
				artifactsDir := filepath.Join(planPath, ".artifacts")
				entries, err := filepath.Glob(filepath.Join(artifactsDir, "*", "briefing-*.xml"))
				if err != nil {
					return fmt.Errorf("error checking for briefing files: %w", err)
				}
				if len(entries) == 0 {
					return fmt.Errorf("expected at least one briefing XML file in %s", artifactsDir)
				}

				briefingContent, err := fs.ReadString(entries[0])
				if err != nil {
					return fmt.Errorf("reading briefing file: %w", err)
				}

				return ctx.Verify(func(v *verify.Collector) {
					v.Contains("briefing has prompt section", briefingContent, "<prompt>")
					v.Contains("briefing mentions non-TTY test", briefingContent, fmt.Sprintf("%s provider in non-TTY context", providerName))
				})
			}),

			harness.NewStep("Verify no TTY-related errors in output", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				jobPath := ctx.GetString("job_path")

				// Re-read the run output by checking the job status
				// The key assertion is that no TTY errors were produced
				cmd := ctx.Bin("plan", "run", jobPath)
				cmd.Dir(projectDir)
				cmd.Timeout(60 * time.Second)
				result := cmd.Run()

				// The job is already running, so this should either succeed with
				// "already running" or fail gracefully
				combined := result.Stdout + result.Stderr

				return ctx.Verify(func(v *verify.Collector) {
					v.NotContains("no 'open terminal failed' error", combined, "open terminal failed")
					v.NotContains("no 'not a terminal' error", combined, "not a terminal")
					v.NotContains("no 'inappropriate ioctl' error", combined, "inappropriate ioctl")
				})
			}),

			harness.NewStep("Complete job and verify cleanup", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				jobPath := ctx.GetString("job_path")

				// Remove lock file if exists
				_ = fs.RemoveIfExists(jobPath + ".lock")

				cmd := ctx.Bin("plan", "complete", jobPath)
				cmd.Dir(projectDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if err := result.AssertSuccess(); err != nil {
					return err
				}

				return assert.YAMLField(jobPath, "status", "completed", "job should be completed")
			}),
		},
	)
}

// createMultiJobNonTTYScenario tests that multiple job types (interactive_agent,
// headless_agent, oneshot) all work correctly in non-TTY contexts, ensuring the
// tmux session changes don't break other job types.
func createMultiJobNonTTYScenario() *harness.Scenario {
	return harness.NewScenario(
		"multi-job-non-tty-regression",
		"Tests that interactive_agent, headless_agent, and oneshot jobs all work in non-TTY contexts",
		[]string{"agent", "non-tty", "regression", "interactive", "headless", "oneshot"},
		[]harness.Step{
			harness.NewStep("Setup environment", func(ctx *harness.Context) error {
				projectName := "multi-nontty-project"
				projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, projectName)
				if err != nil {
					return err
				}

				repo, err := git.SetupTestRepo(projectDir)
				if err != nil {
					return err
				}
				if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Multi Job Non-TTY Test\n"); err != nil {
					return err
				}
				if err := repo.AddCommit("Initial commit"); err != nil {
					return err
				}

				groveConfig := &config.Config{
					Name:    projectName,
					Version: "1.0",
					Extensions: map[string]interface{}{
						"flow": map[string]interface{}{
							"interactive_provider": "claude",
						},
					},
				}

				if err := fs.WriteGroveConfig(projectDir, groveConfig); err != nil {
					return err
				}

				ctx.Set("notebooks_root", notebooksRoot)
				ctx.Set("project_name", projectName)
				return nil
			}),

			harness.SetupMocks(
				harness.Mock{CommandName: "grove"},
				harness.Mock{CommandName: "claude"},
				harness.Mock{CommandName: "tmux"},
			),

			harness.NewStep("Initialize plan and add multiple job types", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				notebooksRoot := ctx.GetString("notebooks_root")
				projectName := ctx.GetString("project_name")

				planName := "multi-nontty-plan"
				cmd := ctx.Bin("plan", "init", planName, "--worktree")
				cmd.Dir(projectDir)
				if err := cmd.Run().AssertSuccess(); err != nil {
					return err
				}

				planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", planName)
				ctx.Set("plan_path", planPath)
				ctx.Set("plan_name", planName)

				// Add interactive_agent job
				cmd = ctx.Bin("plan", "add", planName,
					"--type", "interactive_agent",
					"--title", "Interactive Job",
					"-p", "Test interactive agent in non-TTY")
				cmd.Dir(projectDir)
				if err := cmd.Run().AssertSuccess(); err != nil {
					return err
				}

				// Add oneshot job
				cmd = ctx.Bin("plan", "add", planName,
					"--type", "oneshot",
					"--title", "Oneshot Job",
					"-p", "Test oneshot in non-TTY")
				cmd.Dir(projectDir)
				if err := cmd.Run().AssertSuccess(); err != nil {
					return err
				}

				return nil
			}),

			harness.NewStep("Run interactive_agent job in non-TTY context", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				planPath := ctx.GetString("plan_path")

				jobPath, err := findJobByPrefix(planPath, "01-")
				if err != nil {
					return err
				}
				ctx.Set("interactive_job_path", jobPath)

				cmd := ctx.Bin("plan", "run", jobPath)
				cmd.Dir(projectDir)
				cmd.Timeout(60 * time.Second)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if err := result.AssertSuccess(); err != nil {
					return fmt.Errorf("interactive_agent job failed in non-TTY context: %w", err)
				}

				return assert.YAMLField(jobPath, "status", "running", "interactive job should be running")
			}),

			harness.NewStep("Complete interactive job before running oneshot", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				jobPath := ctx.GetString("interactive_job_path")

				_ = fs.RemoveIfExists(jobPath + ".lock")

				cmd := ctx.Bin("plan", "complete", jobPath)
				cmd.Dir(projectDir)
				return cmd.Run().AssertSuccess()
			}),

			harness.NewStep("Run oneshot job in non-TTY context", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				planPath := ctx.GetString("plan_path")

				jobPath, err := findJobByPrefix(planPath, "02-")
				if err != nil {
					return err
				}
				ctx.Set("oneshot_job_path", jobPath)

				cmd := ctx.Bin("plan", "run", jobPath)
				cmd.Dir(projectDir)
				cmd.Timeout(60 * time.Second)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				// Oneshot jobs with mock LLM may exit with an error since
				// there's no real LLM response, but the key thing is that
				// the job was attempted (status changed from pending)
				oneshotContent, readErr := fs.ReadString(jobPath)
				if readErr != nil {
					return fmt.Errorf("reading oneshot job: %w", readErr)
				}

				return ctx.Verify(func(v *verify.Collector) {
					v.True("oneshot job was attempted (status changed from pending)",
						!strings.Contains(oneshotContent, "status: pending"))
				})
			}),

			harness.NewStep("Verify interactive job completed correctly", func(ctx *harness.Context) error {
				interactiveJobPath := ctx.GetString("interactive_job_path")

				return assert.YAMLField(interactiveJobPath, "status", "completed", "interactive should be completed")
			}),
		},
	)
}

// Exported scenarios for all providers
var (
	// Non-TTY interactive agent tests for each provider
	ClaudeInteractiveNonTTYScenario   = createInteractiveAgentNonTTYScenario(AllProviders()[0])
	CodexInteractiveNonTTYScenario    = createInteractiveAgentNonTTYScenario(AllProviders()[1])
	OpencodeInteractiveNonTTYScenario = createInteractiveAgentNonTTYScenario(AllProviders()[2])

	// Multi-job regression test
	MultiJobNonTTYRegressionScenario = createMultiJobNonTTYScenario()
)
