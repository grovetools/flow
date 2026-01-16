package scenarios

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
)

// ZombieWorktreeLogRecreationScenario reproduces the issue where deleted worktree directories
// are recreated by long-running processes attempting to log.
var ZombieWorktreeLogRecreationScenario = harness.NewScenario(
	"zombie-worktree-log-recreation",
	"Verifies that deleted worktree directories are not recreated by logging.",
	[]string{"core", "logging", "worktree", "regression"},
	[]harness.Step{
		harness.NewStep("Setup project and worktree", func(ctx *harness.Context) error {
			projectDir := ctx.NewDir("zombie-test-proj")
			ctx.Set("project_dir", projectDir)

			worktreeDir := filepath.Join(projectDir, ".grove-worktrees", "zombie-feature")
			ctx.Set("worktree_dir", worktreeDir)

			// 1. Create grove.yml with file logging
			groveYML := `name: zombie-test-proj
version: "1.0"
logging:
  file:
    enabled: true
`
			if err := fs.WriteString(filepath.Join(projectDir, "grove.yml"), groveYML); err != nil {
				return err
			}

			// 2. Init git repo
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := repo.AddCommit("initial commit"); err != nil {
				return err
			}

			// 3. Create worktree
			return repo.CreateWorktree(worktreeDir, "zombie-feature")
		}),

		harness.NewStep("Start background logging process", func(ctx *harness.Context) error {
			worktreeDir := ctx.GetString("worktree_dir")

			// This Go program will run in the background, simulating a long-running process
			// like `flow plan tui` that holds a cwd in the worktree.
			// It runs for 10 seconds then exits to avoid hanging the test.
			program := `
package main

import (
	"fmt"
	"os"
	"time"
	"github.com/grovetools/core/logging"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <workdir>")
		os.Exit(1)
	}
	workDir := os.Args[1]
	if err := os.Chdir(workDir); err != nil {
		fmt.Printf("Failed to chdir to %s: %v\n", workDir, err)
		os.Exit(1)
	}

	// Run for 10 seconds then exit
	endTime := time.Now().Add(10 * time.Second)
	for time.Now().Before(endTime) {
		logging.Reset() // Reset to re-read config and paths on each log attempt
		log := logging.NewLogger("zombie-logger")
		log.Info("Background logger is still active.")
		time.Sleep(500 * time.Millisecond)
	}
}
`
			// Write the program to a temporary file
			tmpDir := ctx.NewDir("bg-process")
			programPath := filepath.Join(tmpDir, "main.go")
			if err := fs.WriteString(programPath, program); err != nil {
				return fmt.Errorf("failed to write background program: %w", err)
			}

			// Build and run the program (no context needed since it self-terminates)
			bgProcess := exec.Command("go", "run", programPath, worktreeDir)
			// Suppress output to avoid test noise
			bgProcess.Stdout = nil
			bgProcess.Stderr = nil

			if err := bgProcess.Start(); err != nil {
				return fmt.Errorf("failed to start background process: %w", err)
			}

			// Give it a moment to start logging
			time.Sleep(2 * time.Second)

			// Verify initial log file was created in the worktree
			logFiles, err := filepath.Glob(filepath.Join(worktreeDir, ".grove", "logs", "*.log"))
			if err != nil || len(logFiles) == 0 {
				return fmt.Errorf("background logger did not create initial log file in worktree")
			}

			return nil
		}),

		harness.NewStep("Delete the worktree directory", func(ctx *harness.Context) error {
			worktreeDir := ctx.GetString("worktree_dir")
			return os.RemoveAll(worktreeDir)
		}),

		harness.NewStep("Verify worktree directory is NOT recreated", func(ctx *harness.Context) error {
			worktreeDir := ctx.GetString("worktree_dir")

			// Wait to see if the logger process recreates the directory
			time.Sleep(2 * time.Second)

			// This is the core assertion. With the fix, the directory should NOT exist.
			_, err := os.Stat(worktreeDir)
			if err == nil {
				// Directory exists - that's a problem!
				content, _ := os.ReadDir(worktreeDir)
				var names []string
				for _, entry := range content {
					names = append(names, entry.Name())
				}
				return fmt.Errorf("worktree directory should not be recreated by the logger. Contents: %v", names)
			}
			if !os.IsNotExist(err) {
				return fmt.Errorf("unexpected error checking worktree directory: %w", err)
			}

			return nil // Directory doesn't exist - success!
		}),
	},
)
