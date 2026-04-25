package scenarios

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
)

// PlaybookEnvInjectionScenario verifies that PLAYBOOK_ROOT and
// PLAYBOOK_NAME are injected into the environment of a headless_agent
// job belonging to a playbook-scoped plan, and are NOT injected for
// plans with no active playbook.
var PlaybookEnvInjectionScenario = harness.NewScenario(
	"playbook-env-injection",
	"Verify PLAYBOOK_ROOT is injected into headless_agent environment when a playbook is active",
	[]string{"playbook", "env"},
	[]harness.Step{
		harness.NewStep("Setup playbook environment", func(ctx *harness.Context) error {
			projectDir, _, playbookDir, err := setupPlaybookEnvironment(ctx, "env-project", "test-pb")
			if err != nil {
				return err
			}
			// A git commit is required for worktree creation.
			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# env-project\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("initial"); err != nil {
				return err
			}
			ctx.Set("playbook_abs_path", playbookDir)
			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "claude"},
			harness.Mock{CommandName: "tmux"},
		),

		harness.NewStep("Init playbook-scoped plan with worktree", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "env-plan", "--playbook", "test-pb", "--worktree")
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "env-project", "plans", "env-plan")
			ctx.Set("plan_path", planPath)
			return nil
		}),

		harness.NewStep("Create headless_agent job and run it with env-dump mock", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			jobContent := "---\nid: env-dump\ntitle: Env Dump\ntype: headless_agent\nstatus: pending\n---\nDump env vars."
			jobPath := filepath.Join(planPath, "01-env-dump.md")
			if err := fs.WriteString(jobPath, jobContent); err != nil {
				return err
			}

			dumpPath := filepath.Join(ctx.RootDir, "claude-env-dump.txt")
			ctx.Set("dump_path", dumpPath)

			// --local keeps execution in this process so the test-specific
			// GROVE_MOCK_CLAUDE_DUMP_ENV env var reaches the mock claude.
			// Daemon-routed runs spawn the agent under the daemon's clean
			// environment and would lose this var.
			cmd := ctx.Bin("plan", "run", "--local", jobPath, "--yes")
			cmd.Dir(projectDir).Env("GROVE_MOCK_CLAUDE_DUMP_ENV=" + dumpPath)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			// We do not strictly assert success — the mock claude exits
			// 0 but upstream status handling may flag incomplete output.
			// The env dump is what we really care about.
			return nil
		}),

		harness.NewStep("Verify PLAYBOOK_ROOT present in dumped env", func(ctx *harness.Context) error {
			dumpPath := ctx.GetString("dump_path")
			playbookDir := ctx.GetString("playbook_abs_path")

			data, err := os.ReadFile(dumpPath)
			if err != nil {
				return fmt.Errorf("env dump file not written by mock claude: %w", err)
			}
			content := string(data)
			if !strings.Contains(content, "PLAYBOOK_ROOT=") {
				return fmt.Errorf("expected PLAYBOOK_ROOT in env dump, got:\n%s", content)
			}
			if !strings.Contains(content, "PLAYBOOK_NAME=test-pb") {
				return fmt.Errorf("expected PLAYBOOK_NAME=test-pb in env dump, got:\n%s", content)
			}
			// The value must resolve to the absolute playbook path.
			// Compare via filepath.EvalSymlinks to tolerate /private
			// prefix differences on macOS.
			var got string
			for _, line := range strings.Split(content, "\n") {
				if strings.HasPrefix(line, "PLAYBOOK_ROOT=") {
					got = strings.TrimPrefix(line, "PLAYBOOK_ROOT=")
					break
				}
			}
			gotResolved, _ := filepath.EvalSymlinks(got)
			wantResolved, _ := filepath.EvalSymlinks(playbookDir)
			if gotResolved == "" {
				gotResolved = got
			}
			if wantResolved == "" {
				wantResolved = playbookDir
			}
			if gotResolved != wantResolved {
				return fmt.Errorf("PLAYBOOK_ROOT resolves to %q, want %q", gotResolved, wantResolved)
			}
			return nil
		}),

		harness.NewStep("Negative: non-playbook plan does not re-inject test playbook", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			playbookDir := ctx.GetString("playbook_abs_path")

			cmd := ctx.Bin("plan", "init", "no-pb-plan", "--worktree")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}
			planPath := filepath.Join(notebooksRoot, "workspaces", "env-project", "plans", "no-pb-plan")

			jobContent := "---\nid: env-dump2\ntitle: Env Dump 2\ntype: headless_agent\nstatus: pending\n---\nDump env vars."
			jobPath := filepath.Join(planPath, "01-env-dump2.md")
			if err := fs.WriteString(jobPath, jobContent); err != nil {
				return err
			}

			dumpPath := filepath.Join(ctx.RootDir, "claude-env-dump-no-pb.txt")
			runCmd := ctx.Bin("plan", "run", "--local", jobPath, "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_CLAUDE_DUMP_ENV=" + dumpPath)
			_ = runCmd.Run()

			data, err := os.ReadFile(dumpPath)
			if err != nil {
				return fmt.Errorf("env dump file not written for non-playbook plan: %w", err)
			}
			// Flow itself must not add PLAYBOOK_ROOT pointing at the
			// test playbook. The parent shell may already export an
			// unrelated PLAYBOOK_ROOT (CI or dev workstations) which
			// we tolerate — we only fail if the injected value
			// matches the test fixture.
			wantResolved, _ := filepath.EvalSymlinks(playbookDir)
			if wantResolved == "" {
				wantResolved = playbookDir
			}
			for _, line := range strings.Split(string(data), "\n") {
				if !strings.HasPrefix(line, "PLAYBOOK_ROOT=") {
					continue
				}
				val := strings.TrimPrefix(line, "PLAYBOOK_ROOT=")
				gotResolved, _ := filepath.EvalSymlinks(val)
				if gotResolved == "" {
					gotResolved = val
				}
				if gotResolved == wantResolved {
					return fmt.Errorf("flow should not inject the test PLAYBOOK_ROOT for a non-playbook plan, got: %s", line)
				}
			}
			return nil
		}),
	},
)
