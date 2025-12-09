package scenarios

import (
	"fmt"
	"path/filepath"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

var CoreOrchestrationScenario = harness.NewScenario(
	"core-orchestration",
	"Validates core CLI orchestration logic: plan init, job execution, and status updates.",
	[]string{"core", "cli"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, _, err := setupDefaultEnvironment(ctx, "my-project")
			return err
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "cx"},
		),

		harness.NewStep("Initialize plan from recipe", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "init", "my-plan", "--recipe", "standard-feature")
			cmd.Dir(projectDir)

			result := cmd.Run()
			return result.AssertSuccess()
		}),

		harness.NewStep("Verify plan creation", func(ctx *harness.Context) error {
			notebooksRoot := ctx.GetString("notebooks_root")
			projectName := "my-project"
			planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", "my-plan")
			ctx.Set("plan_path", planPath)

			if err := fs.AssertExists(planPath); err != nil {
				return err
			}
			if err := fs.AssertExists(filepath.Join(planPath, ".grove-plan.yml")); err != nil {
				return err
			}
			return fs.AssertExists(filepath.Join(planPath, "01-cx.md"))
		}),

		harness.NewStep("Verify plan has jobs from recipe", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Verify that jobs from the recipe were created
			expectedJobs := []string{
				"01-cx.md",
				"02-spec.md",
				"03-generate-plan.md",
				"04-implement.md",
				"06-review.md",
				"07-follow-up.md",
			}

			for _, jobFile := range expectedJobs {
				jobPath := filepath.Join(planPath, jobFile)
				if err := fs.AssertExists(jobPath); err != nil {
					return fmt.Errorf("expected job file %s to exist: %w", jobFile, err)
				}

				// Load job to verify it's valid
				_, err := orchestration.LoadJob(jobPath)
				if err != nil {
					return fmt.Errorf("loading job %s: %w", jobFile, err)
				}
			}

			return nil
		}),
	},
)
