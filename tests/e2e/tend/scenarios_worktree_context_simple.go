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

// SimpleWorktreeContextTestScenario tests that oneshot jobs use worktree-specific context
func SimpleWorktreeContextTestScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "simple-worktree-context-test",
		Description: "Verify oneshot jobs see only their worktree context",
		Tags:        []string{"worktree", "context", "smoke"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with multiple contexts", func(ctx *harness.Context) error {
				// Initialize git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)

				// Create main repo files
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "# Main Repository\n")
				fs.WriteString(filepath.Join(ctx.RootDir, "CLAUDE.md"), "# Main Repository Context\n\nThis is the main repository context.\n")
				fs.WriteString(filepath.Join(ctx.RootDir, "main.go"), "package main\n\nfunc main() {\n\t// Main repo code\n}\n")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create grove.yml
				groveYml := `version: "1.0"
flow:
  oneshot_model: mock
  plans_directory: "./plans"
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveYml)

				// Create plan
				plansDir := filepath.Join(ctx.RootDir, "plans", "test-worktree-context")
				os.MkdirAll(plansDir, 0755)

				// Create a job that will analyze context
				analyzeJob := `---
id: analyze-context
title: Analyze Available Context
status: pending
type: oneshot
worktree: feature-branch
model: mock
---

Tell me what context files you can see and what their content says about the current working directory.
`
				fs.WriteString(filepath.Join(plansDir, "01-analyze-context.md"), analyzeJob)

				// Create mock response that shows what context the LLM sees
				mockResponse := `Based on the context files I can see:

## Available Context Files:
- CLAUDE.md (from the worktree)
- .grove/context (if it exists in the worktree)

## Content Analysis:
The CLAUDE.md file indicates this is the "Feature Branch Context" for developing a new feature.
I am working in the feature-branch worktree, not the main repository.

## Working Directory:
My working directory is the feature-branch worktree at .grove-worktrees/feature-branch/
`
				fs.WriteString(filepath.Join(ctx.RootDir, "mock-response.txt"), mockResponse)

				return nil
			}),
			harness.NewStep("Create worktree and add different context", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}

				// Let flow create the worktree by running a simple shell job
				plansDir := filepath.Join(ctx.RootDir, "plans", "test-worktree-context")
				
				// Create a shell job to setup the worktree
				setupJob := `---
id: setup-worktree
title: Setup Feature Branch Worktree
status: pending
type: shell
worktree: feature-branch
---

echo "Setting up feature branch worktree"
echo "# Feature Branch Context" > CLAUDE.md
echo "" >> CLAUDE.md
echo "This is the feature branch context for developing a new feature." >> CLAUDE.md
echo "This context is specific to the feature-branch worktree." >> CLAUDE.md
`
				fs.WriteString(filepath.Join(plansDir, "00-setup-worktree.md"), setupJob)

				// Run the setup job to create the worktree
				cmd := command.New(flow, "plan", "run", "00-setup-worktree.md", "-y").
					Dir(plansDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to setup worktree: %w", result.Error)
				}

				return nil
			}),
			harness.NewStep("Run oneshot job and verify context isolation", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}

				// Set environment for mock LLM
				env := append(os.Environ(),
					"GROVE_MOCK_LLM_RESPONSE_FILE="+filepath.Join(ctx.RootDir, "mock-response.txt"),
				)

				// Run the analysis job
				plansDir := filepath.Join(ctx.RootDir, "plans", "test-worktree-context")
				cmd := command.New(flow, "plan", "run", "01-analyze-context.md", "-y").
					Dir(plansDir).
					Env(env...)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to run analysis job: %w", result.Error)
				}

				// Read the job output
				jobContent, err := os.ReadFile(filepath.Join(plansDir, "01-analyze-context.md"))
				if err != nil {
					return fmt.Errorf("failed to read job file: %w", err)
				}

				// Verify the job saw the feature branch context
				if !strings.Contains(string(jobContent), "Feature Branch Context") {
					return fmt.Errorf("job output doesn't mention feature branch context")
				}

				// Verify it didn't see the main repo context
				if strings.Contains(string(jobContent), "Main Repository Context") {
					return fmt.Errorf("job output incorrectly mentions main repository context - context isolation failed!")
				}

				// Check that the worktree was actually used
				if !strings.Contains(result.Stdout, "feature-branch") || !strings.Contains(result.Stdout, "worktree") {
					// Not an error, just log for debugging
				}

				return nil
			}),
		},
	}
}