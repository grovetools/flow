package scenarios

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/core/config"
	"github.com/grovetools/core/pkg/env"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// EnvNamedProfileScenario tests that --env flag selects the correct provider.
var EnvNamedProfileScenario = harness.NewScenario(
	"env-named-profile",
	"Named environment profile selection via --env flag",
	[]string{"env", "plan", "profile"},
	[]harness.Step{
		harness.NewStep("Setup two env profiles with different providers", func(ctx *harness.Context) error {
			homeDir := ctx.HomeDir()
			ctx.Set("home_dir", homeDir)

			codeDir := filepath.Join(homeDir, "code")
			if err := fs.CreateDir(codeDir); err != nil {
				return err
			}

			projectDir := filepath.Join(codeDir, "env-profile-project")
			ctx.Set("project_dir", projectDir)
			if err := fs.CreateDir(projectDir); err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}

			// TOML config with default provider and a named "alt" profile
			projConfig := `name = "env-profile-project"
version = "1.0"

[environment]
provider = "testplugin-a"

[environment.config]
region = "us-west-2"

[environments.alt]
provider = "testplugin-b"

[environments.alt.config]
region = "eu-west-1"
`
			if err := fs.WriteString(filepath.Join(projectDir, "grove.toml"), projConfig); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Env Profile Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit with env profiles"); err != nil {
				return err
			}

			// Global config
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
					"code": {Path: "~/code", Enabled: &enabled, Notebook: "default"},
				},
			}
			if err := fs.WriteGroveConfig(groveConfigDir, globalCfg); err != nil {
				return err
			}

			// Create mock binaries for both providers
			respA := env.EnvResponse{
				Status:  "running",
				EnvVars: map[string]string{"PROVIDER": "plugin-a", "REGION": "us-west-2"},
				State:   map[string]string{"instance": "a-123"},
			}
			binDirA, err := createMockEnvBinary(homeDir, "testplugin-a", respA)
			if err != nil {
				return fmt.Errorf("failed to create mock env binary A: %w", err)
			}

			respB := env.EnvResponse{
				Status:  "running",
				EnvVars: map[string]string{"PROVIDER": "plugin-b", "REGION": "eu-west-1"},
				State:   map[string]string{"instance": "b-456"},
			}
			if _, err := createMockEnvBinary(homeDir, "testplugin-b", respB); err != nil {
				return fmt.Errorf("failed to create mock env binary B: %w", err)
			}

			ctx.Set("mock_bin_dir", binDirA) // Both binaries are in the same dir
			return nil
		}),

		harness.NewStep("Plan init without --env uses default provider", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			binDir := ctx.GetString("mock_bin_dir")

			cmd := ctx.Bin("plan", "init", "plan-default", "--worktree")
			cmd.Dir(projectDir)
			cmd.Env(prependPath(binDir))
			result := cmd.Run()
			ctx.ShowCommandOutput("flow plan init (default)", result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			notebooksRoot := ctx.GetString("notebooks_root")
			planPath := filepath.Join(notebooksRoot, "workspaces", "env-profile-project", "plans", "plan-default")
			ctx.Set("plan_path_default", planPath)

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "plan-default")
			ctx.Set("worktree_path_default", worktreePath)
			return nil
		}),

		harness.NewStep("Verify default plan uses plugin A", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path_default")
			worktreePath := ctx.GetString("worktree_path_default")

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal(".env.local has PROVIDER=plugin-a", nil,
					fs.AssertContains(filepath.Join(worktreePath, ".env.local"), "PROVIDER=plugin-a"))
				v.Equal(".env_state.json has testplugin-a", nil,
					fs.AssertContains(filepath.Join(planPath, ".env_state.json"), "testplugin-a"))
			})
		}),

		harness.NewStep("Plan init with --env alt uses provider B", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			binDir := ctx.GetString("mock_bin_dir")

			cmd := ctx.Bin("plan", "init", "plan-alt", "--worktree", "--env", "alt")
			cmd.Dir(projectDir)
			cmd.Env(prependPath(binDir))
			result := cmd.Run()
			ctx.ShowCommandOutput("flow plan init --env alt", result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init with --env alt failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			notebooksRoot := ctx.GetString("notebooks_root")
			planPath := filepath.Join(notebooksRoot, "workspaces", "env-profile-project", "plans", "plan-alt")
			ctx.Set("plan_path_alt", planPath)

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "plan-alt")
			ctx.Set("worktree_path_alt", worktreePath)
			return nil
		}),

		harness.NewStep("Verify alt plan uses plugin B and records environment", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path_alt")
			worktreePath := ctx.GetString("worktree_path_alt")

			envStatePath := filepath.Join(planPath, ".env_state.json")

			return ctx.Verify(func(v *verify.Collector) {
				// .env.local should have plugin B vars
				v.Equal(".env.local has PROVIDER=plugin-b", nil,
					fs.AssertContains(filepath.Join(worktreePath, ".env.local"), "PROVIDER=plugin-b"))

				// .env_state.json should have provider=testplugin-b and environment=alt
				v.Equal(".env_state.json has testplugin-b", nil,
					fs.AssertContains(envStatePath, "testplugin-b"))
				v.Equal(".env_state.json has environment=alt", nil,
					fs.AssertContains(envStatePath, `"environment": "alt"`))
			})
		}),

		harness.NewStep("Verify .env_state.json structure for alt profile", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path_alt")
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
				v.Equal("provider is testplugin-b", "testplugin-b", stateFile.Provider)
				v.Equal("environment is alt", "alt", stateFile.Environment)
				v.Equal("state has instance", "b-456", stateFile.State["instance"])
			})
		}),
	},
)

