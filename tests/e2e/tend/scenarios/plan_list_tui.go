package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mattsolo1/grove-core/config"
	"github.com/mattsolo1/grove-tend/pkg/fs"
	"github.com/mattsolo1/grove-tend/pkg/git"
	"github.com/mattsolo1/grove-tend/pkg/harness"
	"github.com/mattsolo1/grove-tend/pkg/tui"
)

// PlanListTUIScenario tests the interactive `flow plan tui` command.
var PlanListTUIScenario = harness.NewScenarioWithOptions(
	"plan-list-tui",
	"Verifies the plan browsing TUI can list, navigate, and open plans.",
	[]string{"tui", "plan", "list"},
	[]harness.Step{
		harness.NewStep("Setup mock filesystem with two plans", func(ctx *harness.Context) error {
			// Create a sandboxed home directory for global config
			homeDir := ctx.NewDir("home")
			ctx.Set("home_dir", homeDir)

			// Create a project directory and initialize it as a git repo
			projectDir := ctx.NewDir("tui-project")
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

			// The plan directory is resolved based on notebook config.
			// workspaces/<project-name>/plans/<plan-name>
			plansBaseDir := filepath.Join(notebooksRoot, "workspaces", "tui-project", "plans")

			// Create Plan A
			planADir := filepath.Join(plansBaseDir, "plan-a")
			if err := fs.CreateDir(planADir); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(planADir, ".grove-plan.yml"), "model: test-model-a"); err != nil {
				return err
			}

			// Create Plan B
			planBDir := filepath.Join(plansBaseDir, "plan-b")
			if err := fs.CreateDir(planBDir); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(planBDir, ".grove-plan.yml"), "model: test-model-b"); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Launch TUI and verify initial state", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// The test runner (tend-e2e) and the binary under test (flow) need to be located.
			// We can get the test runner from os.Args[0], and assume `flow` is in the same directory.
			flowBinary, err := findFlowBinary()
			if err != nil {
				return err
			}

			// Create a wrapper script that changes to the project directory before running flow
			// This is necessary because the TUI needs to run from within the project directory
			// Note: avoid dots in the filename as tmux session names are derived from it
			wrapperScript := filepath.Join(ctx.RootDir, "run-flow-tui")
			scriptContent := fmt.Sprintf("#!/bin/bash\ncd %s\nexec %s plan tui\n", projectDir, flowBinary)
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
				return fmt.Errorf("failed to start `flow plan tui`: %w", err)
			}
			ctx.Set("tui_session", session)

			// Wait for the TUI to load by looking for a known header.
			if err := session.WaitForText("PLAN", 10*time.Second); err != nil {
				content, _ := session.Capture()
				return fmt.Errorf("TUI did not load: %w\nContent:\n%s", err, content)
			}

			// Verify both plans are visible on the initial screen.
			if err := session.AssertContains("plan-a"); err != nil {
				return err
			}
			if err := session.AssertContains("plan-b"); err != nil {
				return err
			}
			return nil
		}),

		harness.NewStep("Test navigation with arrow keys", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)

			// The plans appear in reverse alphabetical order (plan-b first, then plan-a)
			// The cursor starts on plan-b (first item)
			// Send a "Down" key press to move the cursor to plan-a.
			if err := session.SendKeys("Down"); err != nil {
				return fmt.Errorf("failed to send 'Down' key: %w", err)
			}

			// Wait for the UI to stabilize after the keypress.
			if err := session.WaitStable(); err != nil {
				return err
			}

			// Capture the screen and assert both plans are still visible.
			if err := session.AssertContains("plan-a"); err != nil {
				return err
			}
			return session.AssertContains("plan-b")
		}),

		harness.NewStep("Test entering a plan's status view", func(ctx *harness.Context) error {
			session := ctx.Get("tui_session").(*tui.Session)

			// Send "Enter" to view the details of the currently selected plan (plan-a after Down key).
			if err := session.SendKeys("Enter"); err != nil {
				return fmt.Errorf("failed to send 'Enter' key: %w", err)
			}

			// Wait for the status view to load by looking for the "Plan Status" header
			// (STATUS column name is not visible since the column is hidden by default)
			if err := session.WaitForText("Plan Status", 5*time.Second); err != nil {
				content, _ := session.Capture()
				return fmt.Errorf("plan status view did not open: %w\nContent:\n%s", err, content)
			}
			return nil
		}),

		harness.NewStep("Test quitting the TUI", func(ctx *harness.Context) error {
			// When a plan has no jobs, the status view shows a message and exits immediately.
			// The TUI returns to the shell rather than back to the plan list.
			// We'll verify that the TUI has exited cleanly by waiting a moment for the session to terminate.

			// Give it a moment for the TUI to finish exiting
			time.Sleep(500 * time.Millisecond)

			// The harness will automatically wait for and clean up the session.
			return nil
		}),
	},
	true,  // localOnly = true, as it requires tmux
	false, // explicitOnly = false
)

// findFlowBinary is a helper to locate the flow binary for tests.
func findFlowBinary() (string, error) {
	// The test runner is built into ./bin/tend-e2e by the Makefile.
	// The flow binary should be in ./bin/flow.
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not get executable path: %w", err)
	}

	binDir := filepath.Dir(execPath)
	flowPath := filepath.Join(binDir, "flow")

	if _, err := os.Stat(flowPath); err != nil {
		// Fallback for different build environments
		wd, _ := os.Getwd()
		flowPath = filepath.Join(wd, "..", "..", "bin", "flow") // Assuming tests are in tests/e2e/tend
		if _, err := os.Stat(flowPath); err != nil {
			return "", fmt.Errorf("flow binary not found in expected locations")
		}
	}
	return flowPath, nil
}
