package scenarios

import (
	"fmt"
	"path/filepath"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

var TitleBasedRunScenario = harness.NewScenario(
	"title-based-run",
	"Validates that 'flow run <title>' resolves jobs by title in active plan and notebook directories.",
	[]string{"core", "cli", "run", "title-lookup"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment and mocks", func(ctx *harness.Context) error {
			_, _, err := setupDefaultEnvironment(ctx, "title-run-project")
			if err != nil {
				return err
			}

			// Create mock LLM response file
			mockResponse := `This is a mock LLM response for title-based run test.`
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			if err := fs.WriteString(responseFile, mockResponse); err != nil {
				return err
			}
			ctx.Set("llm_response_file", responseFile)

			return nil
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "grove"},
		),

		// Test Case 1: Title lookup in active plan
		harness.NewStep("Create plan with jobs for title lookup", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			// Initialize a plan
			initCmd := ctx.Bin("plan", "init", "title-test-plan")
			initCmd.Dir(projectDir)
			if err := initCmd.Run().AssertSuccess(); err != nil {
				return err
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "title-run-project", "plans", "title-test-plan")
			ctx.Set("plan_path", planPath)

			// Add a chat job with a specific title
			addCmd := ctx.Bin("plan", "add", "title-test-plan",
				"--type", "chat",
				"--title", "my-special-chat",
				"-p", "User message for title lookup test.")
			addCmd.Dir(projectDir)
			if err := addCmd.Run().AssertSuccess(); err != nil {
				return err
			}

			// Add another shell job
			addCmd2 := ctx.Bin("plan", "add", "title-test-plan",
				"--type", "shell",
				"--title", "build-project",
				"-p", "echo 'Building project...'")
			addCmd2.Dir(projectDir)
			return addCmd2.Run().AssertSuccess()
		}),

		harness.NewStep("Set active plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			setCmd := ctx.Bin("plan", "set", "title-test-plan")
			setCmd.Dir(projectDir)
			return setCmd.Run().AssertSuccess()
		}),

		harness.NewStep("Run job by title in active plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			responseFile := ctx.GetString("llm_response_file")

			// Run using just the title (no path)
			runCmd := ctx.Bin("run", "my-special-chat")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			result := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("title-based run failed: %w", err)
			}

			// Verify the chat file was updated with LLM response
			planPath := ctx.GetString("plan_path")
			chatFile := filepath.Join(planPath, "01-my-special-chat.md")
			return fs.AssertContains(chatFile, "This is a mock LLM response")
		}),

		harness.NewStep("Run shell job by title in active plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Run using just the title (no path)
			runCmd := ctx.Bin("run", "build-project", "--yes")
			runCmd.Dir(projectDir)
			result := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("title-based shell run failed: %w", err)
			}

			// Verify shell job output was captured
			return nil
		}),

		// Test Case 2: Explicit path still works
		harness.NewStep("Verify explicit path takes precedence", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			responseFile := ctx.GetString("llm_response_file")

			// Create a second chat job to test explicit path
			addCmd := ctx.Bin("plan", "add", "title-test-plan",
				"--type", "chat",
				"--title", "explicit-path-chat",
				"-p", "Testing explicit path.")
			addCmd.Dir(projectDir)
			if err := addCmd.Run().AssertSuccess(); err != nil {
				return err
			}

			// Run using explicit path
			chatFile := filepath.Join(planPath, "03-explicit-path-chat.md")
			runCmd := ctx.Bin("run", chatFile)
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			result := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), result.Stdout, result.Stderr)

			return result.AssertSuccess()
		}),

		// Test Case 3: Multiple jobs with title-based lookup
		harness.NewStep("Add another job with different title", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			responseFile := ctx.GetString("llm_response_file")

			// Add a third chat job
			addCmd := ctx.Bin("plan", "add", "title-test-plan",
				"--type", "chat",
				"--title", "another-chat-job",
				"-p", "Testing another chat job.")
			addCmd.Dir(projectDir)
			if err := addCmd.Run().AssertSuccess(); err != nil {
				return err
			}

			// Run using the title
			runCmd := ctx.Bin("run", "another-chat-job")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			result := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("title-based run for another-chat-job failed: %w", err)
			}

			// Verify the chat file was updated with LLM response
			planPath := ctx.GetString("plan_path")
			chatFile := filepath.Join(planPath, "04-another-chat-job.md")
			return fs.AssertContains(chatFile, "This is a mock LLM response")
		}),

		// Test Case 5: Error handling for non-existent title
		harness.NewStep("Verify helpful error for non-existent title", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Try to run with a non-existent title
			runCmd := ctx.Bin("run", "non-existent-job-title")
			runCmd.Dir(projectDir)
			result := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), result.Stdout, result.Stderr)

			// Should fail with helpful error message
			if err := result.AssertFailure(); err != nil {
				return fmt.Errorf("expected command to fail for non-existent title: %w", err)
			}

			// Error should mention the title
			if result.Stderr == "" || (result.Stdout == "" && result.Stderr == "") {
				return fmt.Errorf("expected error output for non-existent title")
			}

			return nil
		}),
	},
)
