package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

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

			// Verify briefing file (now XML format)
			planPath := ctx.GetString("plan_path")
			artifactsDir := filepath.Join(planPath, ".artifacts")
			briefings, _ := filepath.Glob(filepath.Join(artifactsDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found for oneshot job")
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}
			// When prepend_dependencies=true, dependencies are inlined in <inlined_dependency> tags
			if !strings.Contains(content, "<prompt>") {
				return fmt.Errorf("briefing missing root <prompt> tag")
			}
			if !strings.Contains(content, "<inlined_dependency") {
				return fmt.Errorf("briefing missing <inlined_dependency> tag (prepend_dependencies=true)")
			}
			if !strings.Contains(content, "Dependency Content") {
				return fmt.Errorf("briefing missing dependency content")
			}
			if !strings.Contains(content, "<uploaded_source_file") {
				return fmt.Errorf("briefing missing <uploaded_source_file> tag for source file")
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

			// Verify briefing file (now XML format) - should have uploaded tags, not inlined content
			planPath := ctx.GetString("plan_path")
			artifactsDir := filepath.Join(planPath, ".artifacts")
			briefings, _ := filepath.Glob(filepath.Join(artifactsDir, "briefing-*.xml"))

			// Find the briefing file for the second job
			var briefingContent string
			for _, briefing := range briefings {
				content, _ := fs.ReadString(briefing)
				if strings.Contains(content, "Task without prepend") {
					briefingContent = content
					break
				}
			}

			if briefingContent == "" {
				return fmt.Errorf("no briefing file found for second oneshot job")
			}

			// When prepend_dependencies=false, dependencies are referenced as uploaded files
			if !strings.Contains(briefingContent, "<uploaded_dependency") {
				return fmt.Errorf("briefing missing <uploaded_dependency> tag (prepend_dependencies=false)")
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
			artifactsDir := filepath.Join(chatsDir, ".artifacts")

			briefings, _ := filepath.Glob(filepath.Join(artifactsDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found for chat job")
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
	},
)
