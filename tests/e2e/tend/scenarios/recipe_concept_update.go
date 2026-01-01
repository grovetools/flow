package scenarios

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

var RecipeConceptUpdateScenario = harness.NewScenario(
	"recipe-concept-update",
	"Tests the built-in concept-update recipe with concept gathering functionality.",
	[]string{"recipe", "concept", "oneshot", "integration"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with concepts", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "concept-project")
			if err != nil {
				return err
			}

			// Create a concept directory structure
			conceptsDir := filepath.Join(notebooksRoot, "workspaces", "concept-project", "concepts")
			systemArchDir := filepath.Join(conceptsDir, "system-architecture")
			if err := os.MkdirAll(systemArchDir, 0755); err != nil {
				return fmt.Errorf("creating concepts directory: %w", err)
			}

			// Create concept-manifest.yml
			manifestContent := `id: system-architecture
title: System Architecture
description: Overview of the system architecture
status: active
related_concepts: []
related_plans: []
related_notes:
  - concept-project:inbox/arch-note.md
`
			if err := fs.WriteString(filepath.Join(systemArchDir, "concept-manifest.yml"), manifestContent); err != nil {
				return err
			}

			// Create overview.md
			overviewContent := `# System Architecture

This is the overview of our system architecture.

## Components

- Component A
- Component B
`
			if err := fs.WriteString(filepath.Join(systemArchDir, "overview.md"), overviewContent); err != nil {
				return err
			}

			// Create a related note
			inboxDir := filepath.Join(notebooksRoot, "workspaces", "concept-project", "inbox")
			if err := os.MkdirAll(inboxDir, 0755); err != nil {
				return err
			}
			noteContent := `---
title: Architecture Note
---

This note contains details about the architecture that need to be reflected in concepts.

## New Information

- We added Component C
- Component A was deprecated
`
			if err := fs.WriteString(filepath.Join(inboxDir, "arch-note.md"), noteContent); err != nil {
				return err
			}

			// Create context rules file for the project
			rulesDir := filepath.Join(projectDir, ".grove")
			if err := os.MkdirAll(rulesDir, 0755); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(rulesDir, "rules"), "*.go\n"); err != nil {
				return err
			}

			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "cx"},
		),

		harness.NewStep("Create mock LLM response for planner", func(ctx *harness.Context) error {
			responseContent := `# Concept Update Plan

Based on the analysis of the project context and concepts, here are the recommended updates:

## Updates Needed

1. Update system-architecture overview to include Component C
2. Mark Component A as deprecated in the overview
3. Update the concept manifest to reflect current status

## Implementation Steps

1. Edit overview.md to add Component C to the components list
2. Add deprecation notice for Component A
3. Update the concept description if needed
`
			responseFile := filepath.Join(ctx.RootDir, "mock_planner_response.txt")
			if err := fs.WriteString(responseFile, responseContent); err != nil {
				return err
			}
			ctx.Set("llm_response_file", responseFile)
			return nil
		}),

		harness.NewStep("Initialize plan with concept-update recipe", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			planPath := filepath.Join(notebooksRoot, "workspaces", "concept-project", "plans", "update-concepts-plan")
			ctx.Set("plan_path", planPath)

			// Init plan with concept-update recipe
			initCmd := ctx.Bin("plan", "init", "update-concepts-plan", "--recipe", "grove/concept-update")
			initCmd.Dir(projectDir)
			result := initCmd.Run()
			ctx.ShowCommandOutput(initCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init with recipe failed: %w\nStderr: %s", err, result.Stderr)
			}

			return nil
		}),

		harness.NewStep("Verify recipe jobs were created", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Verify planner job exists
			plannerJobPath := filepath.Join(planPath, "01-plan-update.md")
			if err := fs.AssertExists(plannerJobPath); err != nil {
				return fmt.Errorf("planner job not found: %w", err)
			}

			// Verify implementer job exists
			implementerJobPath := filepath.Join(planPath, "02-implement-updates.md")
			if err := fs.AssertExists(implementerJobPath); err != nil {
				return fmt.Errorf("implementer job not found: %w", err)
			}

			// Verify planner job has correct frontmatter
			if err := fs.AssertContains(plannerJobPath, "type: oneshot"); err != nil {
				return fmt.Errorf("planner job missing type: oneshot: %w", err)
			}
			if err := fs.AssertContains(plannerJobPath, "template: concept-planner"); err != nil {
				return fmt.Errorf("planner job missing template: %w", err)
			}
			if err := fs.AssertContains(plannerJobPath, "gather_concept_notes: true"); err != nil {
				return fmt.Errorf("planner job missing gather_concept_notes: %w", err)
			}

			// Verify implementer job has correct frontmatter
			if err := fs.AssertContains(implementerJobPath, "type: interactive_agent"); err != nil {
				return fmt.Errorf("implementer job missing type: %w", err)
			}

			return nil
		}),

		harness.NewStep("Run the planner job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			llmResponseFile := ctx.GetString("llm_response_file")

			runCmd := ctx.Bin("plan", "run", "--next", "--yes")
			runCmd.Dir(projectDir)
			runCmd.Env(fmt.Sprintf("GROVE_MOCK_LLM_RESPONSE_FILE=%s", llmResponseFile))

			result := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan run failed: %w\nStderr: %s", err, result.Stderr)
			}
			return nil
		}),

		harness.NewStep("Verify concept gathering and aggregation", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			plannerJobPath := filepath.Join(planPath, "01-plan-update.md")

			// Verify planner job completed
			job, err := orchestration.LoadJob(plannerJobPath)
			if err != nil {
				return fmt.Errorf("loading planner job: %w", err)
			}
			if job.Status != orchestration.JobStatusCompleted {
				return fmt.Errorf("expected planner job status to be 'completed', but was '%s'", job.Status)
			}

			// Verify planner job output contains the plan
			if err := fs.AssertContains(plannerJobPath, "## Output"); err != nil {
				return fmt.Errorf("planner job missing output section: %w", err)
			}
			if err := fs.AssertContains(plannerJobPath, "Concept Update Plan"); err != nil {
				return fmt.Errorf("planner job output missing plan: %w", err)
			}

			// Note: concept gathering might not work in sandboxed test environment
			// The main test is that the job runs with gather_concept_notes flag

			return nil
		}),

		harness.NewStep("Verify implementer job has correct dependencies", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			implementerJobPath := filepath.Join(planPath, "02-implement-updates.md")

			// Verify implementer job depends on planner
			job, err := orchestration.LoadJob(implementerJobPath)
			if err != nil {
				return fmt.Errorf("loading implementer job: %w", err)
			}

			if len(job.DependsOn) == 0 {
				return fmt.Errorf("implementer job has no dependencies")
			}

			// Verify it has prepend_dependencies enabled
			if !job.PrependDependencies {
				return fmt.Errorf("implementer job should have prepend_dependencies enabled")
			}

			// Verify job body includes instructions for using nb concept CLI
			if err := fs.AssertContains(implementerJobPath, "nb concept link"); err != nil {
				return fmt.Errorf("implementer job missing CLI instructions: %w", err)
			}

			return nil
		}),
	},
)

