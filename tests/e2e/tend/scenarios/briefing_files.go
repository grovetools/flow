package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-flow/pkg/orchestration"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

var BriefingFilesScenario = harness.NewScenario(
	"briefing-files-for-all-jobs",
	"Verifies briefing files are generated for oneshot (with/without prepend_dependencies) and chat jobs.",
	[]string{"core", "briefing", "oneshot", "chat", "dependencies"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment and mocks", func(ctx *harness.Context) error {
			_, _, err := setupDefaultEnvironment(ctx, "briefing-project")
			if err != nil {
				return err
			}
			// Create mock LLM response file
			mockResponse := `This is a mock response.`
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			return fs.WriteString(responseFile, mockResponse)
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "grove"}, // For aglogs
		),

		// Test Case 1a: Oneshot Job WITH prepend_dependencies
		harness.NewStep("Create oneshot job with prepend_dependencies=true", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			// Init plan
			ctx.Bin("plan", "init", "briefing-plan").Dir(projectDir).Run().AssertSuccess()

			// Create dependency file
			planPath := filepath.Join(ctx.GetString("notebooks_root"), "workspaces", "briefing-project", "plans", "briefing-plan")
			ctx.Set("plan_path", planPath)
			depContent := "---\nid: dep-1\ntitle: Dependency\nstatus: completed\ntype: shell\n---\nDependency Content"
			if err := fs.WriteString(filepath.Join(planPath, "01-dep.md"), depContent); err != nil {
				return err
			}

			// Create source file
			if err := fs.WriteString(filepath.Join(projectDir, "source.txt"), "Source File Content"); err != nil {
				return err
			}

			// Add oneshot job with prepend_dependencies
			addCmd := ctx.Bin("plan", "add", "briefing-plan",
				"--type", "oneshot", "--title", "test-oneshot-prepend",
				"-d", "01-dep.md", "--source-files", "source.txt",
				"--prepend-dependencies", "-p", "Main task prompt")
			addCmd.Dir(projectDir)
			return addCmd.Run().AssertSuccess()
		}),
		harness.NewStep("Run oneshot with prepend_dependencies and verify briefing", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")

			runCmd := ctx.Bin("plan", "run", "--all", "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			if err := runCmd.Run().AssertSuccess(); err != nil {
				return err
			}

			// Load plan to get job ID
			planPath := ctx.GetString("plan_path")
			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return err
			}

			// Find the job with title "test-oneshot-prepend"
			var jobID string
			for _, job := range plan.Jobs {
				if job.Title == "test-oneshot-prepend" {
					jobID = job.ID
					break
				}
			}
			if jobID == "" {
				return fmt.Errorf("could not find job with title 'test-oneshot-prepend'")
			}

			// Verify briefing file in new location: .artifacts/{job-id}/briefing-*.xml
			jobArtifactDir := filepath.Join(planPath, ".artifacts", jobID)
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found for oneshot job in %s", jobArtifactDir)
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}
			// When prepend_dependencies=true, dependencies are inlined in <inlined_dependency> tags
			if !strings.Contains(content, "<prompt>") {
				return fmt.Errorf("briefing missing root <prompt> tag")
			}
			if !strings.Contains(content, "<prepended_dependency") {
				return fmt.Errorf("briefing missing <prepended_dependency> tag (prepend_dependencies=true)")
			}
			if !strings.Contains(content, "Dependency Content") {
				return fmt.Errorf("briefing missing dependency content")
			}
			if !strings.Contains(content, "<inlined_source_file") {
				return fmt.Errorf("briefing missing <inlined_source_file> tag for source file")
			}
			if !strings.Contains(content, "Main task prompt") {
				return fmt.Errorf("briefing missing main prompt")
			}
			return nil
		}),

		// Test Case 1b: Oneshot Job WITHOUT prepend_dependencies
		harness.NewStep("Create oneshot job with prepend_dependencies=false", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Add second dependency
			dep2Content := "---\nid: dep-2\ntitle: Dependency 2\nstatus: completed\ntype: shell\n---\nSecond Dependency Content"
			if err := fs.WriteString(filepath.Join(planPath, "03-dep2.md"), dep2Content); err != nil {
				return err
			}

			// Add oneshot job WITHOUT prepend_dependencies (default behavior)
			addCmd := ctx.Bin("plan", "add", "briefing-plan",
				"--type", "oneshot", "--title", "test-oneshot-no-prepend",
				"-d", "03-dep2.md", "-p", "Task without prepend")
			addCmd.Dir(projectDir)
			return addCmd.Run().AssertSuccess()
		}),
		harness.NewStep("Run oneshot without prepend_dependencies and verify briefing", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")

			runCmd := ctx.Bin("plan", "run", "--all", "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			if err := runCmd.Run().AssertSuccess(); err != nil {
				return err
			}

			// Load plan to get job ID
			planPath := ctx.GetString("plan_path")
			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return err
			}

			// Find the job with title "test-oneshot-no-prepend"
			var jobID string
			for _, job := range plan.Jobs {
				if job.Title == "test-oneshot-no-prepend" {
					jobID = job.ID
					break
				}
			}
			if jobID == "" {
				return fmt.Errorf("could not find job with title 'test-oneshot-no-prepend'")
			}

			// Verify briefing file in new location: .artifacts/{job-id}/briefing-*.xml
			jobArtifactDir := filepath.Join(planPath, ".artifacts", jobID)
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found for second oneshot job in %s", jobArtifactDir)
			}

			briefingContent, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}

			// When prepend_dependencies=false, dependencies are inlined elsewhere in the prompt
			if !strings.Contains(briefingContent, "<inlined_dependency") {
				return fmt.Errorf("briefing missing <inlined_dependency> tag (prepend_dependencies=false)")
			}
			if !strings.Contains(briefingContent, "03-dep2.md") {
				return fmt.Errorf("briefing missing dependency file reference")
			}
			// Content should NOT be inlined when prepend_dependencies=false
			if strings.Contains(briefingContent, "Second Dependency Content") {
				return fmt.Errorf("briefing should NOT contain inlined dependency content when prepend_dependencies=false")
			}
			if !strings.Contains(briefingContent, "Task without prepend") {
				return fmt.Errorf("briefing missing main prompt")
			}
			return nil
		}),

		// Test Case 2: Chat Job
		harness.NewStep("Create and run chat job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			chatsDir := filepath.Join(ctx.GetString("notebooks_root"), "workspaces", "briefing-project", "chats")
			fs.CreateDir(chatsDir)
			chatFile := filepath.Join(chatsDir, "test-chat.md")
			fs.WriteString(chatFile, "Initial user message.")
			ctx.Set("chat_file_path", chatFile)

			// Use a non-gemini model so it uses the llm mock command
			ctx.Bin("chat", "-s", chatFile, "--model", "claude-3-5-sonnet-20241022").Dir(projectDir).Run().AssertSuccess()

			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			runCmd := ctx.Bin("chat", "run", chatFile)
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			return runCmd.Run().AssertSuccess()
		}),
		harness.NewStep("Verify chat briefing file", func(ctx *harness.Context) error {
			chatsDir := filepath.Join(ctx.GetString("notebooks_root"), "workspaces", "briefing-project", "chats")
			chatFile := ctx.GetString("chat_file_path")

			// Load the chat file as a job to get its ID from frontmatter
			job, err := orchestration.LoadJob(chatFile)
			if err != nil {
				return fmt.Errorf("loading chat job: %w", err)
			}

			// Verify briefing file in new location: .artifacts/{job-id}/briefing-*.xml
			jobArtifactDir := filepath.Join(chatsDir, ".artifacts", job.ID)
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found for chat job in %s", jobArtifactDir)
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}
			if !strings.Contains(content, "Initial user message.") {
				return fmt.Errorf("chat briefing missing initial user message")
			}
			return nil
		}),

		// Test Case 3: Chat Job with Dependencies (via flow chat run)
		harness.NewStep("Create chat job with dependencies", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Create a dependency job in the plan
			depContent := "---\nid: chat-dep-1\ntitle: Chat Dependency\nstatus: completed\ntype: shell\n---\nThis is dependency content for chat."
			if err := fs.WriteString(filepath.Join(planPath, "05-chat-dep.md"), depContent); err != nil {
				return err
			}

			// Create a chat job with depends_on in the plan directory
			chatContent := `---
id: chat-with-deps
title: Chat With Dependencies
type: chat
template: chat
model: claude-3-5-sonnet-20241022
status: pending_user
depends_on:
  - 05-chat-dep.md
---

<!-- grove: {"template": "chat"} -->

User message that depends on the dependency file.
`
			chatFile := filepath.Join(planPath, "06-chat-with-deps.md")
			if err := fs.WriteString(chatFile, chatContent); err != nil {
				return err
			}
			ctx.Set("chat_with_deps_file", chatFile)
			ctx.Set("chat_with_deps_plan_path", planPath)

			// Verify the file was created
			_, err := fs.ReadString(chatFile)
			if err != nil {
				return fmt.Errorf("failed to read created chat file: %w", err)
			}

			_ = projectDir // Used for context
			return nil
		}),
		harness.NewStep("Run chat with dependencies and verify briefing includes dependency", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			chatFile := ctx.GetString("chat_with_deps_file")
			planPath := ctx.GetString("chat_with_deps_plan_path")
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")

			// Run the chat job via flow chat run (this tests the dependency resolution fix)
			runCmd := ctx.Bin("chat", "run", chatFile)
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			if err := runCmd.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("chat run failed: %w", err)
			}

			// Verify briefing file exists and contains dependency reference
			jobArtifactDir := filepath.Join(planPath, ".artifacts", "chat-with-deps")
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found for chat-with-deps job in %s", jobArtifactDir)
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}

			// Verify the briefing contains the user message
			if !strings.Contains(content, "User message that depends on the dependency file") {
				return fmt.Errorf("chat briefing missing user message")
			}

			// Verify the briefing references the dependency (either inlined or as reference)
			// With the fix, dependencies should be included in the context section
			if !strings.Contains(content, "05-chat-dep.md") && !strings.Contains(content, "inlined_dependency") {
				return fmt.Errorf("chat briefing missing dependency reference - dependencies not being resolved for chat jobs")
			}

			return nil
		}),
	},
)
