// File: tests/e2e/tend/scenarios_chat_interactive.go
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

// ChatInteractivePromptScenario tests the interactive prompt for chat jobs in multi-job plans
func ChatInteractivePromptScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-chat-interactive-prompt",
		Description: "Tests interactive prompt for chat jobs in multi-job plans (run, complete, edit options)",
		Tags:        []string{"chat", "interactive", "plan"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with config", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")
				
				// Create grove.yml config
				configContent := `name: test-project
flow:
  plans_directory: ./plans
  oneshot_model: mock
`
				return fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
			}),
			
			harness.NewStep("Create plan with chat job and oneshot job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Initialize plan
				cmd := command.New(flow, "plan", "init", "multi-job-plan").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}
				
				// Add chat job first
				cmd = command.New(flow, "plan", "add", "multi-job-plan",
					"--title", "Design Discussion",
					"--type", "chat",
					"-p", "Let's discuss the design for our new feature").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to add chat job: %w", err)
				}
				
				// Add oneshot job that depends on chat
				cmd = command.New(flow, "plan", "add", "multi-job-plan",
					"--title", "Implement Design",
					"--type", "oneshot",
					"--depends-on", "01-design-discussion.md",
					"-p", "Implement the design we discussed").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to add oneshot job: %w", err)
				}
				
				return nil
			}),
			
			setupTestEnvironment(),
			
			// Test 1: Run the chat job
			harness.NewStep("Test running chat job with 'r' option", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Run plan and send "y" to confirm, then "r" to run the chat
				cmd := ctx.Command(flow, "plan", "run", "multi-job-plan").Dir(ctx.RootDir)
				cmd.Stdin(strings.NewReader("y\nr\n"))
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				// Should complete successfully
				if result.Error != nil {
					return fmt.Errorf("plan run failed: %v", result.Error)
				}
				
				// Verify the chat job was executed
				if !strings.Contains(result.Stdout, "Running one turn of the chat") {
					return fmt.Errorf("expected to see 'Running one turn of the chat' in output")
				}
				
				// Verify LLM response was added
				chatFile := filepath.Join(ctx.RootDir, "plans", "multi-job-plan", "01-design-discussion.md")
				content, _ := fs.ReadString(chatFile)
				if !strings.Contains(content, "LLM Response") {
					return fmt.Errorf("chat file should contain LLM response")
				}
				
				return nil
			}),
			
			// Test 2: Mark chat as complete
			harness.NewStep("Test completing chat job with 'c' option", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Reset chat status to pending
				chatFile := filepath.Join(ctx.RootDir, "plans", "multi-job-plan", "01-design-discussion.md")
				content, _ := fs.ReadString(chatFile)
				content = strings.Replace(content, "status: pending_user", "status: pending", 1)
				fs.WriteString(chatFile, content)
				
				// Run plan and send "y" to confirm, then "c" to complete
				cmd := ctx.Command(flow, "plan", "run", "multi-job-plan").Dir(ctx.RootDir)
				cmd.Stdin(strings.NewReader("y\nc\n"))
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan run failed: %v", result.Error)
				}
				
				// Verify the message
				if !strings.Contains(result.Stdout, "Marking chat as complete") {
					return fmt.Errorf("expected to see 'Marking chat as complete' in output")
				}
				
				// Verify status is completed
				content, _ = fs.ReadString(chatFile)
				if !strings.Contains(content, "status: completed") {
					return fmt.Errorf("chat status should be 'completed'")
				}
				
				return nil
			}),
			
			// Test 3: Edit and re-prompt behavior
			harness.NewStep("Test edit option loops back to prompt", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Reset chat to pending
				chatFile := filepath.Join(ctx.RootDir, "plans", "multi-job-plan", "01-design-discussion.md")
				content, _ := fs.ReadString(chatFile)
				content = strings.Replace(content, "status: completed", "status: pending", 1)
				fs.WriteString(chatFile, content)
				
				// Create a mock editor that just appends a line
				mockEditorScript := `#!/bin/bash
echo "" >> "$1"
echo "User added this line" >> "$1"
`
				mockEditorPath := filepath.Join(ctx.RootDir, "mock-editor")
				fs.WriteString(mockEditorPath, mockEditorScript)
				os.Chmod(mockEditorPath, 0755)
				
				// Run plan with mock editor
				// Send: y (confirm), e (edit), r (run after edit)
				cmd := ctx.Command(flow, "plan", "run", "multi-job-plan").Dir(ctx.RootDir)
				cmd.Stdin(strings.NewReader("y\ne\nr\n"))
				cmd.Env(fmt.Sprintf("EDITOR=%s", mockEditorPath))
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan run failed: %v", result.Error)
				}
				
				// Verify edit happened
				if !strings.Contains(result.Stdout, "Editing finished") {
					return fmt.Errorf("expected to see 'Editing finished' in output")
				}
				
				// Verify prompt was shown again after edit
				if strings.Count(result.Stdout, "Chat job 'Design Discussion' is pending... Would you like to:") < 2 {
					return fmt.Errorf("prompt should be shown again after editing")
				}
				
				// Verify the file was edited
				content, _ = fs.ReadString(chatFile)
				if !strings.Contains(content, "User added this line") {
					return fmt.Errorf("chat file should contain the line added by mock editor")
				}
				
				return nil
			}),
			
			// Test 4: Invalid input handling
			harness.NewStep("Test invalid input handling", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Reset chat to pending
				chatFile := filepath.Join(ctx.RootDir, "plans", "multi-job-plan", "01-design-discussion.md")
				content, _ := fs.ReadString(chatFile)
				content = strings.Replace(content, "status: pending_user", "status: pending", 1)
				fs.WriteString(chatFile, content)
				
				// Send invalid input first, then valid
				cmd := ctx.Command(flow, "plan", "run", "multi-job-plan").Dir(ctx.RootDir)
				cmd.Stdin(strings.NewReader("y\nx\nc\n")) // x is invalid, then c to complete
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan run failed: %v", result.Error)
				}
				
				// Verify invalid choice message
				if !strings.Contains(result.Stdout, "Invalid choice 'x'") {
					return fmt.Errorf("expected to see invalid choice message")
				}
				
				// Verify prompt was shown again
				if strings.Count(result.Stdout, "Chat job 'Design Discussion' is pending... Would you like to:") < 2 {
					return fmt.Errorf("prompt should be shown again after invalid input")
				}
				
				return nil
			}),
		},
	}
}