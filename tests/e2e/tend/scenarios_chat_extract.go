// File: tests/e2e/tend/scenarios_chat_extract.go
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

// ChatExtractBasicScenario tests basic functionality of the extract command.
func ChatExtractBasicScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-chat-extract-basic",
		Description: "Tests basic chat block extraction functionality",
		Tags:        []string{"chat", "extract"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with plan and chat file", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create config
				configContent := `name: test-project
flow:
  plans_directory: ./plans
  oneshot_model: mock
  agent_model: mock
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				// Create plan directory
				planDir := filepath.Join(ctx.RootDir, "plans", "test-plan")
				fs.CreateDir(planDir)

				// Create .grove-plan.yml with defaults
				planConfigContent := `model: claude-3.5-sonnet
worktree: test-worktree
`
				fs.WriteString(filepath.Join(planDir, ".grove-plan.yml"), planConfigContent)

				// Create a chat file with multiple blocks
				chatContent := `---
id: test-chat
title: test-chat
type: chat
status: completed
---

Initial user prompt asking for cookie recipes.

<!-- grove: {"id": "cfa5f7"} -->

## LLM Response (2025-08-20 08:26:19)

Okay, I can suggest some more interesting variations on the chocolate chip cookie recipe. I'll focus on variations that introduce different flavor profiles and textures, building upon the base recipe I provided earlier.

Here are a few ideas:
1. Double Chocolate Espresso Cookies
2. Salted Caramel Pretzel Cookies
3. Brown Butter Toffee Cookies

Each variation maintains the basic cookie structure while adding unique elements.

<!-- grove: {"template": "chat"} -->

Let's go with option 3; rewrite the entire recipe based on brown butter.

<!-- grove: {"id": "161603"} -->

## LLM Response (2025-08-20 08:29:33)

Okay, I will rewrite the chocolate chip cookie recipe to create a "Brown Butter Toffee Cookie" recipe.

### Brown Butter Toffee Cookies

#### Ingredients:
- 2 1/4 cups all-purpose flour
- 1 tsp baking soda
- 1 tsp salt
- 1 cup butter (for browning)
- 3/4 cup granulated sugar
- 3/4 cup packed brown sugar
- 2 large eggs
- 2 tsp vanilla extract
- 1 cup toffee bits
- 1 cup semi-sweet chocolate chips

#### Instructions:
1. Brown the butter in a saucepan until fragrant and amber-colored
2. Let cool slightly
3. Mix with sugars, eggs, and vanilla
4. Combine dry ingredients
5. Fold in toffee and chocolate chips
6. Bake at 375°F for 9-11 minutes

The brown butter adds a nutty, caramelized flavor that complements the toffee perfectly.
`
				return fs.WriteString(filepath.Join(planDir, "01-recipe-chat.md"), chatContent)
			}),
			harness.NewStep("Extract single block", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "extract", "cfa5f7", "--title", "double-chocolate-variant", "--file", "01-recipe-chat.md").Dir(filepath.Join(ctx.RootDir, "plans", "test-plan"))
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				if !strings.Contains(result.Stdout, "Extracted 1 blocks to new chat job") {
					return fmt.Errorf("expected success message not found")
				}
				return nil
			}),
			harness.NewStep("Verify extracted content", func(ctx *harness.Context) error {
				// Find the created file
				planDir := filepath.Join(ctx.RootDir, "plans", "test-plan")
				files, err := fs.ListFiles(planDir)
				if err != nil {
					return err
				}
				
				var extractedFile string
				for _, file := range files {
					if strings.Contains(file, "double-chocolate-variant") {
						extractedFile = file
						break
					}
				}
				
				if extractedFile == "" {
					return fmt.Errorf("extracted file not found")
				}
				
				content, err := fs.ReadString(filepath.Join(planDir, extractedFile))
				if err != nil {
					return err
				}
				
				// Check frontmatter
				if !strings.Contains(content, "title: double-chocolate-variant") {
					return fmt.Errorf("missing or incorrect title in frontmatter")
				}
				if !strings.Contains(content, "type: chat") {
					return fmt.Errorf("missing type: chat in frontmatter")
				}
				if !strings.Contains(content, "model: claude-3.5-sonnet") {
					return fmt.Errorf("model not inherited from .grove-plan.yml")
				}
				if !strings.Contains(content, "worktree: test-worktree") {
					return fmt.Errorf("worktree not inherited from .grove-plan.yml")
				}
				
				// Check content includes the heading
				if !strings.Contains(content, "## LLM Response (2025-08-20 08:26:19)") {
					return fmt.Errorf("extracted content missing the heading")
				}
				if !strings.Contains(content, "Double Chocolate Espresso Cookies") {
					return fmt.Errorf("extracted content missing expected text")
				}
				if !strings.Contains(content, "Each variation maintains the basic cookie structure") {
					return fmt.Errorf("extracted content incomplete")
				}
				
				return nil
			}),
			harness.NewStep("Extract multiple blocks", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "extract", "cfa5f7", "161603", "--title", "all-recipes", "--file", "01-recipe-chat.md").Dir(filepath.Join(ctx.RootDir, "plans", "test-plan"))
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				if !strings.Contains(result.Stdout, "Extracted 2 blocks to new chat job") {
					return fmt.Errorf("expected to extract 2 blocks")
				}
				return nil
			}),
			harness.NewStep("Extract with dependencies and custom flags", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "extract", "161603", 
					"--title", "brown-butter-recipe",
					"--file", "01-recipe-chat.md",
					"--depends-on", "02-double-chocolate-variant.md",
					"--model", "gpt-4",
					"--worktree", "custom-worktree",
					"--output", "commit",
				).Dir(filepath.Join(ctx.RootDir, "plans", "test-plan"))
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				return nil
			}),
			harness.NewStep("Verify custom flags were applied", func(ctx *harness.Context) error {
				planDir := filepath.Join(ctx.RootDir, "plans", "test-plan")
				files, err := fs.ListFiles(planDir)
				if err != nil {
					return err
				}
				
				var targetFile string
				for _, file := range files {
					if strings.Contains(file, "brown-butter-recipe") {
						targetFile = file
						break
					}
				}
				
				if targetFile == "" {
					return fmt.Errorf("brown-butter-recipe file not found")
				}
				
				content, err := fs.ReadString(filepath.Join(planDir, targetFile))
				if err != nil {
					return err
				}
				
				// Check custom flags override defaults
				if !strings.Contains(content, "model: gpt-4") {
					return fmt.Errorf("custom model not applied")
				}
				if !strings.Contains(content, "worktree: custom-worktree") {
					return fmt.Errorf("custom worktree not applied")
				}
				if !strings.Contains(content, "type: commit") {
					return fmt.Errorf("custom output type not applied")
				}
				if !strings.Contains(content, "depends_on:") || !strings.Contains(content, "02-double-chocolate-variant.md") {
					return fmt.Errorf("dependency not applied")
				}
				
				// Check content
				if !strings.Contains(content, "Brown Butter Toffee Cookie") {
					return fmt.Errorf("extracted content incorrect")
				}
				
				return nil
			}),
		},
	}
}

// ChatExtractErrorScenario tests error handling in the extract command.
func ChatExtractErrorScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-chat-extract-errors",
		Description: "Tests error handling in chat extract command",
		Tags:        []string{"chat", "extract", "errors"},
		Steps: []harness.Step{
			harness.NewStep("Setup project", func(ctx *harness.Context) error {
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				
				configContent := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
				
				planDir := filepath.Join(ctx.RootDir, "plans", "test-plan")
				fs.CreateDir(planDir)
				
				// Create a simple chat file
				chatContent := `---
id: test-chat
title: test-chat
type: chat
---

Initial prompt.

<!-- grove: {"id": "block1"} -->

## Response

This is a response.
`
				return fs.WriteString(filepath.Join(planDir, "test.md"), chatContent)
			}),
			harness.NewStep("Extract with invalid block ID", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "extract", "invalid-id", "--title", "test", "--file", "test.md").Dir(filepath.Join(ctx.RootDir, "plans", "test-plan"))
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error == nil {
					return fmt.Errorf("expected error for invalid block ID")
				}
				if !strings.Contains(result.Stderr, "no valid blocks found to extract") {
					return fmt.Errorf("expected specific error message")
				}
				return nil
			}),
			harness.NewStep("Extract with non-existent file", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "extract", "block1", "--title", "test", "--file", "nonexistent.md").Dir(filepath.Join(ctx.RootDir, "plans", "test-plan"))
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error == nil {
					return fmt.Errorf("expected error for non-existent file")
				}
				if !strings.Contains(result.Stderr, "not found") {
					return fmt.Errorf("expected file not found error")
				}
				return nil
			}),
			harness.NewStep("Extract with invalid dependency", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "extract", "block1", 
					"--title", "test",
					"--file", "test.md",
					"--depends-on", "nonexistent-job.md",
				).Dir(filepath.Join(ctx.RootDir, "plans", "test-plan"))
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error == nil {
					return fmt.Errorf("expected error for invalid dependency")
				}
				if !strings.Contains(result.Stderr, "dependency not found") {
					return fmt.Errorf("expected dependency not found error")
				}
				return nil
			}),
		},
	}
}