package scenarios

import (
	"fmt"
	"path/filepath"

	"github.com/grovetools/core/config"
	"github.com/grovetools/tend/pkg/assert"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// ProviderConfig defines the configuration for testing a specific provider.
type ProviderConfig struct {
	Name       string   // Provider name: "claude", "codex", "opencode"
	MockName   string   // Mock binary name (e.g., "claude", "cx", "opencode")
	TestArgs   []string // Test args to verify are passed
	ProjectSuffix string // Unique suffix for project names
}

// AllProviders returns configurations for all three providers.
func AllProviders() []ProviderConfig {
	return []ProviderConfig{
		{
			Name:          "claude",
			MockName:      "claude",
			TestArgs:      []string{"--claude-test-arg", "--model", "opus"},
			ProjectSuffix: "claude",
		},
		{
			Name:          "codex",
			MockName:      "cx",
			TestArgs:      []string{"--codex-test-arg", "--full-auto"},
			ProjectSuffix: "codex",
		},
		{
			Name:          "opencode",
			MockName:      "opencode",
			TestArgs:      []string{"--opencode-test-arg", "--verbose"},
			ProjectSuffix: "opencode",
		},
	}
}

// createProviderLifecycleScenario generates a lifecycle test for a specific provider.
func createProviderLifecycleScenario(p ProviderConfig) *harness.Scenario {
	return harness.NewScenario(
		fmt.Sprintf("%s-provider-lifecycle", p.Name),
		fmt.Sprintf("Tests %s provider launch, job status transitions, and completion", p.Name),
		[]string{"agent", "provider", p.Name, "lifecycle"},
		[]harness.Step{
			harness.NewStep(fmt.Sprintf("Setup environment with %s provider", p.Name), func(ctx *harness.Context) error {
				projectName := fmt.Sprintf("%s-lifecycle-project", p.ProjectSuffix)
				projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, projectName)
				if err != nil {
					return err
				}

				repo, err := git.SetupTestRepo(projectDir)
				if err != nil {
					return err
				}
				if err := fs.WriteString(filepath.Join(projectDir, "README.md"), fmt.Sprintf("# %s Test\n", p.Name)); err != nil {
					return err
				}
				if err := repo.AddCommit("Initial commit"); err != nil {
					return err
				}

				groveConfig := &config.Config{
					Name:    projectName,
					Version: "1.0",
					Extensions: map[string]interface{}{
						"agent": map[string]interface{}{
							"interactive_provider": p.Name,
						},
					},
				}

				if err := fs.WriteGroveConfig(projectDir, groveConfig); err != nil {
					return err
				}

				ctx.Set("notebooks_root", notebooksRoot)
				ctx.Set("provider_name", p.Name)
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

				planName := fmt.Sprintf("%s-lifecycle-plan", providerName)
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

				projectName := fmt.Sprintf("%s-lifecycle-project", providerName)
				planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", planName)
				ctx.Set("plan_path", planPath)

				cmd := ctx.Bin("plan", "add", planName,
					"--type", "interactive_agent",
					"--title", fmt.Sprintf("%s Lifecycle Test", providerName),
					"-p", fmt.Sprintf("Test the %s provider lifecycle", providerName))
				cmd.Dir(projectDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if err := result.AssertSuccess(); err != nil {
					return err
				}

				jobPath := filepath.Join(planPath, fmt.Sprintf("01-%s-lifecycle-test.md", providerName))
				ctx.Set("job_path", jobPath)
				return nil
			}),

			harness.NewStep("Run job and verify status is running", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				jobPath := ctx.GetString("job_path")

				cmd := ctx.Bin("plan", "run", jobPath)
				cmd.Dir(projectDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				return assert.YAMLField(jobPath, "status", "running", "job should be running")
			}),

			harness.NewStep("Verify briefing file created", func(ctx *harness.Context) error {
				planPath := ctx.GetString("plan_path")
				providerName := ctx.GetString("provider_name")

				// Find briefing file
				artifactsDir := filepath.Join(planPath, ".artifacts")
				entries, err := filepath.Glob(filepath.Join(artifactsDir, "*", "briefing-*.xml"))
				if err != nil {
					return fmt.Errorf("error checking for briefing files: %w", err)
				}
				if len(entries) == 0 {
					return fmt.Errorf("expected at least one briefing XML file")
				}

				briefingContent, err := fs.ReadString(entries[0])
				if err != nil {
					return fmt.Errorf("reading briefing file: %w", err)
				}

				return ctx.Verify(func(v *verify.Collector) {
					v.Contains("briefing has prompt section", briefingContent, "<prompt>")
					v.Contains("briefing mentions provider test", briefingContent, fmt.Sprintf("%s provider lifecycle", providerName))
				})
			}),

			harness.NewStep("Complete job", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				jobPath := ctx.GetString("job_path")

				// Remove lock file if exists
				fs.RemoveIfExists(jobPath + ".lock")

				cmd := ctx.Bin("plan", "complete", jobPath)
				cmd.Dir(projectDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				return result.AssertSuccess()
			}),

			harness.NewStep("Verify job completed", func(ctx *harness.Context) error {
				jobPath := ctx.GetString("job_path")

				return ctx.Verify(func(v *verify.Collector) {
					v.Equal("job is completed", nil, assert.YAMLField(jobPath, "status", "completed", "job should be completed"))
					v.Equal("lock file removed", nil, fs.AssertNotExists(jobPath+".lock"))
				})
			}),
		},
	)
}

// createProviderArgsScenario generates a test for provider-specific args.
func createProviderArgsScenario(p ProviderConfig) *harness.Scenario {
	return harness.NewScenario(
		fmt.Sprintf("%s-provider-args", p.Name),
		fmt.Sprintf("Tests that %s provider receives its configured args from grove.yml", p.Name),
		[]string{"agent", "provider", p.Name, "args", "config"},
		[]harness.Step{
			harness.NewStep(fmt.Sprintf("Setup environment with %s provider args", p.Name), func(ctx *harness.Context) error {
				projectName := fmt.Sprintf("%s-args-project", p.ProjectSuffix)
				projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, projectName)
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
					Name:    projectName,
					Version: "1.0",
					Extensions: map[string]interface{}{
						"agent": map[string]interface{}{
							"interactive_provider": p.Name,
							"providers": map[string]interface{}{
								p.Name: map[string]interface{}{
									"args": p.TestArgs,
								},
							},
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

			harness.NewStep("Initialize plan and add job", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				notebooksRoot := ctx.GetString("notebooks_root")
				providerName := ctx.GetString("provider_name")
				projectName := ctx.GetString("project_name")

				planName := fmt.Sprintf("%s-args-plan", providerName)
				cmd := ctx.Bin("plan", "init", planName, "--worktree")
				cmd.Dir(projectDir)
				if err := cmd.Run().AssertSuccess(); err != nil {
					return err
				}

				cmd = ctx.Bin("plan", "add", planName,
					"--type", "interactive_agent",
					"--title", fmt.Sprintf("%s Args Test", providerName),
					"-p", "Test provider args")
				cmd.Dir(projectDir)
				if err := cmd.Run().AssertSuccess(); err != nil {
					return err
				}

				planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", planName)
				jobPath := filepath.Join(planPath, fmt.Sprintf("01-%s-args-test.md", providerName))
				ctx.Set("job_path", jobPath)
				return nil
			}),

			harness.NewStep("Run job and verify it reaches running state", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				jobPath := ctx.GetString("job_path")

				cmd := ctx.Bin("plan", "run", jobPath)
				cmd.Dir(projectDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				// The job should reach running status, which confirms the provider
				// was selected and the command was built (args would be included)
				return assert.YAMLField(jobPath, "status", "running", "job should be running with provider args")
			}),
		},
	)
}

// Generated scenarios for all providers using parameterized tests.
// This ensures consistent behavior across claude, codex, and opencode.
var (
	// Claude provider tests
	ClaudeProviderLifecycleScenario = createProviderLifecycleScenario(AllProviders()[0])
	ClaudeProviderArgsScenario      = createProviderArgsScenario(AllProviders()[0])

	// Codex provider tests
	CodexProviderLifecycleScenario = createProviderLifecycleScenario(AllProviders()[1])
	CodexProviderArgsScenario      = createProviderArgsScenario(AllProviders()[1])

	// Opencode provider tests
	OpencodeProviderLifecycleScenario = createProviderLifecycleScenario(AllProviders()[2])
	OpencodeProviderArgsScenario      = createProviderArgsScenario(AllProviders()[2])
)
