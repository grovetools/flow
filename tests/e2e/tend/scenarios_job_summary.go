// File: tests/e2e/tend/scenarios_job_summary.go
package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// JobSummaryScenario tests that job content is summarized when a job is marked as complete.
func JobSummaryScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-job-summary-on-complete",
		Description: "Tests that job content is summarized when a job is marked as complete",
		Tags:        []string{"plan", "complete", "summary"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with summarization enabled", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)

				// Create grove.yml
				configContent := `name: test-project
flow:
  plans_directory: ./plans
  summarize_on_complete: true
  summary_model: mock-summarizer
  # The prompt and max_chars will use the defaults in this test
`
				if err := fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent); err != nil {
					return err
				}

				// Initialize plan
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "init", "summary-test").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}

				// Add a job to be completed
				jobContent := `---
id: job-to-summarize
title: Job To Summarize
status: pending
type: oneshot
---

# Main Task

This is the main task for the job. It involves several steps and has a clear outcome.

## Output

The job produced this output, which should be included in the summary.
`
				planDir := filepath.Join(ctx.RootDir, "plans", "summary-test")
				if err := fs.WriteString(filepath.Join(planDir, "01-job-to-summarize.md"), jobContent); err != nil {
					return err
				}

				return nil
			}),
			setupTestEnvironment(map[string]interface{}{
				"additionalMocks": map[string]string{
					"llm": `#!/bin/bash
# Mock LLM for summarization
echo "This is a concise mock summary."
`,
				},
			}),
			harness.NewStep("Run 'flow plan complete' on the job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				jobPath := filepath.Join("plans", "summary-test", "01-job-to-summarize.md")

				cmd := ctx.Command(flow, "plan", "complete", jobPath).Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("'flow plan complete' failed: %w", result.Error)
				}

				// Verify output indicates summary was added
				if !strings.Contains(result.Stdout, "Added summary to job frontmatter") {
					return fmt.Errorf("expected success message for summary addition not found")
				}

				return nil
			}),
			harness.NewStep("Verify job file has summary in frontmatter", func(ctx *harness.Context) error {
				jobPath := filepath.Join(ctx.RootDir, "plans", "summary-test", "01-job-to-summarize.md")
				content, err := fs.ReadString(jobPath)
				if err != nil {
					return fmt.Errorf("failed to read job file: %w", err)
				}

				if !strings.Contains(content, "status: completed") {
					return fmt.Errorf("job status was not updated to 'completed'")
				}

				if !strings.Contains(content, "summary: This is a concise mock summary.") {
					return fmt.Errorf("job frontmatter does not contain the expected summary:\n%s", content)
				}

				return nil
			}),
		},
	}
}

// OneshotJobSummaryScenario tests that oneshot jobs get summarized when they complete automatically.
func OneshotJobSummaryScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-oneshot-job-summary",
		Description: "Tests that oneshot jobs are summarized when they complete via orchestrator",
		Tags:        []string{"plan", "run", "summary", "oneshot"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with summarization enabled", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)

				// Create grove.yml with summarization enabled
				configContent := `name: test-project
flow:
  plans_directory: ./plans
  summarize_on_complete: true
  summary_model: mock-summarizer
  oneshot_model: mock-llm
  summary_max_chars: 100
`
				if err := fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent); err != nil {
					return err
				}

				// Initialize plan
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "init", "oneshot-summary-test").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}

				// Add a oneshot job
				jobContent := `---
id: oneshot-calculation
title: Calculate Sum
status: pending
type: oneshot
---

# Task

Calculate the sum of 15 and 27.
`
				planDir := filepath.Join(ctx.RootDir, "plans", "oneshot-summary-test")
				if err := fs.WriteString(filepath.Join(planDir, "01-calculate.md"), jobContent); err != nil {
					return err
				}

				return nil
			}),
			setupTestEnvironment(map[string]interface{}{
				"additionalMocks": map[string]string{
					"llm": `#!/bin/bash
# Mock LLM - if called with model "mock-summarizer", generate summary
# Otherwise, generate oneshot output
if [[ "$@" == *"mock-summarizer"* ]]; then
    echo "Calculated the sum of 15 and 27, which equals 42."
else
    echo "## Output"
    echo ""
    echo "15 + 27 = 42"
    echo ""
    echo "The sum of 15 and 27 is 42."
fi
`,
				},
			}),
			harness.NewStep("Run the oneshot job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()

				cmd := ctx.Command(flow, "plan", "run", "--next", "--yes", "plans/oneshot-summary-test").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("'flow plan run' failed: %w", result.Error)
				}

				// Give some time for async summarization to complete
				time.Sleep(5 * time.Second)

				return nil
			}),
			harness.NewStep("Verify job file has output and summary", func(ctx *harness.Context) error {
				jobPath := filepath.Join(ctx.RootDir, "plans", "oneshot-summary-test", "01-calculate.md")
				content, err := fs.ReadString(jobPath)
				if err != nil {
					return fmt.Errorf("failed to read job file: %w", err)
				}

				if !strings.Contains(content, "status: completed") {
					return fmt.Errorf("job status was not updated to 'completed'")
				}

				if !strings.Contains(content, "## Output") {
					return fmt.Errorf("job output was not appended")
				}

				if !strings.Contains(content, "15 + 27 = 42") {
					return fmt.Errorf("expected calculation result not found in output")
				}

				if !strings.Contains(content, "summary: Calculated the sum of 15 and 27, which equals 42.") {
					return fmt.Errorf("job frontmatter does not contain the expected summary:\n%s", content)
				}

				return nil
			}),
		},
	}
}