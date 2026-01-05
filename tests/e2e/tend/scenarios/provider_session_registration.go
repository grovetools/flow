package scenarios

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-tend/pkg/verify"
)

// createProviderSessionRegistrationScenario generates a test that verifies
// synchronous session registration happens correctly for a specific provider.
func createProviderSessionRegistrationScenario(p ProviderConfig) *harness.Scenario {
	return harness.NewScenario(
		fmt.Sprintf("%s-session-registration", p.Name),
		fmt.Sprintf("Tests that %s provider creates session registry synchronously before launching agent", p.Name),
		[]string{"agent", "provider", p.Name, "session", "registration"},
		[]harness.Step{
			harness.NewStep(fmt.Sprintf("Setup environment with %s provider", p.Name), func(ctx *harness.Context) error {
				projectName := fmt.Sprintf("%s-session-reg-project", p.ProjectSuffix)
				projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, projectName)
				if err != nil {
					return err
				}

				repo, err := git.SetupTestRepo(projectDir)
				if err != nil {
					return err
				}
				if err := fs.WriteString(filepath.Join(projectDir, "README.md"), fmt.Sprintf("# %s Session Registration Test\n", p.Name)); err != nil {
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

				planName := fmt.Sprintf("%s-session-plan", providerName)
				cmd := ctx.Bin("plan", "init", planName, "--worktree")
				cmd.Dir(projectDir)
				if err := cmd.Run().AssertSuccess(); err != nil {
					return err
				}

				cmd = ctx.Bin("plan", "add", planName,
					"--type", "interactive_agent",
					"--title", fmt.Sprintf("%s Session Reg Test", providerName),
					"-p", "Test session registration")
				cmd.Dir(projectDir)
				if err := cmd.Run().AssertSuccess(); err != nil {
					return err
				}

				planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", planName)
				jobPath := filepath.Join(planPath, fmt.Sprintf("01-%s-session-reg-test.md", providerName))
				ctx.Set("plan_path", planPath)
				ctx.Set("job_path", jobPath)
				return nil
			}),

			harness.NewStep("Extract job ID from job file", func(ctx *harness.Context) error {
				jobPath := ctx.GetString("job_path")
				content, err := fs.ReadString(jobPath)
				if err != nil {
					return fmt.Errorf("reading job file: %w", err)
				}

				var jobID string
				for _, line := range strings.Split(content, "\n") {
					if strings.HasPrefix(line, "id: ") {
						jobID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
						break
					}
				}
				if jobID == "" {
					return fmt.Errorf("could not find job ID in job file")
				}
				ctx.Set("job_id", jobID)
				return nil
			}),

			harness.NewStep("Run job and verify session directory created synchronously", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				jobPath := ctx.GetString("job_path")
				jobID := ctx.GetString("job_id")
				homeDir := ctx.HomeDir()
				providerName := ctx.GetString("provider_name")

				// Run the job
				cmd := ctx.Bin("plan", "run", jobPath)
				cmd.Dir(projectDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				// Verify the session directory was created synchronously
				// This should exist immediately after the run command completes
				sessionDir := filepath.Join(homeDir, ".grove", "hooks", "sessions", jobID)

				// First verify the files exist
				if err := fs.AssertExists(sessionDir); err != nil {
					return fmt.Errorf("session directory should exist at %s: %w", sessionDir, err)
				}

				metadataPath := filepath.Join(sessionDir, "metadata.json")
				if err := fs.AssertExists(metadataPath); err != nil {
					return fmt.Errorf("metadata.json should exist: %w", err)
				}

				pidLockPath := filepath.Join(sessionDir, "pid.lock")
				if err := fs.AssertExists(pidLockPath); err != nil {
					return fmt.Errorf("pid.lock should exist: %w", err)
				}

				// Read and verify metadata content
				metadataContent, err := fs.ReadString(metadataPath)
				if err != nil {
					return fmt.Errorf("could not read metadata.json: %w", err)
				}

				var metadata map[string]interface{}
				if err := json.Unmarshal([]byte(metadataContent), &metadata); err != nil {
					return fmt.Errorf("could not parse metadata.json: %w", err)
				}

				return ctx.Verify(func(v *verify.Collector) {
					// Verify session_id matches job ID
					v.Equal("session_id matches job ID", jobID, metadata["session_id"])

					// Verify provider is set correctly
					v.Equal("provider is set correctly", providerName, metadata["provider"])

					// Verify type is interactive_agent
					v.Equal("type is interactive_agent", "interactive_agent", metadata["type"])
				})
			}),

			harness.NewStep("Complete job and cleanup", func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				jobPath := ctx.GetString("job_path")

				// Remove lock file if exists
				fs.RemoveIfExists(jobPath + ".lock")

				cmd := ctx.Bin("plan", "complete", jobPath)
				cmd.Dir(projectDir)
				return cmd.Run().AssertSuccess()
			}),
		},
	)
}

// Generated session registration scenarios for all providers.
var (
	// Claude session registration test
	ClaudeSessionRegistrationScenario = createProviderSessionRegistrationScenario(AllProviders()[0])

	// Codex session registration test
	CodexSessionRegistrationScenario = createProviderSessionRegistrationScenario(AllProviders()[1])

	// Opencode session registration test
	OpencodeSessionRegistrationScenario = createProviderSessionRegistrationScenario(AllProviders()[2])
)
