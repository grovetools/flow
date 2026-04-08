package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

// PlaybookOverviewXMLScenario verifies that a job running in a
// playbook-scoped plan receives a <playbook_overview> block in its
// briefing XML, and that a non-playbook plan does not.
var PlaybookOverviewXMLScenario = harness.NewScenario(
	"playbook-overview-xml",
	"Verify <playbook_overview> block is rendered into the briefing for playbook-scoped jobs",
	[]string{"playbook", "briefing", "xml"},
	[]harness.Step{
		harness.NewStep("Setup playbook environment", func(ctx *harness.Context) error {
			_, _, _, err := setupPlaybookEnvironment(ctx, "overview-project", "test-pb")
			return err
		}),

		harness.SetupMocks(
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "grove"},
		),

		harness.NewStep("Init playbook-scoped plan and add oneshot job", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			if err := ctx.Bin("plan", "init", "overview-plan", "--playbook", "test-pb").Dir(projectDir).Run().AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %w", err)
			}
			planPath := filepath.Join(notebooksRoot, "workspaces", "overview-project", "plans", "overview-plan")
			ctx.Set("plan_path", planPath)

			jobContent := "---\nid: overview-job\ntitle: Overview Job\ntype: oneshot\nstatus: pending\n---\nInspect me."
			return fs.WriteString(filepath.Join(planPath, "01-overview-job.md"), jobContent)
		}),

		harness.NewStep("Run playbook-scoped oneshot and inspect briefing", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			if err := fs.WriteString(responseFile, "mock"); err != nil {
				return err
			}

			jobPath := filepath.Join(planPath, "01-overview-job.md")
			runCmd := ctx.Bin("plan", "run", jobPath, "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			if err := runCmd.Run().AssertSuccess(); err != nil {
				return fmt.Errorf("plan run failed: %w", err)
			}

			artifactDir := filepath.Join(planPath, ".artifacts", "overview-job")
			briefings, _ := filepath.Glob(filepath.Join(artifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found in %s", artifactDir)
			}
			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}
			expectations := []string{
				`<playbook_overview name="test-pb" version="1.0.0"`,
				`<skills>`,
				`<skill name="pb-hello"`,
				`<skill name="pb-goodbye"`,
				`<prompts>`,
				`<prompt file="greet.md"`,
				`<recipes>`,
				`<recipe file="test-recipe.md"`,
				`<references_note>`,
			}
			for _, want := range expectations {
				if !strings.Contains(content, want) {
					return fmt.Errorf("briefing missing %q in:\n%s", want, content)
				}
			}
			return nil
		}),

		harness.NewStep("Negative: non-playbook plan has no playbook_overview block", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			if err := ctx.Bin("plan", "init", "nopb-plan").Dir(projectDir).Run().AssertSuccess(); err != nil {
				return err
			}
			planPath := filepath.Join(notebooksRoot, "workspaces", "overview-project", "plans", "nopb-plan")

			jobContent := "---\nid: nopb-job\ntitle: No Playbook Job\ntype: oneshot\nstatus: pending\n---\nInspect me."
			if err := fs.WriteString(filepath.Join(planPath, "01-nopb-job.md"), jobContent); err != nil {
				return err
			}

			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			jobPath := filepath.Join(planPath, "01-nopb-job.md")
			runCmd := ctx.Bin("plan", "run", jobPath, "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			if err := runCmd.Run().AssertSuccess(); err != nil {
				return err
			}

			artifactDir := filepath.Join(planPath, ".artifacts", "nopb-job")
			briefings, _ := filepath.Glob(filepath.Join(artifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found in %s", artifactDir)
			}
			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}
			if strings.Contains(content, "<playbook_overview") {
				return fmt.Errorf("non-playbook plan should not include <playbook_overview>, got:\n%s", content)
			}
			return nil
		}),
	},
)
