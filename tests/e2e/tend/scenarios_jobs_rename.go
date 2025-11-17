// File: tests/e2e/tend/scenarios_jobs_rename.go
package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// JobRenameScenario tests the `flow plan jobs rename` command.
func JobRenameScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-jobs-rename",
		Description: "Tests renaming a job and updating dependencies",
		Tags:        []string{"jobs", "rename"},
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

				planPath, err := setupPlanInExpectedLocation(ctx, "test-rename-plan")
				if err != nil {
					return err
				}
				ctx.Set("plan_path", planPath)
				return nil
			}),
			harness.NewStep("Create first job", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)

				// Create 01-design-api.md
				jobContent := `---
id: design-api-123
title: Design API
type: oneshot
status: pending
---

# Design API

This is the API design job.
`
				return fs.WriteString(filepath.Join(planPath, "01-design-api.md"), jobContent)
			}),
			harness.NewStep("Create second job that depends on first", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)

				// Create 02-implement-api.md that depends on 01-design-api.md
				jobContent := `---
id: implement-api-456
title: Implement API
type: oneshot
status: pending
depends_on:
  - 01-design-api.md
---

# Implement API

This job depends on the design job.
`
				return fs.WriteString(filepath.Join(planPath, "02-implement-api.md"), jobContent)
			}),
			harness.NewStep("Create third job that uses first job in prompt_source", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)

				// Create 03-test-api.md that uses 01-design-api.md as prompt_source
				jobContent := `---
id: test-api-789
title: Test API
type: oneshot
status: pending
prompt_source:
  - spec.md
  - 01-design-api.md
---

# Test API

This job uses the design as context.
`
				return fs.WriteString(filepath.Join(planPath, "03-test-api.md"), jobContent)
			}),
			harness.NewStep("Rename the first job", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)
				flow, err := getFlowBinary()
				if err != nil {
					return err
				}

				// Rename 01-design-api.md to "Design REST API"
				jobFile := filepath.Join(planPath, "01-design-api.md")
				cmd := ctx.Command(flow, "plan", "jobs", "rename", jobFile, "Design REST API").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return result.Error
				}

				// Verify success message
				if !strings.Contains(result.Stdout, "Job renamed successfully") {
					return fmt.Errorf("expected success message, got: %s", result.Stdout)
				}
				if !strings.Contains(result.Stdout, "Design API → Design REST API") {
					return fmt.Errorf("expected title change in output, got: %s", result.Stdout)
				}

				return nil
			}),
			harness.NewStep("Verify old file was deleted", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)
				oldFile := filepath.Join(planPath, "01-design-api.md")

				if fs.Exists(oldFile) {
					return fmt.Errorf("old file still exists: %s", oldFile)
				}
				return nil
			}),
			harness.NewStep("Verify new file was created", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)
				newFile := filepath.Join(planPath, "01-design-rest-api.md")

				if !fs.Exists(newFile) {
					return fmt.Errorf("new file does not exist: %s", newFile)
				}

				ctx.Set("new_file", newFile)
				return nil
			}),
			harness.NewStep("Verify frontmatter title was updated", func(ctx *harness.Context) error {
				newFile := ctx.Get("new_file").(string)
				content, err := fs.ReadString(newFile)
				if err != nil {
					return err
				}

				// Check for new title in frontmatter
				if !strings.Contains(content, "title: Design REST API") {
					return fmt.Errorf("frontmatter title was not updated in file: %s\nContent:\n%s", newFile, content)
				}

				// Ensure old title is not present
				if strings.Contains(content, "title: Design API") {
					return fmt.Errorf("old title still present in frontmatter")
				}

				// Ensure body content is preserved
				if !strings.Contains(content, "This is the API design job.") {
					return fmt.Errorf("job body content was not preserved")
				}

				return nil
			}),
			harness.NewStep("Verify dependent job was updated", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)
				dependentFile := filepath.Join(planPath, "02-implement-api.md")

				content, err := fs.ReadString(dependentFile)
				if err != nil {
					return err
				}

				// Check that depends_on was updated to new filename
				if !strings.Contains(content, "01-design-rest-api.md") {
					return fmt.Errorf("dependent job was not updated with new filename\nContent:\n%s", content)
				}

				// Ensure old filename is not present
				if strings.Contains(content, "01-design-api.md") {
					return fmt.Errorf("old filename still present in dependent job")
				}

				return nil
			}),
			harness.NewStep("Verify job with prompt_source was updated", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)
				promptSourceFile := filepath.Join(planPath, "03-test-api.md")

				content, err := fs.ReadString(promptSourceFile)
				if err != nil {
					return err
				}

				// Check that prompt_source was updated to new filename
				if !strings.Contains(content, "01-design-rest-api.md") {
					return fmt.Errorf("prompt_source was not updated with new filename\nContent:\n%s", content)
				}

				// Ensure old filename is not present in prompt_source
				if strings.Contains(content, "01-design-api.md") {
					return fmt.Errorf("old filename still present in prompt_source")
				}

				// Verify spec.md is still there (unchanged)
				if !strings.Contains(content, "spec.md") {
					return fmt.Errorf("other prompt_source entries were lost")
				}

				return nil
			}),
			harness.NewStep("Verify rename with same title is idempotent", func(ctx *harness.Context) error {
				planPath := ctx.Get("plan_path").(string)
				flow, err := getFlowBinary()
				if err != nil {
					return err
				}

				// Try to rename to the same title again
				jobFile := filepath.Join(planPath, "01-design-rest-api.md")
				cmd := ctx.Command(flow, "plan", "jobs", "rename", jobFile, "Design REST API").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				// This should fail because the target filename already exists
				if result.Error == nil {
					return fmt.Errorf("expected error when renaming to existing filename")
				}

				if !strings.Contains(result.Stderr, "already exists") {
					return fmt.Errorf("expected 'already exists' error, got: %s", result.Stderr)
				}

				return nil
			}),
		},
	}
}
