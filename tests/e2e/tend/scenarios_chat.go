// File: tests/e2e/tend/scenarios_chat.go
package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/command"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// BasicChatWorkflowScenario tests the fundamental `flow chat` commands.
func BasicChatWorkflowScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-chat-workflow",
		Description: "Tests chat initialization, listing, and running.",
		Tags:        []string{"chat", "smoke"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with config and chat file", func(ctx *harness.Context) error {
				// Setup git repo for proper operation
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")
				
				chatDir := filepath.Join(ctx.RootDir, "chats")
				fs.CreateDir(chatDir)
				configContent := `name: test-project
flow:
  chat_directory: ./chats
  plans_directory: ./plans
  oneshot_model: mock
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
				return fs.WriteString(filepath.Join(chatDir, "my-idea.md"), "# My Idea\n\nLet's build a thing.")
			}),
			harness.NewStep("Initialize chat job", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				chatFile := filepath.Join(ctx.RootDir, "chats", "my-idea.md")
				cmd := command.New(flow, "chat", "-s", chatFile).Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				content, err := fs.ReadString(chatFile)
				if err != nil {
					return err
				}
				if !strings.HasPrefix(content, "---") {
					return fmt.Errorf("chat file was not initialized with frontmatter")
				}
				return nil
			}),
			harness.NewStep("List chat jobs", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "chat", "list").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if !strings.Contains(result.Stdout, "my-idea") {
					return fmt.Errorf("list command did not find the new chat")
				}
				return nil
			}),
			setupTestEnvironment(),
			harness.NewStep("Run the chat using 'flow plan run'", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				// Run the chat file directly using flow plan run
				chatPath := filepath.Join(ctx.RootDir, "chats", "my-idea.md")
				cmdFunc := getCommandWithTestBin(ctx)
				cmd := cmdFunc(flow, "plan", "run", chatPath, "--yes", "--model", "mock").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				return result.Error
			}),
			harness.NewStep("Verify chat file was updated", func(ctx *harness.Context) error {
				chatFile := filepath.Join(ctx.RootDir, "chats", "my-idea.md")
				content, err := fs.ReadString(chatFile)
				if err != nil {
					return err
				}
				if !strings.Contains(content, "mock LLM response") {
					return fmt.Errorf("chat file was not updated with mock LLM response")
				}
				if !strings.Contains(content, "<!-- grove: {\"id\":") {
					return fmt.Errorf("chat file is missing LLM response directive")
				}
				return nil
			}),
		},
	}
}

// ChatLaunchScenario tests launching an interactive session from a chat file.
func ChatLaunchScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-chat-launch",
		Description: "Tests launching a chat, which should create a worktree.",
		Tags:        []string{"chat", "launch", "worktree"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repo, config, and chat", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Initial commit")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				configContent := `name: test-project
flow:
  target_agent_container: fake-container
  plans_directory: ./plans
  oneshot_model: mock
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				chatFile := filepath.Join(ctx.RootDir, "dev-task.md")
				fs.WriteString(chatFile, "# Dev Task\n\nImplement the login page.")
				flow, _ := getFlowBinary()
				return command.New(flow, "chat", "-s", chatFile).Dir(ctx.RootDir).Run().Error
			}),
			setupTestEnvironment(),
			harness.NewStep("Launch the chat", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				chatFile := filepath.Join(ctx.RootDir, "dev-task.md")
				cmdFunc := getCommandWithTestBin(ctx)
				cmd := cmdFunc(flow, "chat", "launch", chatFile).Dir(ctx.RootDir)
				// Set environment variables for testing
				cmd.Env("GROVE_FLOW_SKIP_DOCKER_CHECK=true")
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				return result.Error
			}),
			harness.NewStep("Verify worktree for chat was created", func(ctx *harness.Context) error {
				// The worktree name is derived from the chat filename 'dev-task'
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "dev-task")
				if !fs.Exists(worktreePath) {
					return fmt.Errorf("worktree path %s should exist", worktreePath)
				}
				return nil
			}),
		},
	}
}