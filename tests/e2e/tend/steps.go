// File: tests/e2e/tend/steps.go
package main

import (
	"fmt"
	"path/filepath"

	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/command"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// InitPlanWithNoteRef creates a standardized step for initializing a plan with note_ref
func InitPlanWithNoteRef(planName, noteRefPath string, extraFlags ...string) harness.Step {
	return harness.NewStep("Initialize plan with --note-ref", func(ctx *harness.Context) error {
		flow, _ := getFlowBinary()
		args := []string{"plan", "init", planName, "--recipe", "chat", "--worktree", "--note-ref", noteRefPath}
		args = append(args, extraFlags...)

		cmd := command.New(flow, args...).Dir(ctx.RootDir)
		result := cmd.Run()
		ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
		return result.Error
	})
}

// VerifyPlanStatus creates a step to verify plan status
func VerifyPlanStatus(planName, expectedStatus string) harness.Step {
	return harness.NewStep(fmt.Sprintf("Verify plan status is '%s'", expectedStatus), func(ctx *harness.Context) error {
		planConfig := filepath.Join(ctx.RootDir, "plans", planName, ".grove-plan.yml")
		return assert.YAMLField(planConfig, "status", expectedStatus)
	})
}

// VerifyJobFrontmatterField creates a step to verify a job frontmatter field
func VerifyJobFrontmatterField(planName, jobFile, field, expectedValue string) harness.Step {
	return harness.NewStep(fmt.Sprintf("Verify job %s has %s=%s", jobFile, field, expectedValue), func(ctx *harness.Context) error {
		jobPath := filepath.Join(ctx.RootDir, "plans", planName, jobFile)
		content, err := fs.ReadString(jobPath)
		if err != nil {
			return err
		}

		// Check if the frontmatter contains the field with the expected value
		expectedLine := fmt.Sprintf("%s: %s", field, expectedValue)
		return assert.Contains(content, expectedLine,
			fmt.Sprintf("job frontmatter should contain '%s'", expectedLine))
	})
}

// RunPlanReview creates a step to run 'flow plan review'
func RunPlanReview(planName string) harness.Step {
	return harness.NewStep("Run 'flow plan review'", func(ctx *harness.Context) error {
		flow, _ := getFlowBinary()
		cmd := command.New(flow, "plan", "review", planName).Dir(ctx.RootDir)
		result := cmd.Run()
		ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
		if err := result.AssertSuccess(); err != nil {
			return err
		}
		return result.AssertStdoutContains(
			"Executing on_review hook",
			"on_review hook executed successfully",
			"marked for review",
		)
	})
}

// VerifyPlanFinishGated creates a step to verify that 'plan finish' is gated
func VerifyPlanFinishGated(planName string) harness.Step {
	return harness.NewStep("Test that 'plan finish' is gated", func(ctx *harness.Context) error {
		flow, _ := getFlowBinary()
		cmd := command.New(flow, "plan", "finish", planName).Dir(ctx.RootDir)
		result := cmd.Run()
		ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

		// Should fail
		if err := result.AssertFailure(); err != nil {
			return err
		}

		// Should contain error message about running review first
		return assert.Contains(result.Stderr, "Please run 'flow plan review",
			"expected error message about running 'plan review' first")
	})
}

// RunPlanFinish creates a step to run 'flow plan finish'
func RunPlanFinish(planName string, flags ...string) harness.Step {
	return harness.NewStep("Run 'flow plan finish'", func(ctx *harness.Context) error {
		flow, _ := getFlowBinary()
		args := []string{"plan", "finish", planName}
		args = append(args, flags...)

		cmd := command.New(flow, args...).Dir(ctx.RootDir)
		result := cmd.Run()
		ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
		if err := result.AssertSuccess(); err != nil {
			return err
		}
		return result.AssertStdoutContains(
			"Marked plan as finished",
			"Plan cleanup finished",
		)
	})
}

// VerifyHooksConfigured creates a step to verify hooks are configured in plan
func VerifyHooksConfigured(planName string, hookName string) harness.Step {
	return harness.NewStep(fmt.Sprintf("Verify %s hook is configured", hookName), func(ctx *harness.Context) error {
		planConfig := filepath.Join(ctx.RootDir, "plans", planName, ".grove-plan.yml")
		content, err := fs.ReadString(planConfig)
		if err != nil {
			return err
		}
		return assert.Contains(content, fmt.Sprintf("%s:", hookName),
			fmt.Sprintf("plan config should contain %s hook", hookName))
	})
}

// VerifyFileExists creates a step to verify a file exists
func VerifyFileExists(relativePath string) harness.Step {
	return harness.NewStep(fmt.Sprintf("Verify file exists: %s", relativePath), func(ctx *harness.Context) error {
		fullPath := filepath.Join(ctx.RootDir, relativePath)
		return fs.AssertExists(fullPath)
	})
}

// VerifyFileContains creates a step to verify file content
func VerifyFileContains(relativePath, expectedContent string) harness.Step {
	return harness.NewStep(fmt.Sprintf("Verify file contains expected content"), func(ctx *harness.Context) error {
		fullPath := filepath.Join(ctx.RootDir, relativePath)
		return fs.AssertContains(fullPath, expectedContent)
	})
}
