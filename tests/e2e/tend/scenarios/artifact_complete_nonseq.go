package scenarios

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

// ArtifactCompleteNonSeqScenario verifies the 09 Fix 4 behavior:
// `flow artifact complete` bypasses sequence validation for jobs
// without a declared skill_sequence, and still rejects unknown skill
// names for jobs that DO declare a sequence.
var ArtifactCompleteNonSeqScenario = harness.NewScenario(
	"artifact-complete-nonseq",
	"Verify flow artifact complete loose-mode for non-sequence jobs and strict validation for sequence jobs",
	[]string{"playbook", "artifact", "fixup"},
	[]harness.Step{
		harness.NewStep("Setup environment with sequence skills", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "artifact-nonseq-project")
			if err != nil {
				return err
			}
			skillsDir := filepath.Join(notebooksRoot, "workspaces", "artifact-nonseq-project", "skills")

			seqASkill := "---\nname: seq-a\ndescription: Sequence step A\n---\n# Seq A\n"
			if err := fs.WriteString(filepath.Join(skillsDir, "seq-a", "SKILL.md"), seqASkill); err != nil {
				return err
			}
			seqBSkill := "---\nname: seq-b\ndescription: Sequence step B\n---\n# Seq B\n"
			if err := fs.WriteString(filepath.Join(skillsDir, "seq-b", "SKILL.md"), seqBSkill); err != nil {
				return err
			}

			groveToml := filepath.Join(projectDir, "grove.toml")
			if err := fs.WriteString(groveToml, "[skills]\nuse = [\"seq-a\", \"seq-b\"]\n"); err != nil {
				return err
			}

			if err := ctx.Bin("plan", "init", "nonseq-plan").Dir(projectDir).Run().AssertSuccess(); err != nil {
				return err
			}
			planPath := filepath.Join(notebooksRoot, "workspaces", "artifact-nonseq-project", "plans", "nonseq-plan")
			ctx.Set("plan_path", planPath)

			// Non-sequence job: fixup-style, no skill_sequence.
			nonseqJob := "---\nid: fixup-job\ntitle: Fixup Job\ntype: interactive_agent\nstatus: pending\n---\nAd-hoc fixup."
			if err := fs.WriteString(filepath.Join(planPath, "01-fixup-job.md"), nonseqJob); err != nil {
				return err
			}
			ctx.Set("nonseq_job_path", filepath.Join(planPath, "01-fixup-job.md"))

			// Sequence job: declares skill_sequence with seq-a, seq-b.
			seqJob := "---\nid: seq-job\ntitle: Sequence Job\ntype: headless_agent\nstatus: pending\nskill_sequence:\n  - seq-a\n  - seq-b\n---\nCook the meal."
			if err := fs.WriteString(filepath.Join(planPath, "02-seq-job.md"), seqJob); err != nil {
				return err
			}
			ctx.Set("seq_job_path", filepath.Join(planPath, "02-seq-job.md"))

			return nil
		}),

		harness.SetupMocks(harness.Mock{CommandName: "grove"}),

		harness.NewStep("Non-sequence job accepts arbitrary skill name", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			nonseqJob := ctx.GetString("nonseq_job_path")

			result := ctx.Bin("artifact", "complete", "arbitrary-marker", "--status", "completed").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + nonseqJob).
				Env("GROVE_FLOW_JOB_ID=fixup-job").
				Run()
			ctx.ShowCommandOutput("complete arbitrary-marker", result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("non-sequence complete should succeed: %w\nstderr: %s", err, result.Stderr)
			}

			planPath := ctx.GetString("plan_path")
			statusFile := filepath.Join(planPath, ".artifacts", "fixup-job", "arbitrary-marker-status.json")
			data, err := os.ReadFile(statusFile)
			if err != nil {
				return fmt.Errorf("expected status file at %s: %w", statusFile, err)
			}
			var parsed map[string]any
			if err := json.Unmarshal(data, &parsed); err != nil {
				return fmt.Errorf("status file is not valid JSON: %w", err)
			}
			if parsed["skill"] != "arbitrary-marker" {
				return fmt.Errorf("expected skill=arbitrary-marker in status, got %v", parsed["skill"])
			}
			if parsed["status"] != "completed" {
				return fmt.Errorf("expected status=completed, got %v", parsed["status"])
			}
			return nil
		}),

		harness.NewStep("Sequence job rejects unknown skill name", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			seqJob := ctx.GetString("seq_job_path")

			result := ctx.Bin("artifact", "complete", "not-in-sequence", "--status", "completed").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + seqJob).
				Env("GROVE_FLOW_JOB_ID=seq-job").
				Run()
			ctx.ShowCommandOutput("complete not-in-sequence", result.Stdout, result.Stderr)
			if result.ExitCode == 0 {
				return fmt.Errorf("expected non-zero exit for skill not in sequence, got stdout=%s", result.Stdout)
			}
			if !strings.Contains(result.Stdout+result.Stderr, "not found in job's skill sequence") {
				return fmt.Errorf("expected sequence-not-found error, got: %s\n%s", result.Stdout, result.Stderr)
			}
			return nil
		}),

		harness.NewStep("Sequence job accepts valid skill from sequence", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			seqJob := ctx.GetString("seq_job_path")

			result := ctx.Bin("artifact", "complete", "seq-a", "--status", "completed").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + seqJob).
				Env("GROVE_FLOW_JOB_ID=seq-job").
				Run()
			ctx.ShowCommandOutput("complete seq-a", result.Stdout, result.Stderr)
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("valid sequence skill should succeed: %w\nstderr: %s", err, result.Stderr)
			}
			return nil
		}),
	},
)
