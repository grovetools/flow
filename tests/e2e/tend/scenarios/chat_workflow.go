package scenarios

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/assert"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// BlockInfo represents information about an extractable block (for JSON parsing)
type BlockInfo struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	LineStart int    `json:"line_start"`
	Preview   string `json:"preview"`
}

var ChatAndExtractWorkflowScenario = harness.NewScenario(
	"chat-workflow",
	"Validates turn-based chat initialization and LLM response.",
	[]string{"core", "cli", "chat"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment and mocks", func(ctx *harness.Context) error {
			_, _, err := setupDefaultEnvironment(ctx, "chat-project")
			if err != nil {
				return err
			}

			// Create mock LLM response file
			mockResponse := `<!-- grove: {"id": "block-123"} -->
This is the LLM response to discuss the feature.`
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
			harness.Mock{CommandName: "grove"}, // For aglogs
		),

		harness.NewStep("Initialize chat job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			// Create chat directory in notebook
			projectName := "chat-project"
			chatsDir := filepath.Join(notebooksRoot, "workspaces", projectName, "chats")
			if err := fs.CreateDir(chatsDir); err != nil {
				return err
			}

			chatFilePath := filepath.Join(chatsDir, "chat-topic.md")
			ctx.Set("chat_file_path", chatFilePath)
			ctx.Set("chat_file_name", "chat-topic.md") // Store just the filename for later assertions

			// Initial chat content - just user's message (no LLM response yet)
			initialContent := `# My Chat Topic

Let's discuss a feature.`
			if err := fs.WriteString(chatFilePath, initialContent); err != nil {
				return err
			}

			// Use a non-gemini model so it uses the llm mock command
			cmd := ctx.Bin("chat", "-s", chatFilePath, "--model", "claude-3-5-sonnet-20241022")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			return assert.YAMLFieldExists(chatFilePath, "type", "Chat file should have frontmatter")
		}),

		harness.NewStep("Run chat to get LLM response", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			llmResponseFile := ctx.GetString("llm_response_file")
			chatFilePath := ctx.GetString("chat_file_path")

			// Run the chat by passing the file path explicitly
			cmd := ctx.Bin("chat", "run", chatFilePath)
			cmd.Dir(projectDir)
			cmd.Env(fmt.Sprintf("GROVE_MOCK_LLM_RESPONSE_FILE=%s", llmResponseFile))
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify the chat file contains an LLM response section with directive
			if err := fs.AssertContains(chatFilePath, "## LLM Response"); err != nil {
				return err
			}

			// Verify the chat file contains a grove directive (the chat executor adds one automatically)
			if err := fs.AssertContains(chatFilePath, "<!-- grove: {\"id\":"); err != nil {
				return err
			}

			// Verify the chat file contains the expected mock response content
			if err := fs.AssertContains(chatFilePath, "This is the LLM response to discuss the feature."); err != nil {
				return err
			}

			// Verify job status was updated to pending_user (waiting for next user input)
			job, err := orchestration.LoadJob(chatFilePath)
			if err != nil {
				return fmt.Errorf("failed to load chat job: %w", err)
			}

			if job.Status != orchestration.JobStatusPendingUser {
				return fmt.Errorf("expected job status 'pending_user', got '%s'", job.Status)
			}

			// Store job ID for next step
			ctx.Set("chat_job_id", job.ID)

			return nil
		}),

		harness.NewStep("Verify standalone chat creates artifacts (logs and briefings)", func(ctx *harness.Context) error {
			chatFilePath := ctx.GetString("chat_file_path")
			jobID := ctx.GetString("chat_job_id")

			// Standalone chats create .artifacts/ in the same directory as the chat file
			chatDir := filepath.Dir(chatFilePath)
			artifactsDir := filepath.Join(chatDir, ".artifacts", jobID)

			// Verify artifacts directory was created
			if err := fs.AssertExists(artifactsDir); err != nil {
				return fmt.Errorf("artifacts directory not created for standalone chat: %w", err)
			}

			// Verify job.log was created and has content
			jobLogPath := filepath.Join(artifactsDir, "job.log")
			if err := fs.AssertExists(jobLogPath); err != nil {
				return fmt.Errorf("job.log not created for standalone chat: %w", err)
			}

			// Verify log contains output (either context info or warning about missing context)
			// Both indicate logging is working
			if err := fs.AssertContains(jobLogPath, "context"); err != nil {
				return fmt.Errorf("job.log appears empty or missing expected content: %w", err)
			}

			// Verify at least one briefing file was created
			briefingPattern := filepath.Join(artifactsDir, "briefing-*.xml")
			briefingFiles, err := filepath.Glob(briefingPattern)
			if err != nil {
				return fmt.Errorf("error searching for briefing files: %w", err)
			}
			if len(briefingFiles) == 0 {
				return fmt.Errorf("no briefing files found in %s", artifactsDir)
			}

			// Verify briefing file contains expected XML structure
			if err := fs.AssertContains(briefingFiles[0], "<prompt>"); err != nil {
				return fmt.Errorf("briefing file missing <prompt> tag: %w", err)
			}
			// Verify new conversation structure
			if err := fs.AssertContains(briefingFiles[0], "<conversation>"); err != nil {
				return fmt.Errorf("briefing file missing <conversation> tag: %w", err)
			}

			return nil
		}),

		harness.NewStep("List extractable blocks", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			chatFilePath := ctx.GetString("chat_file_path")

			cmd := ctx.Bin("plan", "extract", "list", "--file", chatFilePath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Assert that the known block ID is listed
			return assert.Contains(result.Stdout, "ID: block-123", "should list the mock block ID")
		}),

		harness.NewStep("List extractable blocks as JSON", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			chatFilePath := ctx.GetString("chat_file_path")

			cmd := ctx.Bin("plan", "extract", "list", "--file", chatFilePath, "--json")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Assert output is valid JSON and contains the block
			var blocks []BlockInfo
			if err := json.Unmarshal([]byte(result.Stdout), &blocks); err != nil {
				return fmt.Errorf("failed to parse JSON output: %w", err)
			}

			if len(blocks) == 0 {
				return fmt.Errorf("expected at least one block in JSON output, got none")
			}

			found := false
			for _, block := range blocks {
				if block.ID == "block-123" {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("mock block ID 'block-123' not found in JSON output")
			}
			return nil
		}),

		harness.NewStep("Initialize a new plan for extracted jobs", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "init", "extracted-plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Extract a single block into the new plan", func(ctx *harness.Context) error {
			notebooksRoot := ctx.GetString("notebooks_root")
			chatFilePath := ctx.GetString("chat_file_path")
			chatFileName := ctx.GetString("chat_file_name")
			planDir := filepath.Join(notebooksRoot, "workspaces/chat-project/plans/extracted-plan")

			// Extract into the plan we just created
			cmd := ctx.Bin("plan", "extract", "block-123", "--file", chatFilePath, "--title", "Extracted Single Block")
			cmd.Dir(planDir) // Run from within the new plan dir

			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify the new job file and its content
			extractedJobPath := filepath.Join(planDir, "01-extracted-single-block.md")
			if err := fs.AssertExists(extractedJobPath); err != nil {
				return err
			}

			expectedSourceBlock := fmt.Sprintf("%s#block-123", chatFileName)
			return assert.YAMLField(extractedJobPath, "source_block", expectedSourceBlock, "source_block should reference the correct block")
		}),

		harness.NewStep("Extract all content into a new job", func(ctx *harness.Context) error {
			notebooksRoot := ctx.GetString("notebooks_root")
			chatFilePath := ctx.GetString("chat_file_path")
			chatFileName := ctx.GetString("chat_file_name")
			planDir := filepath.Join(notebooksRoot, "workspaces/chat-project/plans/extracted-plan")

			cmd := ctx.Bin("plan", "extract", "all", "--file", chatFilePath, "--title", "Extracted Full Chat")
			cmd.Dir(planDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify the new job file and its content
			extractedJobPath := filepath.Join(planDir, "02-extracted-full-chat.md")
			if err := fs.AssertExists(extractedJobPath); err != nil {
				return err
			}

			return assert.YAMLField(extractedJobPath, "source_block", chatFileName, "source_block should reference the file without a fragment")
		}),

		harness.NewStep("Attempt to extract an invalid block ID", func(ctx *harness.Context) error {
			notebooksRoot := ctx.GetString("notebooks_root")
			chatFilePath := ctx.GetString("chat_file_path")
			planDir := filepath.Join(notebooksRoot, "workspaces/chat-project/plans/extracted-plan")

			cmd := ctx.Bin("plan", "extract", "invalid-id", "--file", chatFilePath, "--title", "Invalid Extraction")
			cmd.Dir(planDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Command should fail because no valid blocks were found
			if err := result.AssertFailure(); err != nil {
				return fmt.Errorf("expected command to fail for invalid block ID, but it succeeded: %w", err)
			}

			// Verify no new job file was created
			invalidJobPath := filepath.Join(planDir, "03-invalid-extraction.md")
			return fs.AssertNotExists(invalidJobPath)
		}),

		harness.NewStep("Attempt to extract from a missing file", func(ctx *harness.Context) error {
			notebooksRoot := ctx.GetString("notebooks_root")
			planDir := filepath.Join(notebooksRoot, "workspaces/chat-project/plans/extracted-plan")

			cmd := ctx.Bin("plan", "extract", "list", "--file", "non-existent-file.md")
			cmd.Dir(planDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			if err := result.AssertFailure(); err != nil {
				return fmt.Errorf("expected command to fail for missing file, but it succeeded: %w", err)
			}
			return assert.Contains(result.Stderr, "not found", "error message should indicate file not found")
		}),
	},
)
