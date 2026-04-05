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

var ArtifactCLIScenario = harness.NewScenario(
	"artifact-cli",
	"Verifies flow artifact write/read/list/complete commands resolve paths from env vars and auto-verify produces.",
	[]string{"core", "artifact", "skill-sequence"},
	[]harness.Step{
		harness.NewStep("Setup environment with skills that have produces fields", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "artifact-project")
			if err != nil {
				return err
			}

			skillsDir := filepath.Join(notebooksRoot, "workspaces", "artifact-project", "skills")

			// Create skills with produces fields
			prepSkill := "---\nname: prep\ndescription: Mise en place\nproduces:\n  - prep-log.md\n---\n# Prep\nPrepare ingredients."
			if err := fs.WriteString(filepath.Join(skillsDir, "prep", "SKILL.md"), prepSkill); err != nil {
				return err
			}

			searSkill := "---\nname: sear\ndescription: High-heat searing\nproduces:\n  - sear-log.md\n---\n# Sear\nSear the protein."
			if err := fs.WriteString(filepath.Join(skillsDir, "sear", "SKILL.md"), searSkill); err != nil {
				return err
			}

			plateSkill := "---\nname: plate\ndescription: Final plating\n---\n# Plate\nPlate the dish."
			if err := fs.WriteString(filepath.Join(skillsDir, "plate", "SKILL.md"), plateSkill); err != nil {
				return err
			}

			// Authorize skills
			groveToml := filepath.Join(projectDir, "grove.toml")
			tomlContent := "[skills]\nuse = [\"prep\", \"sear\", \"plate\"]\n"
			if err := fs.WriteString(groveToml, tomlContent); err != nil {
				return err
			}

			// Init plan
			if err := ctx.Bin("plan", "init", "artifact-plan").Dir(projectDir).Run().AssertSuccess(); err != nil {
				return err
			}
			planPath := filepath.Join(notebooksRoot, "workspaces", "artifact-project", "plans", "artifact-plan")
			ctx.Set("plan_path", planPath)

			// Create a job with skill_sequence
			jobContent := "---\nid: cook-art\ntitle: Cook with Artifacts\ntype: headless_agent\nstatus: pending\nskill_sequence:\n  - prep\n  - sear\n  - plate\n---\nCook the meal using flow artifact CLI."
			if err := fs.WriteString(filepath.Join(planPath, "01-cook-art.md"), jobContent); err != nil {
				return err
			}

			// Store the job file path and artifact dir for env var injection
			jobFilePath := filepath.Join(planPath, "01-cook-art.md")
			artifactDir := filepath.Join(planPath, ".artifacts", "cook-art")
			ctx.Set("job_file_path", jobFilePath)
			ctx.Set("artifact_dir", artifactDir)

			return nil
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "grove"},
		),

		// Case 1: flow artifact write --content
		harness.NewStep("Write artifact with --content flag", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobFilePath := ctx.GetString("job_file_path")

			result := ctx.Bin("artifact", "write", "prep-log.md", "--content", "# Prep Log\nIngredients prepared.").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + jobFilePath).
				Env("GROVE_FLOW_JOB_ID=cook-art").
				Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("artifact write failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			if !strings.Contains(result.Stdout, "wrote artifact: prep-log.md") {
				return fmt.Errorf("expected success message, got: %s", result.Stdout)
			}
			return nil
		}),

		// Case 2: flow artifact read
		harness.NewStep("Read artifact back", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobFilePath := ctx.GetString("job_file_path")

			result := ctx.Bin("artifact", "read", "prep-log.md").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + jobFilePath).
				Env("GROVE_FLOW_JOB_ID=cook-art").
				Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("artifact read failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			if !strings.Contains(result.Stdout, "# Prep Log") {
				return fmt.Errorf("expected prep log content, got: %s", result.Stdout)
			}
			return nil
		}),

		// Case 3: flow artifact write --file
		harness.NewStep("Write artifact with --file flag", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobFilePath := ctx.GetString("job_file_path")

			// Create a source file
			sourceFile := filepath.Join(ctx.RootDir, "sear-source.md")
			if err := fs.WriteString(sourceFile, "# Sear Log\nProtein seared at 500F."); err != nil {
				return err
			}

			result := ctx.Bin("artifact", "write", "sear-log.md", "--file", sourceFile).
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + jobFilePath).
				Env("GROVE_FLOW_JOB_ID=cook-art").
				Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("artifact write --file failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}
			return nil
		}),

		// Case 4: flow artifact list
		harness.NewStep("List artifacts", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobFilePath := ctx.GetString("job_file_path")

			result := ctx.Bin("artifact", "list").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + jobFilePath).
				Env("GROVE_FLOW_JOB_ID=cook-art").
				Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("artifact list failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			if !strings.Contains(result.Stdout, "prep-log.md") || !strings.Contains(result.Stdout, "sear-log.md") {
				return fmt.Errorf("expected both artifacts in list, got: %s", result.Stdout)
			}
			return nil
		}),

		// Case 5: flow artifact list --json
		harness.NewStep("List artifacts as JSON", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobFilePath := ctx.GetString("job_file_path")

			result := ctx.Bin("artifact", "list", "--json").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + jobFilePath).
				Env("GROVE_FLOW_JOB_ID=cook-art").
				Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("artifact list --json failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			var files []string
			if err := json.Unmarshal([]byte(result.Stdout), &files); err != nil {
				return fmt.Errorf("invalid JSON output: %w\nOutput: %s", err, result.Stdout)
			}

			found := map[string]bool{}
			for _, f := range files {
				found[f] = true
			}
			if !found["prep-log.md"] || !found["sear-log.md"] {
				return fmt.Errorf("expected prep-log.md and sear-log.md in JSON list, got: %v", files)
			}
			return nil
		}),

		// Case 6: flow artifact complete succeeds when produces artifacts exist
		harness.NewStep("Complete skill with all produces present", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobFilePath := ctx.GetString("job_file_path")

			result := ctx.Bin("artifact", "complete", "prep", "--status", "completed").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + jobFilePath).
				Env("GROVE_FLOW_JOB_ID=cook-art").
				Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("artifact complete failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			if !strings.Contains(result.Stdout, "marked skill 'prep' as completed") {
				return fmt.Errorf("expected success message, got: %s", result.Stdout)
			}

			// Verify the status file was written
			artifactDir := ctx.GetString("artifact_dir")
			statusContent, err := fs.ReadString(filepath.Join(artifactDir, "prep-status.json"))
			if err != nil {
				return fmt.Errorf("reading prep-status.json: %w", err)
			}

			var state map[string]interface{}
			if err := json.Unmarshal([]byte(statusContent), &state); err != nil {
				return fmt.Errorf("parsing status JSON: %w", err)
			}

			if state["skill"] != "prep" || state["status"] != "completed" {
				return fmt.Errorf("unexpected status content: %s", statusContent)
			}
			return nil
		}),

		// Case 7: flow artifact complete fails when produces artifact is missing
		harness.NewStep("Complete skill fails when artifact missing", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobFilePath := ctx.GetString("job_file_path")

			// Try to complete 'sear' without the artifact being written yet —
			// wait, we already wrote sear-log.md. Let's test with a skill that
			// would need a missing artifact. We'll remove sear-log.md first.
			artifactDir := ctx.GetString("artifact_dir")
			searPath := filepath.Join(artifactDir, "sear-log.md")
			if err := os.Remove(searPath); err != nil {
				return fmt.Errorf("removing sear-log.md for test: %w", err)
			}

			result := ctx.Bin("artifact", "complete", "sear", "--status", "completed").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + jobFilePath).
				Env("GROVE_FLOW_JOB_ID=cook-art").
				Run()

			// Should fail
			if result.ExitCode == 0 {
				return fmt.Errorf("expected failure when produces artifact missing, but got success.\nStdout: %s", result.Stdout)
			}

			combined := result.Stdout + result.Stderr
			if !strings.Contains(combined, "verification failed") {
				return fmt.Errorf("expected 'verification failed' error, got: %s", combined)
			}
			return nil
		}),

		// Case 8: flow artifact complete with failed status (skips verification)
		harness.NewStep("Complete skill with failed status writes error info", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobFilePath := ctx.GetString("job_file_path")

			// First write a diagnostic file
			ctx.Bin("artifact", "write", "sear-diag.md", "--content", "Pan was not hot enough.").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + jobFilePath).
				Env("GROVE_FLOW_JOB_ID=cook-art").
				Run()

			result := ctx.Bin("artifact", "complete", "sear", "--status", "failed", "--error", "Pan temperature too low", "--diagnostic-file", "sear-diag.md").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + jobFilePath).
				Env("GROVE_FLOW_JOB_ID=cook-art").
				Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("artifact complete --status failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			// Verify the status file contains error info
			artifactDir := ctx.GetString("artifact_dir")
			statusContent, err := fs.ReadString(filepath.Join(artifactDir, "sear-status.json"))
			if err != nil {
				return fmt.Errorf("reading sear-status.json: %w", err)
			}

			var state map[string]interface{}
			if err := json.Unmarshal([]byte(statusContent), &state); err != nil {
				return fmt.Errorf("parsing status JSON: %w", err)
			}

			if state["status"] != "failed" {
				return fmt.Errorf("expected status 'failed', got: %v", state["status"])
			}
			if state["error"] != "Pan temperature too low" {
				return fmt.Errorf("expected error message, got: %v", state["error"])
			}
			if state["diagnostic_path"] != "sear-diag.md" {
				return fmt.Errorf("expected diagnostic_path 'sear-diag.md', got: %v", state["diagnostic_path"])
			}
			return nil
		}),

		// Case 9: flow artifact complete with --feedback flag
		harness.NewStep("Complete skill with feedback flag", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobFilePath := ctx.GetString("job_file_path")
			artifactDir := ctx.GetString("artifact_dir")

			// Re-write sear-log.md so completion succeeds
			ctx.Bin("artifact", "write", "sear-log.md", "--content", "# Sear Log\nSeared.").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + jobFilePath).
				Env("GROVE_FLOW_JOB_ID=cook-art").
				Run()

			result := ctx.Bin("artifact", "complete", "sear", "--status", "completed", "--feedback", "Sear instructions were clear").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + jobFilePath).
				Env("GROVE_FLOW_JOB_ID=cook-art").
				Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("artifact complete with feedback failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			// Verify the status file contains feedback
			statusContent, err := fs.ReadString(filepath.Join(artifactDir, "sear-status.json"))
			if err != nil {
				return fmt.Errorf("reading sear-status.json: %w", err)
			}

			var state map[string]interface{}
			if err := json.Unmarshal([]byte(statusContent), &state); err != nil {
				return fmt.Errorf("parsing status JSON: %w", err)
			}

			if state["feedback"] != "Sear instructions were clear" {
				return fmt.Errorf("expected feedback 'Sear instructions were clear', got: %v", state["feedback"])
			}
			return nil
		}),

		// Case 10: flow artifact complete with skipped status for no-produces skill
		harness.NewStep("Complete skill with no produces (plate) as skipped", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")
			jobFilePath := ctx.GetString("job_file_path")

			result := ctx.Bin("artifact", "complete", "plate", "--status", "skipped").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=" + jobFilePath).
				Env("GROVE_FLOW_JOB_ID=cook-art").
				Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("artifact complete plate: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			// Verify status file
			artifactDir := ctx.GetString("artifact_dir")
			statusContent, err := fs.ReadString(filepath.Join(artifactDir, "plate-status.json"))
			if err != nil {
				return fmt.Errorf("reading plate-status.json: %w", err)
			}

			var state map[string]interface{}
			if err := json.Unmarshal([]byte(statusContent), &state); err != nil {
				return fmt.Errorf("parsing status JSON: %w", err)
			}

			if state["status"] != "skipped" {
				return fmt.Errorf("expected status 'skipped', got: %v", state["status"])
			}
			return nil
		}),

		// Case 11: Error when env vars are missing
		harness.NewStep("Fails gracefully without env vars", func(ctx *harness.Context) error {
			projectDir := ctx.GetString("project_dir")

			// Explicitly clear env vars to ensure they are not inherited
			result := ctx.Bin("artifact", "list").
				Dir(projectDir).
				Env("GROVE_FLOW_JOB_PATH=").
				Env("GROVE_FLOW_JOB_ID=").
				Run()

			if result.ExitCode == 0 {
				return fmt.Errorf("expected failure without env vars, but got success.\nStdout: %s\nStderr: %s", result.Stdout, result.Stderr)
			}

			combined := result.Stdout + result.Stderr
			if !strings.Contains(combined, "not in a job session") {
				return fmt.Errorf("expected 'not in a job session' error, got: %s", combined)
			}
			return nil
		}),
	},
)
