package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

var AgentFromChatScenario = harness.NewScenario(
	"agent-from-chat-template",
	"Tests the agent-from-chat template that auto-generates a detailed XML plan from a chat dependency.",
	[]string{"core", "cli", "template", "agent", "chat"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "agent-from-chat-project")
			if err != nil {
				return err
			}

			// Create a git repo for the project
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Test Project\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			// Create mock LLM response file for plan generation
			mockPlanResponse := `<file path="main.go">
  <change type="add" after="package main">
func hello() string {
	return "Hello, World!"
}
  </change>
</file>

This is a detailed implementation plan generated from the chat.`
			responseFile := filepath.Join(ctx.RootDir, "mock_plan_response.txt")
			return fs.WriteString(responseFile, mockPlanResponse)
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},  // Mocks `grove llm request`
			harness.Mock{CommandName: "claude"}, // Mock claude to prevent actual agent launch
			harness.Mock{CommandName: "tmux"},   // Mock tmux to prevent real sessions
		),

		harness.NewStep("Initialize plan", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			cmd := ctx.Bin("plan", "init", "test-plan")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("Add and complete a chat job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			projectName := "agent-from-chat-project"
			planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", "test-plan")
			ctx.Set("plan_path", planPath)

			// Add a chat job
			cmd := ctx.Bin("plan", "add", "test-plan",
				"--type", "chat",
				"--title", "Design Feature",
				"-p", "I want to create a simple hello world function in Go")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Mark the chat as completed and add design content
			chatJobPath := filepath.Join(planPath, "01-design-feature.md")
			content, err := fs.ReadString(chatJobPath)
			if err != nil {
				return fmt.Errorf("reading chat job file: %w", err)
			}

			// Update status to completed and add chat conversation
			updatedContent := strings.Replace(content, "status: pending_user", "status: completed", 1)
			updatedContent += "\n## Chat Conversation\n\n**User:** I want to create a simple hello world function in Go\n\n**Assistant:** Great! I'll create a function that returns \"Hello, World!\". We should:\n1. Create a main.go file\n2. Add a hello() function that returns the greeting\n3. Make it reusable\n"
			if err := fs.WriteString(chatJobPath, updatedContent); err != nil {
				return fmt.Errorf("updating chat job: %w", err)
			}

			return nil
		}),

		harness.NewStep("Add interactive_agent job using agent-from-chat template", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Use the agent-from-chat template
			cmd := ctx.Bin("plan", "add", "test-plan",
				"--template", "agent-from-chat",
				"--title", "Implement Feature",
				"-d", "01-design-feature.md")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}

			// Verify the job has generate_plan_from: true and prepend_dependencies: true
			jobPath := filepath.Join(planPath, "02-implement-feature.md")
			content, err := fs.ReadString(jobPath)
			if err != nil {
				return fmt.Errorf("reading job file: %w", err)
			}
			if !strings.Contains(content, "generate_plan_from: true") {
				return fmt.Errorf("job file missing generate_plan_from: true flag")
			}
			if !strings.Contains(content, "prepend_dependencies: true") {
				return fmt.Errorf("job file missing prepend_dependencies: true flag")
			}
			if !strings.Contains(content, "type: interactive_agent") {
				return fmt.Errorf("job file missing type: interactive_agent")
			}

			ctx.Set("agent_job_path", jobPath)
			return nil
		}),

		harness.NewStep("Run interactive_agent with agent-from-chat template", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			agentJobPath := ctx.GetString("agent_job_path")
			responseFile := filepath.Join(ctx.RootDir, "mock_plan_response.txt")
			debugPromptFile := filepath.Join(ctx.RootDir, "debug_prompt.txt")

			// Run the job with mocked LLM for plan generation and debug logging enabled
			cmd := ctx.Bin("plan", "run", agentJobPath)
			cmd.Dir(projectDir).
				Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile).
				Env("GROVE_MOCK_PROMPT_DEBUG_FILE=" + debugPromptFile).
				Env("GROVE_LOG_LEVEL=debug") // Enable debug logging to capture Gemini request details
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			// Save debug prompt file path for later verification
			ctx.Set("debug_prompt_file", debugPromptFile)

			// Check if there were errors
			if err := result.AssertSuccess(); err != nil {
				// Read the job file to see status
				content, readErr := fs.ReadString(agentJobPath)
				if readErr == nil {
					return fmt.Errorf("job run failed. Job content: %s", content)
				}
				return err
			}

			// The job should reach "running" status
			content, err := fs.ReadString(agentJobPath)
			if err != nil {
				return fmt.Errorf("reading job file: %w", err)
			}
			if !strings.Contains(content, "status: running") {
				return fmt.Errorf("expected job status to be 'running', but got status in: %s", content)
			}

			return nil
		}),

		harness.NewStep("Verify generated plan briefing file", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			artifactsDir := filepath.Join(planPath, ".artifacts")

			// Look for briefing files with "generated-plan" in the name
			briefingFiles, err := filepath.Glob(filepath.Join(artifactsDir, "briefing-*-generated-plan*.xml"))
			if err != nil {
				return fmt.Errorf("error checking for briefing files: %w", err)
			}

			// Debug: list all briefing files if we didn't find the expected one
			if len(briefingFiles) == 0 {
				allBriefings, _ := filepath.Glob(filepath.Join(artifactsDir, "briefing-*.xml"))
				return fmt.Errorf("expected a generated-plan briefing XML file to be created. Found: %v", allBriefings)
			}

			// Read the briefing file content
			briefingContent, err := fs.ReadString(briefingFiles[0])
			if err != nil {
				return fmt.Errorf("reading briefing file: %w", err)
			}

			// Verify the briefing contains the generated plan (from mock response)
			if !strings.Contains(briefingContent, "<file path=\"main.go\">") {
				return fmt.Errorf("briefing missing generated plan content (should contain <file> tags)")
			}
			if !strings.Contains(briefingContent, "detailed implementation plan") {
				return fmt.Errorf("briefing missing expected plan text")
			}

			ctx.Set("briefing_file", briefingFiles[0])
			return nil
		}),

		harness.NewStep("Verify briefing was generated from chat dependency", func(ctx *harness.Context) error {
			briefingFile := ctx.GetString("briefing_file")
			briefingContent, err := fs.ReadString(briefingFile)
			if err != nil {
				return fmt.Errorf("reading briefing file: %w", err)
			}

			// The generated plan should be based on the chat content
			// Verify the prompt that was sent to the mock LLM contained the chat conversation
			debugPromptFile := ctx.GetString("debug_prompt_file")
			if debugPromptContent, err := fs.ReadString(debugPromptFile); err == nil {
				// Check that the prompt contains the chat conversation
				if !strings.Contains(debugPromptContent, "I want to create a simple hello world function in Go") {
					fmt.Printf("DEBUG: Prompt sent to LLM (%d bytes):\n%s\n", len(debugPromptContent), debugPromptContent)
					return fmt.Errorf("prompt should contain the chat conversation content")
				}
				// Verify it contains the agent-xml template instructions
				if !strings.Contains(debugPromptContent, "implementation plan") {
					return fmt.Errorf("prompt should contain agent-xml template instructions")
				}
			}

			// The briefing should contain the XML plan structure
			if !strings.Contains(briefingContent, "<file path=") || !strings.Contains(briefingContent, "<change type=") {
				return fmt.Errorf("briefing should contain XML plan structure generated from chat")
			}

			return nil
		}),

		harness.NewStep("Verify claude agent received correct briefing file path", func(ctx *harness.Context) error {
			// Since claude is mocked, we can't verify the exact command
			// But we can verify the briefing file exists and would be passed correctly
			briefingFile := ctx.GetString("briefing_file")
			if err := fs.AssertExists(briefingFile); err != nil {
				return fmt.Errorf("briefing file should exist to be passed to claude: %w", err)
			}

			// Verify the briefing file has the expected naming pattern
			if !strings.Contains(briefingFile, "generated-plan") {
				return fmt.Errorf("briefing file should include 'generated-plan' in filename for clarity")
			}

			return nil
		}),
	},
)
