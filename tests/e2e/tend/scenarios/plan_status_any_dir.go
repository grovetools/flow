package scenarios

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// PlanStatusDirFlagScenario verifies that the --dir flag allows resolving a plan
// from a workspace other than the current working directory.
var PlanStatusDirFlagScenario = harness.NewScenario(
	"flow-plan-status-dir-flag",
	"Verifies that flow plan status --dir flag resolves a plan relative to a specified workspace.",
	[]string{"flow", "plan", "status", "dir-flag"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "dir-flag-project")
			if err != nil {
				return err
			}
			ctx.Set("project_dir", projectDir)
			ctx.Set("notebooks_root", notebooksRoot)
			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
		),

		harness.NewStep("Initialize plan and create a job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			// Create plan directory in the centralized notebook location
			planPath := filepath.Join(notebooksRoot, "workspaces", "dir-flag-project", "plans", "dir-target-plan")
			if err := fs.CreateDir(planPath); err != nil {
				return err
			}

			// Write a minimal plan config
			if err := fs.WriteString(filepath.Join(planPath, ".grove-plan.yml"), "# Test plan\n"); err != nil {
				return err
			}

			// Create a job file
			jobContent := "---\nid: job1\ntitle: Test Job\nstatus: pending\ntype: oneshot\n---\nTest job body\n"
			if err := fs.WriteString(filepath.Join(planPath, "01-test.md"), jobContent); err != nil {
				return err
			}

			ctx.Set("plan_path", planPath)
			ctx.Set("project_dir", projectDir)
			return nil
		}),

		harness.NewStep("Run plan status with --dir flag from outside directory", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Create a directory that is NOT a workspace
			outsideDir := ctx.NewDir("outside-dir")

			// Run the command from outside the workspace, using --dir to point at it
			cmd := ctx.Bin("plan", "status", "dir-target-plan", "--dir", projectDir, "--json")
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("--dir flag resolution failed: %w", err)
			}

			// Parse JSON to verify the plan was resolved correctly
			var jsonResult map[string]interface{}
			if err := json.Unmarshal([]byte(result.Stdout), &jsonResult); err != nil {
				return fmt.Errorf("failed to parse JSON output: %w\nOutput: %s", err, result.Stdout)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("JSON contains plan key", true, jsonResult["plan"] != nil)
				v.Contains("plan name in output", result.Stdout, "dir-target-plan")
			})
		}),
	},
)

// PlanStatusGlobalResolutionScenario verifies that a plan can be found globally
// from a non-workspace directory when it exists in exactly one known workspace.
var PlanStatusGlobalResolutionScenario = harness.NewScenario(
	"flow-plan-status-global-resolution",
	"Verifies that flow plan status can find a uniquely named plan from any directory globally.",
	[]string{"flow", "plan", "status", "global"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "global-project")
			if err != nil {
				return err
			}
			ctx.Set("project_dir", projectDir)
			ctx.Set("notebooks_root", notebooksRoot)
			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
		),

		harness.NewStep("Create a plan in the workspace", func(ctx *harness.Context) error {
			notebooksRoot := ctx.GetString("notebooks_root")

			// Create plan directory in centralized notebook
			planPath := filepath.Join(notebooksRoot, "workspaces", "global-project", "plans", "unique-global-plan")
			if err := fs.CreateDir(planPath); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(planPath, ".grove-plan.yml"), "# Test plan\n"); err != nil {
				return err
			}

			// Add a job file
			jobContent := "---\nid: job1\ntitle: Global Test Job\nstatus: pending\ntype: oneshot\n---\nGlobal test\n"
			if err := fs.WriteString(filepath.Join(planPath, "01-test.md"), jobContent); err != nil {
				return err
			}

			ctx.Set("plan_path", planPath)
			return nil
		}),

		harness.NewStep("Run plan status from a non-workspace directory", func(ctx *harness.Context) error {
			// Create a directory that is completely outside any workspace
			outsideDir := ctx.NewDir("nowhere")

			cmd := ctx.Bin("plan", "status", "unique-global-plan", "--json")
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("global resolution failed: %w", err)
			}

			// Parse JSON to verify
			var jsonResult map[string]interface{}
			if err := json.Unmarshal([]byte(result.Stdout), &jsonResult); err != nil {
				return fmt.Errorf("failed to parse JSON output: %w\nOutput: %s", err, result.Stdout)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("JSON has plan key", true, jsonResult["plan"] != nil)
				v.Contains("plan name in output", result.Stdout, "unique-global-plan")
			})
		}),
	},
)

