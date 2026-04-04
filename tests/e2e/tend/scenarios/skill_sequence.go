package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

var SkillSequenceBriefingScenario = harness.NewScenario(
	"skill-sequence-briefing",
	"Verifies skill_sequence injection into briefing XML, including descriptions, authorization enforcement, and artifact production.",
	[]string{"core", "briefing", "skill-sequence"},
	[]harness.Step{
		harness.NewStep("Setup sandboxed environment with mock skills", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "sequence-project")
			if err != nil {
				return err
			}

			// Create mock skills in the notebook's workspace skills directory
			skillsDir := filepath.Join(notebooksRoot, "workspaces", "sequence-project", "skills")
			mockSkills := map[string]string{
				"prep":  "Mise en place and ingredient prep",
				"sear":  "Execute high-heat searing",
				"plate": "Final presentation and plating",
			}

			for name, desc := range mockSkills {
				skillPath := filepath.Join(skillsDir, name, "SKILL.md")
				content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\nSkill body for %s.", name, desc, name)
				if err := fs.WriteString(skillPath, content); err != nil {
					return err
				}
			}

			// Authorize the skills in grove.toml
			groveToml := filepath.Join(projectDir, "grove.toml")
			tomlContent := "[skills]\nuse = [\"prep\", \"sear\", \"plate\", \"prep-artifact\", \"sear-artifact\"]\n"
			if err := fs.WriteString(groveToml, tomlContent); err != nil {
				return err
			}

			// Create mock LLM response
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			return fs.WriteString(responseFile, "This is a mock response.")
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "grove"},
		),

		// Case 1: Oneshot job with skill_sequence
		harness.NewStep("Create plan and oneshot job with skill_sequence", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			notebooksRoot := ctx.GetString("notebooks_root")

			// Init plan
			ctx.Bin("plan", "init", "seq-plan").Dir(projectDir).Run().AssertSuccess()
			planPath := filepath.Join(notebooksRoot, "workspaces", "sequence-project", "plans", "seq-plan")
			ctx.Set("plan_path", planPath)

			// Write a job file directly with skill_sequence in frontmatter
			jobContent := "---\nid: full-sequence\ntitle: test-full-sequence\ntype: oneshot\nstatus: pending\nskill_sequence:\n  - prep\n  - sear\n  - plate\n---\nCook the meal."
			return fs.WriteString(filepath.Join(planPath, "01-full-sequence.md"), jobContent)
		}),
		harness.NewStep("Run full sequence job and verify briefing", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")

			runCmd := ctx.Bin("plan", "run", filepath.Join(planPath, "01-full-sequence.md"), "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			if err := runCmd.Run().AssertSuccess(); err != nil {
				return err
			}

			// Verify briefing file
			jobArtifactDir := filepath.Join(planPath, ".artifacts", "full-sequence")
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found in %s", jobArtifactDir)
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}

			if !strings.Contains(content, "<skill_sequence>") {
				return fmt.Errorf("missing <skill_sequence> block in briefing")
			}
			if !strings.Contains(content, "Invoke Skill(prep)") {
				return fmt.Errorf("missing prep skill in sequence")
			}
			if !strings.Contains(content, "Invoke Skill(sear)") {
				return fmt.Errorf("missing sear skill in sequence")
			}
			if !strings.Contains(content, "Invoke Skill(plate)") {
				return fmt.Errorf("missing plate skill in sequence")
			}
			if !strings.Contains(content, "Execute prep") {
				return fmt.Errorf("missing execute line for prep skill")
			}
			if !strings.Contains(content, "Mise en place and ingredient prep") {
				return fmt.Errorf("missing prep skill description")
			}
			if !strings.Contains(content, "Start by invoking Skill(\"prep\") now.") {
				return fmt.Errorf("missing sequence start instruction")
			}
			return nil
		}),

		// Case 2: Job with both skill and skill_sequence
		harness.NewStep("Create job with both skill and skill_sequence", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			jobContent := "---\nid: mixed-skills\ntitle: test-mixed-skills\ntype: oneshot\nstatus: pending\nskill: prep\nskill_sequence:\n  - sear\n  - plate\n---\nMixed skills job."
			return fs.WriteString(filepath.Join(planPath, "02-mixed-skills.md"), jobContent)
		}),
		harness.NewStep("Run mixed job and verify both are injected", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")

			runCmd := ctx.Bin("plan", "run", filepath.Join(planPath, "02-mixed-skills.md"), "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			if err := runCmd.Run().AssertSuccess(); err != nil {
				return err
			}

			jobArtifactDir := filepath.Join(planPath, ".artifacts", "mixed-skills")
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found")
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}

			// Verify BOTH the inlined skill and the sequence exist
			if !strings.Contains(content, `<system_instructions skill="prep">`) {
				return fmt.Errorf("missing inlined prep skill in system_instructions")
			}
			if !strings.Contains(content, "<skill_sequence>") {
				return fmt.Errorf("missing <skill_sequence> block")
			}
			if !strings.Contains(content, "Invoke Skill(sear)") {
				return fmt.Errorf("sear should be in the sequence block")
			}
			if !strings.Contains(content, "Invoke Skill(plate)") {
				return fmt.Errorf("plate should be in the sequence block")
			}
			return nil
		}),

		// Case 3: Unauthorized skill in sequence
		harness.NewStep("Create job with unauthorized skill in sequence", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			jobContent := "---\nid: unauthorized-seq\ntitle: test-unauthorized\ntype: oneshot\nstatus: pending\nskill_sequence:\n  - prep\n  - unauthorized-skill-xyz\n---\nShould fail."
			return fs.WriteString(filepath.Join(planPath, "03-unauthorized.md"), jobContent)
		}),
		harness.NewStep("Run unauthorized job and verify failure", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")

			runCmd := ctx.Bin("plan", "run", filepath.Join(planPath, "03-unauthorized.md"), "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			result := runCmd.Run()

			if result.ExitCode == 0 {
				return fmt.Errorf("expected job to fail due to unauthorized skill, but it succeeded")
			}
			return nil
		}),

		// Case 4: Empty skill_sequence (no skill_sequence field)
		harness.NewStep("Create job with no skill_sequence", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			jobContent := "---\nid: empty-sequence\ntitle: test-empty-sequence\ntype: oneshot\nstatus: pending\n---\nNo sequence here."
			return fs.WriteString(filepath.Join(planPath, "04-empty-sequence.md"), jobContent)
		}),
		harness.NewStep("Run empty sequence job and verify no block", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")

			runCmd := ctx.Bin("plan", "run", filepath.Join(planPath, "04-empty-sequence.md"), "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			if err := runCmd.Run().AssertSuccess(); err != nil {
				return err
			}

			jobArtifactDir := filepath.Join(planPath, ".artifacts", "empty-sequence")
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found")
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}

			if strings.Contains(content, "<skill_sequence>") {
				return fmt.Errorf("briefing should not contain <skill_sequence> block when no sequence is defined")
			}
			return nil
		}),

		// Case 5: Skill Sequence with Artifacts
		harness.NewStep("Create skills with produces field", func(ctx *harness.Context) error {
			notebooksRoot := ctx.GetString("notebooks_root")
			skillsDir := filepath.Join(notebooksRoot, "workspaces", "sequence-project", "skills")

			prepDir := filepath.Join(skillsDir, "prep-artifact")
			if err := fs.CreateDir(prepDir); err != nil {
				return err
			}
			if err := fs.WriteString(filepath.Join(prepDir, "SKILL.md"), "---\nname: prep-artifact\ndescription: Prep ingredients\nproduces:\n  - prep-log.md\n---\n# Prep"); err != nil {
				return err
			}

			searDir := filepath.Join(skillsDir, "sear-artifact")
			if err := fs.CreateDir(searDir); err != nil {
				return err
			}
			return fs.WriteString(filepath.Join(searDir, "SKILL.md"), "---\nname: sear-artifact\ndescription: Sear ingredients\nproduces:\n  - sear-log.md\n---\n# Sear")
		}),
		harness.NewStep("Create and run artifacts job", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			projectDir := ctx.GetString("project_dir")

			jobContent := "---\nid: job-artifacts\ntitle: Cook with Artifacts\ntype: oneshot\nstatus: pending\nskill_sequence:\n  - prep-artifact\n  - sear-artifact\n---\nExecute sequence."
			jobFile := filepath.Join(planPath, "05-job-artifacts.md")
			if err := fs.WriteString(jobFile, jobContent); err != nil {
				return err
			}

			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			runCmd := ctx.Bin("plan", "run", jobFile, "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			return runCmd.Run().AssertSuccess()
		}),
		harness.NewStep("Verify artifact paths in briefing", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			jobArtifactDir := filepath.Join(planPath, ".artifacts", "job-artifacts")
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found for job-artifacts")
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}

			// Compute exactly what the XML briefing paths should look like
			prepArtifactPath := filepath.Join(planPath, ".artifacts", "job-artifacts", "prep-log.md")
			searArtifactPath := filepath.Join(planPath, ".artifacts", "job-artifacts", "sear-log.md")

			expectedPrep := fmt.Sprintf("Execute prep-artifact — write output to %s, verify it exists", prepArtifactPath)
			expectedSear := fmt.Sprintf("Execute sear-artifact — read %s, write %s, verify it exists", prepArtifactPath, searArtifactPath)

			if !strings.Contains(content, expectedPrep) {
				return fmt.Errorf("briefing missing expected prep instruction.\nExpected: %s\nGot:\n%s", expectedPrep, content)
			}
			if !strings.Contains(content, expectedSear) {
				return fmt.Errorf("briefing missing expected sear instruction.\nExpected: %s\nGot:\n%s", expectedSear, content)
			}

			return nil
		}),
	},
)
