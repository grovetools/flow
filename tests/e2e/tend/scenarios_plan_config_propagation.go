package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// PlanConfigPropagationScenario tests that plan config changes propagate to job frontmatter
func PlanConfigPropagationScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-config-propagation",
		Description: "Tests that plan config updates propagate to job files without the field",
		Tags:        []string{"plan", "config", "propagation"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repository and config", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				
				// Create grove.yml
				groveConfig := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveConfig)
				return nil
			}),
			
			harness.NewStep("Initialize plan", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "init", "test-propagation").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %w", result.Error)
				}
				return nil
			}),
			
			harness.NewStep("Add jobs with and without model", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Add first job without model
				cmd := ctx.Command(flow, "plan", "add", "test-propagation",
					"--title", "Job Without Model",
					"--type", "oneshot",
					"-p", "Do something",
				).Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add first job: %w", result.Error)
				}
				
				// Add second job with explicit model by creating file directly
				jobPath := filepath.Join(ctx.RootDir, "plans", "test-propagation", "02-job-with-model.md")
				jobContent := `---
id: job-with-model
title: "Job With Model"
status: pending
type: oneshot
model: gemini-2.0-flash
output:
  type: file
---

Do something else`
				if err := fs.WriteString(jobPath, jobContent); err != nil {
					return fmt.Errorf("failed to create second job: %w", err)
				}
				
				// Add third job without model
				cmd = ctx.Command(flow, "plan", "add", "test-propagation",
					"--title", "Another Job Without Model",
					"--type", "shell",
					"-p", "echo hello",
				).Dir(ctx.RootDir)
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to add third job: %w", result.Error)
				}
				
				return nil
			}),
			
			harness.NewStep("Verify initial state", func(ctx *harness.Context) error {
				// Check first job has no model
				job1Path := filepath.Join(ctx.RootDir, "plans", "test-propagation", "01-job-without-model.md")
				content1, err := fs.ReadString(job1Path)
				if err != nil {
					return fmt.Errorf("failed to read job1: %w", err)
				}
				if strings.Contains(content1, "model:") {
					return fmt.Errorf("job1 should not have model field initially")
				}
				
				// Check second job has model
				job2Path := filepath.Join(ctx.RootDir, "plans", "test-propagation", "02-job-with-model.md")
				content2, err := fs.ReadString(job2Path)
				if err != nil {
					return fmt.Errorf("failed to read job2: %w", err)
				}
				if !strings.Contains(content2, "model: gemini-2.0-flash") {
					return fmt.Errorf("job2 should have model: gemini-2.0-flash")
				}
				
				// Check third job has no model
				job3Path := filepath.Join(ctx.RootDir, "plans", "test-propagation", "03-another-job-without-model.md")
				content3, err := fs.ReadString(job3Path)
				if err != nil {
					return fmt.Errorf("failed to read job3: %w", err)
				}
				if strings.Contains(content3, "model:") {
					return fmt.Errorf("job3 should not have model field initially")
				}
				
				return nil
			}),
			
			harness.NewStep("Set model in plan config", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "config", "test-propagation",
					"--set", "model=claude-3-5-sonnet-20241022",
				).Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan config --set failed: %w", result.Error)
				}
				
				// Verify output mentions propagation
				if !strings.Contains(result.Stdout, "Propagated config changes to 2 job(s)") {
					return fmt.Errorf("expected propagation to 2 jobs, got: %s", result.Stdout)
				}
				
				return nil
			}),
			
			harness.NewStep("Verify propagation", func(ctx *harness.Context) error {
				// Check first job now has model
				job1Path := filepath.Join(ctx.RootDir, "plans", "test-propagation", "01-job-without-model.md")
				content1, err := fs.ReadString(job1Path)
				if err != nil {
					return fmt.Errorf("failed to read job1: %w", err)
				}
				if !strings.Contains(content1, "model: claude-3-5-sonnet-20241022") {
					return fmt.Errorf("job1 should have model: claude-3-5-sonnet-20241022 after propagation")
				}
				
				// Check second job still has original model
				job2Path := filepath.Join(ctx.RootDir, "plans", "test-propagation", "02-job-with-model.md")
				content2, err := fs.ReadString(job2Path)
				if err != nil {
					return fmt.Errorf("failed to read job2: %w", err)
				}
				if !strings.Contains(content2, "model: gemini-2.0-flash") {
					return fmt.Errorf("job2 should still have model: gemini-2.0-flash")
				}
				if strings.Contains(content2, "claude-3-5-sonnet-20241022") {
					return fmt.Errorf("job2 should not have been updated")
				}
				
				// Check third job now has model
				job3Path := filepath.Join(ctx.RootDir, "plans", "test-propagation", "03-another-job-without-model.md")
				content3, err := fs.ReadString(job3Path)
				if err != nil {
					return fmt.Errorf("failed to read job3: %w", err)
				}
				if !strings.Contains(content3, "model: claude-3-5-sonnet-20241022") {
					return fmt.Errorf("job3 should have model: claude-3-5-sonnet-20241022 after propagation")
				}
				
				return nil
			}),
			
			harness.NewStep("Test worktree propagation", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Set worktree in plan config
				cmd := ctx.Command(flow, "plan", "config", "test-propagation",
					"--set", "worktree=feature-branch",
				).Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan config --set worktree failed: %w", result.Error)
				}
				
				// For shell jobs, worktree shouldn't be propagated
				// We have 2 oneshot jobs without worktree (job1 and job2 both had model but no worktree)
				// job2 already has a model but no worktree, so it should get worktree
				// job1 already has a model but no worktree, so it should get worktree
				// job3 is shell, so it shouldn't get worktree
				if !strings.Contains(result.Stdout, "Propagated config changes to 2 job(s)") {
					return fmt.Errorf("expected propagation to 2 jobs for worktree, got: %s", result.Stdout)
				}
				
				// Verify oneshot jobs got worktree
				job1Path := filepath.Join(ctx.RootDir, "plans", "test-propagation", "01-job-without-model.md")
				content1, err := fs.ReadString(job1Path)
				if err != nil {
					return fmt.Errorf("failed to read job1: %w", err)
				}
				if !strings.Contains(content1, "worktree: feature-branch") {
					return fmt.Errorf("oneshot job1 should have worktree after propagation")
				}
				
				job2Path := filepath.Join(ctx.RootDir, "plans", "test-propagation", "02-job-with-model.md")
				content2, err := fs.ReadString(job2Path)
				if err != nil {
					return fmt.Errorf("failed to read job2: %w", err)
				}
				if !strings.Contains(content2, "worktree: feature-branch") {
					return fmt.Errorf("oneshot job2 should have worktree after propagation")
				}
				
				// Verify shell job didn't get worktree
				job3Path := filepath.Join(ctx.RootDir, "plans", "test-propagation", "03-another-job-without-model.md")
				content3, err := fs.ReadString(job3Path)
				if err != nil {
					return fmt.Errorf("failed to read job3: %w", err)
				}
				if strings.Contains(content3, "worktree:") {
					return fmt.Errorf("shell job should not have worktree field")
				}
				
				return nil
			}),
			
			harness.NewStep("Test clearing values doesn't propagate", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Clear model in plan config
				cmd := ctx.Command(flow, "plan", "config", "test-propagation",
					"--set", "model=",
				).Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan config --set with empty value failed: %w", result.Error)
				}
				
				// Should not propagate empty values
				if strings.Contains(result.Stdout, "Propagated config changes") {
					return fmt.Errorf("clearing values should not propagate to jobs")
				}
				
				// Verify jobs still have their models
				job1Path := filepath.Join(ctx.RootDir, "plans", "test-propagation", "01-job-without-model.md")
				content1, err := fs.ReadString(job1Path)
				if err != nil {
					return fmt.Errorf("failed to read job1: %w", err)
				}
				if !strings.Contains(content1, "model: claude-3-5-sonnet-20241022") {
					return fmt.Errorf("job1 should still have its model after clearing plan config")
				}
				
				return nil
			}),
		},
	}
}