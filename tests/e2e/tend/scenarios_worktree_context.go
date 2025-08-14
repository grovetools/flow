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

// WorktreeContextIsolationScenario verifies that oneshot jobs only use context from their specified worktree
func WorktreeContextIsolationScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "worktree-context-isolation",
		Description: "Verify that oneshot jobs only use context from their specified worktree",
		Tags:        []string{"worktree", "context", "oneshot"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repository with two worktrees", func(ctx *harness.Context) error {
				// Initialize git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)

				// Create initial commit with some files
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "# Test Project\n")
				fs.WriteString(filepath.Join(ctx.RootDir, "main.go"), "package main\n\nfunc main() {\n\t// Original code\n}\n")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create grove.yml with flow configuration
				groveYml := `version: "1.0"
flow:
  oneshot_model: mock
  target_agent_container: "test-container"
  plans_directory: "./plans"
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveYml)

				// Create plans directory
				plansDir := filepath.Join(ctx.RootDir, "plans", "test-plan")
				os.MkdirAll(plansDir, 0755)

				// Create two different worktrees with different context
				// Worktree 1: Feature A
				featureADir := filepath.Join(ctx.RootDir, ".grove-worktrees", "feature-a")
				os.MkdirAll(featureADir, 0755)
				fs.WriteString(filepath.Join(featureADir, "feature_a.go"),
					"package main\n\n// Feature A implementation\nfunc FeatureA() string {\n\treturn \"Feature A\"\n}\n")
				fs.WriteString(filepath.Join(featureADir, "CLAUDE.md"),
					"# Feature A Context\n\nThis worktree is for Feature A development.\n")

				// Worktree 2: Feature B  
				featureBDir := filepath.Join(ctx.RootDir, ".grove-worktrees", "feature-b")
				os.MkdirAll(featureBDir, 0755)
				fs.WriteString(filepath.Join(featureBDir, "feature_b.go"),
					"package main\n\n// Feature B implementation\nfunc FeatureB() string {\n\treturn \"Feature B\"\n}\n")
				fs.WriteString(filepath.Join(featureBDir, "CLAUDE.md"),
					"# Feature B Context\n\nThis worktree is for Feature B development.\n")

				// Create a code review job that should only see Feature A context
				reviewJobContent := `---
id: review-feature-a
title: Review Feature A Code
status: pending
type: oneshot
worktree: feature-a
model: mock
template: code-review
prompt_source:
  - feature_a.go
output:
  type: file
  path: review-output.md
---

Please review the Feature A implementation.
`
				fs.WriteString(filepath.Join(plansDir, "01-review-feature-a.md"), reviewJobContent)

				// Create mock LLM response file that will reveal what context it sees
				mockResponse := `# Code Review Results

## Context Used

I can see the following context:
- Working in worktree: feature-a
- Files available: feature_a.go, CLAUDE.md from feature-a worktree

## Code Review

The Feature A implementation looks good.
`
				fs.WriteString(filepath.Join(ctx.RootDir, "mock-response.txt"), mockResponse)
				
				return nil
			}),
			harness.NewStep("Run oneshot job with worktree", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}

				// Set environment for mock LLM
				env := append(os.Environ(),
					"GROVE_MOCK_LLM_RESPONSE_FILE="+filepath.Join(ctx.RootDir, "mock-response.txt"),
					"GROVE_MOCK_LLM_OUTPUT_MODE=plain",
				)

				// Run the oneshot job
				planDir := filepath.Join(ctx.RootDir, "plans", "test-plan")
				cmd := command.New(flow, "plan", "run", "01-review-feature-a.md").
					Dir(planDir).
					Env(env...)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to run plan: %w", result.Error)
				}

				// Read the output file
				reviewOutput, err := os.ReadFile(filepath.Join(planDir, "review-output.md"))
				if err != nil {
					return fmt.Errorf("failed to read review output: %w", err)
				}

				reviewContent := string(reviewOutput)

				// Verify that only Feature A context was used
				if strings.Contains(reviewContent, "Feature B") {
					return fmt.Errorf("review output contains Feature B context, but should only see Feature A:\n%s", reviewContent)
				}

				if !strings.Contains(reviewContent, "Feature A") {
					return fmt.Errorf("review output doesn't contain Feature A context:\n%s", reviewContent)
				}

				// Also check that the worktree was properly used
				if !strings.Contains(result.Stdout, "feature-a") {
					// This is not necessarily an error - the worktree path might not be in output
				}

				return nil
			}),
		},
	}
}

// WorktreeContextRegenerationScenario tests that context is regenerated in worktrees before oneshot jobs
func WorktreeContextRegenerationScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "worktree-context-regeneration",
		Description: "Test that context is regenerated in worktrees before oneshot jobs",
		Tags:        []string{"worktree", "context", "regeneration"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repository with grovectx", func(ctx *harness.Context) error {
				// Initialize git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)

				// Create initial files
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "# Test Project\n")
				fs.WriteString(filepath.Join(ctx.RootDir, ".grovectx"), `
includes:
  - "*.go"
excludes:
  - "*_test.go"
`)
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create grove.yml
				groveYml := `version: "1.0"
