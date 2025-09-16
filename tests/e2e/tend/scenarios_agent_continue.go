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

// AgentContinueScenario tests the agent_continue functionality
func AgentContinueScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-agent-continue",
		Description: "Tests agent_continue flag functionality for interactive agent jobs",
		Tags:        []string{"plan", "agent", "continue"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with git repo", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				// Create initial commit
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")
				
				// Write grove.yml with required config
				configContent := `name: test-project
flow:
  plans_directory: ./plans
  target_agent_container: test-container
agent:
  args:
    - --model
    - claude-3-5-sonnet-20241022
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
				
				return nil
			}),
			
			harness.NewStep("Initialize plan", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "init", "continue-test").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %v", result.Error)
				}
				
				return nil
			}),
			
			harness.NewStep("Add first interactive agent job without continue", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "add", "continue-test",
					"--title", "First Agent Job",
					"--type", "interactive_agent",
					"-p", "This is the first agent job",
				).Dir(ctx.RootDir)
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to add first interactive agent job: %v", result.Error)
				}
				
				// Verify job file was created
				jobFile := filepath.Join(ctx.RootDir, "plans", "continue-test", "01-first-agent-job.md")
				if !fs.Exists(jobFile) {
					return fmt.Errorf("first job file should exist: %s", jobFile)
				}
				
				// Verify agent_continue is NOT in the first job
				content, _ := fs.ReadString(jobFile)
				if strings.Contains(content, "agent_continue") {
					return fmt.Errorf("first job should NOT have agent_continue field")
				}
				
				return nil
			}),
			
			harness.NewStep("Add second interactive agent job with --agent-continue", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "add", "continue-test",
					"--title", "Second Agent Job with Continue",
					"--type", "interactive_agent",
					"--depends-on", "01-first-agent-job.md",
					"--agent-continue",
					"-p", "This job should continue from the previous agent session",
				).Dir(ctx.RootDir)
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to add second interactive agent job: %v", result.Error)
				}
				
				// Verify job file was created
				jobFile := filepath.Join(ctx.RootDir, "plans", "continue-test", "02-second-agent-job-with-continue.md")
				if !fs.Exists(jobFile) {
					return fmt.Errorf("second job file should exist: %s", jobFile)
				}
				
				// Verify agent_continue is in the second job
				content, _ := fs.ReadString(jobFile)
				if !strings.Contains(content, "agent_continue: true") {
					return fmt.Errorf("second job should have 'agent_continue: true' in frontmatter")
				}
				
				// Verify dependency is set correctly
				if !strings.Contains(content, "depends_on:") || !strings.Contains(content, "01-first-agent-job.md") {
					return fmt.Errorf("second job should depend on first job")
				}
				
				return nil
			}),
			
			harness.NewStep("Verify plan status shows both jobs", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "status", "continue-test").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan status failed: %v", result.Error)
				}
				
				// Check that both jobs appear in status (by filename)
				if !strings.Contains(result.Stdout, "01-first-agent-job.md") {
					return fmt.Errorf("status should show first job file")
				}
				if !strings.Contains(result.Stdout, "02-second-agent-job-with-continue.md") {
					return fmt.Errorf("status should show second job file")
				}
				
				// Verify it shows 2 total jobs
				if !strings.Contains(result.Stdout, "Jobs: 2 total") {
					return fmt.Errorf("status should show 2 total jobs")
				}
				
				return nil
			}),
			
			harness.NewStep("Verify job files content", func(ctx *harness.Context) error {
				// Let's read the actual job files and verify their content
				firstJobPath := filepath.Join(ctx.RootDir, "plans", "continue-test", "01-first-agent-job.md")
				secondJobPath := filepath.Join(ctx.RootDir, "plans", "continue-test", "02-second-agent-job-with-continue.md")
				
				// Read first job file
				firstContent, err := fs.ReadString(firstJobPath)
				if err != nil {
					return fmt.Errorf("failed to read first job file: %v", err)
				}
				
				// Read second job file
				secondContent, err := fs.ReadString(secondJobPath)
				if err != nil {
					return fmt.Errorf("failed to read second job file: %v", err)
				}
				
				// Verify first job does NOT have agent_continue
				if strings.Contains(firstContent, "agent_continue") {
					return fmt.Errorf("first job should NOT have agent_continue field")
				}
				
				// Verify second job DOES have agent_continue: true
				if !strings.Contains(secondContent, "agent_continue: true") {
					return fmt.Errorf("second job should have 'agent_continue: true', got:\n%s", secondContent)
				}
				
				// Verify both are interactive_agent type
				if !strings.Contains(firstContent, "type: interactive_agent") {
					return fmt.Errorf("first job should be type: interactive_agent")
				}
				if !strings.Contains(secondContent, "type: interactive_agent") {
					return fmt.Errorf("second job should be type: interactive_agent")
				}
				
				// Verify dependency is set
				if !strings.Contains(secondContent, "depends_on:") || !strings.Contains(secondContent, "- 01-first-agent-job.md") {
					return fmt.Errorf("second job should depend on first job")
				}
				
				return nil
			}),
		},
	}
}

// AgentContinueAutoEnableScenario tests that agent_continue is NOT automatically enabled for subsequent interactive_agent jobs
func AgentContinueAutoEnableScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-agent-continue-auto-enable",
		Description: "Tests that agent_continue is NOT automatically enabled for interactive_agent jobs by default",
		Tags:        []string{"plan", "agent", "continue", "auto"},
		Steps: []harness.Step{
			harness.NewStep("Setup project", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")
				
				// Write grove.yml
				configContent := `name: test-project
flow:
  plans_directory: ./plans
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
				
				return nil
			}),
			
			harness.NewStep("Initialize plan", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "init", "auto-continue-test").Dir(ctx.RootDir)
				result := cmd.Run()
				
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %v", result.Error)
				}
				
				return nil
			}),
			
			harness.NewStep("Add first interactive agent job WITHOUT --agent-continue flag", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Note: NOT using --agent-continue flag
				cmd := command.New(flow, "plan", "add", "auto-continue-test",
					"--title", "First Interactive Agent",
					"--type", "interactive_agent",
					"-p", "This is the first interactive agent job",
				).Dir(ctx.RootDir)
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to add first job: %v", result.Error)
				}
				
				// Verify first job does NOT have agent_continue
				jobFile := filepath.Join(ctx.RootDir, "plans", "auto-continue-test", "01-first-interactive-agent.md")
				content, _ := fs.ReadString(jobFile)
				
				if strings.Contains(content, "agent_continue") {
					return fmt.Errorf("first interactive_agent job should NOT have agent_continue field")
				}
				
				return nil
			}),
			
			harness.NewStep("Add second interactive agent job WITHOUT --agent-continue flag", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Note: NOT using --agent-continue flag, and it should NOT be auto-enabled
				cmd := command.New(flow, "plan", "add", "auto-continue-test",
					"--title", "Second Interactive Agent Auto",
					"--type", "interactive_agent",
					"-p", "This should NOT have continue flag enabled by default",
				).Dir(ctx.RootDir)
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to add second job: %v", result.Error)
				}
				
				// Verify second job does NOT have agent_continue (new default behavior)
				jobFile := filepath.Join(ctx.RootDir, "plans", "auto-continue-test", "02-second-interactive-agent-auto.md")
				content, _ := fs.ReadString(jobFile)
				
				if strings.Contains(content, "agent_continue") {
					return fmt.Errorf("second interactive_agent job should NOT have agent_continue field by default, got:\n%s", content)
				}
				
				return nil
			}),
			
			harness.NewStep("Add agent job (now an alias for interactive_agent)", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Add an agent job (which is now an alias for interactive_agent)
				cmd := command.New(flow, "plan", "add", "auto-continue-test",
					"--title", "Agent Job (Alias)",
					"--type", "agent",
					"--worktree", "test-worktree",
					"-p", "This is an agent job (now behaves as interactive_agent)",
				).Dir(ctx.RootDir)
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to add agent job: %v", result.Error)
				}
				
				// Verify agent job (alias) also does NOT have agent_continue by default
				jobFile := filepath.Join(ctx.RootDir, "plans", "auto-continue-test", "03-agent-job-alias.md")
				content, _ := fs.ReadString(jobFile)
				
				if strings.Contains(content, "agent_continue") {
					return fmt.Errorf("agent job (alias) should NOT have agent_continue field by default")
				}
				
				return nil
			}),
		},
	}
}

