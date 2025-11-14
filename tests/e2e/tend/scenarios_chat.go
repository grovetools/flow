// File: tests/e2e/tend/scenarios_chat.go
package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// BasicChatWorkflowScenario tests the fundamental `flow chat` commands.
func BasicChatWorkflowScenarioDisabled() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-chat-workflow-DISABLED",
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

				// Setup empty global config in sandboxed environment
				if err := setupEmptyGlobalConfig(ctx); err != nil {
					return err
				}

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
				cmd := ctx.Command(flow, "chat", "-s", chatFile).Dir(ctx.RootDir)
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
				cmd := ctx.Command(flow, "chat", "list").Dir(ctx.RootDir)
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
				cmd := ctx.Command(flow, "plan", "run", chatPath, "--yes", "--model", "mock").Dir(ctx.RootDir)
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

