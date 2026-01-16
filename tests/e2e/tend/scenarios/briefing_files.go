package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

var BriefingFilesScenario = harness.NewScenario(
	"briefing-files-for-all-jobs",
	"Verifies briefing files are generated for oneshot (with/without inline/prepend_dependencies) and chat jobs.",
	[]string{"core", "briefing", "oneshot", "chat", "dependencies", "inline"},
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
				"-d", "01-dep.md", "--include", "source.txt",
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
			// When prepend_dependencies=true, dependencies are inlined in <prepended_dependency> tags
			if !strings.Contains(content, "<prompt>") {
				return fmt.Errorf("briefing missing root <prompt> tag")
			}
			if !strings.Contains(content, "<prepended_dependency") {
				return fmt.Errorf("briefing missing <prepended_dependency> tag (prepend_dependencies=true)")
			}
			if !strings.Contains(content, "Dependency Content") {
				return fmt.Errorf("briefing missing dependency content")
			}
			if !strings.Contains(content, `<uploaded_context_file`) || !strings.Contains(content, `type="include"`) {
				return fmt.Errorf("briefing missing <uploaded_context_file type=\"include\"> tag for include file")
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

			// When prepend_dependencies=false, dependencies are listed as uploaded_context_file with type="dependency"
			if !strings.Contains(briefingContent, `<uploaded_context_file`) || !strings.Contains(briefingContent, `type="dependency"`) {
				return fmt.Errorf("briefing missing <uploaded_context_file type=\"dependency\"> tag (prepend_dependencies=false)")
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
			runCmd := ctx.Bin("run", chatFile)
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
			// Verify new XML conversation structure
			if !strings.Contains(content, "<conversation>") {
				return fmt.Errorf("chat briefing missing <conversation> tag")
			}
			if !strings.Contains(content, `<turn role="user"`) {
				return fmt.Errorf("chat briefing missing user turn")
			}
			if !strings.Contains(content, "Initial user message.") {
				return fmt.Errorf("chat briefing missing initial user message content")
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

			// Run the chat job via flow run (this tests the dependency resolution fix)
			runCmd := ctx.Bin("run", chatFile)
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

			// Verify the briefing has the structured conversation format
			if !strings.Contains(content, "<conversation>") {
				return fmt.Errorf("chat briefing missing <conversation> tag")
			}

			// Verify the briefing contains the user message in a turn element
			if !strings.Contains(content, "User message that depends on the dependency file") {
				return fmt.Errorf("chat briefing missing user message")
			}

			// Verify the briefing references the dependency in the context section
			// With the new XML format, dependencies are listed as uploaded_context_file with type="dependency"
			if !strings.Contains(content, "05-chat-dep.md") && !strings.Contains(content, `type="dependency"`) {
				return fmt.Errorf("chat briefing missing dependency reference - dependencies not being resolved for chat jobs")
			}

			return nil
		}),

		// Test Case 4: Chat Job with custom template in frontmatter
		harness.NewStep("Create chat job with custom template in frontmatter", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Create a chat job with a custom template in frontmatter
			chatContent := `---
id: chat-custom-template
title: Chat With Custom Template
type: chat
template: agent-xml
model: claude-3-5-sonnet-20241022
status: pending_user
---

<!-- grove: {"template": "agent-xml"} -->

Test message for custom template.
`
			chatFile := filepath.Join(planPath, "07-chat-custom-template.md")
			if err := fs.WriteString(chatFile, chatContent); err != nil {
				return err
			}
			ctx.Set("chat_custom_template_file", chatFile)
			return nil
		}),
		harness.NewStep("Run chat and verify response uses frontmatter template", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			chatFile := ctx.GetString("chat_custom_template_file")
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")

			// Run the chat job
			runCmd := ctx.Bin("run", chatFile)
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			if err := runCmd.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("chat run failed: %w", err)
			}

			// Read the updated chat file
			content, err := fs.ReadString(chatFile)
			if err != nil {
				return fmt.Errorf("reading chat file: %w", err)
			}

			// Verify the LLM response directive uses the frontmatter template, not hardcoded "chat"
			if !strings.Contains(content, `"template": "agent-xml"`) {
				return fmt.Errorf("LLM response should use frontmatter template 'agent-xml', but found hardcoded template")
			}

			// Make sure it doesn't have the old hardcoded "chat" after the response
			// Count occurrences - should have 2 (one in original directive, one after response)
			agentXmlCount := strings.Count(content, `"template": "agent-xml"`)
			if agentXmlCount < 2 {
				return fmt.Errorf("expected at least 2 occurrences of 'agent-xml' template (original + response), found %d", agentXmlCount)
			}

			return nil
		}),

		// Test Case 5: Oneshot Job with inline: files shorthand
		harness.NewStep("Create oneshot job with inline: files", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			// Create dependency file
			depContent := "---\nid: dep-inline-files\ntitle: Dependency for inline files\nstatus: completed\ntype: shell\n---\nDependency content for inline files test."
			if err := fs.WriteString(filepath.Join(planPath, "08-dep-inline-files.md"), depContent); err != nil {
				return err
			}

			// Add oneshot job with inline: files (shorthand for dependencies + include)
			addCmd := ctx.Bin("plan", "add", "briefing-plan",
				"--type", "oneshot", "--title", "test-oneshot-inline-files",
				"-d", "08-dep-inline-files.md",
				"--inline", "files",
				"-p", "Task with inline files shorthand")
			addCmd.Dir(projectDir)
			return addCmd.Run().AssertSuccess()
		}),
		harness.NewStep("Run oneshot with inline: files and verify briefing", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")

			// Run only the specific oneshot job, not all jobs (avoid chat job loops)
			runCmd := ctx.Bin("plan", "run", filepath.Join(planPath, "09-test-oneshot-inline-files.md"), "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			if err := runCmd.Run().AssertSuccess(); err != nil {
				return err
			}

			// Load plan to get job ID
			plan, err := orchestration.LoadPlan(planPath)
			if err != nil {
				return err
			}

			// Find the job with title "test-oneshot-inline-files"
			var jobID string
			for _, job := range plan.Jobs {
				if job.Title == "test-oneshot-inline-files" {
					jobID = job.ID
					break
				}
			}
			if jobID == "" {
				return fmt.Errorf("could not find job with title 'test-oneshot-inline-files'")
			}

			// Verify briefing file
			jobArtifactDir := filepath.Join(planPath, ".artifacts", jobID)
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found for oneshot job in %s", jobArtifactDir)
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}

			// When inline: files, dependencies should be inlined
			if !strings.Contains(content, "<prepended_dependency") {
				return fmt.Errorf("briefing missing <prepended_dependency> tag (inline: files)")
			}
			if !strings.Contains(content, "Dependency content for inline files test") {
				return fmt.Errorf("briefing missing dependency content")
			}
			return nil
		}),

		// Test Case 6: Chat Job with inline: dependencies (string shorthand)
		harness.NewStep("Create chat job with inline: dependencies (string)", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Create a dependency job
			depContent := "---\nid: chat-inline-str-dep\ntitle: Chat Inline String Dep\nstatus: completed\ntype: shell\n---\nInlined via string shorthand."
			if err := fs.WriteString(filepath.Join(planPath, "10-chat-inline-str-dep.md"), depContent); err != nil {
				return err
			}

			// Create a chat job with inline: dependencies (string, not array)
			chatContent := `---
id: chat-with-inline-str
title: Chat With Inline String
type: chat
template: chat
model: claude-3-5-sonnet-20241022
status: pending_user
inline: dependencies
depends_on:
  - 10-chat-inline-str-dep.md
---

<!-- grove: {"template": "chat"} -->

User message with string shorthand inline.
`
			chatFile := filepath.Join(planPath, "11-chat-with-inline-str.md")
			if err := fs.WriteString(chatFile, chatContent); err != nil {
				return err
			}
			ctx.Set("chat_inline_str_file", chatFile)
			return nil
		}),
		harness.NewStep("Run chat with inline: dependencies (string) and verify", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			chatFile := ctx.GetString("chat_inline_str_file")
			planPath := ctx.GetString("plan_path")
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")

			runCmd := ctx.Bin("run", chatFile)
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			if err := runCmd.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("chat run failed: %w", err)
			}

			// Verify briefing
			jobArtifactDir := filepath.Join(planPath, ".artifacts", "chat-with-inline-str")
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found in %s", jobArtifactDir)
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}

			if !strings.Contains(content, "<prepended_dependency") {
				return fmt.Errorf("briefing missing <prepended_dependency> tag (inline: dependencies string)")
			}
			if !strings.Contains(content, "Inlined via string shorthand") {
				return fmt.Errorf("briefing missing inlined dependency content")
			}
			return nil
		}),

		// Test Case 7: Chat Job with inline: [dependencies] array syntax
		harness.NewStep("Create chat job with inline: [dependencies]", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Create a dependency job in the plan
			depContent := "---\nid: chat-inline-dep\ntitle: Chat Inline Dependency\nstatus: completed\ntype: shell\n---\nThis is INLINED dependency content using array syntax."
			if err := fs.WriteString(filepath.Join(planPath, "12-chat-inline-dep.md"), depContent); err != nil {
				return err
			}

			// Create a chat job with inline: [dependencies] (array syntax)
			chatContent := `---
id: chat-with-inline-deps
title: Chat With Inline Dependencies
type: chat
template: chat
model: claude-3-5-sonnet-20241022
status: pending_user
inline:
  - dependencies
depends_on:
  - 12-chat-inline-dep.md
---

<!-- grove: {"template": "chat"} -->

User message with inlined dependency using array syntax.
`
			chatFile := filepath.Join(planPath, "13-chat-with-inline-deps.md")
			if err := fs.WriteString(chatFile, chatContent); err != nil {
				return err
			}
			ctx.Set("chat_inline_deps_file", chatFile)
			return nil
		}),
		harness.NewStep("Run chat with inline: [dependencies] and verify briefing inlines content", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			chatFile := ctx.GetString("chat_inline_deps_file")
			planPath := ctx.GetString("plan_path")
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")

			// Run the chat job
			runCmd := ctx.Bin("run", chatFile)
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			if err := runCmd.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("chat run failed: %w", err)
			}

			// Verify briefing file exists and contains inlined dependency content
			jobArtifactDir := filepath.Join(planPath, ".artifacts", "chat-with-inline-deps")
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found for chat-with-inline-deps job in %s", jobArtifactDir)
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}

			// Verify the briefing has the structured conversation format
			if !strings.Contains(content, "<conversation>") {
				return fmt.Errorf("chat briefing missing <conversation> tag")
			}

			// Verify the briefing contains the prepended_dependency tag (from inline: [dependencies])
			if !strings.Contains(content, "<prepended_dependency") {
				return fmt.Errorf("chat briefing missing <prepended_dependency> tag (inline: [dependencies])")
			}

			// Verify the dependency content is actually inlined
			if !strings.Contains(content, "This is INLINED dependency content using array syntax") {
				return fmt.Errorf("chat briefing missing inlined dependency content")
			}

			// Verify the user message is present
			if !strings.Contains(content, "User message with inlined dependency using array syntax") {
				return fmt.Errorf("chat briefing missing user message")
			}

			return nil
		}),

		// Test Case 8: Chat Job with prepend_dependencies=true (backwards compatibility)
		harness.NewStep("Create chat job with prepend_dependencies", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			// Create a dependency job in the plan
			depContent := "---\nid: chat-prepend-dep\ntitle: Chat Prepend Dependency\nstatus: completed\ntype: shell\n---\nThis is PREPENDED dependency content for chat."
			if err := fs.WriteString(filepath.Join(planPath, "14-chat-prepend-dep.md"), depContent); err != nil {
				return err
			}

			// Create a chat job with prepend_dependencies=true (backwards compat)
			chatContent := `---
id: chat-with-prepend-deps
title: Chat With Prepend Dependencies
type: chat
template: chat
model: claude-3-5-sonnet-20241022
status: pending_user
prepend_dependencies: true
depends_on:
  - 14-chat-prepend-dep.md
---

<!-- grove: {"template": "chat"} -->

User message with prepended dependency.
`
			chatFile := filepath.Join(planPath, "15-chat-with-prepend-deps.md")
			if err := fs.WriteString(chatFile, chatContent); err != nil {
				return err
			}
			ctx.Set("chat_prepend_deps_file", chatFile)
			return nil
		}),
		harness.NewStep("Run chat with prepend_dependencies and verify briefing inlines content", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			chatFile := ctx.GetString("chat_prepend_deps_file")
			planPath := ctx.GetString("plan_path")
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")

			// Run the chat job
			runCmd := ctx.Bin("run", chatFile)
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			if err := runCmd.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("chat run failed: %w", err)
			}

			// Verify briefing file exists and contains inlined dependency content
			jobArtifactDir := filepath.Join(planPath, ".artifacts", "chat-with-prepend-deps")
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found for chat-with-prepend-deps job in %s", jobArtifactDir)
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}

			// Verify the briefing has the structured conversation format
			if !strings.Contains(content, "<conversation>") {
				return fmt.Errorf("chat briefing missing <conversation> tag")
			}

			// Verify the briefing contains the prepended_dependency tag (not uploaded_context_file)
			if !strings.Contains(content, "<prepended_dependency") {
				return fmt.Errorf("chat briefing missing <prepended_dependency> tag (prepend_dependencies=true)")
			}

			// Verify the dependency content is actually inlined
			if !strings.Contains(content, "This is PREPENDED dependency content for chat") {
				return fmt.Errorf("chat briefing missing inlined dependency content")
			}

			// Verify the user message is present
			if !strings.Contains(content, "User message with prepended dependency") {
				return fmt.Errorf("chat briefing missing user message")
			}

			return nil
		}),
	},
)
