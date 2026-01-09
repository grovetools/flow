package scenarios

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-tend/pkg/verify"
)

var JobLogCaptureScenario = harness.NewScenario(
	"job-log-capture",
	"Verifies that all logging output including context summaries is captured in job.log files",
	[]string{"core", "logging", "oneshot", "chat"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "log-capture-project")
			if err != nil {
				return err
			}

			// Initialize git repository in notebooks workspace to prevent context generation
			// from escaping the sandbox when searching upward from plan directory
			workspaceDir := filepath.Join(notebooksRoot, "workspaces", "log-capture-project")
			if err := fs.CreateDir(workspaceDir); err != nil {
				return err
			}
			if _, err := git.SetupTestRepo(workspaceDir); err != nil {
				return err
			}

			// Create a simple Go project to trigger context generation
			if err := fs.WriteString(filepath.Join(projectDir, "go.mod"), "module log-capture-project\n\ngo 1.22\n"); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "main.go"), "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello, World!\")\n}\n"); err != nil {
				return err
			}

			// Create context rules file to trigger context generation
			rulesDir := filepath.Join(projectDir, ".grove")
			if err := fs.CreateDir(rulesDir); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(rulesDir, "rules"), "*.go\n"); err != nil {
				return err
			}

			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"}, // Mocks `grove llm request`
			// Use real cx binary instead of mock to generate context properly
		),

		harness.NewStep("Create mock LLM response", func(ctx *harness.Context) error {
			responseContent := "This is the mock LLM response for testing."
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
			projectName := "log-capture-project"
			planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", "log-test-plan")
			ctx.Set("plan_path", planPath)

			// Init plan inside the project
			initCmd := ctx.Bin("plan", "init", "log-test-plan")
			initCmd.Dir(projectDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w\nStderr: %s", result.Error, result.Stderr)
			}

			// Add oneshot job that references an include file
			addCmd := ctx.Bin("plan", "add", "log-test-plan",
				"--type", "oneshot",
				"--title", "test-oneshot-logging",
				"--include", "main.go",
				"-p", "Review the code in main.go")
			addCmd.Dir(projectDir)

			result := addCmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Edit the job file to use gemini model (which the mock supports)
			// We need to add model to the frontmatter since plan add doesn't set it
			jobPath := filepath.Join(planPath, "01-test-oneshot-logging.md")
			content, err := fs.ReadString(jobPath)
			if err != nil {
				return fmt.Errorf("reading job file: %w", err)
			}
			// Add model field after the first line (---)
			lines := strings.Split(content, "\n")
			if len(lines) > 1 {
				// Insert model after opening ---
				newLines := append([]string{lines[0], "model: gemini-2.0-flash"}, lines[1:]...)
				updatedContent := strings.Join(newLines, "\n")
				if err := fs.WriteString(jobPath, updatedContent); err != nil {
					return fmt.Errorf("updating job file: %w", err)
				}
			}

			return nil
		}),

		harness.NewStep("Run oneshot job with custom output writer", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			llmResponseFile := ctx.GetString("llm_response_file")
			jobPath := filepath.Join(planPath, "01-test-oneshot-logging.md")

			// Load the job and plan
			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading job file: %w", err)
			}
			job.FilePath = jobPath // Set FilePath manually since LoadJob doesn't do it

			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return fmt.Errorf("loading plan: %w", err)
			}

			// Create job.log file (like TUI does)
			jobLogPath, err := orchestration.GetJobLogPath(plan, job)
			if err != nil {
				return fmt.Errorf("getting job log path: %w", err)
			}
			ctx.Set("oneshot_job_log_path", jobLogPath)

			logFile, err := os.OpenFile(jobLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return fmt.Errorf("creating job log file: %w", err)
			}
			defer logFile.Close()

			// Create a MultiWriter like the TUI does - write to both log file and a buffer
			var buffer bytes.Buffer
			multiWriter := io.MultiWriter(logFile, &buffer)

			// Set environment for mock LLM
			os.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", llmResponseFile)
			defer os.Unsetenv("GROVE_MOCK_LLM_RESPONSE_FILE")

			// Create orchestrator with test-aware executor and SkipInteractive to avoid prompts in tests
			orch, err := orchestration.NewOrchestrator(plan, &orchestration.OrchestratorConfig{
				SkipInteractive: true,
				CommandExecutor: ctx.CommandExecutor(),
			})
			if err != nil {
				return fmt.Errorf("creating orchestrator: %w", err)
			}

			// Execute the job with the custom writer
			if err := orch.ExecuteJobWithWriter(context.Background(), job, multiWriter); err != nil {
				return fmt.Errorf("executing job: %w\nBuffer: %s", err, buffer.String())
			}

			return nil
		}),

		harness.NewStep("Verify oneshot job.log contains context summary", func(ctx *harness.Context) error {
			jobLogPath := ctx.GetString("oneshot_job_log_path")

			// Verify job.log exists
			if err := ctx.Check("job.log file exists", fs.AssertExists(jobLogPath)); err != nil {
				return err
			}

			// Verify job.log contains context generation output
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("job.log contains context summary header", nil, fs.AssertContains(jobLogPath, "Context Summary"))
				v.Equal("job.log contains total files count", nil, fs.AssertContains(jobLogPath, "Total Files"))
				v.Equal("job.log contains total tokens", nil, fs.AssertContains(jobLogPath, "Total Tokens"))
			})
		}),

		harness.NewStep("Add chat job to the same plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Add chat job
			addCmd := ctx.Bin("plan", "add", "log-test-plan",
				"--type", "chat",
				"--title", "test-chat-logging",
				"-p", "Discuss the main.go implementation")
			addCmd.Dir(projectDir)

			result := addCmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Edit the job file to use gemini model (which the mock supports)
			// We need to add model to the frontmatter since plan add doesn't set it
			jobPath := filepath.Join(planPath, "02-test-chat-logging.md")
			content, err := fs.ReadString(jobPath)
			if err != nil {
				return fmt.Errorf("reading job file: %w", err)
			}
			// Add model field after the first line (---)
			lines := strings.Split(content, "\n")
			if len(lines) > 1 {
				// Insert model after opening ---
				newLines := append([]string{lines[0], "model: gemini-2.0-flash"}, lines[1:]...)
				updatedContent := strings.Join(newLines, "\n")
				if err := fs.WriteString(jobPath, updatedContent); err != nil {
					return fmt.Errorf("updating job file: %w", err)
				}
			}

			return nil
		}),

		harness.NewStep("Run chat job with custom output writer", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			llmResponseFile := ctx.GetString("llm_response_file")
			jobPath := filepath.Join(planPath, "02-test-chat-logging.md")

			// Load the job and plan
			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading job file: %w", err)
			}
			job.FilePath = jobPath // Set FilePath manually since LoadJob doesn't do it

			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return fmt.Errorf("loading plan: %w", err)
			}

			// Create job.log file (like TUI does)
			jobLogPath, err := orchestration.GetJobLogPath(plan, job)
			if err != nil {
				return fmt.Errorf("getting job log path: %w", err)
			}
			ctx.Set("chat_job_log_path", jobLogPath)

			logFile, err := os.OpenFile(jobLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return fmt.Errorf("creating job log file: %w", err)
			}
			defer logFile.Close()

			// Create a MultiWriter like the TUI does - write to both log file and a buffer
			var buffer bytes.Buffer
			multiWriter := io.MultiWriter(logFile, &buffer)

			// Set environment for mock LLM
			os.Setenv("GROVE_MOCK_LLM_RESPONSE_FILE", llmResponseFile)
			defer os.Unsetenv("GROVE_MOCK_LLM_RESPONSE_FILE")

			// Create orchestrator with test-aware executor and SkipInteractive to avoid prompts in tests
			orch, err := orchestration.NewOrchestrator(plan, &orchestration.OrchestratorConfig{
				SkipInteractive: true,
				CommandExecutor: ctx.CommandExecutor(),
			})
			if err != nil {
				return fmt.Errorf("creating orchestrator: %w", err)
			}

			// Execute the job with the custom writer
			if err := orch.ExecuteJobWithWriter(context.Background(), job, multiWriter); err != nil {
				return fmt.Errorf("executing chat job: %w\nBuffer: %s", err, buffer.String())
			}

			return nil
		}),

		harness.NewStep("Verify chat job.log contains context summary", func(ctx *harness.Context) error {
			jobLogPath := ctx.GetString("chat_job_log_path")

			// Verify job.log exists
			if err := ctx.Check("chat job.log file exists", fs.AssertExists(jobLogPath)); err != nil {
				return err
			}

			// Verify job.log contains context generation output
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("chat job.log contains context summary header", nil, fs.AssertContains(jobLogPath, "Context Summary"))
				v.Equal("chat job.log contains total files count", nil, fs.AssertContains(jobLogPath, "Total Files"))
				v.Equal("chat job.log contains total tokens", nil, fs.AssertContains(jobLogPath, "Total Tokens"))
			})
		}),

		harness.NewStep("Add shell job to the same plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			addCmd := ctx.Bin("plan", "add", "log-test-plan",
				"--type", "shell",
				"--title", "test-shell-logging",
				"-p", "echo 'This is a shell job execution'")
			addCmd.Dir(projectDir)

			result := addCmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return err
			}
			return nil
		}),

		harness.NewStep("Run shell job with custom output writer", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobPath := filepath.Join(planPath, "03-test-shell-logging.md")

			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("loading job file: %w", err)
			}
			job.FilePath = jobPath

			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return fmt.Errorf("loading plan: %w", err)
			}

			jobLogPath, err := orchestration.GetJobLogPath(plan, job)
			if err != nil {
				return fmt.Errorf("getting job log path: %w", err)
			}
			ctx.Set("shell_job_log_path", jobLogPath)

			logFile, err := os.OpenFile(jobLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return fmt.Errorf("creating job log file: %w", err)
			}
			defer logFile.Close()

			var buffer bytes.Buffer
			multiWriter := io.MultiWriter(logFile, &buffer)

			// Create orchestrator with test-aware executor and SkipInteractive to avoid prompts in tests
			orch, err := orchestration.NewOrchestrator(plan, &orchestration.OrchestratorConfig{
				SkipInteractive: true,
				CommandExecutor: ctx.CommandExecutor(),
			})
			if err != nil {
				return fmt.Errorf("creating orchestrator: %w", err)
			}

			if err := orch.ExecuteJobWithWriter(context.Background(), job, multiWriter); err != nil {
				return fmt.Errorf("executing shell job: %w\nBuffer: %s", err, buffer.String())
			}

			return nil
		}),

		harness.NewStep("Verify shell job.log contains context summary", func(ctx *harness.Context) error {
			jobLogPath := ctx.GetString("shell_job_log_path")

			if err := ctx.Check("shell job.log file exists", fs.AssertExists(jobLogPath)); err != nil {
				return err
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("shell job.log contains context summary header", nil, fs.AssertContains(jobLogPath, "Context Summary"))
				v.Equal("shell job.log contains total files count", nil, fs.AssertContains(jobLogPath, "Total Files"))
				v.Equal("shell job.log contains total tokens", nil, fs.AssertContains(jobLogPath, "Total Tokens"))
				v.Equal("shell job.log contains shell job output", nil, fs.AssertContains(jobLogPath, "This is a shell job execution"))
			})
		}),

		harness.NewStep("Run shell job via 'flow plan run' and verify log capture", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			shellJobPath := filepath.Join(planPath, "03-test-shell-logging.md")

			// First, reset the job status to pending so we can run it again
			content, err := os.ReadFile(shellJobPath)
			if err != nil {
				return fmt.Errorf("failed to read job file: %w", err)
			}

			updates := map[string]interface{}{
				"status": "pending",
			}
			newContent, err := orchestration.UpdateFrontmatter(content, updates)
			if err != nil {
				return fmt.Errorf("failed to update frontmatter: %w", err)
			}

			if err := os.WriteFile(shellJobPath, newContent, 0644); err != nil {
				return fmt.Errorf("failed to write job file: %w", err)
			}

			// Clear the existing log file to verify new content
			jobLogPath := ctx.GetString("shell_job_log_path")
			if err := os.Remove(jobLogPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to clear log file: %w", err)
			}

			// Run the shell job using the CLI command
			runCmd := ctx.Bin("plan", "run", shellJobPath, "--yes")
			runCmd.Dir(projectDir)
			result := runCmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("CLI 'plan run' failed: %w\nOutput: %s", err, result.Stdout+result.Stderr)
			}

			// Verify its log file was created and has content
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("CLI shell job.log contains context summary", nil, fs.AssertContains(jobLogPath, "Context Summary"))
				v.Equal("CLI shell job.log contains shell output", nil, fs.AssertContains(jobLogPath, "This is a shell job execution"))
			})
		}),

		harness.NewStep("Run chat job via 'flow run' and verify log capture", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			chatJobPath := filepath.Join(planPath, "02-test-chat-logging.md")
			llmResponseFile := ctx.GetString("llm_response_file")

			// Reset the chat job to pending_user status and remove LLM responses
			content, err := os.ReadFile(chatJobPath)
			if err != nil {
				return fmt.Errorf("failed to read job file: %w", err)
			}

			// Parse to get frontmatter and body
			frontmatter, body, err := orchestration.ParseFrontmatter(content)
			if err != nil {
				return fmt.Errorf("failed to parse frontmatter: %w", err)
			}

			// Update status
			frontmatter["status"] = "pending_user"

			// Rebuild the file with just the initial user prompt (before any LLM responses)
			// Find the first "<!-- grove:" directive which marks the LLM turn
			bodyStr := string(body)
			llmTurnIndex := strings.Index(bodyStr, "<!-- grove: {\"id\":")
			if llmTurnIndex > 0 {
				// Truncate at the LLM turn
				bodyStr = bodyStr[:llmTurnIndex]
			}

			// Rebuild
			newContent, err := orchestration.RebuildMarkdownWithFrontmatter(frontmatter, []byte(bodyStr))
			if err != nil {
				return fmt.Errorf("failed to rebuild markdown: %w", err)
			}

			if err := os.WriteFile(chatJobPath, newContent, 0644); err != nil {
				return fmt.Errorf("failed to write job file: %w", err)
			}

			// Clear the existing log file to verify new content
			jobLogPath := ctx.GetString("chat_job_log_path")
			if err := os.Remove(jobLogPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to clear log file: %w", err)
			}

			// Run the chat job using the unified 'flow run' command
			runCmd := ctx.Bin("run", chatJobPath)
			runCmd.Dir(projectDir).Env(fmt.Sprintf("GROVE_MOCK_LLM_RESPONSE_FILE=%s", llmResponseFile))
			result := runCmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("CLI 'run' failed: %w\nOutput: %s", err, result.Stdout+result.Stderr)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("CLI chat job.log contains context summary", nil, fs.AssertContains(jobLogPath, "Context Summary"))
			})
		}),
	},
)