// PlanStatusNotFoundErrorScenario verifies that a clear error message is shown
// when a plan cannot be found anywhere.
var PlanStatusNotFoundErrorScenario = harness.NewScenario(
	"flow-plan-status-not-found-error",
	"Verifies correct error message when a plan is not found locally or globally.",
	[]string{"flow", "plan", "status", "errors"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, _, err := setupDefaultEnvironment(ctx, "error-project")
			return err
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
		),

		harness.NewStep("Run plan status for nonexistent plan", func(ctx *harness.Context) error {
			outsideDir := ctx.NewDir("empty-dir")

			cmd := ctx.Bin("plan", "status", "nonexistent-plan-xyz", "--json")
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// The command should fail
			if err := result.AssertFailure(); err != nil {
				return fmt.Errorf("expected command to fail for nonexistent plan: %w", err)
			}

			// Check for the user-friendly error message
			combinedOutput := result.Stdout + result.Stderr
			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("error mentions plan not found", combinedOutput, "not found")
				v.Contains("error contains hint about --dir", combinedOutput, "--dir")
			})
		}),
	},
)

// PlanStatusAmbiguousErrorScenario verifies that an error is shown when a plan
// slug exists in multiple workspaces and cannot be uniquely resolved.
var PlanStatusAmbiguousErrorScenario = harness.NewScenario(
	"flow-plan-status-ambiguous-error",
	"Verifies correct error message when a plan slug exists in multiple workspaces.",
	[]string{"flow", "plan", "status", "errors", "ambiguous"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with two workspaces", func(ctx *harness.Context) error {
			homeDir := ctx.HomeDir()
			codeDir := filepath.Join(homeDir, "code")
			if err := fs.CreateDir(codeDir); err != nil {
				return err
			}

			notebooksRoot := filepath.Join(homeDir, "notebooks")
			ctx.Set("notebooks_root", notebooksRoot)

			// Create TWO projects that will both have a plan with the same slug
			for _, projectName := range []string{"project-alpha", "project-beta"} {
				projectDir := filepath.Join(codeDir, projectName)
				if err := fs.CreateDir(projectDir); err != nil {
					return err
				}
				if _, err := setupGitRepo(projectDir); err != nil {
					return err
				}
				if err := fs.WriteString(filepath.Join(projectDir, "grove.yml"), fmt.Sprintf("name: %s\nversion: \"1.0\"\n", projectName)); err != nil {
					return err
				}

				// Create the same plan slug in both workspaces
				planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", "ambiguous-plan")
				if err := fs.CreateDir(planPath); err != nil {
					return err
				}
				if err := fs.WriteString(filepath.Join(planPath, ".grove-plan.yml"), "# Test plan\n"); err != nil {
					return err
				}
				jobContent := "---\nid: job1\ntitle: Test Job\nstatus: pending\ntype: oneshot\n---\nTest\n"
				if err := fs.WriteString(filepath.Join(planPath, "01-test.md"), jobContent); err != nil {
					return err
				}
			}

			// Write global config with notebooks and groves
			configDir := ctx.ConfigDir()
			groveConfigDir := filepath.Join(configDir, "grove")
			globalConfig := fmt.Sprintf(`version: "1.0"
notebooks:
  definitions:
    default:
      root_dir: %s
  rules:
    default: default
groves:
  code:
    path: %s
    enabled: true
    notebook: default
`, notebooksRoot, codeDir)
			if err := fs.WriteString(filepath.Join(groveConfigDir, "grove.yml"), globalConfig); err != nil {
				return err
			}

			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
		),

		harness.NewStep("Run plan status for ambiguous plan slug", func(ctx *harness.Context) error {
			outsideDir := ctx.NewDir("outside")

			cmd := ctx.Bin("plan", "status", "ambiguous-plan", "--json")
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Should fail due to ambiguity
			if err := result.AssertFailure(); err != nil {
				return fmt.Errorf("expected command to fail for ambiguous plan: %w", err)
			}

			combinedOutput := result.Stdout + result.Stderr
			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("error mentions multiple plans", combinedOutput, "multiple plans found")
				v.Contains("error suggests --dir", combinedOutput, "--dir")
			})
		}),
	},
)

