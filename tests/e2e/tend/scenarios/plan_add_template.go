package scenarios

import (
	"fmt"
	"path/filepath"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/tend/pkg/assert"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

var PlanAddTemplateScenario = harness.NewScenario(
	"plan-add-template",
	"Tests the `flow plan add --template` command to ensure template-based job creation works correctly.",
	[]string{"core", "plan", "template", "cli"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			_, _, err := setupDefaultEnvironment(ctx, "test-add-template-project")
			return err
		}),

		harness.NewStep("Initialize plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			initCmd := ctx.Bin("plan", "init", "test-add-template-fix")
			initCmd.Dir(projectDir)
			result := initCmd.Run()
			ctx.ShowCommandOutput(initCmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "test-add-template-project", "plans", "test-add-template-fix")
			ctx.Set("plan_path", planPath)

			return nil
		}),

		harness.NewStep("Test plan add with template flag", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Run the command that was previously failing
			cmd := ctx.Bin("plan", "add", "test-add-template-fix", "--title", "My Template Job", "--template", "agent-xml", "-p", "test prompt")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// The command should succeed
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add with --template flag should succeed: %w", err)
			}

			// Verify the job file was created
			jobFile := filepath.Join(planPath, "01-my-template-job.md")
			if err := fs.AssertExists(jobFile); err != nil {
				return fmt.Errorf("job file should be created: %w", err)
			}

			// Verify the template was correctly added to the frontmatter
			if err := assert.YAMLField(jobFile, "template", "agent-xml"); err != nil {
				return fmt.Errorf("template should be in frontmatter: %w", err)
			}

			// Verify the job has the expected type from the template
			job, err := orchestration.LoadJob(jobFile)
			if err != nil {
				return fmt.Errorf("failed to load job: %w", err)
			}

			// agent-xml template has type "oneshot"
			if job.Type != "oneshot" {
				return fmt.Errorf("expected job type 'oneshot', got '%s'", job.Type)
			}

			// Verify the title is correct
			if job.Title != "My Template Job" {
				return fmt.Errorf("expected title 'My Template Job', got '%s'", job.Title)
			}

			// Verify the prompt body contains the user-provided prompt
			if err := assert.Contains(job.PromptBody, "test prompt"); err != nil {
				return fmt.Errorf("job prompt should contain user-provided prompt: %w", err)
			}

			return nil
		}),
	},
)
