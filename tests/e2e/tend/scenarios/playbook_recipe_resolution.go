package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

// PlaybookRecipeResolutionScenario verifies recipe precedence:
// local .grove/recipes > playbook recipes > builtin. When both a
// local and a playbook recipe exist with the same name, the local
// copy wins. Removing the local copy falls through to the playbook.
var PlaybookRecipeResolutionScenario = harness.NewScenario(
	"playbook-recipe-resolution",
	"Verify local .grove/recipes wins over playbook recipes with the same name",
	[]string{"playbook", "recipe"},
	[]harness.Step{
		harness.NewStep("Setup playbook and overlapping local recipe", func(ctx *harness.Context) error {
			projectDir, _, playbookDir, err := setupPlaybookEnvironment(ctx, "recipe-res-project", "test-pb")
			if err != nil {
				return err
			}

			// Overwrite the playbook's test-recipe.md with a distinct
			// marker so we can tell it apart from the local copy.
			playbookRecipe := `---
description: Playbook recipe
---

# Playbook Recipe Body
---
id: pb-job
title: From Playbook
type: oneshot
---
Body from playbook.
`
			if err := fs.WriteString(filepath.Join(playbookDir, "recipes", "test-recipe.md"), playbookRecipe); err != nil {
				return err
			}

			// Local .grove/recipes/test-recipe/ directory containing
			// a single job .md file. This is the case A layout
			// (recipe dir) in GetProjectRecipe.
			localRecipeDir := filepath.Join(projectDir, ".grove", "recipes", "test-recipe")
			localJob := `---
id: local-job
title: From Local
type: oneshot
---
Body from local.
`
			if err := fs.WriteString(filepath.Join(localRecipeDir, "01-local-job.md"), localJob); err != nil {
				return err
			}
			return nil
		}),

		harness.NewStep("Playbook-scoped plan with overlapping recipe uses local copy", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "local-wins-plan", "--playbook", "test-pb", "--recipe", "test-recipe")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}
			planPath := filepath.Join(notebooksRoot, "workspaces", "recipe-res-project", "plans", "local-wins-plan")

			// Local recipe had job file 01-local-job.md → the plan
			// should contain a job with title "From Local".
			entries, err := os.ReadDir(planPath)
			if err != nil {
				return err
			}
			var jobBody string
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
					data, _ := os.ReadFile(filepath.Join(planPath, e.Name()))
					if strings.Contains(string(data), "Body from local") {
						jobBody = string(data)
						break
					}
				}
			}
			if jobBody == "" {
				return fmt.Errorf("expected local recipe to win; no job with 'Body from local' found in %s", planPath)
			}
			return nil
		}),

		harness.NewStep("Removing local recipe falls through to playbook copy", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			localRecipeDir := filepath.Join(projectDir, ".grove", "recipes", "test-recipe")
			if err := os.RemoveAll(localRecipeDir); err != nil {
				return err
			}

			cmd := ctx.Bin("plan", "init", "playbook-wins-plan", "--playbook", "test-pb", "--recipe", "test-recipe")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}
			planPath := filepath.Join(notebooksRoot, "workspaces", "recipe-res-project", "plans", "playbook-wins-plan")

			entries, err := os.ReadDir(planPath)
			if err != nil {
				return err
			}
			found := false
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
					data, _ := os.ReadFile(filepath.Join(planPath, e.Name()))
					if strings.Contains(string(data), "Body from playbook") {
						found = true
						break
					}
				}
			}
			if !found {
				return fmt.Errorf("expected playbook recipe to be used after local removed, not found in %s", planPath)
			}
			return nil
		}),

		harness.NewStep("Unknown recipe name fails with error", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "init", "bad-plan", "--playbook", "test-pb", "--recipe", "no-such-recipe")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if result.ExitCode == 0 {
				return fmt.Errorf("expected non-zero exit for unknown recipe, got stdout=%s", result.Stdout)
			}
			return nil
		}),
	},
)
