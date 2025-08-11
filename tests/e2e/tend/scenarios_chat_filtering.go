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

// ChatRunFilteringScenario tests chat run with title filtering
func ChatRunFilteringScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-chat-run-filtering",
		Description: "Test that 'flow chat run' correctly filters chats by status and title",
		Tags:        []string{"chat", "filtering"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with multiple chat files", func(ctx *harness.Context) error {
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
				
				// Setup mock LLM
				mockDir := filepath.Join(ctx.RootDir, "mocks")
				fs.CreateDir(mockDir)
				mockLLMScript := `#!/bin/bash
# Mock LLM that marks chats as completed
echo "Test response from mock LLM"
`
				mockPath := filepath.Join(mockDir, "llm")
				fs.WriteString(mockPath, mockLLMScript)
				os.Chmod(mockPath, 0755)
				
				// Store the mock directory for later use
				ctx.Set("test_bin_dir", mockDir)
				
				return nil
			}),
			
			harness.NewStep("Create and initialize chat1 - runnable", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				chatFile := filepath.Join(ctx.RootDir, "chats", "chat1.md")
				
				// Create initial content
				initialContent := "# Chat One\n\nUser: Tell me about testing.\n"
				fs.WriteString(chatFile, initialContent)
				
				// Initialize the chat using flow
				cmd := command.New(flow, "chat", "-s", chatFile).Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to initialize chat1: %v", result.Error)
				}
				
				// The chat should now be in pending_user status by default
				return nil
			}),
			
			harness.NewStep("Create and mark chat2 as completed", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				chatFile := filepath.Join(ctx.RootDir, "chats", "chat2.md")
				
				// Create initial content
				initialContent := "# Chat Two\n\nUser: Already done.\n"
				fs.WriteString(chatFile, initialContent)
				
				// Initialize the chat
				cmd := command.New(flow, "chat", "-s", chatFile).Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to initialize chat2: %v", result.Error)
				}
				
				// Manually mark as completed by editing the file
				content, _ := fs.ReadString(chatFile)
				newContent := strings.Replace(content, "status: pending_user", "status: completed", 1)
				newContent += "\n\nAssistant: This chat is completed.\n"
				return fs.WriteString(chatFile, newContent)
			}),
			
			harness.NewStep("Create and initialize chat3 - runnable", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				chatFile := filepath.Join(ctx.RootDir, "chats", "chat3.md")
				
				// Create initial content
				initialContent := "# Chat Three\n\nUser: Another test question.\n"
				fs.WriteString(chatFile, initialContent)
				
				// Initialize the chat
				cmd := command.New(flow, "chat", "-s", chatFile).Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to initialize chat3: %v", result.Error)
				}
				
				return nil
			}),
			
			harness.NewStep("Create and mark chat4 as running", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				chatFile := filepath.Join(ctx.RootDir, "chats", "chat4.md")
				
				// Create initial content
				initialContent := "# Chat Four\n\nUser: Currently being processed.\n"
				fs.WriteString(chatFile, initialContent)
				
				// Initialize the chat
				cmd := command.New(flow, "chat", "-s", chatFile).Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to initialize chat4: %v", result.Error)
				}
				
				// Manually mark as running by editing the file
				content, _ := fs.ReadString(chatFile)
				newContent := strings.Replace(content, "status: pending_user", "status: running", 1)
				return fs.WriteString(chatFile, newContent)
			}),
			
			harness.NewStep("Test 1: Run all runnable chats", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Run without arguments - should process all pending_user chats
				binDir := ctx.GetString("test_bin_dir")
				cmd := command.New(flow, "chat", "run").Dir(ctx.RootDir)
				if binDir != "" {
					currentPath := os.Getenv("PATH")
					cmd.Env(fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
				}
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("chat run failed: %v", result.Error)
				}
				
				output := result.Stdout
				
				// Verify we found 2 runnable chats
				if !strings.Contains(output, "Found 2 runnable chat(s)") {
					return fmt.Errorf("expected to find 2 runnable chats, output: %s", output)
				}
				
				// Verify only chat1 and chat3 were processed
				if !strings.Contains(output, "Running Chat: chat1") {
					return fmt.Errorf("expected chat1 to be processed, output: %s", output)
				}
				if !strings.Contains(output, "Running Chat: chat3") {
					return fmt.Errorf("expected chat3 to be processed, output: %s", output)
				}
				if strings.Contains(output, "Running Chat: chat2") {
					return fmt.Errorf("chat2 (completed) should not have been processed")
				}
				if strings.Contains(output, "Running Chat: chat4") {
					return fmt.Errorf("chat4 (running) should not have been processed")
				}
				
				return nil
			}),
			
			harness.NewStep("Reset chat1 status back to pending_user", func(ctx *harness.Context) error {
				chatFile := filepath.Join(ctx.RootDir, "chats", "chat1.md")
				content, err := fs.ReadString(chatFile)
				if err != nil {
					return err
				}
				// Change status back to pending_user
				newContent := strings.Replace(content, "status: completed", "status: pending_user", 1)
				return fs.WriteString(chatFile, newContent)
			}),
			
			harness.NewStep("Test 2: Run specific chats by title", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Run with specific titles (using filenames which become the titles)
				binDir := ctx.GetString("test_bin_dir")
				cmd := command.New(flow, "chat", "run", "chat1", "chat4").Dir(ctx.RootDir)
				if binDir != "" {
					currentPath := os.Getenv("PATH")
					cmd.Env(fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
				}
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("chat run with titles failed: %v", result.Error)
				}
				
				output := result.Stdout
				
				// Since chat1 was already run, it should now show no runnable chats
				if !strings.Contains(output, "No runnable chats found") {
					return fmt.Errorf("expected no runnable chats (chat1 already completed, chat4 is running), output: %s", output)
				}
				
				// Verify it shows the available chats with their statuses
				if !strings.Contains(output, "Available chats:") {
					return fmt.Errorf("expected to see available chats list")
				}
				// Should not run any chats
				if strings.Contains(output, "Running Chat:") {
					return fmt.Errorf("no chats should have been processed")
				}
				
				return nil
			}),
			
		},
	}
}