package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
)

// PlanFinishBinaryRelinkScenario tests that binaries correctly fall back to main repo after worktree removal
func PlanFinishBinaryRelinkScenario() *harness.Scenario {
	return &harness.Scenario{
		Name:        "flow-plan-finish-binary-relink",
		Description: "Tests that globally-activated worktree binaries correctly fall back to main repo after plan finish",
		Tags:        []string{"plan", "finish", "cleanup", "devlinks", "binary", "relink"},
		Steps: []harness.Step{
			harness.NewStep("Setup project with Makefile and binary in main repo", func(ctx *harness.Context) error {
				// Init git repo
				git.Init(ctx.RootDir)
				git.SetupTestConfig(ctx.RootDir)

				// Create a simple Makefile that creates a binary
				makefileContent := `
.PHONY: build

build:
	@mkdir -p bin
	@echo '#!/bin/bash' > bin/testbin
	@echo 'echo "version: $${TESTBIN_VERSION:-main}"' >> bin/testbin
	@chmod +x bin/testbin
`
				fs.WriteString(filepath.Join(ctx.RootDir, "Makefile"), makefileContent)
				fs.WriteString(filepath.Join(ctx.RootDir, "README.md"), "Test project")
				git.Add(ctx.RootDir, ".")
				git.Commit(ctx.RootDir, "Initial commit")

				// Build the main repo binary
				cmd := ctx.Command("make", "build").Dir(ctx.RootDir)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to build main binary: %w", result.Error)
				}

				// Setup empty global config in sandboxed environment
				if err := setupEmptyGlobalConfig(ctx); err != nil {
					return err
				}

				// Create grove.yml
				configContent := `name: test-project
notebooks:
  rules:
    default: "local"
  definitions:
    local:
      root_dir: ""
`
				fs.WriteString(filepath.Join(ctx.RootDir, "grove.yml"), configContent)

				return nil
			}),
			setupTestEnvironment(),
			harness.NewStep("Register main repo binary with mock grove dev link", func(ctx *harness.Context) error {
				// Use the mock grove to register the main repo binary
				cmd := ctx.Command("grove", "dev", "link", ctx.RootDir, "--as", "main-repo")
				result := cmd.Run()
				if result.Error != nil {
					ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
					return fmt.Errorf("failed to register main repo binary: %w", result.Error)
				}

				// Verify the dev-links.json was created
				devLinksPath := filepath.Join(ctx.HomeDir(), ".grove", "dev-links.json")
				if _, err := os.Stat(devLinksPath); err != nil {
					return fmt.Errorf("dev-links.json not created: %w", err)
				}

				return nil
			}),
			harness.NewStep("Create plan with worktree and build worktree binary", func(ctx *harness.Context) error {
				flow, _ := getFlowBinary()
				cmd := ctx.Command(flow, "plan", "init", "binary-test", "--worktree").Dir(ctx.RootDir)
				if err := cmd.Run().Error; err != nil {
					return fmt.Errorf("failed to init plan: %w", err)
				}

				// Create worktree
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "binary-test")
				git.CreateWorktree(ctx.RootDir, "binary-test", worktreePath)

				// Build binary in worktree
				cmd = ctx.Command("make", "build").Dir(worktreePath)
				result := cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("failed to build worktree binary: %w", result.Error)
				}

				return nil
			}),
			harness.NewStep("Activate worktree binary with mock grove dev link", func(ctx *harness.Context) error {
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "binary-test")

				// Use the mock grove to activate the worktree binary
				cmd := ctx.Command("grove", "dev", "link", worktreePath, "--as", "binary-test")
				result := cmd.Run()
				if result.Error != nil {
					ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
					return fmt.Errorf("failed to activate worktree binary: %w", result.Error)
				}

				// Verify grove dev list shows the worktree binary as active
				cmd = ctx.Command("grove", "dev", "list")
				result = cmd.Run()
				if result.Error != nil {
					return fmt.Errorf("grove dev list failed: %w", result.Error)
				}
				if !strings.Contains(result.Stdout, "binary-test") {
					return fmt.Errorf("worktree binary not listed as active:\n%s", result.Stdout)
				}

				return nil
			}),
			harness.NewStep("Mark plan as ready for cleanup", func(ctx *harness.Context) error {
				// Update plan status to "ready_for_cleanup" so it can be finished
				// Try both main repo and worktree locations
				possiblePaths := []string{
					filepath.Join(ctx.RootDir, ".notebook", "plans", "binary-test", ".grove-plan.yml"),
					filepath.Join(ctx.RootDir, ".grove-worktrees", "binary-test", ".notebook", "plans", "binary-test", ".grove-plan.yml"),
				}

				var planConfigPath string
				for _, path := range possiblePaths {
					if _, err := os.Stat(path); err == nil {
						planConfigPath = path
						break
					}
				}

				if planConfigPath == "" {
					return fmt.Errorf("could not find plan config in any expected location")
				}

				content, err := fs.ReadString(planConfigPath)
				if err != nil {
					return fmt.Errorf("failed to read plan config: %w", err)
				}

				// Replace status with ready_for_cleanup
				updatedContent := strings.Replace(content, "status: active", "status: ready_for_cleanup", 1)
				if err := fs.WriteString(planConfigPath, updatedContent); err != nil {
					return fmt.Errorf("failed to update plan status: %w", err)
				}

				return nil
			}),
			harness.NewStep("Remove worktree and run grove dev prune", func(ctx *harness.Context) error {
				// Remove the worktree to simulate plan finish
				worktreePath := filepath.Join(ctx.RootDir, ".grove-worktrees", "binary-test")
				if err := os.RemoveAll(worktreePath); err != nil {
					return fmt.Errorf("failed to remove worktree: %w", err)
				}

				// Call grove dev prune to trigger the binary fallback logic
				// This is what `flow plan finish --clean-dev-links` calls internally
				cmd := ctx.Command("grove", "dev", "prune")
				result := cmd.Run()

				// Show output for debugging
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

				if result.Error != nil {
					return fmt.Errorf("grove dev prune failed: %w", result.Error)
				}

				// Verify worktree was removed (already removed above)
				if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
					return fmt.Errorf("worktree should have been removed but still exists")
				}

				return nil
			}),
			harness.NewStep("Verify binary fell back to main repo", func(ctx *harness.Context) error {

				// Read dev-links.json to verify the fallback
				devLinksPath := filepath.Join(ctx.HomeDir(), ".grove", "dev-links.json")
				data, err := os.ReadFile(devLinksPath)
				if err != nil {
					return fmt.Errorf("failed to read dev-links.json: %w", err)
				}

				var config struct {
					Binaries map[string]struct {
						Links   map[string]interface{} `json:"links"`
						Current string                 `json:"current"`
					} `json:"binaries"`
				}
				if err := json.Unmarshal(data, &config); err != nil {
					return fmt.Errorf("failed to parse dev-links.json: %w", err)
				}

				// Verify binary-test link was removed and fell back to main-repo
				testbin, exists := config.Binaries["testbin"]
				if !exists {
					return fmt.Errorf("testbin should still exist in dev-links")
				}

				if _, hasBinaryTest := testbin.Links["binary-test"]; hasBinaryTest {
					return fmt.Errorf("binary-test link should have been removed")
				}

				if _, hasMainRepo := testbin.Links["main-repo"]; !hasMainRepo {
					return fmt.Errorf("main-repo link should still exist")
				}

				if testbin.Current != "main-repo" {
					return fmt.Errorf("current link should be 'main-repo', got: %s", testbin.Current)
				}

				fmt.Println("✓ Binary successfully fell back to main-repo after worktree removal")
				return nil
			}),
		},
	}
}
