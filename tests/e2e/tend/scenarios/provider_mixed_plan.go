package scenarios

import (
	"fmt"
	"path/filepath"

	"github.com/grovetools/core/config"
	"github.com/grovetools/tend/pkg/assert"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/git"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

// createMixedProviderPlanScenario tests P1's per-job `provider:` frontmatter
// across ONE plan holding a job for every supported provider: the global
// flow.interactive_provider stays claude, each job overrides (or omits) the
// provider, and every launch must resolve the job's own provider — asserted
// through the provider name flow stamps into the session-intent registry
// record at launch (the same resolved spec supplies the launch binary).
// Jobs are run and completed one at a time so each launch is isolated.
func createMixedProviderPlanScenario() *harness.Scenario {
	type mixedJob struct {
		providerFlag string // --provider value; "" = inherit the claude global
		wantProvider string // provider expected in frontmatter + session intent
		title        string
		jobFile      string
	}
	jobs := []mixedJob{
		{providerFlag: "", wantProvider: "claude", title: "Mixed Claude Job", jobFile: "01-mixed-claude-job.md"},
		{providerFlag: "codex", wantProvider: "codex", title: "Mixed Codex Job", jobFile: "02-mixed-codex-job.md"},
		{providerFlag: "opencode", wantProvider: "opencode", title: "Mixed Opencode Job", jobFile: "03-mixed-opencode-job.md"},
		{providerFlag: "pi", wantProvider: "pi", title: "Mixed Pi Job", jobFile: "04-mixed-pi-job.md"},
	}

	steps := []harness.Step{
		harness.NewStep("Setup environment with claude as the global provider", func(ctx *harness.Context) error {
			projectName := "mixed-provider-project"
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, projectName)
			if err != nil {
				return err
			}

			repo, err := git.SetupTestRepo(projectDir)
			if err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(projectDir, "README.md"), "# Mixed provider plan test\n"); err != nil {
				return err
			}
			if err := repo.AddCommit("Initial commit"); err != nil {
				return err
			}

			groveConfig := &config.Config{
				Name:    projectName,
				Version: "1.0",
				Extensions: map[string]interface{}{
					"flow": map[string]interface{}{
						"interactive_provider": "claude",
					},
				},
			}
			if err := fs.WriteGroveConfig(projectDir, groveConfig); err != nil {
				return err
			}

			ctx.Set("notebooks_root", notebooksRoot)
			ctx.Set("project_name", projectName)
			return nil
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
			harness.Mock{CommandName: "claude"},
			harness.Mock{CommandName: "codex"},
			harness.Mock{CommandName: "opencode"},
			harness.Mock{CommandName: "pi"},
			harness.Mock{CommandName: "tmux"},
		),

		harness.NewStep("Initialize plan and add one job per provider", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")
			projectName := ctx.GetString("project_name")

			planName := "mixed-provider-plan"
			cmd := ctx.Bin("plan", "init", planName, "--worktree")
			cmd.Dir(projectDir)
			if err := cmd.Run().AssertSuccess(); err != nil {
				return err
			}
			planPath := filepath.Join(notebooksRoot, "workspaces", projectName, "plans", planName)
			ctx.Set("plan_path", planPath)

			for _, j := range jobs {
				args := []string{
					"plan", "add", planName,
					"--type", "interactive_agent",
					"--title", j.title,
					"-p", fmt.Sprintf("Run under the %s provider", j.wantProvider),
				}
				if j.providerFlag != "" {
					args = append(args, "--provider", j.providerFlag)
				}
				cmd := ctx.Bin(args...)
				cmd.Dir(projectDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if err := result.AssertSuccess(); err != nil {
					return err
				}
			}
			return nil
		}),
	}

	for _, j := range jobs {
		j := j // capture
		steps = append(steps, harness.NewStep(
			fmt.Sprintf("Run + verify + complete the %s job", j.wantProvider),
			func(ctx *harness.Context) error {
				projectDir := ctx.GetString("project_dir")
				planPath := ctx.GetString("plan_path")
				jobPath := filepath.Join(planPath, j.jobFile)

				// Frontmatter: an explicit --provider must be persisted;
				// inheriting jobs record none.
				if j.providerFlag != "" {
					if err := assert.YAMLField(jobPath, "provider", j.providerFlag, "job frontmatter records the per-job provider"); err != nil {
						return err
					}
				}

				jobID, err := jobIDFromFile(jobPath)
				if err != nil {
					return err
				}

				cmd := ctx.Bin("plan", "run", jobPath)
				cmd.Dir(projectDir)
				result := cmd.Run()
				ctx.ShowCommandOutput(cmd.String(), result.Stdout, result.Stderr)
				if err := result.AssertSuccess(); err != nil {
					return err
				}
				if err := assert.YAMLField(jobPath, "status", "running", "job should be running"); err != nil {
					return err
				}

				// The launch registers a session intent stamped with the
				// RESOLVED provider — the same registry spec whose Binary the
				// launch command was built from. A wrong resolution here means
				// the wrong CLI would have been launched.
				meta, err := readSessionMetadata(filepath.Join(hooksSessionsDir(ctx), jobID))
				if err != nil {
					return fmt.Errorf("session intent for %s job: %w", j.wantProvider, err)
				}
				if err := ctx.Verify(func(v *verify.Collector) {
					v.Equal(fmt.Sprintf("%s job launched with provider %s", j.title, j.wantProvider),
						j.wantProvider, meta["provider"])
				}); err != nil {
					return err
				}

				// Complete before the next job so launches stay isolated.
				_ = fs.RemoveIfExists(jobPath + ".lock")
				cmd = ctx.Bin("plan", "complete", jobPath)
				cmd.Dir(projectDir)
				if err := cmd.Run().AssertSuccess(); err != nil {
					return err
				}
				return assert.YAMLField(jobPath, "status", "completed", "job should be completed")
			}))
	}

	return harness.NewScenario(
		"mixed-provider-plan",
		"Tests a single plan with per-job provider: frontmatter across all supported providers (claude default, codex, opencode, pi): each job's launch resolves and registers its own provider",
		[]string{"agent", "provider", "per-job", "mixed", "config"},
		steps,
	)
}

// MixedProviderPlanScenario is the exported mixed-provider plan scenario.
var MixedProviderPlanScenario = createMixedProviderPlanScenario()
