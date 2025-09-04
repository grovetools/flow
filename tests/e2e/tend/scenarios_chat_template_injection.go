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

// ChatTemplateInjectionScenario tests automatic template injection for chat jobs
func ChatTemplateInjectionScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-chat-template-injection",
		Description: "Test automatic injection of template: chat when missing",
		Tags:        []string{"chat", "template"},
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
				
				// Create a custom chat template
				chatTemplate := `---
title: Test Chat Template
type: chat
---

You are a helpful assistant. IMPORTANT: Start every response with "Greetings!" to indicate this template is being used.
`
				fs.WriteString(filepath.Join(templateDir, "chat.md"), chatTemplate)
				
				// Write grove.yml
				configContent := `name: test-project
flow:
  chat_directory: ./chats
  oneshot_model: mock
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
				
				// Setup mock LLM
				mockDir := filepath.Join(ctx.RootDir, "mocks")
				fs.CreateDir(mockDir)
				
				mockLLMScript := `#!/bin/bash
echo "Greetings! I am ready to assist you with your request."
`
				mockPath := filepath.Join(mockDir, "llm")
				fs.WriteString(mockPath, mockLLMScript)
				os.Chmod(mockPath, 0755)
				
				ctx.Set("test_bin_dir", mockDir)
				
				return nil
			}),
			
			harness.NewStep("Create chat without template in frontmatter", func(ctx *harness.Context) error {
				chatFile := filepath.Join(ctx.RootDir, "chats", "test-chat.md")
				
				// Create chat content WITHOUT template field
				initialContent := `---
id: test-chat
title: Test Chat Without Template
status: pending_user
type: chat
---

# Test Chat

User: Please help me with a test task.
`
				fs.WriteString(chatFile, initialContent)
				
				// Verify template is not in the file
				content, err := fs.ReadString(chatFile)
				if err != nil {
					return err
				}
				
				if strings.Contains(content, "template:") {
					return fmt.Errorf("template field should not be present initially")
				}
				
				return nil
			}),
			
			harness.NewStep("Run chat and verify template injection", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := ctx.Command(flow, "chat", "run", "Test Chat Without Template").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("chat run failed: %v", result.Error)
				}
				
				// Read the chat file after running
				chatFile := filepath.Join(ctx.RootDir, "chats", "test-chat.md")
				content, err := fs.ReadString(chatFile)
				if err != nil {
					return err
				}
				
				// Verify template was injected
				if !strings.Contains(content, "template: chat") {
					return fmt.Errorf("expected 'template: chat' to be injected into frontmatter")
				}
				
				// Verify the LLM response uses the template
				if !strings.Contains(content, "Greetings!") {
					return fmt.Errorf("expected 'Greetings!' in response to verify template was applied")
				}
				
				return nil
			}),
			
			harness.NewStep("Run chat again to ensure template persists", func(ctx *harness.Context) error {
				// Add another user message
				chatFile := filepath.Join(ctx.RootDir, "chats", "test-chat.md")
				content, err := fs.ReadString(chatFile)
				if err != nil {
					return err
				}
				
				newContent := content + "\n\nUser: Thank you! Can you help with another task?\n"
				fs.WriteString(chatFile, newContent)
				
				// Run chat again
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "chat", "run", "Test Chat Without Template").Dir(ctx.RootDir)
				result := cmd.Run()
				
				if result.Error != nil {
					return fmt.Errorf("second chat run failed: %v", result.Error)
				}
				
				// Read the file again
				finalContent, err := fs.ReadString(chatFile)
				if err != nil {
					return err
				}
				
				// Verify template is still there
				if !strings.Contains(finalContent, "template: chat") {
					return fmt.Errorf("template: chat should persist in frontmatter")
				}
				
				// Count Greetings to ensure both responses use the template
				greetingsCount := strings.Count(finalContent, "Greetings!")
				if greetingsCount < 2 {
					return fmt.Errorf("expected at least 2 'Greetings!' in responses, got %d", greetingsCount)
				}
				
				return nil
			}),
		},
	}
}