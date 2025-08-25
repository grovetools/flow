package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
				templateDir := filepath.Join(ctx.RootDir, ".grove", "job-templates")
				fs.CreateDir(filepath.Join(ctx.RootDir, ".grove"))
				fs.CreateDir(templateDir)
				
				// Create a custom chat template with easily verifiable instructions
				chatTemplate := `---
title: Test Chat Template
type: chat
---

You are a helpful assistant. IMPORTANT: Start every response with "Ahoy!" to indicate this template is being used.

Always be concise and helpful.
`
				fs.WriteString(filepath.Join(templateDir, "chat.md"), chatTemplate)
				
				// Write grove.yml
				configContent := `name: test-project
flow:
  chat_directory: ./chats
  oneshot_model: mock
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
    echo "Ahoy! First response: I'll help you build a test application. Let me outline the basic structure we'll need."
    ;;
  1)
    echo "Ahoy! Second response: Based on your feedback, I'll add database configuration to the plan."
    ;;
  *)
    echo "Ahoy! Additional response: Continuing our conversation about the test application."
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
				chatFile := filepath.Join(ctx.RootDir, "chats", "pipeline-test.md")
				
				// Create initial content with template in frontmatter
				initialContent := `---
id: pipeline-test
title: Pipeline Test Chat
status: pending_user
type: chat
template: chat
---

# Pipeline Test

User: I want to build a test application.
`
				fs.WriteString(chatFile, initialContent)
				
				return nil
			}),
			
			harness.NewStep("Run chat for first LLM response", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmdFunc := getCommandWithTestBin(ctx)
				cmd := cmdFunc(flow, "chat", "run", "Pipeline Test Chat").Dir(ctx.RootDir)
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
				
				// CRITICAL: Verify the template was applied to the first response
				if !strings.Contains(content, "Ahoy!") {
					return fmt.Errorf("expected 'Ahoy!' in first response to verify template was applied")
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
				
				cmdFunc := getCommandWithTestBin(ctx)
				cmd := cmdFunc(flow, "chat", "run", "Pipeline Test Chat").Dir(ctx.RootDir)
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
				
				// Verify the template continues to be applied
				if strings.Count(content, "Ahoy!") < 2 {
					return fmt.Errorf("expected 'Ahoy!' in both responses to verify template is consistently applied")
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