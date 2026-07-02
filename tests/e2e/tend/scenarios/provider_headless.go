package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/grovetools/agentlogs/pkg/transcript"
	"github.com/grovetools/core/config"
	"github.com/grovetools/tend/pkg/assert"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// setupHeadlessProviderEnv prepares a project with a plan and a directly
// written headless_agent job pinned to the given provider (per-job
// `provider:` frontmatter, P1). Returns nothing; paths land in ctx.
func setupHeadlessProviderEnv(providerName string, providerArgs []string) harness.Step {
	return harness.NewStep(fmt.Sprintf("Setup environment + headless job for %s", providerName), func(ctx *harness.Context) error {
		projectName := fmt.Sprintf("%s-headless-project", providerName)
		projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, projectName)
		if err != nil {
			return err
		}

		repo, err := git.SetupTestRepo(projectDir)
		if err != nil {
			return err
		}
		if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Headless provider test\n"); err != nil {
			return err
		}
		if err := repo.AddCommit("Initial commit"); err != nil {
			return err
		}

		flowExt := map[string]interface{}{
			"interactive_provider": "claude", // global stays claude; the JOB pins the provider
		}
		if len(providerArgs) > 0 {
			flowExt["providers"] = map[string]interface{}{
				providerName: map[string]interface{}{"args": providerArgs},
			}
		}
		groveConfig := &config.Config{
			Name:    projectName,
			Version: "1.0",
			Extensions: map[string]interface{}{
				"flow": flowExt,
			},
		}
		if err := fs.WriteGroveConfig(projectDir, groveConfig); err != nil {
			return err
		}

		ctx.Set("notebooks_root", notebooksRoot)
		ctx.Set("project_name", projectName)
		return nil
	})
}

// addHeadlessJob initializes a plan and writes the headless job file directly
// (fixed id so env assertions are deterministic), mirroring the
// playbook-env-injection scenario's direct job authoring.
func addHeadlessJob(providerName string) harness.Step {
	return harness.NewStep("Initialize plan and write headless_agent job", func(ctx *harness.Context) error {
		projectDir := ctx.GetString("project_dir")
		notebooksRoot := ctx.GetString("notebooks_root")
		projectName := ctx.GetString("project_name")

		planName := fmt.Sprintf("%s-headless-plan", providerName)
		cmd := ctx.Bin("plan", "init", planName)
		cmd.Dir(projectDir)
		if err := cmd.Run().AssertSuccess(); err != nil {
			return err
		}

		planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", planName)
		jobID := fmt.Sprintf("%s-headless-e2e", providerName)
		jobPath := filepath.Join(planPath, fmt.Sprintf("01-%s-headless.md", providerName))
		jobContent := fmt.Sprintf(`---
id: %s
title: %s Headless E2E
type: headless_agent
status: pending
provider: %s
---
Exercise the %s headless launch path.
`, jobID, providerName, providerName, providerName)
		if err := fs.WriteString(jobPath, jobContent); err != nil {
			return err
		}

		ctx.Set("plan_path", planPath)
		ctx.Set("job_path", jobPath)
		ctx.Set("job_id", jobID)
		return nil
	})
}

