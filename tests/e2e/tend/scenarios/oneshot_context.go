package scenarios

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

var OneshotWithContextScenario = harness.NewScenario(
	"oneshot-with-context",
	"Ensures a oneshot job correctly triggers context generation and uses it.",
	[]string{"core", "cli", "oneshot", "context"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "context-project")
			if err != nil {
				return err
			}

			// Create Go project files
			if err := fs.WriteString(filepath.Join(projectDir, "go.mod"), "module context-project\n\ngo 1.22\n"); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "main.go"), "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello, World!\")\n}\n"); err != nil {
				return err
			}

			// Create context rules file - this is critical for triggering context generation
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
			responseContent := "This is the mock LLM response."
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			if err := fs.WriteString(responseFile, responseContent); err != nil {
				return err
			}
			ctx.Set("llm_response_file", responseFile)
			return nil
		}),

		harness.NewStep("Initialize plan and add oneshot job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			projectName := "context-project"
			planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", "context-test-plan")
			ctx.Set("plan_path", planPath)

			// Init plan inside the project
			initCmd := ctx.Bin("plan", "init", "context-test-plan")
			initCmd.Dir(projectDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w\nStderr: %s", result.Error, result.Stderr)
			}

			// Add job that references a source file
			addCmd := ctx.Bin("plan", "add", "context-test-plan",
				"--type", "oneshot",
				"--title", "review-code",
				"--source-files", "main.go",
				"-p", "Review the code in main.go")
			addCmd.Dir(projectDir)

			result := addCmd.Run()
			return result.AssertSuccess()
		}),

		harness.NewStep("Run the plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			llmResponseFile := ctx.GetString("llm_response_file")

			runCmd := ctx.Bin("plan", "run", "--all", "--yes")
			runCmd.Dir(projectDir)
			runCmd.Env(fmt.Sprintf("GROVE_MOCK_LLM_RESPONSE_FILE=%s", llmResponseFile))

			result := runCmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan run failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}
			return nil
		}),

		harness.NewStep("Verify context generation and job output", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			jobPath := filepath.Join(planPath, "01-review-code.md")

			// 1. Verify context file was created (either by mock or real cx)
			// The important part is that context generation was triggered
			contextFile := filepath.Join(projectDir, ".grove", "context")
			if err := fs.AssertExists(contextFile); err != nil {
				return fmt.Errorf("context file not found: %w", err)
			}

			// 2. Verify job output contains the LLM response
			if err := fs.AssertExists(jobPath); err != nil {
				return fmt.Errorf("job file not found: %w", err)
			}
			if err := fs.AssertContains(jobPath, "## Output"); err != nil {
				return fmt.Errorf("job file missing Output section: %w", err)
			}
			if err := fs.AssertContains(jobPath, "This is the mock LLM response."); err != nil {
				return fmt.Errorf("job output missing LLM response: %w", err)
			}

			// 3. Verify job status is completed
			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading job file: %w", err)
			}
			if job.Status != orchestration.JobStatusCompleted {
				return fmt.Errorf("expected job status to be 'completed', but was '%s'", job.Status)
			}

			// 4. Additional verification using assert package
			if err := assert.YAMLContains(jobPath, "status", "completed"); err != nil {
				return fmt.Errorf("YAML assertion failed: %w", err)
			}

			return nil
		}),
	},
)
