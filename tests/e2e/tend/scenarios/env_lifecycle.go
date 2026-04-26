package scenarios

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/env"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// createMockEnvBinary writes a shell script that mimics a grove-env-<name> exec
// plugin. It reads JSON from stdin and writes a fixed EnvResponse to stdout.
func createMockEnvBinary(dir, name string, response env.EnvResponse) (string, error) {
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", err
	}

	respBytes, err := json.Marshal(response)
	if err != nil {
		return "", err
	}

	binaryPath := filepath.Join(binDir, fmt.Sprintf("grove-env-%s", name))
	script := fmt.Sprintf("#!/bin/sh\ncat /dev/stdin > /dev/null\necho '%s'\n", string(respBytes))
	if err := os.WriteFile(binaryPath, []byte(script), 0755); err != nil { //nolint:gosec // test binary needs execute permission
		return "", err
	}

	return binDir, nil
}

// prependPath returns a PATH=... env var string with binDir prepended to the current PATH.
func prependPath(binDir string) string {
	return fmt.Sprintf("PATH=%s:%s", binDir, os.Getenv("PATH"))
}

// EnvLifecycleScenario tests the full environment provisioning lifecycle:
// 1. plan init with an exec-plugin environment provider → creates .env.local + .env_state.json
// 2. plan finish → tears down via provider and removes state files
var EnvLifecycleScenario = harness.NewScenario(
	"env-lifecycle",
	"Environment provisioning via exec plugin during plan init/finish",
	[]string{"env", "plan", "lifecycle"},
	[]harness.Step{
		harness.NewStep("Setup environment with exec plugin provider", func(ctx *harness.Context) error {
			homeDir := ctx.HomeDir()
			ctx.Set("home_dir", homeDir)

			codeDir := filepath.Join(homeDir, "code")
			if err := fs.CreateDir(codeDir); err != nil {
				return err
			}

			projectDir := filepath.Join(codeDir, "env-lifecycle-project")
			ctx.Set("project_dir", projectDir)
			if err := fs.CreateDir(projectDir); err != nil {
				return err
			}

			// Initialize git repo
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}

			// Write project config as TOML with environment provider.
			// Must use TOML because the YAML custom unmarshaler doesn't
			// include the Environment field in its rawConfig struct.
			projConfig := `name = "env-lifecycle-project"
version = "1.0"

[environment]
provider = "testplugin"

[environment.config]
region = "us-west-2"
`
			if err := fs.WriteString(filepath.Join(projectDir, "grove.toml"), projConfig); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Env Lifecycle Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit with env config"); err != nil {
				return err
			}

			// Setup global config with grove + notebook
			notebooksRoot := filepath.Join(homeDir, "notebooks")
			ctx.Set("notebooks_root", notebooksRoot)
			configDir := ctx.ConfigDir()
			groveConfigDir := filepath.Join(configDir, "grove")

			enabled := true
			globalCfg := &config.Config{
				Version: "1.0",
				Notebooks: &config.NotebooksConfig{
					Definitions: map[string]*config.Notebook{
						"default": {RootDir: notebooksRoot},
					},
					Rules: &config.NotebookRules{Default: "default"},
				},
				Groves: map[string]config.GroveSourceConfig{
					"code": {
						Path:     "~/code",
						Enabled:  &enabled,
						Notebook: "default",
					},
				},
			}
			if err := fs.WriteGroveConfig(groveConfigDir, globalCfg); err != nil {
				return err
			}

			// Create mock grove-env-testplugin binary
			mockResp := env.EnvResponse{
				Status:    "running",
				EnvVars:   map[string]string{"DATABASE_URL": "postgres://localhost:5432/test", "API_PORT": "8080"},
				Endpoints: []string{"http://localhost:8080"},
				State:     map[string]string{"container_id": "mock-abc123"},
			}
			binDir, err := createMockEnvBinary(homeDir, "testplugin", mockResp)
			if err != nil {
				return fmt.Errorf("failed to create mock env binary: %w", err)
			}
			ctx.Set("mock_bin_dir", binDir)

			return nil
		}),

		harness.NewStep("Plan init provisions environment", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			binDir := ctx.GetString("mock_bin_dir")

			cmd := ctx.Bin("plan", "init", "env-test-plan", "--worktree")
			cmd.Dir(projectDir)
			cmd.Env(prependPath(binDir))
			result := cmd.Run()
			ctx.ShowCommandOutput("flow plan init", result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			// Store combined output for verification
			ctx.Set("init_output", result.Stdout+result.Stderr)

			// Resolve plan path
			notebooksRoot := ctx.GetString("notebooks_root")
			planPath := filepath.Join(notebooksRoot, "workspaces", "env-lifecycle-project", "plans", "env-test-plan")
			ctx.Set("plan_path", planPath)

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "env-test-plan")
			ctx.Set("worktree_path", worktreePath)

			return nil
		}),

		harness.NewStep("Verify provisioning artifacts", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			worktreePath := ctx.GetString("worktree_path")

			envLocalPath := filepath.Join(worktreePath, ".env.local")
			envStatePath := filepath.Join(planPath, ".env_state.json")

			return ctx.Verify(func(v *verify.Collector) {
				// .env.local should exist in the worktree with the mock env vars
				v.Equal(".env.local exists", nil, fs.AssertExists(envLocalPath))
				v.Equal(".env.local has DATABASE_URL", nil, fs.AssertContains(envLocalPath, "DATABASE_URL"))
				v.Equal(".env.local has API_PORT", nil, fs.AssertContains(envLocalPath, "API_PORT"))

				// .env_state.json should exist in the plan dir
				v.Equal(".env_state.json exists", nil, fs.AssertExists(envStatePath))
				v.Equal(".env_state.json has provider", nil, fs.AssertContains(envStatePath, "testplugin"))
				v.Equal(".env_state.json has container_id", nil, fs.AssertContains(envStatePath, "mock-abc123"))
			})
		}),

		harness.NewStep("Verify .env_state.json structure", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			envStatePath := filepath.Join(planPath, ".env_state.json")

			data, err := os.ReadFile(envStatePath)
			if err != nil {
				return fmt.Errorf("failed to read .env_state.json: %w", err)
			}

			var stateFile env.EnvStateFile
			if err := json.Unmarshal(data, &stateFile); err != nil {
				return fmt.Errorf("failed to parse .env_state.json: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("provider is testplugin", "testplugin", stateFile.Provider)
				v.Equal("state has container_id", "mock-abc123", stateFile.State["container_id"])
			})
		}),

		// Mock git for the finish step since it tries to do worktree/branch cleanup
		harness.SetupMocks(harness.Mock{CommandName: "git"}),

		harness.NewStep("Plan finish tears down environment", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			binDir := ctx.GetString("mock_bin_dir")

			// Overwrite mock binary with a "down" response
			downResp := env.EnvResponse{Status: "stopped"}
			if _, err := createMockEnvBinary(ctx.HomeDir(), "testplugin", downResp); err != nil {
				return fmt.Errorf("failed to update mock env binary for down: %w", err)
			}

			// Set plan to review status first (required for finish)
			reviewCmd := ctx.Bin("plan", "review", "env-test-plan")
			reviewCmd.Dir(projectDir)
			reviewCmd.Env(prependPath(binDir))
			reviewResult := reviewCmd.Run()
			ctx.ShowCommandOutput("flow plan review", reviewResult.Stdout, reviewResult.Stderr)
			if err := reviewResult.AssertSuccess(); err != nil {
				return fmt.Errorf("plan review failed: %w", err)
			}

			cmd := ctx.Bin("plan", "finish", "env-test-plan", "--yes", "--force")
			cmd.Dir(projectDir)
			cmd.Env(prependPath(binDir))
			result := cmd.Run()
			ctx.ShowCommandOutput("flow plan finish", result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan finish failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			ctx.Set("finish_output", result.Stdout+result.Stderr)
			return nil
		}),

		harness.NewStep("Verify teardown cleaned up state files", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			envStatePath := filepath.Join(planPath, ".env_state.json")

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal(".env_state.json removed after teardown", nil, fs.AssertNotExists(envStatePath))
			})
		}),
	},
)

