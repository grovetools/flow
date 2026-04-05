package scenarios

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/grovetools/tend/pkg/fs"
	"github.com/grovetools/tend/pkg/harness"
)

var SkillFidelityTrackingScenario = harness.NewScenario(
	"skill-fidelity-tracking",
	"Verifies skill sequence fidelity observability: execution protocol in briefings, status.json paths, and CLI fidelity query.",
	[]string{"core", "briefing", "skill-sequence", "fidelity"},
	[]harness.Step{
		harness.NewStep("Setup environment with skills that have produces fields", func(ctx *harness.Context) error {
			projectDir, notebooksRoot, err := setupDefaultEnvironment(ctx, "fidelity-project")
			if err != nil {
				return err
			}

			skillsDir := filepath.Join(notebooksRoot, "workspaces", "fidelity-project", "skills")

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

			// Create mock LLM response
			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			if err := fs.WriteString(responseFile, "Mock response for fidelity test."); err != nil {
				return err
			}

			// Init plan
			if err := ctx.Bin("plan", "init", "fidelity-plan").Dir(projectDir).Run().AssertSuccess(); err != nil {
				return err
			}
			planPath := filepath.Join(notebooksRoot, "workspaces", "fidelity-project", "plans", "fidelity-plan")
			ctx.Set("plan_path", planPath)

			return nil
		}),
		harness.SetupMocks(
			harness.Mock{CommandName: "llm"},
			harness.Mock{CommandName: "cx"},
			harness.Mock{CommandName: "grove"},
		),

		// Case 1: Verify briefing contains execution protocol with status file paths
		harness.NewStep("Create and run job with skill_sequence", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			projectDir := ctx.GetString("project_dir")

			jobContent := "---\nid: cook-fidelity\ntitle: Cook with Fidelity\ntype: oneshot\nstatus: pending\nskill_sequence:\n  - prep\n  - sear\n  - plate\n---\nCook the meal with fidelity tracking."
			if err := fs.WriteString(filepath.Join(planPath, "01-cook-fidelity.md"), jobContent); err != nil {
				return err
			}

			responseFile := filepath.Join(ctx.RootDir, "mock_llm_response.txt")
			runCmd := ctx.Bin("plan", "run", filepath.Join(planPath, "01-cook-fidelity.md"), "--yes")
			runCmd.Dir(projectDir).Env("GROVE_MOCK_LLM_RESPONSE_FILE=" + responseFile)
			return runCmd.Run().AssertSuccess()
		}),

		harness.NewStep("Verify briefing contains status file and diagnostic paths", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")

			jobArtifactDir := filepath.Join(planPath, ".artifacts", "cook-fidelity")
			briefings, _ := filepath.Glob(filepath.Join(jobArtifactDir, "briefing-*.xml"))
			if len(briefings) == 0 {
				return fmt.Errorf("no briefing file found in %s", jobArtifactDir)
			}

			content, err := fs.ReadString(briefings[0])
			if err != nil {
				return err
			}

			// Verify status file paths are in the briefing
			prepStatusPath := filepath.Join(jobArtifactDir, "prep-status.json")
			searStatusPath := filepath.Join(jobArtifactDir, "sear-status.json")
			plateStatusPath := filepath.Join(jobArtifactDir, "plate-status.json")

			if !strings.Contains(content, prepStatusPath) {
				return fmt.Errorf("briefing missing prep status file path.\nExpected path: %s\nContent:\n%s", prepStatusPath, content)
			}
			if !strings.Contains(content, searStatusPath) {
				return fmt.Errorf("briefing missing sear status file path.\nExpected path: %s", searStatusPath)
			}
			if !strings.Contains(content, plateStatusPath) {
				return fmt.Errorf("briefing missing plate status file path.\nExpected path: %s", plateStatusPath)
			}

			// Verify diagnostic paths are in the briefing
			prepDiagPath := filepath.Join(jobArtifactDir, "prep-diagnostic.md")
			if !strings.Contains(content, prepDiagPath) {
				return fmt.Errorf("briefing missing prep diagnostic path.\nExpected: %s", prepDiagPath)
			}

			// Verify execution protocol instructions
			if !strings.Contains(content, "write status to") {
				return fmt.Errorf("briefing missing 'write status to' instruction")
			}
			if !strings.Contains(content, "write a diagnostic to") {
				return fmt.Errorf("briefing missing diagnostic instruction")
			}

			return nil
		}),

		// Case 2: Simulate agent writing status.json files and verify CLI reads them
		harness.NewStep("Simulate status.json files written by agent", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			jobArtifactDir := filepath.Join(planPath, ".artifacts", "cook-fidelity")

			// Write mock status.json files as the agent would
			prepStatus := `{"skill":"prep","status":"completed","artifacts_expected":["prep-log.md"],"artifacts_produced":["prep-log.md"],"error":null,"diagnostic_path":null}`
			if err := fs.WriteString(filepath.Join(jobArtifactDir, "prep-status.json"), prepStatus); err != nil {
				return err
			}

			searStatus := `{"skill":"sear","status":"failed","artifacts_expected":["sear-log.md"],"artifacts_produced":[],"error":"Pan temperature too low","diagnostic_path":"sear-diagnostic.md"}`
			if err := fs.WriteString(filepath.Join(jobArtifactDir, "sear-status.json"), searStatus); err != nil {
				return err
			}

			plateStatus := `{"skill":"plate","status":"skipped","artifacts_expected":[],"artifacts_produced":[],"error":null,"diagnostic_path":null}`
			if err := fs.WriteString(filepath.Join(jobArtifactDir, "plate-status.json"), plateStatus); err != nil {
				return err
			}

			return nil
		}),

		harness.NewStep("Verify flow plan status --json includes fidelity data", func(ctx *harness.Context) error {
			planPath := ctx.GetString("plan_path")
			projectDir := ctx.GetString("project_dir")

			statusCmd := ctx.Bin("plan", "status", planPath, "--json")
			statusCmd.Dir(projectDir).Env("GROVE_SKIP_PID_CHECK=true")
			result := statusCmd.Run()
			if err := result.AssertSuccess(); err != nil {
				return fmt.Errorf("status command failed: %w\nStdout: %s\nStderr: %s", err, result.Stdout, result.Stderr)
			}

			// Parse the JSON output
			var output struct {
				Jobs []struct {
					ID            string `json:"id"`
					SkillFidelity []struct {
						Skill             string   `json:"skill"`
						Status            string   `json:"status"`
						ArtifactsExpected []string `json:"artifacts_expected"`
						ArtifactsProduced []string `json:"artifacts_produced"`
						Error             *string  `json:"error"`
					} `json:"skill_fidelity"`
				} `json:"jobs"`
			}
			if err := json.Unmarshal([]byte(result.Stdout), &output); err != nil {
				return fmt.Errorf("failed to parse JSON output: %w\nOutput: %s", err, result.Stdout)
			}

			// Find the cook-fidelity job
			var fidelityJob *struct {
				ID            string `json:"id"`
				SkillFidelity []struct {
					Skill             string   `json:"skill"`
					Status            string   `json:"status"`
					ArtifactsExpected []string `json:"artifacts_expected"`
					ArtifactsProduced []string `json:"artifacts_produced"`
					Error             *string  `json:"error"`
				} `json:"skill_fidelity"`
			}
			for i := range output.Jobs {
				if output.Jobs[i].ID == "cook-fidelity" {
					fidelityJob = &output.Jobs[i]
					break
				}
			}
			if fidelityJob == nil {
				return fmt.Errorf("cook-fidelity job not found in JSON output")
			}

			if len(fidelityJob.SkillFidelity) == 0 {
				return fmt.Errorf("expected skill_fidelity data in JSON output but got none")
			}

			// Verify we have 3 status entries
			if len(fidelityJob.SkillFidelity) != 3 {
				return fmt.Errorf("expected 3 skill fidelity entries, got %d", len(fidelityJob.SkillFidelity))
			}

			// Check individual entries
			statusMap := make(map[string]string)
			for _, sf := range fidelityJob.SkillFidelity {
				statusMap[sf.Skill] = sf.Status
			}

			if statusMap["prep"] != "completed" {
				return fmt.Errorf("expected prep status 'completed', got '%s'", statusMap["prep"])
			}
			if statusMap["sear"] != "failed" {
				return fmt.Errorf("expected sear status 'failed', got '%s'", statusMap["sear"])
			}
			if statusMap["plate"] != "skipped" {
				return fmt.Errorf("expected plate status 'skipped', got '%s'", statusMap["plate"])
			}

			return nil
		}),
	},
)