// PiHeadlessLaunchScenario asserts the pi headless execution mode end to end
// with the real (mock) binary actually executing:
//
//   - flow builds `pi <provider-args> -p` (the -p appended AFTER provider
//     args, P3) and pipes the briefing instruction via stdin;
//   - the process env carries GROVE_AGENT_PROVIDER=pi + GROVE_FLOW_JOB_ID;
//   - the agent writes its v3 session file into the per-cwd
//     ~/.pi/agent/sessions/--<munged-cwd>--/ layout, discoverable via the
//     shared PiSessionsGlob;
//   - the detached job stays `running` (the mock outlives the CLI) until an
//     explicit `flow plan complete` finishes it.
var PiHeadlessLaunchScenario = harness.NewScenario(
	"pi-headless-launch",
	"Tests the pi headless path: `pi <args> -p` with the briefing prompt on stdin, GROVE_AGENT_PROVIDER=pi env, v3 session file in the per-cwd pi layout, and explicit completion of the detached job",
	[]string{"agent", "provider", "pi", "headless"},
	[]harness.Step{
		setupHeadlessProviderEnv("pi", []string{"--pi-test-arg", "--verbose"}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "pi"},
			harness.Mock{CommandName: "tmux"},
		),

		addHeadlessJob("pi"),

		harness.NewStep("Run headless job with env/args dump hooks", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobPath := ctx.GetString("job_path")

			envDump := filepath.Join(ctx.RootDir, "pi-env-dump.txt")
			argsDump := filepath.Join(ctx.RootDir, "pi-args-dump.txt")
			ctx.Set("env_dump", envDump)
			ctx.Set("args_dump", argsDump)

			// --local keeps execution in this process so the dump-hook env
			// vars reach the mock pi (daemon-routed runs would lose them).
			cmd := ctx.Bin("plan", "run", "--local", jobPath, "--yes")
			cmd.Dir(projectDir).
				Env("GROVE_MOCK_PI_DUMP_ENV=" + envDump).
				Env("GROVE_MOCK_PI_DUMP_ARGS=" + argsDump).
				// Keep the mock alive well past the CLI so the detached-job
				// lifecycle is deterministic: the job must still be running
				// when we assert below.
				Env("GROVE_MOCK_PI_HEADLESS_SLEEP_MS=10000")
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			// Like the playbook env-injection headless scenario, we do not
			// strictly assert the CLI exit: the launch detaches and the
			// session-file / args / env / job-status assertions below are the
			// real contract.
			return nil
		}),

		harness.NewStep("Wait for the pi session file via the shared discovery glob", func(ctx *harness.Context) error {
			home := ctx.HomeDir()
			pattern := transcript.PiSessionsGlob(home, "")

			deadline := time.Now().Add(15 * time.Second)
			for {
				matches, err := filepath.Glob(pattern)
				if err != nil {
					return err
				}
				if len(matches) > 0 {
					ctx.Set("pi_session_file", matches[0])
					return nil
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("no pi session file appeared for glob %s", pattern)
				}
				time.Sleep(200 * time.Millisecond)
			}
		}),

		harness.NewStep("Verify session layout, stdin prompt, args, and env", func(ctx *harness.Context) error {
			home := ctx.HomeDir()
			projectDir := ctx.GetString("project_dir")
			jobID := ctx.GetString("job_id")
			sessionFile := ctx.GetString("pi_session_file")

			sessionContent, err := fs.ReadString(sessionFile)
			if err != nil {
				return err
			}

			// The per-cwd dir must be the munged (resolved) workdir per the
			// shared PiSessionsDir helper. Headless jobs without a worktree
			// run in the project git root.
			resolvedDir := projectDir
			if r, rerr := filepath.EvalSymlinks(projectDir); rerr == nil {
				resolvedDir = r
			}
			wantDir := transcript.PiSessionsDir(home, resolvedDir)

			argsContent, err := fs.ReadString(ctx.GetString("args_dump"))
			if err != nil {
				return fmt.Errorf("args dump not written by mock pi: %w", err)
			}
			argv := strings.Split(strings.TrimSpace(argsContent), "\n")

			envContent, err := fs.ReadString(ctx.GetString("env_dump"))
			if err != nil {
				return fmt.Errorf("env dump not written by mock pi: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("session file lives in the per-cwd pi layout", wantDir, filepath.Dir(sessionFile))
				v.Contains("v3 session header", sessionContent, `"version":3`)
				v.Contains("stdin briefing prompt captured as the user message", sessionContent, "Read the briefing file at")
				v.Contains("assistant usage/cost recorded", sessionContent, `"cost"`)

				v.True("argv contains provider args from grove.toml",
					containsArg(argv, "--pi-test-arg") && containsArg(argv, "--verbose"))
				v.Equal("-p is the LAST arg (appended after provider args)", "-p", argv[len(argv)-1])
				v.True("briefing instruction is NOT passed as argv (stdin only)",
					!strings.Contains(argsContent, "Read the briefing file"))

				v.Contains("agent env identifies the provider", envContent, "GROVE_AGENT_PROVIDER=pi")
				v.Contains("agent env carries the job id", envContent, "GROVE_FLOW_JOB_ID="+jobID)
			})
		}),

		harness.NewStep("Detached job stays running until explicit completion", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobPath := ctx.GetString("job_path")

			if err := assert.YAMLField(jobPath, "status", "running", "detached headless job should still be running"); err != nil {
				return err
			}

			_ = fs.RemoveIfExists(jobPath + ".lock")
			cmd := ctx.Bin("plan", "complete", jobPath)
			cmd.Dir(projectDir)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return err
			}
			return assert.YAMLField(jobPath, "status", "completed", "job should be completed")
		}),
	},
)

// CodexHeadlessUnsupportedScenario asserts flow rejects a headless_agent job
// pinned to codex with an actionable error (codex has no headless mode in the
// provider registry; SupportsHeadless=false), instead of launching anything.
var CodexHeadlessUnsupportedScenario = harness.NewScenario(
	"codex-headless-unsupported",
	"Tests that a headless_agent job with provider: codex fails fast with an actionable 'does not support headless execution' error naming the supported providers",
	[]string{"agent", "provider", "codex", "headless", "validation"},
	[]harness.Step{
		setupHeadlessProviderEnv("codex", nil),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "codex"},
			harness.Mock{CommandName: "tmux"},
		),

		addHeadlessJob("codex"),

		harness.NewStep("Run headless codex job and expect an actionable failure", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobPath := ctx.GetString("job_path")

			cmd := ctx.Bin("plan", "run", "--local", jobPath, "--yes")
			cmd.Dir(projectDir)
			cmd.Timeout(60 * time.Second)
			result := cmd.Run()
			ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)

			combined := result.Stdout + result.Stderr
			if err := ctx.Verify(func(v *verify.Collector) {
				v.Contains("error explains codex cannot run headless", combined, "does not support headless execution")
				v.Contains("error lists the supported headless providers", combined, "supported headless providers")
			}); err != nil {
				return err
			}

			// No codex session may have been written: nothing launched.
			matches, err := filepath.Glob(filepath.Join(ctx.HomeDir(), ".codex", "sessions", "*", "*", "*", "*.jsonl"))
			if err != nil {
				return err
			}
			if len(matches) != 0 {
				return fmt.Errorf("codex mock ran despite headless being unsupported: %v", matches)
			}

			return assert.YAMLField(jobPath, "status", "failed", "job should be failed")
		}),
	},
)

// containsArg reports whether argv contains an exact argument.
func containsArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}
