package scenarios

import (
	"fmt"
	"path/filepath"

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

			// Authorize skills in grove.toml
			groveToml := filepath.Join(projectDir, "grove.toml")
			tomlContent := "[skills]\nuse = [\"parent-skill\", \"step-a\", \"step-b\", \"simple-skill\"]\n"
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
	},
)
