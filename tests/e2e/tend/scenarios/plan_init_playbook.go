package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

// PlanInitPlaybookScenario verifies `flow plan init --playbook --recipe`
// writes playbook scoping to the plan manifest (not per-job frontmatter)
// and loads the recipe from the playbook's recipes/ directory.
var PlanInitPlaybookScenario = harness.NewScenario(
	"plan-init-playbook",
	"Verify --playbook flag writes plan-level scoping and resolves playbook recipes",
	[]string{"playbook", "plan", "recipe"},
	[]harness.Step{
		harness.NewStep("Setup playbook with a recipe", func(ctx *harness.Context) error {
			_, _, _, err := setupPlaybookEnvironment(ctx, "init-project", "test-pb")
			return err
		}),

		harness.NewStep("Run plan init --playbook --recipe", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "init", "my-test-plan", "--playbook", "test-pb", "--recipe", "test-recipe")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Verify .grove-plan.yml contains playbook scoping", func(ctx *harness.Context) error {
			notebooksRoot := ctx.GetString("notebooks_root")
			planPath := filepath.Join(notebooksRoot, "workspaces", "init-project", "plans", "my-test-plan")
			ctx.Set("plan_path", planPath)

			manifest := filepath.Join(planPath, ".grove-plan.yml")
			content, err := fs.ReadString(manifest)
			if err != nil {
				return fmt.Errorf("failed to read plan manifest: %w", err)
			}
			if !strings.Contains(content, "playbook: test-pb") {
				return fmt.Errorf("expected .grove-plan.yml to contain 'playbook: test-pb', got:\n%s", content)
			}
			return nil
		}),

		harness.NewStep("Verify recipe job was created without per-job playbook frontmatter", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			entries, err := os.ReadDir(planPath)
			if err != nil {
				return fmt.Errorf("failed to list plan dir: %w", err)
			}
			var jobFile string
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
					jobFile = filepath.Join(planPath, e.Name())
					break
				}
			}
			if jobFile == "" {
				return fmt.Errorf("no job .md file created from recipe in %s", planPath)
			}
			content, err := fs.ReadString(jobFile)
			if err != nil {
				return err
			}
			// Per 05 turn 4: playbook scoping lives on the plan only,
			// not in job frontmatter.
			if strings.Contains(content, "\nplaybook:") || strings.HasPrefix(content, "playbook:") {
				return fmt.Errorf("job frontmatter should not contain 'playbook:' field: %s", jobFile)
			}
			return nil
		}),
	},
)
