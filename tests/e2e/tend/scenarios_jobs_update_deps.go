// File: tests/e2e/tend/scenarios_jobs_update_deps.go
package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// JobUpdateDepsScenario tests the `flow plan jobs update-deps` command.
func JobUpdateDepsScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-jobs-update-deps",
		Description: "Tests updating job dependencies via CLI",
		Tags:        []string{"jobs", "dependencies"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with git", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Setup empty global config
				return setupEmptyGlobalConfig(ctx)
			}),
			harness.NewStep("Create a test plan", func(ctx *harness.Context) error {
				if err := createTestGroveConfig(ctx); err != nil {
					return err
				}

				planPath, err := setupPlanInExpectedLocation(ctx, "test-deps-plan")
				if err != nil {
					return err
				}
				ctx.Set("plan_path", planPath)
				return nil
			}),
			harness.NewStep("Create job 01", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)

				jobContent := `---
id: design-api-123
title: Design API
type: oneshot
status: pending
---

# Design API

Design job content.
`
				return fs.WriteString(filepath.Join(planPath, "01-design-api.md"), jobContent)
			}),
			harness.NewStep("Create job 02", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)

				jobContent := `---
id: implement-api-456
title: Implement API
type: oneshot
status: pending
---

# Implement API

Implementation job content.
`
				return fs.WriteString(filepath.Join(planPath, "02-implement-api.md"), jobContent)
			}),
			harness.NewStep("Create job 03 with no dependencies", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)

				jobContent := `---
id: test-api-789
title: Test API
type: oneshot
status: pending
---

# Test API

Test job with no initial dependencies.
`
				return fs.WriteString(filepath.Join(planPath, "03-test-api.md"), jobContent)
			}),
			harness.NewStep("Update job 03 to depend on jobs 01 and 02", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)
				flow, err := getFlowBinary()
				if err != nil {
					return err
				}

				jobFile := filepath.Join(planPath, "03-test-api.md")
				cmd := ctx.Command(flow, "plan", "jobs", "update-deps", jobFile, "01-design-api.md", "02-implement-api.md").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return result.Error
				}

				// Verify success message
				if !strings.Contains(result.Stdout, "Dependencies updated") {
					return fmt.Errorf("expected success message, got: %s", result.Stdout)
				}

				return nil
			}),
			harness.NewStep("Verify job 03 dependencies were added", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)
				jobFile := filepath.Join(planPath, "03-test-api.md")

				content, err := fs.ReadString(jobFile)
				if err != nil {
					return err
				}

				// Check for both dependencies
				if !strings.Contains(content, "01-design-api.md") {
					return fmt.Errorf("dependency 01-design-api.md not found in job file\nContent:\n%s", content)
				}
				if !strings.Contains(content, "02-implement-api.md") {
					return fmt.Errorf("dependency 02-implement-api.md not found in job file\nContent:\n%s", content)
				}

				// Verify proper YAML array format
				if !strings.Contains(content, "depends_on:") {
					return fmt.Errorf("depends_on field not found in frontmatter")
				}

				return nil
			}),
			harness.NewStep("Update job 03 to only depend on job 01", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)
				flow, err := getFlowBinary()
				if err != nil {
					return err
				}

				jobFile := filepath.Join(planPath, "03-test-api.md")
				cmd := ctx.Command(flow, "plan", "jobs", "update-deps", jobFile, "01-design-api.md").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return result.Error
				}

				return nil
			}),
			harness.NewStep("Verify job 03 now only depends on job 01", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)
				jobFile := filepath.Join(planPath, "03-test-api.md")

				content, err := fs.ReadString(jobFile)
				if err != nil {
					return err
				}

				// Check for job 01 dependency
				if !strings.Contains(content, "01-design-api.md") {
					return fmt.Errorf("dependency 01-design-api.md not found in job file\nContent:\n%s", content)
				}

				// Job 02 should NOT be present
				if strings.Contains(content, "02-implement-api.md") {
					return fmt.Errorf("dependency 02-implement-api.md should have been removed\nContent:\n%s", content)
				}

				return nil
			}),
			harness.NewStep("Clear all dependencies from job 03", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)
				flow, err := getFlowBinary()
				if err != nil {
					return err
				}

				jobFile := filepath.Join(planPath, "03-test-api.md")
				// No dependencies specified = clear all
				cmd := ctx.Command(flow, "plan", "jobs", "update-deps", jobFile).Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return result.Error
				}

				// Verify output shows no new dependencies
				if !strings.Contains(result.Stdout, "New dependencies: (none)") {
					return fmt.Errorf("expected 'New dependencies: (none)' in output, got: %s", result.Stdout)
				}

				return nil
			}),
			harness.NewStep("Verify job 03 has no dependencies", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)
				jobFile := filepath.Join(planPath, "03-test-api.md")

				content, err := fs.ReadString(jobFile)
				if err != nil {
					return err
				}

				// The depends_on field should be empty
				// Valid empty states: "depends_on: []" or no depends_on field at all
				// Invalid: "depends_on:" followed by "  - some-file.md"

				// Check if any actual dependencies are listed
				if strings.Contains(content, "01-design-api.md") {
					return fmt.Errorf("job should not depend on 01-design-api.md\nContent:\n%s", content)
				}
				if strings.Contains(content, "02-implement-api.md") {
					return fmt.Errorf("job should not depend on 02-implement-api.md\nContent:\n%s", content)
				}

				return nil
			}),
		},
	}
}
