package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
)

// fixtureGoVersion is the go directive written into the ecosystem's source
// go.work. configureGoWorkspace must propagate THIS version into the
// generated worktree go.work; the bug it guards against is a silent fallback
// to "go 1.24.4" when the source git root can't be located (the Phase 5 /
// risk-9 "go.work degradation under XDG" dispute). We deliberately pick a
// version that is neither the fallback nor the repo's real toolchain so the
// assertion is unambiguous.
const fixtureGoVersion = "go 1.23.5"

// SiblingWorkspacesLifecycleScenario is the XDG-worktree centerpiece: it drives
// a sibling-workspaces plan through its full lifecycle — create, resolve, run a
// job, update, finish — and pins every destructive and path-resolution
// behavior against the XDG layout.
//
//	init --sibling-workspaces svc-a,svc-b --worktree=wt1
//	  -> worktree at the XDG location, sibling repos linked, go.work carries the
//	     fixture's real go version (NOT the 1.24.4 fallback)
//	plan status                 -> resolves the XDG worktree
//	run a shell job             -> DetermineWorkingDirectory lands the cwd
//	                               INSIDE the XDG worktree
//	plan update-worktree        -> succeeds against the XDG worktree
//	plan finish --prune-worktree -> the XDG worktree dir is REMOVED while the
//	                               identifier dir and the worktrees base SURVIVE
//
// grove env up/down is intentionally NOT exercised here — see the dedicated
// step for the harness-gap rationale.
var SiblingWorkspacesLifecycleScenario = harness.NewScenario(
	"sibling-workspaces-lifecycle",
	"Full XDG-worktree lifecycle for a --sibling-workspaces plan (create, resolve, run, update, finish).",
	[]string{"xdg", "worktree", "sibling-workspaces", "lifecycle", "plan"},
	[]harness.Step{
		// grove is mocked so plan finish's incidental env-teardown/grove calls
		// are safe no-ops; llm/cx cover the shell job's context regeneration.
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "llm"},
		),

		harness.NewStep("Setup realpath-rooted ecosystem with two sibling Go repos", func(ctx *harness.Context) error {
			gitRoot, repoDirs, _, err := setupEcosystemEnvironment(ctx, "lifecycle-eco", []string{"svc-a", "svc-b"})
			if err != nil {
				return err
			}
			ctx.Set("git_root", gitRoot)

			// Each sibling becomes a Go module. The go.mod must be COMMITTED so
			// it shows up in the linked worktree git creates.
			for repo, dir := range repoDirs {
				goMod := fmt.Sprintf("module example.com/%s\n\n%s\n", repo, fixtureGoVersion)
				if err := fs.WriteString(filepath.Join(dir, "go.mod"), goMod); err != nil {
					return err
				}
				if err := git.Add(dir, "go.mod"); err != nil {
					return err
				}
				if err := git.Commit(dir, "Add go.mod"); err != nil {
					return err
				}
			}

			// Source go.work at the ecosystem root carries the fixture go
			// version. configureGoWorkspace reads it (FindRootGoWorkspace walks
			// up from the source git root) to stamp the worktree go.work. It is
			// only read from disk, so it need not be committed.
			goWork := fmt.Sprintf("%s\n\nuse (\n\t./svc-a\n\t./svc-b\n)\n", fixtureGoVersion)
			return fs.WriteString(filepath.Join(gitRoot, "go.work"), goWork)
		}),

		harness.NewStep("Init plan with --sibling-workspaces under XDG", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			notebooksRoot := ctx.GetString("notebooks_root")

			// --worktree REQUIRES the = form: its NoOptDefVal turns a bare
			// --worktree into the "__AUTO__" marker, so "--worktree", "wt1"
			// would be parsed as a flag with no value plus a stray positional.
			cmd := ctx.Bin("plan", "init", "lifecycle-plan",
				"--sibling-workspaces=svc-a,svc-b",
				"--worktree=wt1")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "lifecycle-eco", "plans", "lifecycle-plan")
			ctx.Set("plan_path", planPath)

			worktreePath := expectedWorktreePath(ctx, gitRoot, "wt1", true /* sibling/XDG */)
			ctx.Set("worktree_path", worktreePath)
			return nil
		}),

		harness.NewStep("Assert XDG worktree location, linked siblings, and go.work version", func(ctx *harness.Context) error {
			worktreePath := ctx.GetString("worktree_path")

			if err := fs.AssertExists(worktreePath); err != nil {
				return fmt.Errorf("XDG worktree should exist at %s: %w", worktreePath, err)
			}

			// Sibling repos linked as worktrees (each has its own .git pointer).
			for _, repo := range []string{"svc-a", "svc-b"} {
				if err := fs.AssertExists(filepath.Join(worktreePath, repo, ".git")); err != nil {
					return fmt.Errorf("sibling repo %q should be linked into the worktree: %w", repo, err)
				}
				if err := fs.AssertExists(filepath.Join(worktreePath, repo, "go.mod")); err != nil {
					return fmt.Errorf("sibling repo %q go.mod should be present in the worktree: %w", repo, err)
				}
			}

			// go.work carries the fixture version, NOT the 1.24.4 fallback.
			goWorkPath := filepath.Join(worktreePath, "go.work")
			content, err := fs.ReadString(goWorkPath)
			if err != nil {
				return fmt.Errorf("worktree go.work should exist: %w", err)
			}
			if !strings.Contains(content, fixtureGoVersion) {
				return fmt.Errorf("worktree go.work should carry the source go version %q, got:\n%s", fixtureGoVersion, content)
			}
			if strings.Contains(content, "go 1.24.4") {
				return fmt.Errorf("worktree go.work fell back to the hardcoded go 1.24.4 instead of the source version; got:\n%s", content)
			}
			return nil
		}),

		harness.NewStep("plan status resolves the XDG worktree", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")

			cmd := ctx.Bin("plan", "status", "lifecycle-plan", "--json", "-d", gitRoot)
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan status failed: %w", err)
			}
			// The status JSON includes a worktree block keyed by name; resolving
			// the worktree at all proves the XDG layout is discoverable.
			if !strings.Contains(result.Stdout, "\"wt1\"") {
				return fmt.Errorf("plan status JSON should reference worktree wt1, got:\n%s", result.Stdout)
			}
			return nil
		}),

		harness.NewStep("Run a shell job and assert its cwd landed inside the XDG worktree", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			worktreePath := ctx.GetString("worktree_path")

			// The job records its own working directory. With an XDG worktree,
			// DetermineWorkingDirectory must resolve cwd to that worktree.
			addCmd := ctx.Bin("plan", "add", "lifecycle-plan",
				"--type", "shell",
				"--title", "cwd-check",
				"-p", "pwd > job_cwd.txt")
			addCmd.Dir(gitRoot)
			if err := addCmd.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("plan add shell job failed: %w", err)
			}

			if err := ctx.Bin("plan", "set", "lifecycle-plan").Dir(gitRoot).Run().AssertSuccess(); err != nil {
				return fmt.Errorf("plan set active failed: %w", err)
			}

			runCmd := ctx.Bin("run", "--local", "cwd-check", "--yes")
			runCmd.Dir(gitRoot)
			result := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("shell job run failed: %w", err)
			}

			// The marker file lands in the job's cwd. Its presence inside the
			// XDG worktree is the assertion that the cwd was the worktree.
			markerPath := filepath.Join(worktreePath, "job_cwd.txt")
			if err := fs.AssertExists(markerPath); err != nil {
				return fmt.Errorf("shell job should have run inside the XDG worktree (no marker at %s): %w", markerPath, err)
			}
			recorded, err := fs.ReadString(markerPath)
			if err != nil {
				return fmt.Errorf("reading recorded cwd: %w", err)
			}
			// pwd reports the symlink-resolved path; compare resolved forms.
			gotResolved, _ := filepath.EvalSymlinks(strings.TrimSpace(recorded))
			wantResolved, _ := filepath.EvalSymlinks(worktreePath)
			if gotResolved != wantResolved {
				return fmt.Errorf("shell job cwd %q did not match XDG worktree %q", gotResolved, wantResolved)
			}
			return nil
		}),

		harness.NewStep("plan update-worktree succeeds against the XDG worktree", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")

			cmd := ctx.Bin("plan", "update-worktree", "lifecycle-plan", "-d", gitRoot)
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan update-worktree failed: %w", err)
			}
			// The worktree must still be present after the rebase-on-main update.
			return fs.AssertExists(ctx.GetString("worktree_path"))
		}),

		harness.NewStep("grove env up/down: documented harness gap", func(ctx *harness.Context) error {
			// grove env up/down provisions environments through a running
			// `groved` daemon (the Phase-4 daemon Workspace.Name patch lives in
			// that process). The tend harness does NOT start groved, and the
			// real grove binary is mocked here, so a genuine env up/down round
			// trip cannot be exercised end-to-end in this scenario.
			//
			// Coverage for the daemon side lives in the daemon repo's unit
			// tests (env/manager_test Restore-over-XDG and dashboard/state_test
			// per Phase 5); plan finish below still drives ItemEnvTeardown
			// against the mocked grove, exercising the teardown wiring. This
			// step records the gap explicitly rather than silently skipping it.
			return nil
		}),

		harness.NewStep("Finish prunes the XDG worktree while the base and identifier dir survive", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			planPath := ctx.GetString("plan_path")
			worktreePath := ctx.GetString("worktree_path")

			// finish requires the plan to be in review status first.
			reviewCmd := ctx.Bin("plan", "review", "lifecycle-plan", "-d", gitRoot)
			reviewCmd.Dir(gitRoot)
			if err := reviewCmd.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("plan review failed: %w", err)
			}

			finishCmd := ctx.Bin("plan", "finish", "lifecycle-plan",
				"--yes", "--prune-worktree", "--delete-branch", "-d", gitRoot)
			finishCmd.Dir(gitRoot)
			result := finishCmd.Run()
			ctx.ShowCommandOutput(finishCmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan finish failed: %w", err)
			}

			// The worktree directory itself is gone...
			if err := fs.AssertNotExists(worktreePath); err != nil {
				return fmt.Errorf("XDG worktree dir should be pruned: %w", err)
			}
			// ...but the identifier container and the worktrees base survive:
			// the prune guard must remove ONLY the leaf worktree, never its
			// containers (risk-1 destructive-deletion-guards-mis-scoped).
			identifierDir := filepath.Dir(worktreePath)
			if err := fs.AssertExists(identifierDir); err != nil {
				return fmt.Errorf("identifier dir should survive prune: %w", err)
			}
			worktreesBase := filepath.Dir(identifierDir)
			if err := fs.AssertExists(worktreesBase); err != nil {
				return fmt.Errorf("worktrees base should survive prune: %w", err)
			}
			// The plan slug is unset / archived after a clean finish.
			_ = planPath
			return nil
		}),
	},
)
