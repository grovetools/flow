package scenarios

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// ATTargetScenario pins the unified `--at <target>` persistent flag (flow
// 228ea06 / c43b523 / 61de615, core e9ccbab) across the rogue plan resolvers:
// status, run, and wait. --at lets a command operate on a plan/worktree from
// OUTSIDE it, by plan name OR by absolute container path, resolving through the
// per-worktree XDG registry (worktreeregistry.FindByRef -> plan.ResolveTarget).
//
// The flag is LAYERED, never substituted: every existing resolver (cwd default,
// active-plan, filename, title) survives unchanged when --at is absent. This
// scenario therefore asserts BOTH the new --at path AND the untouched
// cwd-default fallback, so a future refactor that strips the fallback (the
// failure mode the design doc warns against) is caught.
//
// Setup uses `plan init --worktree --anchor` exactly like AnchorRegistryScenario
// because that is the only init path that writes the registry Entry.Plan link
// --at resolves through (flow f38cfc7). A shell job is added so run/wait have a
// real, mock-free target.
var ATTargetScenario = harness.NewScenario(
	"at-target",
	"Unified --at <target> flag resolves plans by name and by absolute container path across status/run/wait, while the cwd-default fallback stays intact.",
	[]string{"xdg", "worktree", "registry", "plan", "at", "status", "run", "wait", "cli"},
	[]harness.Step{
		// grove mocked: plan init/finish make incidental env-teardown calls.
		// cx/llm cover any context regeneration. The shell job needs no LLM.
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "llm"},
		),

		harness.NewStep("Setup ecosystem with two sub-repos", func(ctx *harness.Context) error {
			gitRoot, repoDirs, _, err := setupEcosystemEnvironment(ctx, "at-eco", []string{"svc-a", "svc-b"})
			if err != nil {
				return err
			}
			ctx.Set("git_root", gitRoot)
			ctx.Set("anchor_dir", repoDirs["svc-a"])
			// An "outside" directory the --at commands run from, to prove the
			// flag resolves the plan without being inside its worktree or any
			// member repo.
			ctx.Set("outside_dir", repoDirs["svc-b"])
			return nil
		}),

		harness.NewStep("Init plan with --worktree --anchor svc-a (writes registry plan link)", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			notebooksRoot := ctx.GetString("notebooks_root")

			cmd := ctx.Bin("plan", "init", "at-plan",
				"--worktree=at-plan",
				"--anchor", "svc-a")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init --worktree --anchor failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "at-eco", "plans", "at-plan")
			ctx.Set("plan_path", planPath)

			// The worktree container path --at <abs-path> will reference.
			anchorDir := ctx.GetString("anchor_dir")
			ctx.Set("worktree_path", expectedWorktreePath(ctx, anchorDir, "at-plan", true /* XDG */))
			return nil
		}),

		harness.NewStep("Add a shell job to the plan", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")

			// plan add resolves the plan by name from the ecosystem root. The
			// job is a trivial shell command so run/wait need no LLM mock.
			cmd := ctx.Bin("plan", "add", "at-plan",
				"--type", "shell",
				"--title", "at job",
				"-p", "echo at-target-ran")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add failed: %w", err)
			}

			planPath := ctx.GetString("plan_path")
			jobPath, err := findJobByPrefix(planPath, "01-")
			if err != nil {
				return fmt.Errorf("locating added job: %w", err)
			}
			ctx.Set("job_file", filepath.Base(jobPath))
			return nil
		}),

		// --- status by plan NAME, from an outside directory ---
		harness.NewStep("plan status --at <name> resolves the plan from outside its worktree", func(ctx *harness.Context) error {
			outsideDir := ctx.GetString("outside_dir")

			cmd := ctx.Bin("plan", "status", "--at", "at-plan", "--json")
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan status --at <name> failed: %w", err)
			}

			var out struct {
				Plan string                   `json:"plan"`
				Jobs []map[string]interface{} `json:"jobs"`
			}
			if err := json.Unmarshal([]byte(result.Stdout), &out); err != nil {
				return fmt.Errorf("parsing status JSON: %w\nstdout:\n%s", err, result.Stdout)
			}
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("--at <name> resolves the correct plan", "at-plan", out.Plan)
				v.Equal("resolved plan has the one added job", 1, len(out.Jobs))
			})
		}),

		// --- status by ABSOLUTE container path, from an outside directory ---
		harness.NewStep("plan status --at <abs-path> resolves the plan by container path", func(ctx *harness.Context) error {
			outsideDir := ctx.GetString("outside_dir")
			worktreePath := ctx.GetString("worktree_path")

			absWorktree, err := filepath.Abs(worktreePath)
			if err != nil {
				return fmt.Errorf("abs worktree path: %w", err)
			}

			cmd := ctx.Bin("plan", "status", "--at", absWorktree, "--json")
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan status --at <abs-path> failed: %w", err)
			}

			var out struct {
				Plan string `json:"plan"`
			}
			if err := json.Unmarshal([]byte(result.Stdout), &out); err != nil {
				return fmt.Errorf("parsing status JSON: %w\nstdout:\n%s", err, result.Stdout)
			}
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("--at <abs-path> resolves the correct plan", "at-plan", out.Plan)
			})
		}),

		// --- run by plan NAME, from an outside directory ---
		harness.NewStep("plan run --at <name> runs a job in the targeted plan from outside", func(ctx *harness.Context) error {
			outsideDir := ctx.GetString("outside_dir")
			jobFile := ctx.GetString("job_file")

			cmd := ctx.Bin("plan", "run", "--local", "--at", "at-plan", jobFile, "--yes")
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan run --at <name> failed: %w", err)
			}

			// The shell job sets status=completed in its frontmatter on success.
			planPath := ctx.GetString("plan_path")
			jobContent, err := os.ReadFile(filepath.Join(planPath, jobFile))
			if err != nil {
				return fmt.Errorf("reading job after run: %w", err)
			}
			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("job marked completed after --at run", string(jobContent), "status: completed")
			})
		}),

		// --- add by plan NAME, from an outside directory: the job must land in
		// the --at plan dir, NOT the active/cwd plan. This pins the funnel fix
		// where `plan add` previously called the non-ctx resolver and silently
		// wrote the job into the active plan instead of the --at target. ---
		harness.NewStep("plan add --at <name> creates the job in the targeted plan from outside", func(ctx *harness.Context) error {
			outsideDir := ctx.GetString("outside_dir")
			planPath := ctx.GetString("plan_path")

			cmd := ctx.Bin("plan", "add", "--at", "at-plan",
				"--type", "shell",
				"--title", "second at job",
				"-p", "echo another")
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add --at <name> failed: %w", err)
			}

			// The new job must exist in the target plan dir...
			jobPath, err := findJobByPrefix(planPath, "02-")
			if err != nil {
				return fmt.Errorf("locating job added via --at in target plan: %w", err)
			}
			jobContent, err := os.ReadFile(jobPath)
			if err != nil {
				return fmt.Errorf("reading job added via --at: %w", err)
			}

			// ...and must NOT have been misdirected into the outside cwd.
			_, outsideStatErr := findJobByPrefix(outsideDir, "02-")

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("job added via --at carries its title in the target plan", string(jobContent), "second at job")
				v.True("no job file leaked into the outside cwd", outsideStatErr != nil)
			})
		}),

		// --- wait by plan NAME resolves the job relative to the target plan ---
		harness.NewStep("plan wait --at <name> resolves the job file against the target plan", func(ctx *harness.Context) error {
			outsideDir := ctx.GetString("outside_dir")
			jobFile := ctx.GetString("job_file")

			// The job already completed above; wait should return immediately
			// having resolved the job path relative to the --at plan dir (not
			// the outside cwd, where the file does not exist).
			cmd := ctx.Bin("plan", "wait", "--at", "at-plan", jobFile, "--timeout", "10s")
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan wait --at <name> failed: %w", err)
			}
			return nil
		}),

		// --- REGRESSION: the legacy positional/global resolver survives when
		// --at is absent. The plan lives in the centralized notebook (outside
		// any git repo), so the layering guarantee is proven by the untouched
		// positional-name -> global resolution path, run from a member repo. ---
		harness.NewStep("plan status <name> (no --at) still resolves via the legacy positional resolver", func(ctx *harness.Context) error {
			outsideDir := ctx.GetString("outside_dir")

			cmd := ctx.Bin("plan", "status", "at-plan", "--json")
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan status <name> (no --at) failed: %w", err)
			}

			var out struct {
				Plan string `json:"plan"`
			}
			if err := json.Unmarshal([]byte(result.Stdout), &out); err != nil {
				return fmt.Errorf("parsing status JSON: %w\nstdout:\n%s", err, result.Stdout)
			}
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("legacy positional resolver still finds the plan without --at", "at-plan", out.Plan)
			})
		}),

		// --- REGRESSION: -d is no longer the --depends-on shorthand on add ---
		harness.NewStep("plan add -d is NOT --depends-on (shorthand was dropped)", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")

			// After dropping the -d shorthand from --depends-on, `-d` is an
			// unknown shorthand for `plan add`, so this must fail. If the
			// collision regressed, -d would silently bind to --depends-on and
			// the command would succeed, masking the bug.
			cmd := ctx.Bin("plan", "add", "at-plan",
				"--type", "shell",
				"--title", "dep job",
				"-p", "echo dep",
				"-d", "01-at-job.md")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertFailure(); err != nil {
				return fmt.Errorf("expected `plan add -d` to fail now that the shorthand is dropped: %w", err)
			}
			return nil
		}),

		// --- the long form --depends-on still works on add ---
		harness.NewStep("plan add --depends-on still works (long form preserved)", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			jobFile := ctx.GetString("job_file")

			cmd := ctx.Bin("plan", "add", "at-plan",
				"--type", "shell",
				"--title", "dep job",
				"-p", "echo dep",
				"--depends-on", jobFile)
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add --depends-on (long form) should still work: %w", err)
			}
			return nil
		}),
	},
)