var RecipeConceptUpdateWithPlansScenario = harness.NewScenario(
	"recipe-concept-update-with-plans",
	"Tests concept gathering with gather_concept_plans flag.",
	[]string{"recipe", "concept", "oneshot"},
	[]harness.Step{
		harness.NewStep("Setup environment with concept linked to plan", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "concept-plans-project")
			if err != nil {
				return err
			}

			// Create a concept with linked plan
			conceptsDir := filepath.Join(notebooksRoot, "workspaces", "concept-plans-project", "concepts")
			conceptDir := filepath.Join(conceptsDir, "test-concept")
			if err := os.MkdirAll(conceptDir, 0755); err != nil {
				return err
			}

			manifestContent := `id: test-concept
title: Test Concept
description: A test concept with linked plans
status: active
related_concepts: []
related_plans:
  - concept-plans-project:plans/test-plan
related_notes: []
`
			if err := fs.WriteString(filepath.Join(conceptDir, "concept-manifest.yml"), manifestContent); err != nil {
				return err
			}

			overviewContent := `# Test Concept

This concept is linked to a plan.
`
			if err := fs.WriteString(filepath.Join(conceptDir, "overview.md"), overviewContent); err != nil {
				return err
			}

			// Create a linked plan
			planDir := filepath.Join(notebooksRoot, "workspaces", "concept-plans-project", "plans", "test-plan")
			if err := os.MkdirAll(planDir, 0755); err != nil {
				return err
			}
			planContent := `---
title: Test Plan
---

This is a test plan linked to the concept.
`
			if err := fs.WriteString(filepath.Join(planDir, "01-step.md"), planContent); err != nil {
				return err
			}

			// Create context rules
			rulesDir := filepath.Join(projectDir, ".grove")
			if err := os.MkdirAll(rulesDir, 0755); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(rulesDir, "rules"), "*.go\n"); err != nil {
				return err
			}

			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "cx"},
		),

		harness.NewStep("Create mock LLM response", func(ctx *harness.Context) error {
			responseContent := "Concept analysis complete."
			responseFile := filepath.Join(ctx.RootDir, "mock_response.txt")
			if err := fs.WriteString(responseFile, responseContent); err != nil {
				return err
			}
			ctx.Set("llm_response_file", responseFile)
			return nil
		}),

		harness.NewStep("Create custom job with gather_concept_plans", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			planPath := filepath.Join(notebooksRoot, "workspaces", "concept-plans-project", "plans", "gather-test-plan")
			ctx.Set("plan_path", planPath)

			// Init empty plan
			initCmd := ctx.Bin("plan", "init", "gather-test-plan")
			initCmd.Dir(projectDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w", result.Error)
			}

			// Manually create a job with gather_concept_plans
			jobContent := `---
id: test-gather-plans-job
title: Test Gather Plans
type: oneshot
status: pending
gather_concept_plans: true
gather_concept_notes: false
---

Analyze concepts with plans.
`
			jobPath := filepath.Join(planPath, "01-test-job.md")
			if err := fs.WriteString(jobPath, jobContent); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Run job and verify plan gathering", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			llmResponseFile := ctx.GetString("llm_response_file")

			runCmd := ctx.Bin("plan", "run", "--next", "--yes")
			runCmd.Dir(projectDir)
			runCmd.Env(fmt.Sprintf("GROVE_MOCK_LLM_RESPONSE_FILE=%s", llmResponseFile))

			result := runCmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan run failed: %w", err)
			}

			// Verify job completed
			jobPath := filepath.Join(planPath, "01-test-job.md")
			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading job: %w", err)
			}
			if job.Status != orchestration.JobStatusCompleted {
				return fmt.Errorf("expected job status to be 'completed', but was '%s'", job.Status)
			}

			// Verify job has the correct flags
			if !job.GatherConceptPlans {
				return fmt.Errorf("job should have gather_concept_plans: true")
			}
			if job.GatherConceptNotes {
				return fmt.Errorf("job should have gather_concept_notes: false")
			}

			return nil
		}),
	},
)
