package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

var MemoryIntegrationScenario = harness.NewScenario(
	"memory-integration",
	"Tests Flow's memory search integration into briefings",
	[]string{"core", "memory", "briefing"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment", func(ctx *harness.Context) error {
			projectDir, _, err := setupDefaultEnvironment(ctx, "memory-project")
			if err != nil {
				return err
			}

			// Create a mock LLM response file
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			if err := fs.WriteString(responseFile, "mock response"); err != nil {
				return err
			}
			ctx.Set("llm_response_file", responseFile)
			_ = projectDir
			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
		),

		// --- Test 1: Missing DB (no GROVE_MOCK_MEMORY_RESULTS set) ---
		harness.NewStep("Init plan and add missing-db job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			initCmd := ctx.Bin("plan", "init", "memory-test")
			initCmd.Dir(projectDir)
			if result := initCmd.Run(); result.Error != nil {
				return fmt.Errorf("plan init failed: %w\nStderr: %s", result.Error, result.Stderr)
			}

			addCmd := ctx.Bin("plan", "add", "memory-test",
				"--type", "oneshot",
				"--title", "missing-db",
				"-p", "What is the secret code?")
			addCmd.Dir(projectDir)
			result := addCmd.Run()
			return result.AssertSuccess()
		}),

		harness.NewStep("Run missing-db job and verify no memories in briefing", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			llmResponseFile := ctx.GetString("llm_response_file")

			runCmd := ctx.Bin("plan", "run", "memory-test", "--target", "missing-db", "--yes")
			runCmd.Dir(projectDir)
			runCmd.Env(fmt.Sprintf("GROVE_MOCK_LLM_RESPONSE_FILE=%s", llmResponseFile))

			result := runCmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan run failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			// Find the briefing file in artifacts
			notebooksRoot := ctx.GetString("notebooks_root")
			artifactsDir := filepath.Join(notebooksRoot, "workspaces", "memory-project", "plans", "memory-test", ".artifacts", "missing-db")
			entries, err := os.ReadDir(artifactsDir)
			if err != nil {
				return fmt.Errorf("reading artifacts dir: %w", err)
			}
			if len(entries) == 0 {
				return fmt.Errorf("no briefing file found in %s", artifactsDir)
			}

			briefingPath := filepath.Join(artifactsDir, entries[0].Name())
			content, err := os.ReadFile(briefingPath)
			if err != nil {
				return fmt.Errorf("reading briefing file: %w", err)
			}

			if strings.Contains(string(content), "<related_memories>") {
				return fmt.Errorf("expected briefing to omit <related_memories> when no DB exists")
			}

			return nil
		}),

		// --- Test 2: Opt-out (memory: false) ---
		harness.NewStep("Add opt-out job with memory: false", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			planDir := filepath.Join(notebooksRoot, "workspaces", "memory-project", "plans", "memory-test")

			// Write a job file directly with memory: false
			jobContent := `---
id: opt-out
title: Test memory opt-out
type: oneshot
status: pending
memory: false
---

What is the secret code?
`
			jobPath := filepath.Join(planDir, "02-opt-out.md")
			if err := fs.WriteString(jobPath, jobContent); err != nil {
				return err
			}
			_ = projectDir
			return nil
		}),

		harness.NewStep("Run opt-out job with mock memories and verify omitted", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			llmResponseFile := ctx.GetString("llm_response_file")

			runCmd := ctx.Bin("plan", "run", "memory-test", "--target", "opt-out", "--yes")
			runCmd.Dir(projectDir)
			runCmd.Env(
				fmt.Sprintf("GROVE_MOCK_LLM_RESPONSE_FILE=%s", llmResponseFile),
				"GROVE_MOCK_MEMORY_RESULTS=Dummy memory content",
			)

			result := runCmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan run failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			// Verify briefing does NOT contain memories
			notebooksRoot := ctx.GetString("notebooks_root")
			artifactsDir := filepath.Join(notebooksRoot, "workspaces", "memory-project", "plans", "memory-test", ".artifacts", "opt-out")
			entries, err := os.ReadDir(artifactsDir)
			if err != nil {
				return fmt.Errorf("reading artifacts dir: %w", err)
			}
			if len(entries) == 0 {
				return fmt.Errorf("no briefing file found")
			}

			briefingPath := filepath.Join(artifactsDir, entries[0].Name())
			content, err := os.ReadFile(briefingPath)
			if err != nil {
				return fmt.Errorf("reading briefing file: %w", err)
			}

			if strings.Contains(string(content), "<related_memories>") {
				return fmt.Errorf("expected briefing to omit <related_memories> when memory: false is set")
			}

			return nil
		}),

		// --- Test 3: Success (memories injected via mock) ---
		harness.NewStep("Add success job", func(ctx *harness.Context) error {
			notebooksRoot := ctx.GetString("notebooks_root")
			planDir := filepath.Join(notebooksRoot, "workspaces", "memory-project", "plans", "memory-test")

			jobContent := `---
id: success
title: Test memory success
type: oneshot
status: pending
---

What is the secret code?
`
			jobPath := filepath.Join(planDir, "03-success.md")
			return fs.WriteString(jobPath, jobContent)
		}),

		harness.NewStep("Run success job with mock memories and verify injected", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			llmResponseFile := ctx.GetString("llm_response_file")

			runCmd := ctx.Bin("plan", "run", "memory-test", "--target", "success", "--yes")
			runCmd.Dir(projectDir)
			runCmd.Env(
				fmt.Sprintf("GROVE_MOCK_LLM_RESPONSE_FILE=%s", llmResponseFile),
				"GROVE_MOCK_MEMORY_RESULTS=Dummy memory content",
			)

			result := runCmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan run failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			// Verify briefing DOES contain memories
			notebooksRoot := ctx.GetString("notebooks_root")
			artifactsDir := filepath.Join(notebooksRoot, "workspaces", "memory-project", "plans", "memory-test", ".artifacts", "success")
			entries, err := os.ReadDir(artifactsDir)
			if err != nil {
				return fmt.Errorf("reading artifacts dir: %w", err)
			}
			if len(entries) == 0 {
				return fmt.Errorf("no briefing file found")
			}

			briefingPath := filepath.Join(artifactsDir, entries[0].Name())
			content, err := os.ReadFile(briefingPath)
			if err != nil {
				return fmt.Errorf("reading briefing file: %w", err)
			}

			briefing := string(content)
			if !strings.Contains(briefing, "<related_memories>") {
				return fmt.Errorf("expected briefing to contain <related_memories>")
			}
			if !strings.Contains(briefing, "Dummy memory content") {
				return fmt.Errorf("expected briefing to contain mock memory content")
			}
			if !strings.Contains(briefing, "test-memory.md") {
				return fmt.Errorf("expected briefing to contain memory path test-memory.md")
			}

			return nil
		}),
	},
)
