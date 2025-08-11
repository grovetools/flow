package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/command"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// ChatPipelineScenario tests multi-turn chat conversations
func ChatPipelineScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-chat-pipeline",
		Description: "Test multi-turn chat conversations with LLM responses",
		Tags:        []string{"chat", "pipeline"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with chat configuration", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				
				// Create directories
				chatDir := filepath.Join(ctx.RootDir, "chats")
				fs.CreateDir(chatDir)
				
				// Write grove.yml
				configContent := `name: test-project
flow:
  chat_directory: ./chats
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
				
				// Setup mock LLM that returns different responses based on call count
				mockDir := filepath.Join(ctx.RootDir, "mocks")
				fs.CreateDir(mockDir)
				
				// Create a state file to track LLM calls
				stateFile := filepath.Join(mockDir, "llm_call_count")
				fs.WriteString(stateFile, "0")
				
				mockLLMScript := `#!/bin/bash
# Mock LLM that returns different responses based on call count
STATE_FILE="$(dirname "$0")/llm_call_count"
COUNT=$(cat "$STATE_FILE" 2>/dev/null || echo "0")
NEXT_COUNT=$((COUNT + 1))
echo "$NEXT_COUNT" > "$STATE_FILE"

case "$COUNT" in
  0)
    echo "First response: I'll help you build a test application. Let me outline the basic structure we'll need."
    ;;
  1)
    echo "Second response: Based on your feedback, I'll add database configuration to the plan."
    ;;
  *)
    echo "Additional response: Continuing our conversation about the test application."
    ;;
esac
`
				mockPath := filepath.Join(mockDir, "llm")
				fs.WriteString(mockPath, mockLLMScript)
				os.Chmod(mockPath, 0755)
				
				// Store the mock directory for later use
				ctx.Set("test_bin_dir", mockDir)
				
				return nil
			}),
			
			harness.NewStep("Initialize chat with user prompt", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				chatFile := filepath.Join(ctx.RootDir, "chats", "pipeline-test.md")
				
				// Create initial content
				initialContent := "# Pipeline Test\n\nUser: I want to build a test application.\n"
				fs.WriteString(chatFile, initialContent)
				
				// Initialize the chat using flow
				cmd := command.New(flow, "chat", "-s", chatFile).Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to initialize chat: %v", result.Error)
				}
				
				return nil
			}),
			
			harness.NewStep("Run chat for first LLM response", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				binDir := ctx.GetString("test_bin_dir")
				cmd := command.New(flow, "chat", "run", "pipeline-test").Dir(ctx.RootDir)
				if binDir != "" {
					currentPath := os.Getenv("PATH")
					cmd.Env(fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
				}
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("first chat run failed: %v", result.Error)
				}
				
				// Verify the chat file was updated with LLM response
				chatFile := filepath.Join(ctx.RootDir, "chats", "pipeline-test.md")
				content, err := fs.ReadString(chatFile)
				if err != nil {
					return err
				}
				
				if !strings.Contains(content, "First response") {
					return fmt.Errorf("expected first LLM response in chat file")
				}
				
				if !strings.Contains(content, "status: pending_user") {
					return fmt.Errorf("chat status should remain pending_user after LLM response")
				}
				
				return nil
			}),
			
			harness.NewStep("Append user follow-up question", func(ctx *harness.Context) error {
				chatFile := filepath.Join(ctx.RootDir, "chats", "pipeline-test.md")
				content, err := fs.ReadString(chatFile)
				if err != nil {
					return err
				}
				
				// Append user's follow-up
				newContent := content + "\n\nUser: Please include database configuration in the plan.\n"
				return fs.WriteString(chatFile, newContent)
			}),
			
			harness.NewStep("Run chat for second LLM response", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				binDir := ctx.GetString("test_bin_dir")
				cmd := command.New(flow, "chat", "run", "pipeline-test").Dir(ctx.RootDir)
				if binDir != "" {
					currentPath := os.Getenv("PATH")
					cmd.Env(fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
				}
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("second chat run failed: %v", result.Error)
				}
				
				// Verify both responses are in the chat file
				chatFile := filepath.Join(ctx.RootDir, "chats", "pipeline-test.md")
				content, err := fs.ReadString(chatFile)
				if err != nil {
					return err
				}
				
				if !strings.Contains(content, "First response") {
					return fmt.Errorf("first LLM response should still be in chat file")
				}
				
				if !strings.Contains(content, "Second response") {
					return fmt.Errorf("expected second LLM response in chat file")
				}
				
				// Count the number of LLM responses by looking for grove directives
				responseCount := strings.Count(content, "## LLM Response")
				if responseCount != 2 {
					return fmt.Errorf("expected 2 LLM responses, got %d", responseCount)
				}
				
				return nil
			}),
			
			harness.NewStep("Verify conversation flow", func(ctx *harness.Context) error {
				chatFile := filepath.Join(ctx.RootDir, "chats", "pipeline-test.md")
				content, err := fs.ReadString(chatFile)
				if err != nil {
					return err
				}
				
				// Verify the conversation structure
				lines := strings.Split(content, "\n")
				var foundUser1, foundAssistant1, foundUser2, foundAssistant2 bool
				
				for _, line := range lines {
					if strings.Contains(line, "User: I want to build") {
						foundUser1 = true
					} else if strings.Contains(line, "First response") {
						foundAssistant1 = true
					} else if strings.Contains(line, "User: Please include database") {
						foundUser2 = true
					} else if strings.Contains(line, "Second response") {
						foundAssistant2 = true
					}
				}
				
				if !foundUser1 || !foundAssistant1 || !foundUser2 || !foundAssistant2 {
					return fmt.Errorf("conversation flow is not correct")
				}
				
				return nil
			}),
		},
	}
}