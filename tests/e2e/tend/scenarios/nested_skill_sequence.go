package scenarios

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

var NestedSkillSequenceScenario = harness.NewScenario(
	"nested-skill-sequence",
	"Verifies nested skill sequence expansion, circular dependency detection, and depth limits.",
	[]string{"core", "briefing", "skill-sequence", "nested"},
	[]harness.Step{
		harness.NewStep("Setup environment with nested skills", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "nested-sequence-project")
			if err != nil {
				return err
			}

			skillsDir := filepath.Join(notebooksRoot, "workspaces", "nested-sequence-project", "skills")

			writeSkill := func(relPath, name, desc string, seq, produces []string) error {
				dir := filepath.Join(skillsDir, relPath)
				if err := fs.CreateDir(dir); err != nil {
					return err
				}
				var seqStr string
				if len(seq) > 0 {
					seqStr = "skill_sequence:\n"
					for _, s := range seq {
						seqStr += fmt.Sprintf("  - %s\n", s)
					}
				}
				var prodStr string
				if len(produces) > 0 {
					prodStr = "produces:\n"
					for _, p := range produces {
						prodStr += fmt.Sprintf("  - %s\n", p)
					}
				}
				content := fmt.Sprintf("---\nname: %s\ndescription: %s\n%s%s---\n# %s\n", name, desc, seqStr, prodStr, name)
				return fs.WriteString(filepath.Join(dir, "SKILL.md"), content)
			}

			// Normal nested skill: parent-skill -> child-a, child-b (children nested under parent)
			_ = writeSkill("parent-skill", "parent-skill", "The Parent", []string{"child-a", "child-b"}, nil)
			_ = writeSkill("parent-skill/child-a", "child-a", "First Child", nil, nil)
			_ = writeSkill("parent-skill/child-b", "child-b", "Second Child", nil, nil)

			// Circular dependency: circular-a -> circular-b -> circular-a
			_ = writeSkill("circular-a", "circular-a", "Circ A", []string{"circular-b"}, nil)
			_ = writeSkill("circular-b", "circular-b", "Circ B", []string{"circular-a"}, nil)

			// Depth limit: depth-1 -> depth-2 -> depth-3 -> depth-4 -> depth-5
			_ = writeSkill("depth-1", "depth-1", "D1", []string{"depth-2"}, nil)
			_ = writeSkill("depth-2", "depth-2", "D2", []string{"depth-3"}, nil)
			_ = writeSkill("depth-3", "depth-3", "D3", []string{"depth-4"}, nil)
			_ = writeSkill("depth-4", "depth-4", "D4", []string{"depth-5"}, nil)
			_ = writeSkill("depth-5", "depth-5", "D5", nil, nil)

			// Authorize only root-level skills in grove.toml
			groveToml := filepath.Join(projectDir, "grove.toml")
			tomlContent := "[skills]\nuse = [\"parent-skill\", \"circular-a\", \"depth-1\"]\n"
			if err := fs.WriteString(groveToml, tomlContent); err != nil {
				return err
			}

			// Create mock LLM response
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			if err := fs.WriteString(responseFile, "Mock response"); err != nil {
				return err
			}

			// Init plan
			if err := ctx.Bin("plan", "init", "nested-plan").Dir(projectDir).Run().AssertSuccess(); err != nil {
				return err
			}
			planPath := filepath.Join(notebooksRoot, "workspaces", "nested-sequence-project", "plans", "nested-plan")
			ctx.Set("plan_path", planPath)

			return nil
		}),
		harness.SetupMocks(harness.Mock{CommandName: "llm"}, harness.Mock{CommandName: "cx"}, harness.Mock{CommandName: "grove"}),

		// Case 1: Successful nested expansion
		harness.NewStep("Run nested expansion and verify briefing", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			projectDir := ctx.GetString("project_dir")

			jobContent := "---\nid: job-nested\ntitle: Test Nested Sequence\ntype: oneshot\nstatus: pending\nskill_sequence:\n  - parent-skill\n---\nRun sequence."
			if err := fs.WriteString(filepath.Join(planPath, "01-nested.md"), jobContent); err != nil {
				return err
			}

			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			runCmd := ctx.Bin("plan", "run", "--local", filepath.Join(planPath, "01-nested.md"), "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			if err := runCmd.Run().AssertSuccess(); err != nil {
				return err
			}

			jobArtifactDir := filepath.Join(planPath, ".artifacts", "job-nested")
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found for job-nested")
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}

			// Validate: parent-skill is rendered, and children are indented below it
			if !strings.Contains(content, "Invoke Skill(parent-skill)") {
				return fmt.Errorf("missing parent-skill invocation. Content:\n%s", content)
			}
			if !strings.Contains(content, "Invoke Skill(child-a)") {
				return fmt.Errorf("missing child-a sub-step. Content:\n%s", content)
			}
			if !strings.Contains(content, "Execute child-a") {
				return fmt.Errorf("missing execute child-a. Content:\n%s", content)
			}
			if !strings.Contains(content, "Invoke Skill(child-b)") {
				return fmt.Errorf("missing child-b sub-step. Content:\n%s", content)
			}
			if !strings.Contains(content, "Execute child-b") {
				return fmt.Errorf("missing execute child-b. Content:\n%s", content)
			}
			return nil
		}),

		// Case 2: Circular dependency detection
		harness.NewStep("Run circular dependency and verify failure", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			projectDir := ctx.GetString("project_dir")

			jobContent := "---\nid: job-circ\ntitle: Test Circular\ntype: oneshot\nstatus: pending\nskill_sequence:\n  - circular-a\n---\nRun circ."
			jobFile := filepath.Join(planPath, "02-circ.md")
			if err := fs.WriteString(jobFile, jobContent); err != nil {
				return err
			}

			runCmd := ctx.Bin("plan", "run", "--local", jobFile, "--yes")
			runCmd.Dir(projectDir)
			res := runCmd.Run()
			if res.ExitCode == 0 {
				return fmt.Errorf("expected failure on circular dependency")
			}
			combined := res.Stdout + res.Stderr
			if !strings.Contains(combined, "circular skill sequence dependency detected") {
				return fmt.Errorf("expected circular dependency error message, got: %s", combined)
			}
			return nil
		}),

		// Case 3: Depth limit enforcement
		harness.NewStep("Run depth limit and verify failure", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			projectDir := ctx.GetString("project_dir")

			jobContent := "---\nid: job-depth\ntitle: Test Depth\ntype: oneshot\nstatus: pending\nskill_sequence:\n  - depth-1\n---\nRun depth."
			jobFile := filepath.Join(planPath, "03-depth.md")
			if err := fs.WriteString(jobFile, jobContent); err != nil {
				return err
			}

			runCmd := ctx.Bin("plan", "run", "--local", jobFile, "--yes")
			runCmd.Dir(projectDir)
			res := runCmd.Run()
			if res.ExitCode == 0 {
				return fmt.Errorf("expected failure on depth limit")
			}
			combined := res.Stdout + res.Stderr
			if !strings.Contains(combined, "skill sequence max depth (3) exceeded") {
				return fmt.Errorf("expected depth error message, got: %s", combined)
			}
			return nil
		}),
	},
)
