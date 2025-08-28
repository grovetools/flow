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

// ReferencePromptScenario tests prompt_source file references using flow plan add
func ReferencePromptScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-reference-prompts",
		Description: "Test jobs that reference source files via prompt_source using flow plan add",
		Tags:        []string{"plan", "reference", "prompt_source"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with source files", func(ctx *harness.Context) error {
				// Setup git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)
				
				// Create initial commit
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")
				
				// Write grove.yml
				configContent := `name: test-project
flow:
  plans_directory: ./plans
  oneshot_model: test
orchestration:
  target_agent_container: test-container
llm:
  provider: openai
  model: test
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)
				
				// Create source files with unique markers
				srcDir := filepath.Join(ctx.RootDir, "src")
				fs.CreateDir(srcDir)
				
				mainGoContent := `package main

import "fmt"

// TestMarker12345 - unique marker for testing
func TestFunction() string {
    return "test-marker-12345"
}

func main() {
    fmt.Println(TestFunction())
}
`
				fs.WriteString(filepath.Join(srcDir, "main.go"), mainGoContent)
				
				utilsGoContent := `package main

// UtilsMarker67890 - another unique marker
func HelperFunction() string {
    return "utils-marker-67890"
}
`
				fs.WriteString(filepath.Join(srcDir, "utils.go"), utilsGoContent)
				
				// Setup echo mock LLM that shows its input
				mockDir := filepath.Join(ctx.RootDir, "mocks")
				fs.CreateDir(mockDir)
				
				mockLLMScript := `#!/bin/bash
# Echo mock LLM that shows what it receives
echo "=== LLM INPUT START ==="
cat
echo ""
echo "=== LLM INPUT END ==="
echo ""
echo "Based on the provided source files, I can see TestMarker12345 and UtilsMarker67890. Code review completed."
`
				mockPath := filepath.Join(mockDir, "llm")
				fs.WriteString(mockPath, mockLLMScript)
				os.Chmod(mockPath, 0755)
				
				// Store the mock directory for later use
				ctx.Set("test_bin_dir", mockDir)
				
				return nil
			}),
			
			harness.NewStep("Initialize plan", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				
				cmd := command.New(flow, "plan", "init", "code-review").Dir(ctx.RootDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan init failed: %v", result.Error)
				}
				
				return nil
			}),
			
			harness.NewStep("Create prompt files for reference", func(ctx *harness.Context) error {
				// Create prompts directory
				promptsDir := filepath.Join(ctx.RootDir, "prompts")
				fs.CreateDir(promptsDir)
				
				// Create prompt file for main review
				mainPrompt := `Please review the provided Go source files and check for:
1. Code quality issues
2. Potential bugs  
3. Suggested improvements

Focus on the main.go file first.`
				fs.WriteString(filepath.Join(promptsDir, "review-main.txt"), mainPrompt)
				
				// Create prompt file for utils review
				utilsPrompt := `Review the utility functions in the provided source files.
Check for proper error handling and clear function names.`
				fs.WriteString(filepath.Join(promptsDir, "review-utils.txt"), utilsPrompt)
				
				return nil
			}),
			
			harness.NewStep("Add job with source file references", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				binDir := ctx.GetString("test_bin_dir")
				
				// Add job that references source files
				cmd := command.New(flow, "plan", "add", "code-review",
					"--title", "Review Main Code",
					"--type", "oneshot",
					"--prompt-file", "prompts/review-main.txt",
					"--source-files", "src/main.go,src/utils.go",
				).Dir(ctx.RootDir)
				
				if binDir != "" {
					currentPath := os.Getenv("PATH")
					cmd.Env(fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
				}
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to add job with source files: %v", result.Error)
				}
				
				// Store job filename
				if strings.Contains(result.Stdout, "01-review-main-code.md") {
					ctx.Set("job1_file", "01-review-main-code.md")
				} else {
					// Extract filename from output
					lines := strings.Split(result.Stdout, "\n")
					for _, line := range lines {
						if strings.HasPrefix(line, "Created job:") || strings.Contains(line, ".md") {
							parts := strings.Fields(line)
							for _, part := range parts {
								if strings.HasSuffix(part, ".md") {
									ctx.Set("job1_file", filepath.Base(part))
									break
								}
							}
						}
					}
				}
				
				return nil
			}),
			
			harness.NewStep("Run job and capture output", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				binDir := ctx.GetString("test_bin_dir")
				
				// Set active plan
				setCmd := command.New(flow, "plan", "set", "code-review").Dir(ctx.RootDir)
				setCmd.Run()
				
				// Get job filename
				jobFile := ctx.GetString("job1_file")
				if jobFile == "" {
					jobFile = "01-review-main-code.md"
				}
				
				// Run the job
				cmd := command.New(flow, "plan", "run", filepath.Join("plans", "code-review", jobFile)).Dir(ctx.RootDir)
				if binDir != "" {
					currentPath := os.Getenv("PATH")
					cmd.Env(fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
				}
				cmd.Env("GROVE_DEBUG=1") // Enable debug logging
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan run failed: %v", result.Error)
				}
				
				// Store output for verification
				ctx.Set("run_output", result.Stdout + "\n" + result.Stderr)
				
				return nil
			}),
			
			harness.NewStep("Verify source files were included", func(ctx *harness.Context) error {
				runOutput := ctx.GetString("run_output")
				jobFile := ctx.GetString("job1_file")
				if jobFile == "" {
					jobFile = "01-review-main-code.md"
				}
				
				// Also check the job file content
				jobPath := filepath.Join(ctx.RootDir, "plans", "code-review", jobFile)
				jobContent, err := fs.ReadString(jobPath)
				if err != nil {
					return fmt.Errorf("failed to read job file: %v", err)
				}
				
				// Combine outputs
				allOutput := runOutput + "\n" + jobContent
				
				// Verify that our unique markers are present OR that the LLM saw the files
				// The mock LLM should output the markers, but if using a real LLM, 
				// we check that it mentions the functions/files
				hasMainMarker := strings.Contains(allOutput, "TestMarker12345") || 
					strings.Contains(allOutput, "test-marker-12345") ||
					strings.Contains(allOutput, "TestFunction") // Real LLM would mention the function
				
				hasUtilsMarker := strings.Contains(allOutput, "UtilsMarker67890") || 
					strings.Contains(allOutput, "utils-marker-67890") ||
					strings.Contains(allOutput, "HelperFunction") // Real LLM would mention the function
				
				if !hasMainMarker {
					return fmt.Errorf("main.go content not found in output. Output:\n%s", allOutput)
				}
				
				if !hasUtilsMarker {
					return fmt.Errorf("utils.go content not found in output. Output:\n%s", allOutput)
				}
				
				// Verify prompt file content was included
				if !strings.Contains(allOutput, "Please review the provided Go source files") {
					return fmt.Errorf("prompt file content not found in output")
				}
				
				// Verify job completed
				if !strings.Contains(jobContent, "status: completed") {
					return fmt.Errorf("job should be marked as completed")
				}
				
				return nil
			}),
			
			harness.NewStep("Verify prompt log file was created", func(ctx *harness.Context) error {
				// Construct the expected path for the log file.
				// We use a glob because the filename includes a timestamp.
				logPattern := filepath.Join(
					ctx.RootDir, 
					".grove", 
					"logs", 
					"code-review", // Plan name
					"prompts", 
					"review-main-code-*-prompt.txt", // Job ID comes from the job title
				)

				matches, err := filepath.Glob(logPattern)
				if err != nil {
					return fmt.Errorf("error searching for log file: %w", err)
				}

				if len(matches) == 0 {
					return fmt.Errorf("expected prompt log file was not created. Pattern: %s", logPattern)
				}

				// Read the log file content
				logFile := matches[0]
				content, err := fs.ReadString(logFile)
				if err != nil {
					return fmt.Errorf("failed to read prompt log file %s: %w", logFile, err)
				}

				// Verify the content is what we expect the LLM to see.
				// This should contain the prompt sources and context files.
				if !strings.Contains(content, "TestMarker12345") {
					return fmt.Errorf("log file is missing content from main.go")
				}
				if !strings.Contains(content, "UtilsMarker67890") {
					return fmt.Errorf("log file is missing content from utils.go")
				}
				if !strings.Contains(content, "Please review the provided Go source files") {
					return fmt.Errorf("log file is missing content from the prompt file")
				}

				ctx.ShowCommandOutput("Log Verification", "✓ Prompt log file created and verified successfully.", "")
				return nil
			}),
			
			harness.NewStep("Add second job with single source file", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				binDir := ctx.GetString("test_bin_dir")
				
				// Add job that references only utils
				cmd := command.New(flow, "plan", "add", "code-review", 
					"--title", "Review Utils Only",
					"--type", "oneshot",
					"--prompt-file", "prompts/review-utils.txt",
					"--source-files", "src/utils.go",
				).Dir(ctx.RootDir)
				
				if binDir != "" {
					currentPath := os.Getenv("PATH")
					cmd.Env(fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
				}
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("failed to add second job: %v", result.Error)
				}
				
				return nil
			}),
			
			harness.NewStep("Verify plan status shows both jobs", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				binDir := ctx.GetString("test_bin_dir")
				
				cmd := command.New(flow, "plan", "status", "code-review").Dir(ctx.RootDir)
				if binDir != "" {
					currentPath := os.Getenv("PATH")
					cmd.Env(fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
				}
				
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				
				if result.Error != nil {
					return fmt.Errorf("plan status failed: %v", result.Error)
				}
				
				output := result.Stdout
				if !strings.Contains(output, "01-review-main-code.md") {
					return fmt.Errorf("status should show first job")
				}
				
				if !strings.Contains(output, "02-review-utils-only.md") {
					return fmt.Errorf("status should show second job")
				}
				
				// Should have 1 completed and 1 pending
				if !strings.Contains(output, "Completed: 1") {
					return fmt.Errorf("expected 1 completed job")
				}
				
				return nil
			}),
		},
	}
}