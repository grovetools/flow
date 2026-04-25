package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/flow/pkg/orchestration"
	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
	"github.com/grovetools/tend/pkg/verify"
)

var SkillInheritFlagScenario = harness.NewScenario(
	"skill-inherit",
	"Verifies --skill flag sets skill field and skill_sequence is auto-inherited from SKILL.md frontmatter, with explicit override support.",
	[]string{"core", "skill-sequence", "plan-add"},
	[]harness.Step{
		harness.NewStep("Setup environment with parent and child skills", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "skill-inherit-project")
			if err != nil {
				return err
			}

			skillsDir := filepath.Join(notebooksRoot, "workspaces", "skill-inherit-project", "skills")

			// Create parent skill with skill_sequence in frontmatter
			parentContent := "---\nname: parent-skill\ndescription: A parent skill with sequence\nskill_sequence:\n  - step-a\n  - step-b\n---\n# Parent Skill\nThis skill orchestrates step-a and step-b."
			if err := fs.WriteString(filepath.Join(skillsDir, "parent-skill", "SKILL.md"), parentContent); err != nil {
				return err
			}

			// Create step-a skill
			stepAContent := "---\nname: step-a\ndescription: First step\n---\n# Step A"
			if err := fs.WriteString(filepath.Join(skillsDir, "step-a", "SKILL.md"), stepAContent); err != nil {
				return err
			}

			// Create step-b skill
			stepBContent := "---\nname: step-b\ndescription: Second step\n---\n# Step B"
			if err := fs.WriteString(filepath.Join(skillsDir, "step-b", "SKILL.md"), stepBContent); err != nil {
				return err
			}

			// Create a simple skill with no sequence
			simpleContent := "---\nname: simple-skill\ndescription: A simple skill without sequence\n---\n# Simple Skill"
			if err := fs.WriteString(filepath.Join(skillsDir, "simple-skill", "SKILL.md"), simpleContent); err != nil {
				return err
			}

			// Authorize skills in grove.toml — note: step-a and step-b are NOT listed,
			// they should be implicitly authorized via parent-skill's skill_sequence.
			groveToml := filepath.Join(projectDir, "grove.toml")
			tomlContent := "[skills]\nuse = [\"parent-skill\", \"simple-skill\"]\n"
			if err := fs.WriteString(groveToml, tomlContent); err != nil {
				return err
			}

			// Init a plan
			result := ctx.Bin("plan", "init", "inherit-plan").Dir(projectDir).Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan init failed: %s", result.Stderr)
			}

			planPath := filepath.Join(notebooksRoot, "workspaces", "skill-inherit-project", "plans", "inherit-plan")
			ctx.Set("plan_path", planPath)

			return nil
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "grove"},
		),

		// Case 1: --skill flag sets the skill field on the job
		harness.NewStep("Skill flag sets skill field", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			result := ctx.Bin("plan", "add", "inherit-plan",
				"--skill", "simple-skill",
				"--type", "oneshot",
				"--title", "test-skill-field",
				"-p", "test prompt",
			).Dir(projectDir).Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add failed: %s\nstdout: %s", result.Stderr, result.Stdout)
			}

			// Find and load the created job file
			jobPath, err := findJobByPrefix(planPath, "01-")
			if err != nil {
				return fmt.Errorf("could not find job file: %w", err)
			}

			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("failed to load job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("skill field is set", "simple-skill", job.Skill)
				v.Equal("skill_sequence is empty for simple skill", 0, len(job.SkillSequence))
			})
		}),

		// Case 2: Auto-inherit skill_sequence from parent skill
		harness.NewStep("Auto-inherit skill_sequence from parent skill", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			result := ctx.Bin("plan", "add", "inherit-plan",
				"--skill", "parent-skill",
				"--type", "oneshot",
				"--title", "test-inherit-sequence",
				"-p", "test prompt",
			).Dir(projectDir).Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add failed: %s\nstdout: %s", result.Stderr, result.Stdout)
			}

			// Find the second job file
			jobPath, err := findJobByPrefix(planPath, "02-")
			if err != nil {
				return fmt.Errorf("could not find job file: %w", err)
			}

			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("failed to load job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("skill field is set", "parent-skill", job.Skill)
				v.Equal("skill_sequence has 2 entries", 2, len(job.SkillSequence))
				if len(job.SkillSequence) >= 2 {
					v.Equal("first sequence entry is step-a", "step-a", job.SkillSequence[0])
					v.Equal("second sequence entry is step-b", "step-b", job.SkillSequence[1])
				}
			})
		}),

		// Case 3: Explicit --skill-sequence overrides inherited sequence
		harness.NewStep("Explicit skill-sequence overrides inherited", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")

			result := ctx.Bin("plan", "add", "inherit-plan",
				"--skill", "parent-skill",
				"--skill-sequence", "other-a,other-b",
				"--type", "oneshot",
				"--title", "test-explicit-override",
				"-p", "test prompt",
			).Dir(projectDir).Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("plan add failed: %s\nstdout: %s", result.Stderr, result.Stdout)
			}

			// Find the third job file
			jobPath, err := findJobByPrefix(planPath, "03-")
			if err != nil {
				return fmt.Errorf("could not find job file: %w", err)
			}

			job, err := orchestration.LoadJob(jobPath)
			if err != nil {
				return fmt.Errorf("failed to load job: %w", err)
			}

			return ctx.Verify(func(v *verify.Collector) {
				v.Equal("skill field is set", "parent-skill", job.Skill)
				v.Equal("skill_sequence has 2 entries", 2, len(job.SkillSequence))
				if len(job.SkillSequence) >= 2 {
					v.Equal("first sequence entry is other-a (explicit)", "other-a", job.SkillSequence[0])
					v.Equal("second sequence entry is other-b (explicit)", "other-b", job.SkillSequence[1])
				}
			})
		}),
		// Case 4: Implicit authorization — parent skill's sub-skills don't need explicit auth
		harness.NewStep("Implicit auth: inherited sequence resolves without explicit auth", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			if err := fs.WriteString(responseFile, "Mock response."); err != nil {
				return err
			}

			// The parent-skill has skill_sequence: [step-a, step-b].
			// Only parent-skill is in grove.toml use — step-a, step-b are implicitly authorized.
			// Create a job with --skill parent-skill (which inherits the sequence).
			jobContent := "---\nid: implicit-auth\ntitle: test-implicit-auth\ntype: oneshot\nstatus: pending\nskill: parent-skill\nskill_sequence:\n  - step-a\n  - step-b\n---\nTest implicit auth."
			if err := fs.WriteString(filepath.Join(planPath, "04-implicit-auth.md"), jobContent); err != nil {
				return err
			}

			runCmd := ctx.Bin("plan", "run", "--local", filepath.Join(planPath, "04-implicit-auth.md"), "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			result := runCmd.Run()

			if result.ExitCode != 0 {
				return fmt.Errorf("expected implicit auth to succeed, but got exit code %d: %s", result.ExitCode, result.Stderr)
			}

			// Verify the briefing contains both sub-skills
			jobArtifactDir := filepath.Join(planPath, ".artifacts", "implicit-auth")
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found in %s", jobArtifactDir)
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}

			if !strings.Contains(content, "Invoke Skill(step-a)") {
				return fmt.Errorf("missing step-a in briefing sequence")
			}
			if !strings.Contains(content, "Invoke Skill(step-b)") {
				return fmt.Errorf("missing step-b in briefing sequence")
			}
			return nil
		}),

		// Case 5: Transitive auth at registry level — standalone sequence with
		// transitively authorized skills (via parent-skill's skill_sequence) succeeds
		harness.NewStep("Transitive registry auth: standalone sequence with parent's sub-skills succeeds", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			planPath := ctx.GetString("plan_path")
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")

			// step-a/step-b are transitively authorized via parent-skill's skill_sequence,
			// even without the parent being set on this job.
			jobContent := "---\nid: transitive-seq\ntitle: test-transitive-seq\ntype: oneshot\nstatus: pending\nskill_sequence:\n  - step-a\n  - step-b\n---\nTransitive auth test."
			if err := fs.WriteString(filepath.Join(planPath, "05-transitive-seq.md"), jobContent); err != nil {
				return err
			}

			runCmd := ctx.Bin("plan", "run", "--local", filepath.Join(planPath, "05-transitive-seq.md"), "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			result := runCmd.Run()

			if result.ExitCode != 0 {
				return fmt.Errorf("expected transitive auth to succeed for standalone sequence, but got exit code %d: %s", result.ExitCode, result.Stderr)
			}

			// Verify the briefing was generated with both sub-skills
			jobArtifactDir := filepath.Join(planPath, ".artifacts", "transitive-seq")
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found in %s", jobArtifactDir)
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}

			if !strings.Contains(content, "Invoke Skill(step-a)") {
				return fmt.Errorf("missing step-a in briefing sequence")
			}
			if !strings.Contains(content, "Invoke Skill(step-b)") {
				return fmt.Errorf("missing step-b in briefing sequence")
			}
			return nil
		}),
	},
)