// PlanStatusBackwardCompatibilityScenario verifies that existing behavior is
// unchanged when running from within a workspace directory (no --dir flag).
var PlanStatusBackwardCompatibilityScenario = harness.NewScenario(
	"flow-plan-status-backward-compat",
	"Verifies that existing plan status behavior is unchanged when running from within a workspace.",
	[]string{"flow", "plan", "status", "backward-compat"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "compat-project")
			if err != nil {
				return err
			}
			ctx.Set("project_dir", projectDir)
			ctx.Set("notebooks_root", notebooksRoot)
			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
		),

		harness.NewStep("Create plan and run status from workspace directory", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			// Create plan
			planPath := filepath.Join(notebooksRoot, "workspaces", "compat-project", "plans", "compat-plan")
			if err := fs.CreateDir(planPath); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(planPath, ".grove-plan.yml"), "# Test plan\n"); err != nil {
				return err
			}
			jobContent := "---\nid: job1\ntitle: Compat Job\nstatus: completed\ntype: oneshot\n---\nTest\n"
			if err := fs.WriteString(filepath.Join(planPath, "01-test.md"), jobContent); err != nil {
				return err
			}

			// Run from WITHIN the workspace directory (classic behavior, no --dir)
			cmd := ctx.Bin("plan", "status", "compat-plan", "--json")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("backward compatible status failed: %w", err)
			}

			// Parse and verify JSON
			var jsonResult map[string]interface{}
			if err := json.Unmarshal([]byte(result.Stdout), &jsonResult); err != nil {
				return fmt.Errorf("failed to parse JSON: %w\nOutput: %s", err, result.Stdout)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("JSON has plan key", true, jsonResult["plan"] != nil)
				v.Contains("plan name in output", result.Stdout, "compat-plan")
				v.Contains("job status present", result.Stdout, "completed")
			})
		}),
	},
)

// PlanStatusDirFlagOverrideScenario verifies that --dir takes precedence
// over the current working directory for workspace resolution.
var PlanStatusDirFlagOverrideScenario = harness.NewScenario(
	"flow-plan-status-dir-overrides-cwd",
	"Verifies that --dir flag overrides CWD for workspace context, even when CWD is a different workspace.",
	[]string{"flow", "plan", "status", "dir-flag", "override"},
	[]harness.Step{
		harness.NewStep("Setup two workspaces with different plans", func(ctx *harness.Context) error {
			homeDir := ctx.HomeDir()
			codeDir := filepath.Join(homeDir, "code")
			if err := fs.CreateDir(codeDir); err != nil {
				return err
			}

			notebooksRoot := filepath.Join(homeDir, "notebooks")
			ctx.Set("notebooks_root", notebooksRoot)

			// Create project-a (will be the CWD)
			projectA := filepath.Join(codeDir, "project-a")
			if err := fs.CreateDir(projectA); err != nil {
				return err
			}
			if _, err := setupGitRepo(projectA); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectA, "grove.yml"), "name: project-a\nversion: \"1.0\"\n"); err != nil {
				return err
			}
			ctx.Set("project_a_dir", projectA)

			// Create project-b (will be the --dir target)
			projectB := filepath.Join(codeDir, "project-b")
			if err := fs.CreateDir(projectB); err != nil {
				return err
			}
			if _, err := setupGitRepo(projectB); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectB, "grove.yml"), "name: project-b\nversion: \"1.0\"\n"); err != nil {
				return err
			}
			ctx.Set("project_b_dir", projectB)

			// Create a plan ONLY in project-b
			planPath := filepath.Join(notebooksRoot, "workspaces", "project-b", "plans", "b-only-plan")
			if err := fs.CreateDir(planPath); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(planPath, ".grove-plan.yml"), "# Test plan\n"); err != nil {
				return err
			}
			jobContent := "---\nid: job1\ntitle: B Only Job\nstatus: pending\ntype: oneshot\n---\nTest\n"
			if err := fs.WriteString(filepath.Join(planPath, "01-test.md"), jobContent); err != nil {
				return err
			}

			// Write global config
			configDir := ctx.ConfigDir()
			groveConfigDir := filepath.Join(configDir, "grove")
			globalConfig := fmt.Sprintf(`version: "1.0"
notebooks:
  definitions:
    default:
      root_dir: %s
  rules:
    default: default
groves:
  code:
    path: %s
    enabled: true
    notebook: default
`, notebooksRoot, codeDir)
			return fs.WriteString(filepath.Join(groveConfigDir, "grove.yml"), globalConfig)
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
		),

		harness.NewStep("Run from project-a with --dir pointing to project-b", func(ctx *harness.Context) error {
			projectA := ctx.GetString("project_a_dir")
			projectB := ctx.GetString("project_b_dir")

			// CWD is project-a, but --dir points to project-b
			cmd := ctx.Bin("plan", "status", "b-only-plan", "--dir", projectB, "--json")
			cmd.Dir(projectA)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("--dir override failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("plan name in output", result.Stdout, "b-only-plan")
			})
		}),
	},
)

// setupGitRepo is a minimal helper to initialize a git repo for test purposes.
func setupGitRepo(dir string) (string, error) {
	_, err := git.SetupTestRepo(dir)
	if err != nil {
		return "", err
	}
	return dir, nil
}