// EnvInvalidProfileScenario tests that --env with a nonexistent profile fails with a clear error.
var EnvInvalidProfileScenario = harness.NewScenario(
	"env-invalid-profile",
	"--env with nonexistent profile produces clear error",
	[]string{"env", "plan", "error"},
	[]harness.Step{
		harness.NewStep("Setup project with only default env", func(ctx *harness.Context) error {
			homeDir := ctx.HomeDir()
			ctx.Set("home_dir", homeDir)

			codeDir := filepath.Join(homeDir, "code")
			if err := fs.CreateDir(codeDir); err != nil {
				return err
			}

			projectDir := filepath.Join(codeDir, "env-invalid-project")
			ctx.Set("project_dir", projectDir)
			if err := fs.CreateDir(projectDir); err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}

			projConfig := `name = "env-invalid-project"
version = "1.0"

[environment]
provider = "testplugin"
`
			if err := fs.WriteString(filepath.Join(projectDir, "grove.toml"), projConfig); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Env Invalid Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			// Global config
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
					"code": {Path: "~/code", Enabled: &enabled, Notebook: "default"},
				},
			}
			if err := fs.WriteGroveConfig(groveConfigDir, globalCfg); err != nil {
				return err
			}

			// Create mock binary so it doesn't fail on binary lookup
			mockResp := env.EnvResponse{Status: "running"}
			binDir, err := createMockEnvBinary(homeDir, "testplugin", mockResp)
			if err != nil {
				return fmt.Errorf("failed to create mock env binary: %w", err)
			}
			ctx.Set("mock_bin_dir", binDir)

			return nil
		}),

		harness.NewStep("Plan init with --env nonexistent shows error in output", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			binDir := ctx.GetString("mock_bin_dir")

			cmd := ctx.Bin("plan", "init", "plan-bad-env", "--worktree", "--env", "nonexistent")
			cmd.Dir(projectDir)
			cmd.Env(prependPath(binDir))
			result := cmd.Run()
			ctx.ShowCommandOutput("flow plan init --env nonexistent", result.Stdout, result.Stderr)

			// The plan init itself should succeed (environment provisioning is non-fatal),
			// but the output should contain an error about the profile not being found
			combinedOutput := result.Stdout + result.Stderr
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("output mentions profile not found", true,
					strings.Contains(combinedOutput, "not found"))
			})
		}),
	},
)

// EnvStickyDefaultScenario tests that state.Set("environment", ...) is used as fallback
// when --env flag is not provided.
var EnvStickyDefaultScenario = harness.NewScenario(
	"env-sticky-default",
	"Sticky default environment via .grove/state.yml",
	[]string{"env", "plan", "state"},
	[]harness.Step{
		harness.NewStep("Setup project with default and alt profiles", func(ctx *harness.Context) error {
			homeDir := ctx.HomeDir()
			ctx.Set("home_dir", homeDir)

			codeDir := filepath.Join(homeDir, "code")
			if err := fs.CreateDir(codeDir); err != nil {
				return err
			}

			projectDir := filepath.Join(codeDir, "env-sticky-project")
			ctx.Set("project_dir", projectDir)
			if err := fs.CreateDir(projectDir); err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}

			projConfig := `name = "env-sticky-project"
version = "1.0"

[environment]
provider = "testplugin-a"

[environments.docker]
provider = "testplugin-b"
`
			if err := fs.WriteString(filepath.Join(projectDir, "grove.toml"), projConfig); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Env Sticky Test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			// Global config
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
					"code": {Path: "~/code", Enabled: &enabled, Notebook: "default"},
				},
			}
			if err := fs.WriteGroveConfig(groveConfigDir, globalCfg); err != nil {
				return err
			}

			// Create mock binaries
			respA := env.EnvResponse{
				Status:  "running",
				EnvVars: map[string]string{"PROVIDER": "plugin-a"},
				State:   map[string]string{},
			}
			binDir, err := createMockEnvBinary(homeDir, "testplugin-a", respA)
			if err != nil {
				return fmt.Errorf("failed to create mock env binary A: %w", err)
			}

			respB := env.EnvResponse{
				Status:  "running",
				EnvVars: map[string]string{"PROVIDER": "plugin-b"},
				State:   map[string]string{},
			}
			if _, err := createMockEnvBinary(homeDir, "testplugin-b", respB); err != nil {
				return fmt.Errorf("failed to create mock env binary B: %w", err)
			}
			ctx.Set("mock_bin_dir", binDir)

			// Write sticky default: .grove/state.yml with environment: docker
			groveDir := filepath.Join(projectDir, ".grove")
			if err := fs.CreateDir(groveDir); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(groveDir, "state.yml"), "environment: docker\n"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Plan init without --env uses sticky default (docker/plugin-b)", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			binDir := ctx.GetString("mock_bin_dir")

			cmd := ctx.Bin("plan", "init", "plan-sticky", "--worktree")
			cmd.Dir(projectDir)
			cmd.Env(prependPath(binDir))
			result := cmd.Run()
			ctx.ShowCommandOutput("flow plan init (sticky default)", result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			worktreePath := filepath.Join(projectDir, ".grove-worktrees", "plan-sticky")
			ctx.Set("worktree_path_sticky", worktreePath)
			return nil
		}),

		harness.NewStep("Verify sticky default used plugin B", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path_sticky")

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal(".env.local has PROVIDER=plugin-b from sticky default", nil,
					fs.AssertContains(filepath.Join(worktreePath, ".env.local"), "PROVIDER=plugin-b"))
			})
		}),
	},
)
