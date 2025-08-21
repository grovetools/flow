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
				
				// Create .grove-plan.yml
				fs.WriteString(filepath.Join(planDir, ".grove-plan.yml"), "")
				
				// Create a simple chat file
				chatContent := `---
id: test-chat
title: test-chat
type: chat
status: completed
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

// ChatExtractAllScenario tests the "all" argument functionality.
func ChatExtractAllScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-chat-extract-all",
		Description: "Tests extracting all content below frontmatter with 'all' argument",
		Tags:        []string{"chat", "extract", "all"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with markdown file", func(ctx *harness.Context) error {
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

				// Create a markdown file with frontmatter and content
				mdContent := `---
id: spec-doc
title: Specification Document
type: document
status: draft
metadata:
  author: Test Author
  version: 1.0
---

# Grove NeoVim Text Interaction Specification

## Overview

This document specifies the text interaction features for Grove NeoVim plugin.

### Key Features

1. **Smart Selection**: Intelligent text selection based on context
2. **Quick Actions**: Fast access to common operations
3. **Integration**: Seamless integration with existing vim workflows

## Implementation Details

The implementation should follow these principles:
- Maintain vim philosophy
- Be performant
- Provide clear visual feedback

### Technical Requirements

- NeoVim 0.9+ compatibility
- Lua-based implementation
- Treesitter integration

## Conclusion

This specification provides the foundation for implementing advanced text interaction features in Grove NeoVim.
`
				return fs.WriteString(filepath.Join(planDir, "spec-document.md"), mdContent)
			}),
			harness.NewStep("Extract all content with 'all' argument", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "extract", "all", "--title", "extracted-spec", "--file", "spec-document.md").Dir(filepath.Join(ctx.RootDir, "plans", "test-plan"))
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
			harness.NewStep("Verify extracted content contains all body", func(ctx *harness.Context) error {
				// Find the created file
				planDir := filepath.Join(ctx.RootDir, "plans", "test-plan")
				files, err := fs.ListFiles(planDir)
				if err != nil {
					return err
				}
				
				var extractedFile string
				for _, file := range files {
					if strings.Contains(file, "extracted-spec") {
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
				if !strings.Contains(content, "title: extracted-spec") {
					return fmt.Errorf("missing or incorrect title in frontmatter")
				}
				if !strings.Contains(content, "type: chat") {
					return fmt.Errorf("missing type: chat in frontmatter")
				}
				
				// Check that all content below original frontmatter is included
				if !strings.Contains(content, "# Grove NeoVim Text Interaction Specification") {
					return fmt.Errorf("missing main heading")
				}
				if !strings.Contains(content, "## Overview") {
					return fmt.Errorf("missing overview section")
				}
				if !strings.Contains(content, "### Key Features") {
					return fmt.Errorf("missing key features section")
				}
				if !strings.Contains(content, "1. **Smart Selection**: Intelligent text selection based on context") {
					return fmt.Errorf("missing smart selection feature")
				}
				if !strings.Contains(content, "## Implementation Details") {
					return fmt.Errorf("missing implementation details section")
				}
				if !strings.Contains(content, "### Technical Requirements") {
					return fmt.Errorf("missing technical requirements section")
				}
				if !strings.Contains(content, "## Conclusion") {
					return fmt.Errorf("missing conclusion section")
				}
				if !strings.Contains(content, "This specification provides the foundation") {
					return fmt.Errorf("missing conclusion text")
				}
				
				// Ensure original frontmatter is NOT included
				if strings.Contains(content, "metadata:") && strings.Contains(content, "author: Test Author") {
					return fmt.Errorf("original frontmatter should not be included in extracted content")
				}
				
				return nil
			}),
			harness.NewStep("Extract all with custom parameters", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := command.New(flow, "plan", "extract", "all", 
					"--title", "full-spec-custom",
					"--file", "spec-document.md",
					"--model", "gpt-4-turbo",
					"--worktree", "feature-branch",
					"--output", "generate_jobs",
				).Dir(filepath.Join(ctx.RootDir, "plans", "test-plan"))
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				return nil
			}),
			harness.NewStep("Verify custom parameters applied with 'all'", func(ctx *harness.Context) error {
				planDir := filepath.Join(ctx.RootDir, "plans", "test-plan")
				files, err := fs.ListFiles(planDir)
				if err != nil {
					return err
				}
				
				var targetFile string
				for _, file := range files {
					if strings.Contains(file, "full-spec-custom") {
						targetFile = file
						break
					}
				}
				
				if targetFile == "" {
					return fmt.Errorf("full-spec-custom file not found")
				}
				
				content, err := fs.ReadString(filepath.Join(planDir, targetFile))
				if err != nil {
					return err
				}
				
				// Check custom parameters
				if !strings.Contains(content, "model: gpt-4-turbo") {
					return fmt.Errorf("custom model not applied")
				}
				if !strings.Contains(content, "worktree: feature-branch") {
					return fmt.Errorf("custom worktree not applied")
				}
				if !strings.Contains(content, "type: generate_jobs") {
					return fmt.Errorf("custom output type not applied")
				}
				
				// Verify full content is still there
				if !strings.Contains(content, "# Grove NeoVim Text Interaction Specification") {
					return fmt.Errorf("content not properly extracted with 'all'")
				}
				
				return nil
			}),
			harness.NewStep("Setup external markdown file", func(ctx *harness.Context) error {
				// Create a file outside the plan directory
				externalDir := filepath.Join(ctx.RootDir, "external-docs")
				fs.CreateDir(externalDir)
				
				externalContent := `---
title: External Document
type: document
---

# External Content

This content is outside the plan directory and will be extracted using an absolute path.

## Section 1

Important content that needs to be extracted.

## Section 2

More content to include in the extraction.
`
				return fs.WriteString(filepath.Join(externalDir, "external.md"), externalContent)
			}),
			harness.NewStep("Extract all from absolute path", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				absolutePath := filepath.Join(ctx.RootDir, "external-docs", "external.md")
				cmd := command.New(flow, "plan", "extract", "all", 
					"--title", "external-content",
					"--file", absolutePath,
				).Dir(filepath.Join(ctx.RootDir, "plans", "test-plan"))
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if result.Error != nil {
					return result.Error
				}
				if !strings.Contains(result.Stdout, "Extracted 1 blocks to new chat job") {
					return fmt.Errorf("expected success message for absolute path extraction")
				}
				return nil
			}),
			harness.NewStep("Verify external content was extracted", func(ctx *harness.Context) error {
				planDir := filepath.Join(ctx.RootDir, "plans", "test-plan")
				files, err := fs.ListFiles(planDir)
				if err != nil {
					return err
				}
				
				var externalFile string
				for _, file := range files {
					if strings.Contains(file, "external-content") {
						externalFile = file
						break
					}
				}
				
				if externalFile == "" {
					return fmt.Errorf("external-content file not found")
				}
				
				content, err := fs.ReadString(filepath.Join(planDir, externalFile))
				if err != nil {
					return err
				}
				
				// Verify content
				if !strings.Contains(content, "# External Content") {
					return fmt.Errorf("external content not properly extracted")
				}
				if !strings.Contains(content, "This content is outside the plan directory") {
					return fmt.Errorf("missing expected external content")
				}
				if !strings.Contains(content, "## Section 1") {
					return fmt.Errorf("missing section 1")
				}
				if !strings.Contains(content, "## Section 2") {
					return fmt.Errorf("missing section 2")
				}
				
				return nil
			}),
		},
	}
}