// AgentContinueFlagPropagationScenario tests that the agent_continue flag is properly used when executing jobs
func AgentContinueFlagPropagationScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-agent-continue-flag-propagation",
		Description: "Tests that agent_continue flag properly adds --continue to claude command",
		Tags:        []string{"plan", "agent", "continue", "command"},
		Steps: []harness.Step{
			harness.NewStep("Setup project", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")
				
				// Write grove.yml
				configContent := `name: test-project
flow:
  plans_directory: ./plans
  target_agent_container: test-container
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
				
				return nil
			}),
			
			harness.NewStep("Create plan with agent job having agent_continue", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				// Initialize plan
				cmd := command.New(flow, "plan", "init", "command-test").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %v", result.Error)
				}
				
				// Add agent job with continue flag and a worktree
				cmd = command.New(flow, "plan", "add", "command-test",
					"--title", "Continue Test Job",
					"--type", "agent",  // Use 'agent' type which is supported by launch
					"--worktree", "test-worktree",
					"--agent-continue",
					"-p", "Test prompt for continue functionality",
				).Dir(ctx.RootDir)
				
				result = cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to add agent job: %v", result.Error)
				}
				
				// Verify the job file has agent_continue: true
				jobFile := filepath.Join(ctx.RootDir, "plans", "command-test", "01-continue-test-job.md")
				content, _ := fs.ReadString(jobFile)
				
				if !strings.Contains(content, "agent_continue: true") {
					return fmt.Errorf("job file should contain 'agent_continue: true'")
				}
				
				if !strings.Contains(content, "worktree: test-worktree") {
					return fmt.Errorf("job file should contain 'worktree: test-worktree'")
				}
				
				if !strings.Contains(content, "type: agent") {
					return fmt.Errorf("job file should contain 'type: agent'")
				}
				
				return nil
			}),
			
			harness.NewStep("Verify help shows --agent-continue flag", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "add", "--help").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("help command failed: %v", result.Error)
				}
				
				// Check that help output includes the new flag
				if !strings.Contains(result.Stdout, "--agent-continue") {
					return fmt.Errorf("help output should include --agent-continue flag")
				}
				
				if !strings.Contains(result.Stdout, "Continue the last agent session") {
					return fmt.Errorf("help output should describe the --agent-continue flag")
				}
				
				return nil
			}),
		},
	}
}