// EnvNoConfigScenario verifies that plan init works normally when no environment
// provider is configured — no .env.local or .env_state.json should be created.
var EnvNoConfigScenario = harness.NewScenario(
	"env-no-config",
	"Plan init without environment config does not create env artifacts",
	[]string{"env", "plan"},
	[]harness.Step{
		harness.NewStep("Setup environment without env config", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "env-noconfig-project")
			if err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# No Env Config Test\n"); err != nil {
				return err
			}
			return repo.AddCommit("Initial commit")
		}),

		harness.NewStep("Plan init without environment config", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			cmd := ctx.Bin("plan", "init", "no-env-plan", "--worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput("flow plan init", result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			notebooksRoot := ctx.GetString("notebooks_root")
			planPath := filepath.Join(notebooksRoot, "workspaces", "env-noconfig-project", "plans", "no-env-plan")
			ctx.Set("plan_path", planPath)

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "no-env-plan")
			ctx.Set("worktree_path", worktreePath)

			return nil
		}),

		harness.NewStep("Verify no env artifacts created", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			worktreePath := ctx.GetString("worktree_path")

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("no .env.local created", nil, fs.AssertNotExists(filepath.Join(worktreePath, ".env.local")))
				v.Equal("no .env_state.json created", nil, fs.AssertNotExists(filepath.Join(planPath, ".env_state.json")))
			})
		}),
	},
)
