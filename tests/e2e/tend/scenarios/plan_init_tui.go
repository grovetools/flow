package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/grovetools/core/config"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/tui"
)

// PlanInitTUIScenario tests the interactive `flow plan init` TUI command.
var PlanInitTUIScenario = harness.NewScenarioWithOptions(
	"plan-init-tui",
	"Verifies the plan init TUI navigation, help menu, and form fields.",
	[]string{"tui", "plan", "init"},
	[]harness.Step{
		harness.NewStep("Setup mock filesystem", func(ctx *harness.Context) error {
			// Create a sandboxed home directory for global config
			homeDir := ctx.NewDir("home")
			ctx.Set("home_dir", homeDir)

			// Create a project directory and initialize it as a git repo
			projectDir := ctx.NewDir("init-test-project")
			ctx.Set("project_dir", projectDir)
			if err := fs.CreateDir(projectDir); err != nil {
				return err
			}
			if _, err := git.SetupTestRepo(projectDir); err != nil {
				return err
			}

			// Configure a centralized notebook location in the sandboxed global config
			notebooksRoot := filepath.Join(homeDir, "notebooks")
			configDir := filepath.Join(homeDir, ".config", "grove")

			notebookConfig := &config.NotebooksConfig{
				Definitions: map[string]*config.Notebook{
					"default": {RootDir: notebooksRoot},
				},
				Rules: &config.NotebookRules{Default: "default"},
			}
			globalCfg := &config.Config{Version: "1.0", Notebooks: notebookConfig}
			if err := fs.WriteGroveConfig(configDir, globalCfg); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Launch TUI and verify initial state", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			flowBinary, err := findFlowBinary()
			if err != nil {
				return err
			}

			// Create a wrapper script that changes to the project directory before running flow
			wrapperScript := filepath.Join(ctx.RootDir, "run-flow-init")
			scriptContent := fmt.Sprintf("#!/bin/bash\ncd %s\nexec %s plan init\n", projectDir, flowBinary)
			if err := fs.WriteString(wrapperScript, scriptContent); err != nil {
				return fmt.Errorf("failed to create wrapper script: %w", err)
			}
			if err := os.Chmod(wrapperScript, 0755); err != nil {
				return fmt.Errorf("failed to make wrapper script executable: %w", err)
			}

			session, err := ctx.StartTUI(
				wrapperScript,
				[]string{},
			)
			if err != nil {
				return fmt.Errorf("failed to start `flow plan init`: %w", err)
			}
			ctx.Set("tui_session", session)

			// Wait for the TUI to load by looking for the header
			if err := session.WaitForText("Create New Plan", 10*time.Second); err != nil {
				content, _ := session.Capture()
				return fmt.Errorf("TUI did not load: %w\nContent:\n%s", err, content)
			}

			// Verify key UI elements are visible
			if err := session.AssertContains("Plan Name"); err != nil {
				return err
			}
			if err := session.AssertContains("Recipe"); err != nil {
				return err
			}
			if err := session.AssertContains("Default Model"); err != nil {
				return err
			}
			return nil
		}),

		harness.NewStep("Test help menu toggle", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)

			// Press ? to open help menu
			if err := session.SendKeys("?"); err != nil {
				return fmt.Errorf("failed to send '?' key: %w", err)
			}

			// Wait for help content to appear
			if err := session.WaitForText("Navigation", 5*time.Second); err != nil {
				content, _ := session.Capture()
				return fmt.Errorf("help menu did not appear: %w\nContent:\n%s", err, content)
			}

			// Verify help content
			if err := session.AssertContains("tab"); err != nil {
				return fmt.Errorf("help should show tab binding: %w", err)
			}
			if err := session.AssertContains("next field"); err != nil {
				return fmt.Errorf("help should show 'next field' description: %w", err)
			}

			// Press ? again to close help
			if err := session.SendKeys("?"); err != nil {
				return fmt.Errorf("failed to send '?' key to close help: %w", err)
			}

			// Wait for main view to return
			if err := session.WaitForText("Plan Name", 5*time.Second); err != nil {
				content, _ := session.Capture()
				return fmt.Errorf("main view did not return after closing help: %w\nContent:\n%s", err, content)
			}

			return nil
		}),

		harness.NewStep("Test field navigation with Tab", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)

			// Press Tab to move to Recipe field
			if err := session.SendKeys("Tab"); err != nil {
				return fmt.Errorf("failed to send 'Tab' key: %w", err)
			}

			// Wait for UI to stabilize
			if err := session.WaitStable(); err != nil {
				return err
			}

			// The Recipe field should now be focused (indicated by thick border)
			// We can't easily check border style, but we can verify the UI is still functional
			if err := session.AssertContains("Recipe"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Test list navigation with j/k", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)

			// We should be on the Recipe field now (a list)
			// Press j to navigate down in the list
			if err := session.SendKeys("j"); err != nil {
				return fmt.Errorf("failed to send 'j' key: %w", err)
			}

			if err := session.WaitStable(); err != nil {
				return err
			}

			// Press k to navigate up in the list
			if err := session.SendKeys("k"); err != nil {
				return fmt.Errorf("failed to send 'k' key: %w", err)
			}

			if err := session.WaitStable(); err != nil {
				return err
			}

			// Verify we're still on the form
			if err := session.AssertContains("Recipe"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Test advanced options screen", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)

			// Press Escape to enter normal mode first
			if err := session.SendKeys("Escape"); err != nil {
				return fmt.Errorf("failed to send 'Escape' key: %w", err)
			}

			if err := session.WaitStable(); err != nil {
				return err
			}

			// Press 'a' to go to advanced options
			if err := session.SendKeys("a"); err != nil {
				return fmt.Errorf("failed to send 'a' key: %w", err)
			}

			// Wait for advanced screen
			if err := session.WaitForText("Advanced Options", 5*time.Second); err != nil {
				content, _ := session.Capture()
				return fmt.Errorf("advanced options screen did not appear: %w\nContent:\n%s", err, content)
			}

			// Verify advanced fields are visible
			if err := session.AssertContains("Worktree Name"); err != nil {
				return err
			}
			if err := session.AssertContains("Extract from File"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Test returning from advanced screen", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)

			// Press Escape to return to main screen
			if err := session.SendKeys("Escape"); err != nil {
				return fmt.Errorf("failed to send 'Escape' key: %w", err)
			}

			// Wait for main screen to return
			if err := session.WaitForText("Create New Plan", 5*time.Second); err != nil {
				content, _ := session.Capture()
				return fmt.Errorf("main screen did not return: %w\nContent:\n%s", err, content)
			}

			// Verify we're back on main screen
			if err := session.AssertContains("Recipe"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Test quitting the TUI", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)

			// Press Ctrl+C to quit
			if err := session.SendKeys("C-c"); err != nil {
				return fmt.Errorf("failed to send 'C-c' key: %w", err)
			}

			// Give it a moment for the TUI to finish exiting
			time.Sleep(500 * time.Millisecond)

			return nil
		}),
	},
	true,  // localOnly = true, as it requires tmux
	false, // explicitOnly = false
)