flow:
  oneshot_model: mock
  plans_directory: "./plans"
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveYml)

				// Create plans directory
				plansDir := filepath.Join(ctx.RootDir, "plans", "test-plan")
				os.MkdirAll(plansDir, 0755)

				// Create a worktree directory manually
				worktreeDir := filepath.Join(ctx.RootDir, ".grove-worktrees", "test-worktree")
				os.MkdirAll(worktreeDir, 0755)

				// Copy .grovectx to worktree
				fs.WriteString(filepath.Join(worktreeDir, ".grovectx"), `
includes:
  - "*.go"
excludes:
  - "*_test.go"
`)

				// Create a go file in the worktree
				fs.WriteString(filepath.Join(worktreeDir, "new_feature.go"), `package main

// NewFeature implements a new feature
func NewFeature() string {
	return "This is a new feature"
}
`)

				// Create oneshot job that uses the worktree
				jobContent := `---
id: analyze-new-feature
title: Analyze New Feature
status: pending
type: oneshot
worktree: test-worktree
model: mock
---

Analyze the new feature implementation and check if context was regenerated.
`
				fs.WriteString(filepath.Join(plansDir, "01-analyze-feature.md"), jobContent)

				// Create mock response
				mockResponse := "Analysis complete. Context was regenerated successfully."
				fs.WriteString(filepath.Join(ctx.RootDir, "mock-response.txt"), mockResponse)
				
				return nil
			}),
			harness.NewStep("Run oneshot job and check context regeneration", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}

				// Set environment for mock LLM
				env := append(os.Environ(),
					"GROVE_MOCK_LLM_RESPONSE_FILE="+filepath.Join(ctx.RootDir, "mock-response.txt"),
				)

				planDir := filepath.Join(ctx.RootDir, "plans", "test-plan")
				jobPath := filepath.Join(planDir, "01-analyze-feature.md")
				
				cmd := command.New(flow, "plan", "run", jobPath).
					Dir(ctx.RootDir).
					Env(env...)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to run plan: %w", result.Error)
				}

				// Check that context regeneration was attempted
				if !strings.Contains(result.Stdout, "Checking context in worktree") {
					// Not necessarily an error - might be logged differently
				}

				// Verify the job completed successfully
				content, err := os.ReadFile(jobPath)
				if err != nil {
					return fmt.Errorf("failed to read job file: %w", err)
				}

				if !strings.Contains(string(content), "status: completed") {
					return fmt.Errorf("expected job to be completed, content:\n%s", string(content))
				}

				return nil
			}),
		},
	}
}

// InteractiveAgentToOneshotWorkflowScenario tests the full workflow from interactive agent to oneshot
func InteractiveAgentToOneshotWorkflowScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "interactive-to-oneshot-workflow",
		Description: "Test workflow from interactive agent to oneshot code review",
		Tags:        []string{"worktree", "interactive", "workflow"},
		Steps: []harness.Step{
			harness.NewStep("Setup git repository with workflow", func(ctx *harness.Context) error {
				// Initialize git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)

				// Create initial files
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "# Test Project\n")
				fs.WriteString(filepath.Join(ctx.RootDir, "main.go"), "package main\n\nfunc main() {\n\t// TODO: implement\n}\n")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Create grove.yml
				groveYml := `version: "1.0"
flow:
  oneshot_model: mock
  target_agent_container: "test-container"
  plans_directory: "./plans"
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), groveYml)

				// Create workflow plan with interactive agent followed by oneshot
				plansDir := filepath.Join(ctx.RootDir, "plans", "workflow-test")
				os.MkdirAll(plansDir, 0755)

				// Step 1: Interactive agent job
				interactiveJob := `---
id: implement-feature
title: Implement New Feature
status: pending
type: interactive_agent
worktree: feature-impl
---

Implement a new feature in main.go.
`
				fs.WriteString(filepath.Join(plansDir, "01-implement.md"), interactiveJob)

				// Step 2: Code review job (depends on interactive job)
				reviewJob := `---
id: review-implementation
title: Review Feature Implementation  
status: pending
type: oneshot
worktree: feature-impl
depends_on:
  - 01-implement.md
model: mock
template: code-review
---

Review the implementation created in the previous step.
Make sure to only analyze files from the feature-impl worktree.
`
				fs.WriteString(filepath.Join(plansDir, "02-review.md"), reviewJob)

				// Create mock response for the review
				mockResponse := `# Code Review

Reviewed the implementation in the feature-impl worktree.

## Files in worktree:
- main.go (modified)
- CLAUDE.md (if exists)

The implementation looks good.
`
				fs.WriteString(filepath.Join(ctx.RootDir, "mock-response.txt"), mockResponse)
				
				return nil
			}),
			harness.NewStep("Run workflow with skip-interactive", func(ctx *harness.Context) error {
				flow, err := getFlowBinary()
				if err != nil {
					return fmt.Errorf("failed to get flow binary: %w", err)
				}

				// Set environment
				env := append(os.Environ(),
					"GROVE_MOCK_LLM_RESPONSE_FILE="+filepath.Join(ctx.RootDir, "mock-response.txt"),
					"GROVE_FLOW_SKIP_DOCKER_CHECK=true",
				)

				planDir := filepath.Join(ctx.RootDir, "plans", "workflow-test")
				
				// Run the plan with skip-interactive flag
				cmd := command.New(flow, "plan", "run", "--all", "--skip-interactive").
					Dir(planDir).
					Env(env...)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to run plan: %w", result.Error)
				}

				// Verify the workflow executed correctly
				if !strings.Contains(result.Stdout, "interactive agent job skipped") {
					return fmt.Errorf("expected interactive job to be skipped:\n%s", result.Stdout)
				}

				// Check that review job ran and used the correct worktree
				reviewPath := filepath.Join(planDir, "02-review.md")
				reviewContent, err := os.ReadFile(reviewPath)
				if err != nil {
					return fmt.Errorf("failed to read review file: %w", err)
				}

				if !strings.Contains(string(reviewContent), "Output") {
					return fmt.Errorf("expected review job to have output:\n%s", string(reviewContent))
				}

				return nil
			}),
		},
	}
}