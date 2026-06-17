package scenarios

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/grovetools/core/pkg/worktreeregistry"
	"github.com/grovetools/core/util/pathutil"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// CoordinatorWorkflowScenario replays the exact `flow plan` command sequence the
// feature coordinator / sub-coordinator SOP runs end-to-end, so the workflow is
// regression-pinned as a single scenario rather than scattered across the unit
// surface. It is the integration counterpart to ATTargetScenario (the --at
// funnel) and AnchorRegistryScenario (anchored worktree + registry lifecycle):
// where those pin individual contracts, this proves the contracts compose into
// the real coordinator workflow.
//
// The sequence, all from OUTSIDE the worktree (a sibling member repo) except the
// init/review/finish that the orchestrator runs from the ecosystem root:
//
//	init feat --worktree=feat --anchor svc-a   -> anchored worktree + registry Entry.Plan="feat"
//	add  --at feat --type file        spec     -> 01 lands in the feat plan (the --at funnel guard)
//	add  --at feat --type interactive_agent    -> 02 carries skill+type frontmatter (skill resolves at RUN time,
//	                                              so add succeeds with no skill installed in the sandbox)
//	status --at feat --json                    -> resolves feat with both jobs from outside
//	add  --at feat --type shell; run --local   -> mock-free shell run -> completed
//	run  --at feat <agent>  (mocks)            -> running + briefing-*.xml carrying the prompt
//	complete --at feat <agent>                 -> completed (after lockfile removal)
//	wait --at feat <shell>                     -> returns immediately (already complete)
//	review + finish --prune-worktree           -> worktree dir + registry entry deleted
//
// Depends on the --at funnel fix (flow 1ba7e62): before it, `plan add --at feat`
// silently wrote into the active/cwd plan instead of the target, so steps 4-5
// would land jobs in the wrong place.
var CoordinatorWorkflowScenario = harness.NewScenario(
	"coordinator-workflow",
	"Replays the coordinator -> sub-coordinator plan command sequence (init --worktree --anchor, --at add/status/run/complete/wait, review, finish --prune-worktree) end-to-end.",
	[]string{"xdg", "worktree", "coordinator", "workflow", "plan", "at"},
	[]harness.Step{
		// grove mocked: init/finish make incidental env-teardown calls.
		// cx/llm cover any context regeneration. claude/tmux back the
		// interactive_agent run so it reaches `running` hermetically.
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "claude"},
			harness.Mock{CommandName: "tmux"},
		),

		harness.NewStep("Setup ecosystem with two sub-repos (anchor svc-a, outside svc-b)", func(ctx *harness.Context) error {
			gitRoot, repoDirs, _, err := setupEcosystemEnvironment(ctx, "coord-eco", []string{"svc-a", "svc-b"})
			if err != nil {
				return err
			}
			ctx.Set("git_root", gitRoot)
			ctx.Set("anchor_dir", repoDirs["svc-a"])
			// Every --at command runs from here, proving the flag resolves the
			// plan without being inside its worktree or any member of it.
			ctx.Set("outside_dir", repoDirs["svc-b"])
			return nil
		}),

		harness.NewStep("init feat --worktree=feat --anchor svc-a writes the anchored worktree + registry plan link", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			notebooksRoot := ctx.GetString("notebooks_root")
			anchorDir := ctx.GetString("anchor_dir")

			// --worktree REQUIRES the = form: its NoOptDefVal turns a bare
			// --worktree into the "__AUTO__" marker. --anchor is a plain string.
			cmd := ctx.Bin("plan", "init", "feat",
				"--worktree=feat",
				"--anchor", "svc-a")
			cmd.Dir(gitRoot)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init --worktree --anchor failed: %w", err)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "coord-eco", "plans", "feat")
			ctx.Set("plan_path", planPath)

			// Worktree is anchored under svc-a's XDG base, computed via the SAME
			// DirIdentifier core uses so the expectation never drifts.
			worktreePath := expectedWorktreePath(ctx, anchorDir, "feat", true /* XDG */)
			ctx.Set("worktree_path", worktreePath)
			if err := fs.AssertExists(worktreePath); err != nil {
				return fmt.Errorf("anchored worktree should exist at %s: %w", worktreePath, err)
			}

			// Registry entry: read the JSON directly (NOT worktreeregistry.Load,
			// which resolves StateDir from the runner env, not the sandbox).
			absWorktree, err := filepath.Abs(worktreePath)
			if err != nil {
				return fmt.Errorf("abs worktree path: %w", err)
			}
			registryPath := filepath.Join(ctx.StateDir(), "grove", "worktrees", pathutil.WorktreeID(absWorktree)+".json")
			ctx.Set("registry_path", registryPath)
			if err := fs.AssertExists(registryPath); err != nil {
				return fmt.Errorf("per-worktree registry JSON should exist at %s: %w", registryPath, err)
			}

			data, err := fs.ReadString(registryPath)
			if err != nil {
				return fmt.Errorf("reading registry entry: %w", err)
			}
			entry := worktreeregistry.Entry{}
			if uerr := json.Unmarshal([]byte(data), &entry); uerr != nil {
				return fmt.Errorf("unmarshal registry entry: %w", uerr)
			}

			// owner must be the ANCHOR (svc-a) path; resolve symlinks both sides
			// for the macOS /var -> /private/var realpath divergence.
			wantOwner, _ := filepath.Abs(anchorDir)
			gotOwner := entry.Owner
			if r, rerr := filepath.EvalSymlinks(gotOwner); rerr == nil {
				gotOwner = r
			}
			if w, werr := filepath.EvalSymlinks(wantOwner); werr == nil {
				wantOwner = w
			}

			// Claude folder-trust pre-seed: Prepare seeds ~/.claude.json (in the
			// sandbox HOME) so agents launched inside the worktree skip the
			// interactive trust prompt. Trust is per-exact-path and keyed by the
			// CANONICAL path (the same form flow hands Claude), so canonicalize
			// the expected keys. Both the container AND the <worktree>/<repo>
			// subdir (svc-a, the anchor member) must be trusted.
			claudeJSONPath := filepath.Join(ctx.HomeDir(), ".claude.json")
			claudeData, claudeErr := fs.ReadString(claudeJSONPath)
			claudeTrust := map[string]any{}
			if claudeErr == nil {
				_ = json.Unmarshal([]byte(claudeData), &claudeTrust)
			}
			wantContainerKey, _ := pathutil.CanonicalPath(worktreePath)
			wantRepoKey, _ := pathutil.CanonicalPath(filepath.Join(worktreePath, "svc-a"))

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("registry Entry.Plan links to the feat plan", "feat", entry.Plan)
				v.Equal("registry owner is the anchor (svc-a)", wantOwner, gotOwner)
				v.True("registry repos populated", len(entry.Repos) > 0)
				v.True("Claude trust file (~/.claude.json) seeded in sandbox HOME", claudeErr == nil)
				v.True("Claude trust seeded for the worktree container", claudeTrustAccepted(claudeTrust, wantContainerKey))
				v.True("Claude trust seeded for the anchor repo subdir (svc-a)", claudeTrustAccepted(claudeTrust, wantRepoKey))
			})
		}),

		harness.NewStep("add --at feat --type file lands 01-spec in the FEAT plan from outside", func(ctx *harness.Context) error {
			outsideDir := ctx.GetString("outside_dir")
			planPath := ctx.GetString("plan_path")

			// The coordinator seeds the ticket as a file job. Pre-write it in the
			// outside cwd; ResolvePromptSource finds it relative to cwd.
			if err := fs.WriteString(filepath.Join(outsideDir, "ticket.md"), "# Ticket\n\nBuild the feature.\n"); err != nil {
				return fmt.Errorf("writing ticket.md: %w", err)
			}

			cmd := ctx.Bin("plan", "add", "--at", "feat",
				"--title", "spec",
				"--type", "file",
				"-f", "ticket.md")
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add --at feat --type file failed: %w", err)
			}

			// The job must land in the FEAT plan dir, not the outside cwd.
			jobPath, err := findJobByPrefix(planPath, "01-")
			if err != nil {
				return fmt.Errorf("locating 01-spec in feat plan (the --at funnel guard): %w", err)
			}
			content, err := os.ReadFile(jobPath)
			if err != nil {
				return fmt.Errorf("reading spec job: %w", err)
			}
			_, leakErr := findJobByPrefix(outsideDir, "01-")

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("spec job is a file job", string(content), "type: file")
				v.True("no job leaked into the outside cwd", leakErr != nil)
			})
		}),

		harness.NewStep("add --at feat --type interactive_agent --skill carries skill+type frontmatter from outside", func(ctx *harness.Context) error {
			outsideDir := ctx.GetString("outside_dir")
			planPath := ctx.GetString("plan_path")

			// --skill is resolved at RUN time, not add time, so this succeeds
			// even though grove-feature-subcoordinator is not installed in the
			// sandbox — the coordinator bootstrap is testable as pure job state.
			cmd := ctx.Bin("plan", "add", "--at", "feat",
				"--title", "coordinate-feat",
				"--type", "interactive_agent",
				"--skill", "grove-feature-subcoordinator",
				"-p", "build the thing")
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add --at feat --type interactive_agent failed: %w", err)
			}

			jobPath, err := findJobByPrefix(planPath, "02-")
			if err != nil {
				return fmt.Errorf("locating 02-coordinate-feat in feat plan: %w", err)
			}
			content, err := os.ReadFile(jobPath)
			if err != nil {
				return fmt.Errorf("reading coordinate-feat job: %w", err)
			}
			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("coordinate job is an interactive_agent", string(content), "type: interactive_agent")
				v.Contains("coordinate job carries the subcoordinator skill", string(content), "skill: grove-feature-subcoordinator")
			})
		}),

		harness.NewStep("status --at feat --json resolves the feat plan with both jobs from outside", func(ctx *harness.Context) error {
			outsideDir := ctx.GetString("outside_dir")

			cmd := ctx.Bin("plan", "status", "--at", "feat", "--json")
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan status --at feat --json failed: %w", err)
			}

			var out struct {
				Plan string                   `json:"plan"`
				Jobs []map[string]interface{} `json:"jobs"`
			}
			if err := json.Unmarshal([]byte(result.Stdout), &out); err != nil {
				return fmt.Errorf("parsing status JSON: %w\nstdout:\n%s", err, result.Stdout)
			}
			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("--at resolves the feat plan", "feat", out.Plan)
				v.Equal("feat plan has the spec + coordinate jobs", 2, len(out.Jobs))
			})
		}),

		harness.NewStep("add + run --local --at feat a shell job completes mock-free from outside", func(ctx *harness.Context) error {
			outsideDir := ctx.GetString("outside_dir")
			planPath := ctx.GetString("plan_path")

			addCmd := ctx.Bin("plan", "add", "--at", "feat",
				"--title", "dummy",
				"--type", "shell",
				"-p", "echo hi")
			addCmd.Dir(outsideDir)
			addResult := addCmd.Run()
			ctx.ShowCommandOutput(addCmd.String(), addResult.Stdout, addResult.Stderr)
			if err := addResult.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add --at feat --type shell failed: %w", err)
			}

			shellJobPath, err := findJobByPrefix(planPath, "03-")
			if err != nil {
				return fmt.Errorf("locating 03-dummy shell job: %w", err)
			}
			shellJobFile := filepath.Base(shellJobPath)
			ctx.Set("shell_job_file", shellJobFile)

			runCmd := ctx.Bin("plan", "run", "--local", "--at", "feat", shellJobFile, "--yes")
			runCmd.Dir(outsideDir)
			runResult := runCmd.Run()
			ctx.ShowCommandOutput(runCmd.String(), runResult.Stdout, runResult.Stderr)
			if err := runResult.AssertSuccess(); err != nil {
				return fmt.Errorf("plan run --local --at feat <shell> failed: %w", err)
			}

			jobContent, err := os.ReadFile(shellJobPath)
			if err != nil {
				return fmt.Errorf("reading shell job after run: %w", err)
			}
			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("shell job marked completed after --at run", string(jobContent), "status: completed")
			})
		}),

		// The coordinate-feat job (02) carries --skill grove-feature-subcoordinator
		// to pin the coordinator's bootstrap job SPEC (asserted above as created
		// state). It cannot be RUN in the sandbox: --skill is resolved at run time
		// when building the briefing XML, and an unauthorized/uninstalled skill
		// makes that build fail by design (the workspace [skills] use gate). So
		// the agent run -> briefing -> complete lifecycle is exercised by a
		// dedicated skill-less interactive_agent job, exactly as job 141 planned
		// ("for deterministic runs ... optionally run the interactive_agent under
		// mocks"). This still proves the real coordinator behavior: the briefing
		// is materialized from the job prompt and the job advances running ->
		// completed under the claude/tmux mocks.
		harness.NewStep("add + run --at feat a skill-less agent job launches under mocks (running + briefing carries the prompt)", func(ctx *harness.Context) error {
			outsideDir := ctx.GetString("outside_dir")
			planPath := ctx.GetString("plan_path")

			addCmd := ctx.Bin("plan", "add", "--at", "feat",
				"--title", "agent-run",
				"--type", "interactive_agent",
				"-p", "drive the agent run")
			addCmd.Dir(outsideDir)
			addResult := addCmd.Run()
			ctx.ShowCommandOutput(addCmd.String(), addResult.Stdout, addResult.Stderr)
			if err := addResult.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add --at feat agent-run failed: %w", err)
			}

			runJobPath, err := findJobByPrefix(planPath, "04-")
			if err != nil {
				return fmt.Errorf("locating 04-agent-run job: %w", err)
			}
			runJobFile := filepath.Base(runJobPath)
			ctx.Set("run_job_file", runJobFile)

			cmd := ctx.Bin("plan", "run", "--at", "feat", runJobFile)
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			jobContent, err := os.ReadFile(runJobPath)
			if err != nil {
				return fmt.Errorf("reading agent job after run: %w", err)
			}

			// The briefing XML the agent would have been handed lives under the
			// plan's .artifacts/<job>/ dir and contains the prompt payload.
			briefings, err := filepath.Glob(filepath.Join(planPath, ".artifacts", "*", "briefing-*.xml"))
			if err != nil {
				return fmt.Errorf("globbing briefing files: %w", err)
			}
			if len(briefings) == 0 {
				return fmt.Errorf("expected a briefing-*.xml under %s/.artifacts\nrun stderr:\n%s\njob content:\n%s", planPath, result.Stderr, string(jobContent))
			}
			briefingContent, err := fs.ReadString(briefings[0])
			if err != nil {
				return fmt.Errorf("reading briefing file: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("agent job reached running", string(jobContent), "status: running")
				v.Contains("briefing carries the prompt payload", briefingContent, "drive the agent run")
			})
		}),

		harness.NewStep("complete --at feat <agent> drives the agent job to completed", func(ctx *harness.Context) error {
			outsideDir := ctx.GetString("outside_dir")
			planPath := ctx.GetString("plan_path")
			agentJobFile := ctx.GetString("run_job_file")
			agentJobPath := filepath.Join(planPath, agentJobFile)

			// Simulate the agent finishing: drop its run lock, then complete.
			_ = fs.RemoveIfExists(agentJobPath + ".lock")

			cmd := ctx.Bin("plan", "complete", "--at", "feat", agentJobFile)
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan complete --at feat <agent> failed: %w", err)
			}

			jobContent, err := os.ReadFile(agentJobPath)
			if err != nil {
				return fmt.Errorf("reading agent job after complete: %w", err)
			}
			return ctx.Verify(func(v *verify.Collector) {
				v.Contains("agent job marked completed", string(jobContent), "status: completed")
			})
		}),

		harness.NewStep("wait --at feat <shell> resolves the job against the target plan and returns immediately", func(ctx *harness.Context) error {
			outsideDir := ctx.GetString("outside_dir")
			shellJobFile := ctx.GetString("shell_job_file")

			// The shell job already completed; wait must resolve the job file
			// relative to the feat plan dir (not the outside cwd) and return 0.
			cmd := ctx.Bin("plan", "wait", "--at", "feat", shellJobFile, "--timeout", "10s")
			cmd.Dir(outsideDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			return result.AssertSuccess()
		}),

		harness.NewStep("review + finish --prune-worktree deletes the worktree dir and registry entry", func(ctx *harness.Context) error {
			gitRoot := ctx.GetString("git_root")
			worktreePath := ctx.GetString("worktree_path")
			registryPath := ctx.GetString("registry_path")

			// finish requires the plan to be in review status first. The
			// orchestrator runs review/finish from the ecosystem root.
			reviewCmd := ctx.Bin("plan", "review", "feat", "-d", gitRoot)
			reviewCmd.Dir(gitRoot)
			reviewResult := reviewCmd.Run()
			ctx.ShowCommandOutput(reviewCmd.String(), reviewResult.Stdout, reviewResult.Stderr)
			if err := reviewResult.AssertSuccess(); err != nil {
				return fmt.Errorf("plan review feat failed: %w", err)
			}

			finishCmd := ctx.Bin("plan", "finish", "feat",
				"--yes", "--prune-worktree", "--delete-branch", "-d", gitRoot)
			finishCmd.Dir(gitRoot)
			finishResult := finishCmd.Run()
			ctx.ShowCommandOutput(finishCmd.String(), finishResult.Stdout, finishResult.Stderr)
			if err := finishResult.AssertSuccess(); err != nil {
				return fmt.Errorf("plan finish --prune-worktree failed: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("worktree dir pruned", nil, fs.AssertNotExists(worktreePath))
				v.Equal("registry entry deleted", nil, fs.AssertNotExists(registryPath))
			})
		}),
	},
)

// claudeTrustAccepted reports whether the parsed ~/.claude.json marks the given
// path as folder-trusted: projects[path].hasTrustDialogAccepted == true.
func claudeTrustAccepted(root map[string]any, path string) bool {
	projects, ok := root["projects"].(map[string]any)
	if !ok {
		return false
	}
	entry, ok := projects[path].(map[string]any)
	if !ok {
		return false
	}
	return entry["hasTrustDialogAccepted"] == true
}
