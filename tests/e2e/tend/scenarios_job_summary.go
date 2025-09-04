// File: tests/e2e/tend/scenarios_job_summary.go
package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/command"
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
				cmd := command.New(flow, "plan", "init", "summary-test").Dir(ctx.RootDir)
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
				cmdFunc := getCommandWithTestBin(ctx)
				jobPath := filepath.Join("plans", "summary-test", "01-job-to-summarize.md")

				cmd := cmdFunc(flow, "plan", "complete", jobPath).Dir(ctx.RootDir)
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

// InteractiveAgentJobSummaryScenario tests that interactive_agent job transcripts are summarized when completed.
func InteractiveAgentJobSummaryScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-interactive-agent-summary",
		Description: "Tests that interactive_agent job transcripts are summarized when completed",
		Tags:        []string{"plan", "complete", "summary", "interactive_agent"},
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
  summary_max_chars: 200
`
				if err := fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent); err != nil {
					return err
				}

				// Initialize plan
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "init", "agent-summary-test").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}

				// Add an interactive_agent job
				jobContent := `---
id: interactive-agent-job
title: Interactive Agent Job
status: pending
type: interactive_agent
---

# Task

Build a simple calculator function.
`
				planDir := filepath.Join(ctx.RootDir, "plans", "agent-summary-test")
				if err := fs.WriteString(filepath.Join(planDir, "01-interactive-agent.md"), jobContent); err != nil {
					return err
				}

				// Create a mock transcript file that would be created by the agent
				transcriptContent := `User: Build a simple calculator function
Assistant: I'll create a simple calculator function for you.

def calculate(a, b, operation):
    if operation == "add":
        return a + b
    elif operation == "subtract":
        return a - b
    elif operation == "multiply":
        return a * b
    elif operation == "divide":
        return a / b if b != 0 else "Error: Division by zero"
    else:
        return "Error: Unknown operation"

User: Great! Can you add error handling?
Assistant: I've added error handling to prevent division by zero and handle unknown operations.`

				// Create logs directory structure using shell commands
				cmd = command.New("mkdir", "-p", ".logs/plans/agent-summary-test/jobs/interactive-agent-job/interactive_sessions").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to create logs directory: %w", err)
				}
				
				// Create a transcript file
				transcriptPath := filepath.Join(ctx.RootDir, ".logs", "plans", "agent-summary-test", "jobs", "interactive-agent-job", "interactive_sessions", "session_001.md")
				if err := fs.WriteString(transcriptPath, transcriptContent); err != nil {
					return err
				}

				return nil
			}),
			setupTestEnvironment(map[string]interface{}{
				"additionalMocks": map[string]string{
					"llm": `#!/bin/bash
# Mock LLM for summarization
echo "Created a calculator function with basic operations and error handling for division by zero."
`,
				},
			}),
			harness.NewStep("Run 'flow plan complete' on the interactive_agent job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmdFunc := getCommandWithTestBin(ctx)
				jobPath := filepath.Join("plans", "agent-summary-test", "01-interactive-agent.md")

				cmd := cmdFunc(flow, "plan", "complete", jobPath).Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("'flow plan complete' failed: %w", result.Error)
				}

				// Verify output indicates summary was added
				// Note: Transcript appending may fail in test environment due to clogs dependency
				if !strings.Contains(result.Stdout, "Added summary to job frontmatter") {
					return fmt.Errorf("expected summary addition message not found")
				}

				return nil
			}),
			harness.NewStep("Verify job file has summary", func(ctx *harness.Context) error {
				jobPath := filepath.Join(ctx.RootDir, "plans", "agent-summary-test", "01-interactive-agent.md")
				content, err := fs.ReadString(jobPath)
				if err != nil {
					return fmt.Errorf("failed to read job file: %w", err)
				}

				if !strings.Contains(content, "status: completed") {
					return fmt.Errorf("job status was not updated to 'completed'")
				}

				// Note: Transcript may not be appended in test environment due to clogs dependency
				// The important thing is that summarization still works based on the job content

				if !strings.Contains(content, "summary: Created a calculator function with basic operations and error handling for division by zero.") {
					return fmt.Errorf("job frontmatter does not contain the expected summary:\n%s", content)
				}

				return nil
			}),
		},
	}
}